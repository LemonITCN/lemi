package sqldump

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func openLite(t testing.TB, path string) *sql.Conn {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func liteExec(t testing.TB, conn *sql.Conn, qs ...string) {
	t.Helper()
	for _, q := range qs {
		if _, err := conn.ExecContext(context.Background(), q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

var fixedNow = func() time.Time { return time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC) }

// liteSnapshot 独立于 TableChecksum 的比对：结构原文 + 每张表逐行（含 rowid、存储类型、精确值）排序后的文本。
func liteSnapshot(t testing.TB, conn *sql.Conn) map[string][]string {
	t.Helper()
	ctx := context.Background()
	snap := map[string][]string{}
	rows, err := conn.QueryContext(ctx, "SELECT type, name, tbl_name, IFNULL(sql, '') FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name")
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var typ, name, tbl, s string
		if err := rows.Scan(&typ, &name, &tbl, &s); err != nil {
			t.Fatal(err)
		}
		snap["schema"] = append(snap["schema"], typ+"|"+name+"|"+tbl+"|"+s)
		if typ == "table" {
			tables = append(tables, name)
		}
	}
	rows.Close()
	for _, tb := range tables {
		var kind string
		_ = conn.QueryRowContext(ctx, "SELECT type FROM pragma_table_list WHERE schema='main' AND name=?", tb).Scan(&kind)
		if kind == "shadow" {
			continue
		}
		cols, rowid, err := liteColumns(ctx, conn, tb, kind == "virtual")
		if err != nil {
			t.Fatal(err)
		}
		var exprs []string
		if rowid != "" {
			exprs = append(exprs, "'rowid='||"+rowid)
		}
		for _, c := range cols {
			q := quoteSQLiteIdent(c.name)
			exprs = append(exprs, fmt.Sprintf("typeof(%[1]s)||':'||CASE typeof(%[1]s) WHEN 'real' THEN printf('%%!.20e', %[1]s) WHEN 'null' THEN '' ELSE hex(%[1]s) END", q))
		}
		r2, err := conn.QueryContext(ctx, "SELECT "+strings.Join(exprs, "||'|'||")+" FROM "+quoteSQLiteIdent(tb))
		if err != nil {
			t.Fatal(err)
		}
		var lines []string
		for r2.Next() {
			var s string
			if err := r2.Scan(&s); err != nil {
				t.Fatal(err)
			}
			lines = append(lines, s)
		}
		r2.Close()
		sort.Strings(lines)
		snap["data:"+tb] = lines
	}
	r3, err := conn.QueryContext(ctx, "SELECT name, seq FROM sqlite_sequence ORDER BY name")
	if err == nil {
		for r3.Next() {
			var n string
			var s int64
			r3.Scan(&n, &s)
			snap["seq"] = append(snap["seq"], fmt.Sprintf("%s=%d", n, s))
		}
		r3.Close()
	}
	return snap
}

func compareSnapshots(t testing.TB, a, b map[string][]string) {
	t.Helper()
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	for k := range keys {
		x, y := a[k], b[k]
		if len(x) != len(y) {
			t.Fatalf("%s: 源 %d 行，还原后 %d 行\n源: %.2000q\n后: %.2000q", k, len(x), len(y), x, y)
		}
		for i := range x {
			if x[i] != y[i] {
				t.Fatalf("%s 第 %d 行不同:\n源: %q\n后: %q", k, i, x[i], y[i])
			}
		}
	}
}

func buildLiteFixture(t testing.TB, conn *sql.Conn) {
	ctx := context.Background()
	liteExec(t, conn,
		`CREATE TABLE "parent" (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, child_id INTEGER REFERENCES "child"(id))`,
		`CREATE TABLE "child" (id INTEGER PRIMARY KEY, parent_id INTEGER REFERENCES "parent"(id), note TEXT DEFAULT 'x')`,
		`CREATE TABLE "plain rowid" (a TEXT, b BLOB, c REAL, d DATETIME, e)`,
		`CREATE TABLE "no_rowid" (k TEXT PRIMARY KEY, v INTEGER) WITHOUT ROWID`,
		`CREATE TABLE "strict_t" (id INTEGER PRIMARY KEY, i INTEGER, r REAL, t TEXT, b BLOB, a ANY) STRICT`,
		`CREATE TABLE "gen" (a INTEGER, b INTEGER GENERATED ALWAYS AS (a * 2) VIRTUAL, c TEXT GENERATED ALWAYS AS ('v' || a) STORED)`,
		`CREATE TABLE "has""quote and space" ("col ""q""" TEXT, "rowid" INTEGER)`,
		"CREATE TABLE \"line\nbreak*/ name\" (x INTEGER)",
		`CREATE TABLE "emptied" (id INTEGER PRIMARY KEY AUTOINCREMENT, v TEXT)`,
		`CREATE INDEX "idx_parent_name" ON "parent"(name)`,
		`CREATE UNIQUE INDEX "idx_child_note" ON "child"(note) WHERE note IS NOT NULL`,
		`CREATE INDEX "idx_expr" ON "plain rowid"(lower(a))`,
		`CREATE VIEW "v_parent" AS SELECT id, name FROM "parent"`,
		`CREATE VIEW "v_nested" AS SELECT name FROM "v_parent" WHERE id > 1`,
		`CREATE TABLE "log" (msg TEXT)`,
		`CREATE TRIGGER "trg_parent" AFTER INSERT ON "parent" BEGIN INSERT INTO "log" VALUES ('added; END;'); INSERT INTO "log" VALUES (new.name); END`,
		`CREATE TRIGGER "trg_view" INSTEAD OF INSERT ON "v_parent" BEGIN INSERT INTO "parent"(name) VALUES (new.name); END`,
	)
	// 外键环：parent.child_id → child，child.parent_id → parent
	liteExec(t, conn, "PRAGMA foreign_keys = OFF")
	for i := 1; i <= 5; i++ {
		liteExec(t, conn, fmt.Sprintf(`INSERT INTO "parent"(name, child_id) VALUES ('p%d', %d)`, i, i))
		liteExec(t, conn, fmt.Sprintf(`INSERT INTO "child"(id, parent_id, note) VALUES (%d, %d, 'n%d')`, i, i, i))
	}
	liteExec(t, conn, `DELETE FROM "parent" WHERE id = 5`, `DELETE FROM "child" WHERE id = 5`, `UPDATE "child" SET note = NULL WHERE id = 4`, "PRAGMA foreign_keys = ON")
	liteExec(t, conn, `INSERT INTO "emptied"(v) VALUES ('a'), ('b')`, `DELETE FROM "emptied"`)
	liteExec(t, conn, `DELETE FROM "log"`)

	all := make([]byte, 256)
	for i := range all {
		all[i] = byte(i)
	}
	texts := []any{
		"plain", "", "it's \"quoted\" \\ back", "line1\nline2\r\nline3\ttab", "nul\x00inside", "\x1a ctrl \x07",
		"emoji 😀 中文 日本語 한국어 العربية", "invalid utf8 \xff\xfe", strings.Repeat("长文本", 20000), nil,
	}
	reals := []float64{0, math.Copysign(0, -1), 1.5, -2.25, math.MaxFloat64, math.SmallestNonzeroFloat64, 1e-300, 0.1, 1.0 / 3, math.Inf(1), math.Inf(-1), 123456789012345678}
	ins, err := conn.PrepareContext(ctx, `INSERT INTO "plain rowid"(rowid, a, b, c, d, e) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		var e any
		switch i % 5 {
		case 0:
			e = int64(i) * -1000000007
		case 1:
			e = reals[i%len(reals)]
		case 2:
			e = texts[i%len(texts)]
		case 3:
			e = all[:i%256]
		}
		var b any = all
		if i%3 == 1 {
			b = []byte{}
		}
		if _, err := ins.ExecContext(ctx, int64(i*7+3), texts[i%len(texts)], b, reals[i%len(reals)], "2026-01-02 03:04:05.678", e); err != nil {
			t.Fatal(err)
		}
	}
	ins.Close()
	liteExec(t, conn,
		`INSERT INTO "no_rowid" VALUES ('b', 2), ('a', 1), ('中', -9223372036854775808), ('max', 9223372036854775807)`,
		`INSERT INTO "strict_t" VALUES (1, 42, 1.25, 'text', X'00FF', 3.5), (2, NULL, -0.0, '', X'', 'any text'), (3, -1, 1e308, 'x', X'01', X'0203')`,
		`INSERT INTO "gen"(a) VALUES (1), (2), (NULL)`,
		`INSERT INTO "has""quote and space" VALUES ('a', 10), ('b', 20)`,
		"INSERT INTO \"line\nbreak*/ name\" VALUES (1)",
	)
}

func exportLite(t testing.TB, conn *sql.Conn, opts ExportOptions) ([]byte, *Manifest) {
	t.Helper()
	opts.Dialect = SQLite
	if opts.Now == nil {
		opts.Now = fixedNow
	}
	var buf bytes.Buffer
	man, err := Export(context.Background(), conn, &buf, opts)
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	return buf.Bytes(), man
}

func importLite(t testing.TB, conn *sql.Conn, data []byte) *ImportResult {
	t.Helper()
	res, err := Import(context.Background(), conn, bytes.NewReader(data), ImportOptions{Dialect: SQLite})
	if err != nil {
		t.Fatalf("导入失败: %v\n%+v", err, res)
	}
	return res
}

func checkLiteManifest(t testing.TB, conn *sql.Conn, man *Manifest) {
	t.Helper()
	for _, tm := range man.Databases[0].Tables {
		rows, hash, err := TableChecksum(context.Background(), conn, SQLite, "main", tm.Name)
		if err != nil {
			t.Fatal(err)
		}
		if rows != tm.Rows || hash != tm.Hash {
			t.Fatalf("表 %q 校验不一致：还原后 %d 行 %s，清单 %d 行 %s", tm.Name, rows, hash, tm.Rows, tm.Hash)
		}
	}
}

func liteIntegrity(t testing.TB, conn *sql.Conn) {
	t.Helper()
	var s string
	if err := conn.QueryRowContext(context.Background(), "PRAGMA integrity_check").Scan(&s); err != nil || s != "ok" {
		t.Fatalf("integrity_check: %s %v", s, err)
	}
	rows, err := conn.QueryContext(context.Background(), "PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign_key_check 有违反外键的行")
	}
}

func TestSQLiteRoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := openLite(t, filepath.Join(dir, "src.db"))
	buildLiteFixture(t, src)
	before := liteSnapshot(t, src)
	for _, ext := range []bool{false, true} {
		t.Run(fmt.Sprintf("extended=%v", ext), func(t *testing.T) {
			data, man := exportLite(t, src, ExportOptions{ExtendedInsert: ext, MaxStatementBytes: 4096, ConnectionName: "测试\n连接*/"})
			if !bytes.HasPrefix(data, []byte("/*\n Lemi Dump SQL\n")) {
				t.Fatalf("注释头不对: %.200s", data)
			}
			hdr := data[:bytes.Index(data, []byte("*/"))+2]
			if !bytes.Contains(hdr, []byte("测试 连接* /")) || !bytes.Contains(hdr, []byte("Date: 26/11/2026 12:00:00")) {
				t.Fatal("注释头没有清理连接名里的换行或 */")
			}
			dst := openLite(t, filepath.Join(dir, fmt.Sprintf("dst-%v.db", ext)))
			res := importLite(t, dst, data)
			if !res.LemiDump || !res.Complete || res.Errors != 0 {
				t.Fatalf("%+v", res)
			}
			compareSnapshots(t, before, liteSnapshot(t, dst))
			checkLiteManifest(t, dst, man)
			liteIntegrity(t, dst)
			// 再导入一次（DROP IF EXISTS 覆盖已有对象）结果不变
			importLite(t, dst, data)
			compareSnapshots(t, before, liteSnapshot(t, dst))
			// 自己再导出一次，文件逐字节相同
			again, _ := exportLite(t, dst, ExportOptions{ExtendedInsert: ext, MaxStatementBytes: 4096, ConnectionName: "测试\n连接*/"})
			if !bytes.Equal(again, data) {
				t.Fatal("还原后再导出的文件与原文件不同")
			}
		})
	}
}

func TestSQLiteOptions(t *testing.T) {
	dir := t.TempDir()
	src := openLite(t, filepath.Join(dir, "src.db"))
	buildLiteFixture(t, src)

	data, man := exportLite(t, src, ExportOptions{NoData: true})
	if bytes.Contains(data, []byte("Records of")) || man.Databases[0].Tables[0].Hash != "" {
		t.Fatal("只导结构时不应该有数据")
	}
	dst := openLite(t, filepath.Join(dir, "nodata.db"))
	importLite(t, dst, data)
	var n int
	dst.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM "parent"`).Scan(&n)
	if n != 0 {
		t.Fatal("只导结构却有数据")
	}
	// 只导数据：导进结构相同的空库（先删掉触发器，否则插入数据会触发它们往 log 表写行）
	liteExec(t, dst, `DROP TRIGGER "trg_parent"`, `DROP TRIGGER "trg_view"`)
	data2, man2 := exportLite(t, src, ExportOptions{NoStructure: true})
	if bytes.Contains(data2, []byte("CREATE TABLE")) || bytes.Contains(data2, []byte("DROP ")) {
		t.Fatal("只导数据时不应该有结构语句")
	}
	importLite(t, dst, data2)
	checkLiteManifest(t, dst, man2)

	// 选表
	data3, man3 := exportLite(t, src, ExportOptions{Tables: []string{"child", "v_parent"}})
	if len(man3.Databases[0].Tables) != 1 || man3.Databases[0].Views != 1 || bytes.Contains(data3, []byte(`"parent" (`)) {
		t.Fatalf("选表导出不对: %+v", man3.Databases[0])
	}
	// 不写 DROP
	data4, _ := exportLite(t, src, ExportOptions{NoDrop: true})
	if bytes.Contains(data4, []byte("DROP ")) {
		t.Fatal("NoDrop 仍写了 DROP")
	}
}

// 随机表结构与数据（固定种子可复现），多轮往返。
func TestSQLiteRandomRoundTrip(t *testing.T) {
	for seed := int64(1); seed <= 8; seed++ {
		t.Run(fmt.Sprint("seed", seed), func(t *testing.T) {
			rnd := rand.New(rand.NewSource(seed))
			dir := t.TempDir()
			src := openLite(t, filepath.Join(dir, "src.db"))
			ntab := 1 + rnd.Intn(4)
			for ti := 0; ti < ntab; ti++ {
				types := []string{"INTEGER", "REAL", "TEXT", "BLOB", "NUMERIC", "", "DATETIME", "VARCHAR(10)", "BOOLEAN"}
				ncol := 1 + rnd.Intn(6)
				var defs []string
				for ci := 0; ci < ncol; ci++ {
					name := fmt.Sprintf("c%d_%s", ci, randIdent(rnd))
					if ci == 0 && rnd.Intn(2) == 0 {
						defs = append(defs, quoteSQLiteIdent(name)+" INTEGER PRIMARY KEY")
					} else {
						defs = append(defs, quoteSQLiteIdent(name)+" "+types[rnd.Intn(len(types))])
					}
				}
				tname := fmt.Sprintf("t%d_%s", ti, randIdent(rnd))
				liteExec(t, src, "CREATE TABLE "+quoteSQLiteIdent(tname)+" ("+strings.Join(defs, ", ")+")")
				ph := strings.TrimSuffix(strings.Repeat("?, ", ncol), ", ")
				stmt, err := src.PrepareContext(context.Background(), "INSERT INTO "+quoteSQLiteIdent(tname)+" VALUES ("+ph+")")
				if err != nil {
					t.Fatal(err)
				}
				nrow := rnd.Intn(300)
				for r := 0; r < nrow; r++ {
					args := make([]any, ncol)
					for ci := range args {
						args[ci] = randLiteValue(rnd)
					}
					if strings.HasSuffix(defs[0], "INTEGER PRIMARY KEY") {
						args[0] = int64(r*3 + 1)
					}
					if _, err := stmt.ExecContext(context.Background(), args...); err != nil {
						t.Fatal(err)
					}
				}
				stmt.Close()
				if rnd.Intn(2) == 0 {
					liteExec(t, src, "DELETE FROM "+quoteSQLiteIdent(tname)+" WHERE rowid % 5 = 0")
				}
			}
			before := liteSnapshot(t, src)
			data, man := exportLite(t, src, ExportOptions{ExtendedInsert: rnd.Intn(2) == 0, MaxStatementBytes: 1 + rnd.Intn(20000)})
			dst := openLite(t, filepath.Join(dir, "dst.db"))
			importLite(t, dst, data)
			compareSnapshots(t, before, liteSnapshot(t, dst))
			checkLiteManifest(t, dst, man)
		})
	}
}

func randIdent(rnd *rand.Rand) string {
	parts := []string{"a", "b", "名", "x y", "q\"q", "end", "select", "rowid", "_", "😀", "back`tick", "semi;colon", "dash--dash", "slash/*"}
	n := 1 + rnd.Intn(3)
	var s string
	for i := 0; i < n; i++ {
		s += parts[rnd.Intn(len(parts))]
	}
	return s
}

func randLiteValue(rnd *rand.Rand) any {
	switch rnd.Intn(7) {
	case 0:
		return nil
	case 1:
		return int64(rnd.Uint64())
	case 2:
		for {
			f := math.Float64frombits(rnd.Uint64())
			if !math.IsNaN(f) {
				return f
			}
		}
	case 3:
		b := make([]byte, rnd.Intn(40))
		rnd.Read(b)
		return string(b)
	case 4:
		b := make([]byte, rnd.Intn(40))
		rnd.Read(b)
		return b
	case 5:
		return []string{"'", "''", "\\", "\n", ";", "-- x", "/* y */", "END;", "中文", "\r\n", "\x00"}[rnd.Intn(11)]
	}
	return rnd.Float64() * 1000
}

func TestSQLiteTruncatedFileFails(t *testing.T) {
	dir := t.TempDir()
	src := openLite(t, filepath.Join(dir, "src.db"))
	buildLiteFixture(t, src)
	data, _ := exportLite(t, src, ExportOptions{})
	// 在每个语句边界截断（只有结束标记能发现）以及若干任意位置截断
	cuts := []int{}
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' && i > 0 && data[i-1] == ';' {
			cuts = append(cuts, i+1)
		}
	}
	rnd := rand.New(rand.NewSource(7))
	for i := 0; i < 30; i++ {
		cuts = append(cuts, rnd.Intn(len(data)))
	}
	end := bytes.LastIndex(data, []byte(endMarkerPrefix))
	for _, c := range cuts {
		if c >= end {
			continue
		}
		dst := openLite(t, filepath.Join(dir, fmt.Sprintf("cut%d.db", c)))
		res, err := Import(context.Background(), dst, bytes.NewReader(data[:c]), ImportOptions{Dialect: SQLite})
		if err == nil {
			t.Fatalf("截断在 %d 字节（共 %d）的文件导入竟然成功: %+v", c, len(data), res)
		}
		if res.Complete {
			t.Fatalf("截断文件不应标记为完整")
		}
	}
	// ContinueOnError 也不能把截断文件当成功
	dst := openLite(t, filepath.Join(dir, "cut-continue.db"))
	_, err := Import(context.Background(), dst, bytes.NewReader(data[:end]), ImportOptions{Dialect: SQLite, ContinueOnError: true})
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("应报 ErrIncomplete，得到 %v", err)
	}
}

