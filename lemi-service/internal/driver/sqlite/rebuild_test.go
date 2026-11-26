package sqlite

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LemonITCN/lemi/lemi-service/internal/driver"
	"github.com/LemonITCN/lemi/lemi-service/internal/sqlsplit"
)

const richSchema = `
CREATE TABLE customers (id INTEGER PRIMARY KEY, name TEXT);
CREATE TABLE orders (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  customer_id INTEGER REFERENCES customers(id) ON DELETE CASCADE,
  code TEXT NOT NULL COLLATE NOCASE UNIQUE,
  amount REAL CHECK (amount >= 0),
  qty INTEGER DEFAULT 1,
  total REAL GENERATED ALWAYS AS (amount * qty) VIRTUAL,
  note TEXT,
  CONSTRAINT ck_qty CHECK (qty > 0),
  UNIQUE (customer_id, note)
);
CREATE INDEX ix_amount ON orders(amount) WHERE amount > 100;
CREATE INDEX ix_lower ON orders(lower(note));
CREATE TABLE audit (msg TEXT);
CREATE TRIGGER tr_ins AFTER INSERT ON orders BEGIN INSERT INTO audit VALUES ('ins ' || NEW.code); END;
CREATE VIEW v_orders AS SELECT id, code, amount, total FROM orders;
INSERT INTO customers VALUES (1, 'a'), (2, 'b');
INSERT INTO orders (customer_id, code, amount, qty, note) VALUES (1, 'abc', 10.5, 2, 'n1'), (2, 'def', 200, 1, 'n2'), (1, 'ghi', 0, 3, NULL);
DELETE FROM orders WHERE code = 'ghi';
DELETE FROM audit;
`

func openTestDB(t *testing.T, schema string) (*Conn, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "t.db")
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(schema); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	dc, err := (&Driver{}).Open(context.Background(), &driver.ConnConfig{Params: map[string]any{"file": path}, SQLite: driver.SQLiteEnv{AllowedDirs: []string{dir}}})
	if err != nil {
		t.Fatal(err)
	}
	c := dc.(*Conn)
	t.Cleanup(func() { c.Close() })
	return c, path
}

// designOf 与前端设计器一样，把现有结构原样变成设计（自动 UNIQUE 索引不提交，表达式索引原样提交）。
func designOf(t *driver.TableInfo) *driver.TableDesign {
	d := &driver.TableDesign{Name: t.Name}
	for _, c := range t.Columns {
		d.Columns = append(d.Columns, driver.ColumnDesign{OrigName: c.Name, Name: c.Name, Type: c.Type, Nullable: c.Nullable && !c.PrimaryKey,
			Default: c.Default, DefaultIsExpr: c.DefaultIsExpr, AutoIncrement: c.AutoIncrement, PrimaryKey: c.PrimaryKey, Generated: c.Generated})
	}
	for _, ix := range t.Indexes {
		if strings.HasPrefix(ix.Name, "sqlite_autoindex_") {
			continue
		}
		d.Indexes = append(d.Indexes, ix)
	}
	d.ForeignKeys = append(d.ForeignKeys, t.ForeignKeys...)
	return d
}

func col(d *driver.TableDesign, name string) *driver.ColumnDesign {
	for i := range d.Columns {
		if d.Columns[i].OrigName == name || (d.Columns[i].OrigName == "" && d.Columns[i].Name == name) {
			return &d.Columns[i]
		}
	}
	panic("没有列 " + name)
}

func renameIn(d *driver.TableDesign, from, to string) {
	col(d, from).Name = to
	for i := range d.Indexes {
		for j, c := range d.Indexes[i].Columns {
			if c == from {
				d.Indexes[i].Columns[j] = to
			}
		}
	}
	for i := range d.ForeignKeys {
		for j, c := range d.ForeignKeys[i].Columns {
			if c == from {
				d.ForeignKeys[i].Columns[j] = to
			}
		}
	}
}

func dropIn(d *driver.TableDesign, name string) {
	var cols []driver.ColumnDesign
	for _, c := range d.Columns {
		if c.OrigName != name {
			cols = append(cols, c)
		}
	}
	d.Columns = cols
	var idx []driver.IndexInfo
	for _, ix := range d.Indexes {
		var keep []string
		for _, c := range ix.Columns {
			if c != name {
				keep = append(keep, c)
			}
		}
		if len(keep) > 0 {
			ix.Columns = keep
			idx = append(idx, ix)
		}
	}
	d.Indexes = idx
}