// 导入中途取消必须报错。
func TestSQLiteImportCancel(t *testing.T) {
	dir := t.TempDir()
	src := openLite(t, filepath.Join(dir, "src.db"))
	liteExec(t, src, "CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)")
	liteExec(t, src, "WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x < 20000) INSERT INTO t SELECT x, 'value ' || x FROM c")
	data, _ := exportLite(t, src, ExportOptions{})
	dst := openLite(t, filepath.Join(dir, "dst.db"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res, err := Import(ctx, dst, &cancelReader{r: bytes.NewReader(data), at: int64(len(data) / 2), cancel: cancel}, ImportOptions{Dialect: SQLite})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("取消后应返回 context.Canceled，得到 %v", err)
	}
	if res.Complete || res.Statements >= 20000 {
		t.Fatalf("%+v", res)
	}
}

func TestSQLiteImportNavicatFile(t *testing.T) {
	src := `/*
 Navicat Premium Dump SQL

 Source Server         : demo
 Source Server Type    : SQLite
 Source Server Version : 3045000 (3.45.0)
 Source Schema         : main

 Target Server Type    : SQLite
 Target Server Version : 3045000 (3.45.0)
 File Encoding         : 65001

 Date: 25/01/2026 22:09:11
*/

PRAGMA foreign_keys = false;

-- ----------------------------
-- Table structure for alert
-- ----------------------------
DROP TABLE IF EXISTS "alert";
CREATE TABLE "alert" (
  "id" INTEGER PRIMARY KEY AUTOINCREMENT,
  "title" VARCHAR NOT NULL
);

-- ----------------------------
-- Records of alert
-- ----------------------------
BEGIN;
INSERT INTO "alert" ("id", "title") VALUES (1, 'a;b');
INSERT INTO "alert" ("id", "title") VALUES (2, 'it''s');
COMMIT;

-- ----------------------------
-- Table structure for sqlite_sequence
-- ----------------------------
DROP TABLE IF EXISTS "sqlite_sequence";
CREATE TABLE "sqlite_sequence" (
  "name",
  "seq"
);

-- ----------------------------
-- Auto increment value for alert
-- ----------------------------
UPDATE "sqlite_sequence" SET seq = 9 WHERE name = 'alert';

-- ----------------------------
-- Indexes structure for table alert
-- ----------------------------
CREATE INDEX "alert_title_idx"
ON "alert" (
  "title" ASC
);

-- ----------------------------
-- Triggers structure for table alert
-- ----------------------------
CREATE TRIGGER "alert_trg"
AFTER INSERT
ON "alert"
BEGIN
  UPDATE "alert" SET title = title || ';' WHERE id = new.id;
END;

PRAGMA foreign_keys = true;
`
	dst := openLite(t, filepath.Join(t.TempDir(), "nav.db"))
	res, err := Import(context.Background(), dst, strings.NewReader(src), ImportOptions{Dialect: SQLite})
	if err != nil {
		t.Fatalf("%v %+v", err, res)
	}
	if res.Format != "navicat" || res.Skipped != 2 || res.Errors != 0 || !res.Complete {
		t.Fatalf("%+v", res)
	}
	var seq int
	dst.QueryRowContext(context.Background(), "SELECT seq FROM sqlite_sequence WHERE name = 'alert'").Scan(&seq)
	if seq != 9 {
		t.Fatalf("自增值 %d", seq)
	}
	var title string
	dst.QueryRowContext(context.Background(), `SELECT title FROM "alert" WHERE id = 2`).Scan(&title)
	if title != "it's" {
		t.Fatal(title)
	}
}

func TestSQLiteImportRejectsAttach(t *testing.T) {
	dir := t.TempDir()
	dst := openLite(t, filepath.Join(dir, "dst.db"))
	for _, q := range []string{"ATTACH DATABASE '" + filepath.Join(dir, "evil.db") + "' AS evil;", "VACUUM INTO '" + filepath.Join(dir, "copy.db") + "';"} {
		res, err := Import(context.Background(), dst, strings.NewReader(q), ImportOptions{Dialect: SQLite})
		if err == nil || res.Errors != 1 {
			t.Fatalf("%s 应被拒绝: %v %+v", q, err, res)
		}
	}
}

func TestSQLiteImportErrorsAndContinue(t *testing.T) {
	src := "CREATE TABLE t (id INTEGER PRIMARY KEY);\nINSERT INTO t VALUES (1);\nINSERT INTO t VALUES (1);\nINSERT INTO nosuch VALUES (1);\nINSERT INTO t VALUES (2);\n"
	dir := t.TempDir()
	dst := openLite(t, filepath.Join(dir, "a.db"))
	res, err := Import(context.Background(), dst, strings.NewReader(src), ImportOptions{Dialect: SQLite})
	if err == nil || res.Errors != 1 || res.ErrorList[0].Index != 3 || res.ErrorList[0].Line != 3 {
		t.Fatalf("%v %+v", err, res)
	}
	var n int
	dst.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM t").Scan(&n)
	if n != 1 {
		t.Fatalf("出错即停时已执行的部分应保留，得到 %d 行", n)
	}
	dst2 := openLite(t, filepath.Join(dir, "b.db"))
	res, err = Import(context.Background(), dst2, strings.NewReader(src), ImportOptions{Dialect: SQLite, ContinueOnError: true})
	if err != nil || res.Errors != 2 || res.Statements != 5 {
		t.Fatalf("%v %+v", err, res)
	}
	dst2.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM t").Scan(&n)
	if n != 2 {
		t.Fatalf("跳过错误后应有 2 行，得到 %d", n)
	}
}

// cancelReader 读到 at 字节时取消 ctx（模拟用户在导入中途点取消）。
type cancelReader struct {
	r      io.Reader
	n, at  int64
	cancel func()
}

func (c *cancelReader) Read(p []byte) (int, error) {
	if len(p) > 4096 {
		p = p[:4096]
	}
	n, err := c.r.Read(p)
	c.n += int64(n)
	if c.n >= c.at {
		c.cancel()
	}
	return n, err
}