// apply 生成并在一个连接上依次执行（与控制台执行一致）。
func apply(t *testing.T, c *Conn, table string, edit func(d *driver.TableDesign)) ([]string, error) {
	t.Helper()
	ctx := context.Background()
	old, err := c.Table(ctx, "main", table)
	if err != nil {
		t.Fatal(err)
	}
	d := designOf(old)
	edit(d)
	stmts, err := c.AlterSQL(ctx, "main", old, d)
	if err != nil {
		return nil, err
	}
	conn, err := c.DB().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// 与真正的执行路径一致：拼成一段 SQL 再按方言切分（触发器 BEGIN…END 里有分号）
	parts := sqlsplit.Split(strings.Join(stmts, ";\n")+";", sqlsplit.Dialect("sqlite"))
	if len(parts) != len(stmts) {
		t.Fatalf("切分后 %d 条，生成了 %d 条：\n%s", len(parts), len(stmts), strings.Join(stmts, ";\n"))
	}
	for _, p := range parts {
		s := p.Text
		if _, err := conn.ExecContext(ctx, s); err != nil {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
			t.Fatalf("执行失败：%v\n语句：%s\n全部：\n%s", err, s, strings.Join(stmts, ";\n"))
		}
	}
	return stmts, nil
}

func mustExec(t *testing.T, c *Conn, q string, args ...any) {
	t.Helper()
	if _, err := c.DB().Exec(q, args...); err != nil {
		t.Fatalf("%s：%v", q, err)
	}
}

func mustFail(t *testing.T, c *Conn, why, q string, args ...any) {
	t.Helper()
	if _, err := c.DB().Exec(q, args...); err == nil {
		t.Fatalf("应当失败（%s）：%s", why, q)
	}
}

func scalar(t *testing.T, c *Conn, q string, args ...any) string {
	t.Helper()
	var s sql.NullString
	if err := c.DB().QueryRow(q, args...).Scan(&s); err != nil {
		t.Fatalf("%s：%v", q, err)
	}
	return s.String
}

func healthy(t *testing.T, c *Conn) {
	t.Helper()
	if s := scalar(t, c, "PRAGMA integrity_check"); s != "ok" {
		t.Fatalf("integrity_check：%s", s)
	}
	rows, err := c.DB().Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign_key_check 有违反")
	}
}

func TestRebuildKeepsEverything(t *testing.T) {
	c, _ := openTestDB(t, richSchema)
	stmts, err := apply(t, c, "orders", func(d *driver.TableDesign) {
		col(d, "amount").Type = "NUMERIC" // 改类型：必须重建
		renameIn(d, "note", "remark")     // 同时改名
		// 调整顺序：remark 挪到 code 后面
		var r driver.ColumnDesign
		var rest []driver.ColumnDesign
		for _, x := range d.Columns {
			if x.OrigName == "note" {
				r = x
			} else {
				rest = append(rest, x)
			}
		}
		d.Columns = append(rest[:3:3], append([]driver.ColumnDesign{r}, rest[3:]...)...)
	})
	if err != nil {
		t.Fatal(err)
	}
	ddl := strings.Join(stmts, "\n")
	if !strings.Contains(ddl, "_lemi_new_orders") {
		t.Fatalf("应当走重建：\n%s", ddl)
	}
	healthy(t, c)
	// 数据原样
	if n := scalar(t, c, "SELECT count(*) FROM orders"); n != "2" {
		t.Fatalf("行数 %s", n)
	}
	if v := scalar(t, c, "SELECT remark FROM orders WHERE code = 'abc'"); v != "n1" {
		t.Fatalf("改名列的数据 %q", v)
	}
	if v := scalar(t, c, "SELECT total FROM orders WHERE code = 'abc'"); v != "21" {
		t.Fatalf("生成列 %q", v)
	}
	// 列顺序与类型
	tbl, err := c.Table(context.Background(), "main", "orders")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, x := range tbl.Columns {
		names = append(names, x.Name)
		if x.Name == "amount" && x.Type != "NUMERIC" {
			t.Fatalf("类型没改：%s", x.Type)
		}
	}
	if strings.Join(names, ",") != "id,customer_id,code,remark,amount,qty,total" {
		t.Fatalf("列顺序：%v", names)
	}
	// 约束都还在
	mustFail(t, c, "COLLATE NOCASE + UNIQUE", "INSERT INTO orders (customer_id, code, amount, qty) VALUES (1, 'ABC', 1, 1)")
	mustFail(t, c, "列级 CHECK", "INSERT INTO orders (customer_id, code, amount, qty) VALUES (1, 'x1', -1, 1)")
	mustFail(t, c, "表级 CHECK", "INSERT INTO orders (customer_id, code, amount, qty) VALUES (1, 'x2', 1, 0)")
	mustFail(t, c, "表级 UNIQUE（列已改名）", "INSERT INTO orders (customer_id, code, amount, qty, remark) VALUES (1, 'x3', 1, 1, 'n1')")
	mustFail(t, c, "外键", "INSERT INTO orders (customer_id, code, amount, qty) VALUES (99, 'x4', 1, 1)")
	// 自增计数器延续（删掉的 3 号不会被复用）
	mustExec(t, c, "INSERT INTO orders (customer_id, code, amount, qty) VALUES (2, 'new', 5, 1)")
	if id := scalar(t, c, "SELECT id FROM orders WHERE code = 'new'"); id != "4" {
		t.Fatalf("AUTOINCREMENT 计数器丢了，新 id = %s", id)
	}
	// 触发器还在
	if v := scalar(t, c, "SELECT msg FROM audit"); v != "ins new" {
		t.Fatalf("触发器：%q", v)
	}
	// 部分索引、表达式索引还在，引用已跟着改名
	if s := scalar(t, c, "SELECT sql FROM sqlite_master WHERE name = 'ix_amount'"); !strings.Contains(s, "WHERE amount > 100") {
		t.Fatalf("部分索引：%s", s)
	}
	if s := scalar(t, c, "SELECT sql FROM sqlite_master WHERE name = 'ix_lower'"); !strings.Contains(s, "remark") {
		t.Fatalf("表达式索引：%s", s)
	}
	// 视图还能用
	if n := scalar(t, c, "SELECT count(*) FROM v_orders"); n != "3" {
		t.Fatalf("视图：%s", n)
	}
	// 级联删除
	mustExec(t, c, "DELETE FROM customers WHERE id = 1")
	if n := scalar(t, c, "SELECT count(*) FROM orders WHERE customer_id = 1"); n != "0" {
		t.Fatalf("ON DELETE CASCADE 丢了")
	}
}

func TestRebuildDropColumnRemovesItsConstraints(t *testing.T) {
	c, _ := openTestDB(t, richSchema)
	if _, err := apply(t, c, "orders", func(d *driver.TableDesign) {
		col(d, "amount").Type = "NUMERIC"
		dropIn(d, "note")
	}); err != nil {
		t.Fatal(err)
	}
	healthy(t, c)
	if s := scalar(t, c, "SELECT count(*) FROM sqlite_master WHERE name = 'ix_lower'"); s != "0" {
		t.Fatal("引用被删列的表达式索引应当随之去掉")
	}
	ddl := scalar(t, c, "SELECT sql FROM sqlite_master WHERE name = 'orders'")
	if strings.Contains(ddl, "note") || !strings.Contains(ddl, "ck_qty") || !strings.Contains(ddl, "COLLATE NOCASE") {
		t.Fatalf("表定义不对：%s", ddl)
	}
	mustFail(t, c, "列级 CHECK", "INSERT INTO orders (customer_id, code, amount, qty) VALUES (2, 'x1', -1, 1)")
}

func TestRebuildRefusesWhenTriggerOrGeneratedUsesDroppedColumn(t *testing.T) {
	c, _ := openTestDB(t, richSchema)
	// amount 被生成列 total 用到
	if _, err := apply(t, c, "orders", func(d *driver.TableDesign) { dropIn(d, "amount"); col(d, "qty").Type = "NUMERIC" }); err == nil || !strings.Contains(err.Error(), "total") {
		t.Fatalf("应当拒绝：%v", err)
	}
	// code 被触发器用到
	if _, err := apply(t, c, "orders", func(d *driver.TableDesign) { dropIn(d, "code"); col(d, "qty").Type = "NUMERIC" }); err == nil || !strings.Contains(err.Error(), "tr_ins") {
		t.Fatalf("应当拒绝：%v", err)
	}
	// 拒绝后表没有被动过
	if n := scalar(t, c, "SELECT count(*) FROM orders"); n != "2" {
		t.Fatal("表被动过了")
	}
}

func TestRebuildSwapNamesAndRenameTable(t *testing.T) {
	c, _ := openTestDB(t, `
CREATE TABLE p (a TEXT CHECK (length(a) < 10), b TEXT, c INTEGER);
CREATE TRIGGER tp AFTER UPDATE OF a ON p BEGIN SELECT 1; END;
CREATE VIEW vp AS SELECT a, b FROM p;
INSERT INTO p VALUES ('x', 'y', 1);
`)
	if _, err := apply(t, c, "p", func(d *driver.TableDesign) {
		col(d, "a").Name = "b"
		col(d, "b").Name = "a"
		col(d, "c").Type = "TEXT"
		d.Name = "p2"
	}); err != nil {
		t.Fatal(err)
	}
	healthy(t, c)
	if v := scalar(t, c, "SELECT a || b FROM p2"); v != "yx" {
		t.Fatalf("互换后数据：%q", v)
	}
	// CHECK 跟着原来的 a（现在叫 b）
	mustFail(t, c, "CHECK 跟随改名", "INSERT INTO p2 (a, b) VALUES ('ok', 'this is too long')")
	mustExec(t, c, "INSERT INTO p2 (a, b) VALUES ('this is long ok', 'ok')")
	if n := scalar(t, c, "SELECT count(*) FROM vp"); n != "2" {
		t.Fatalf("视图：%s", n)
	}
	if s := scalar(t, c, "SELECT tbl_name FROM sqlite_master WHERE name = 'tp'"); s != "p2" {
		t.Fatalf("触发器：%s", s)
	}
}

func TestRebuildKeepsTableOptions(t *testing.T) {
	c, _ := openTestDB(t, `
CREATE TABLE s (k TEXT PRIMARY KEY, v INTEGER NOT NULL) WITHOUT ROWID, STRICT;
INSERT INTO s VALUES ('a', 1);
`)
	if _, err := apply(t, c, "s", func(d *driver.TableDesign) {
		col(d, "v").Nullable = true
		d.Columns = append(d.Columns, driver.ColumnDesign{Name: "w", Type: "TEXT", Nullable: true})
	}); err != nil {
		t.Fatal(err)
	}
	healthy(t, c)
	ddl := scalar(t, c, "SELECT sql FROM sqlite_master WHERE name = 's'")
	if !strings.Contains(ddl, "WITHOUT ROWID") || !strings.Contains(ddl, "STRICT") {
		t.Fatalf("表选项丢了：%s", ddl)
	}
	mustFail(t, c, "STRICT 类型检查", "INSERT INTO s (k, v) VALUES ('b', 'not a number')")
}

func TestRebuildNewColumnTakesOldName(t *testing.T) {
	c, _ := openTestDB(t, `CREATE TABLE q (a TEXT, b INTEGER); INSERT INTO q VALUES ('x', 1);`)
	if _, err := apply(t, c, "q", func(d *driver.TableDesign) {
		col(d, "a").Name = "a_old"
		col(d, "b").Type = "TEXT"
		d.Columns = append(d.Columns, driver.ColumnDesign{Name: "a", Type: "TEXT", Nullable: true})
	}); err != nil {
		t.Fatal(err)
	}
	healthy(t, c)
	if v := scalar(t, c, "SELECT a_old || '|' || coalesce(a, 'null') FROM q"); v != "x|null" {
		t.Fatalf("数据：%q", v)
	}
}

func TestSameFKsDetectsEdit(t *testing.T) {
	c, _ := openTestDB(t, richSchema)
	stmts, err := apply(t, c, "orders", func(d *driver.TableDesign) { d.ForeignKeys[0].OnDelete = "SET NULL" })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(stmts, "\n"), "ON DELETE SET NULL") {
		t.Fatalf("外键修改没有生效：\n%s", strings.Join(stmts, "\n"))
	}
	healthy(t, c)
	mustExec(t, c, "DELETE FROM customers WHERE id = 1")
	if v := scalar(t, c, "SELECT count(*) FROM orders WHERE customer_id IS NULL"); v != "1" {
		t.Fatalf("ON DELETE SET NULL：%s", v)
	}
}

func TestParseTableDDL(t *testing.T) {
	p, err := parseTableDDL(`CREATE TABLE "a(b" ( "x,y" TEXT CONSTRAINT nn NOT NULL CONSTRAINT u UNIQUE ON CONFLICT REPLACE, z INT REFERENCES t(id) ON DELETE SET NULL NOT DEFERRABLE CHECK (z <> 0), g AS (z * 2) STORED, [w] TEXT DEFAULT 'a,b' COLLATE RTRIM, CHECK ("x,y" <> 'q'), PRIMARY KEY (z)) STRICT`)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"x,y": "CONSTRAINT u UNIQUE ON CONFLICT REPLACE",
		"z":   "CHECK (z <> 0)",
		"g":   "AS (z * 2) STORED",
		"w":   "COLLATE RTRIM",
	}
	for k, v := range want {
		if got := strings.Join(p.cols[k], " | "); got != v {
			t.Errorf("列 %s：%q，应为 %q", k, got, v)
		}
	}
	if !p.generated["g"] || p.generated["z"] {
		t.Error("生成列识别不对")
	}
	if len(p.constraints) != 1 || p.constraints[0] != `CHECK ("x,y" <> 'q')` {
		t.Errorf("表级约束：%q", p.constraints)
	}
	if p.options != "STRICT" {
		t.Errorf("表选项：%q", p.options)
	}
}

func TestMain(m *testing.M) { os.Exit(m.Run()) }
