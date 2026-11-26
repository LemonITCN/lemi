package sqldump

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

// 测试服务器：LEMI_TEST_MYSQL=mysql57=root:pw@tcp(127.0.0.1:33057)/,mysql84=...
// 可选 LEMI_TEST_MYSQL_DOCKER=mysql57=容器名,...：有它时额外跑与官方 mysql 客户端、mysqldump 的交叉测试。
type myServer struct {
	name, dsn, container string
	cfg                  *mysql.Config
	ver                  myVersion
}

func myServers(t *testing.T) []*myServer {
	spec := os.Getenv("LEMI_TEST_MYSQL")
	if spec == "" {
		t.Skip("没有设置 LEMI_TEST_MYSQL，跳过 MySQL 测试")
	}
	containers := map[string]string{}
	for _, kv := range strings.Split(os.Getenv("LEMI_TEST_MYSQL_DOCKER"), ",") {
		if k, v, ok := strings.Cut(strings.TrimSpace(kv), "="); ok {
			containers[k] = v
		}
	}
	var out []*myServer
	for _, kv := range strings.Split(spec, ",") {
		name, dsn, ok := strings.Cut(strings.TrimSpace(kv), "=")
		if !ok {
			t.Fatalf("LEMI_TEST_MYSQL 格式不对: %q", kv)
		}
		cfg, err := mysql.ParseDSN(dsn)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out = append(out, &myServer{name: name, dsn: dsn, cfg: cfg, container: containers[name]})
	}
	return out
}

var dbSeq atomic.Int64

// openMy 打开一个连接池（单连接），可指定库与 parseTime。
func (s *myServer) open(t testing.TB, db string, parseTime bool) *sql.DB {
	t.Helper()
	cfg := s.cfg.Clone()
	cfg.DBName = db
	cfg.ParseTime = parseTime
	cfg.AllowAllFiles = false
	cfg.MultiStatements = false
	cfg.InterpolateParams = false
	cfg.Timeout = 10 * time.Second
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	cfg.Params["charset"] = "utf8mb4"
	pool, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(4)
	t.Cleanup(func() { pool.Close() })
	return pool
}

func (s *myServer) conn(t testing.TB, db string, parseTime bool) *sql.Conn {
	t.Helper()
	c, err := s.open(t, db, parseTime).Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// newDB 建一个空库，测试结束删掉。
func (s *myServer) newDB(t testing.TB, admin *sql.Conn, tag string) string {
	t.Helper()
	name := fmt.Sprintf("lemi_%s_%d_%d", tag, time.Now().UnixNano()%1e6, dbSeq.Add(1))
	myExec(t, admin, "CREATE DATABASE "+quoteMySQLIdent(name)+" CHARACTER SET utf8mb4")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		c, err := s.open(t, "", false).Conn(ctx)
		if err == nil {
			c.ExecContext(ctx, "DROP DATABASE IF EXISTS "+quoteMySQLIdent(name))
			c.Close()
		}
	})
	return name
}

func myExec(t testing.TB, c *sql.Conn, qs ...string) {
	t.Helper()
	for _, q := range qs {
		if _, err := c.ExecContext(context.Background(), q); err != nil {
			t.Fatalf("%.300s: %v", q, err)
		}
	}
}

func (s *myServer) password() string { return s.cfg.Passwd }

func (s *myServer) clientBin() (client, dump string) {
	if s.ver.mariadb {
		return "mariadb", "mariadb-dump"
	}
	return "mysql", "mysqldump"
}

// dockerClient 用容器里的官方客户端执行 SQL 文件。
func (s *myServer) dockerClient(t testing.TB, db string, data []byte) {
	t.Helper()
	client, _ := s.clientBin()
	cmd := exec.Command("docker", "exec", "-i", s.container, client, "-uroot", "-p"+s.password(), "--default-character-set=utf8mb4", "--comments", db)
	cmd.Stdin = bytes.NewReader(data)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("官方客户端导入失败: %v\n%s", err, stderr.String())
	}
}

func (s *myServer) dockerDump(t testing.TB, extra ...string) []byte {
	t.Helper()
	_, dump := s.clientBin()
	args := []string{"exec", s.container, dump, "-uroot", "-p" + s.password(), "--single-transaction", "--routines", "--triggers", "--events", "--hex-blob", "--default-character-set=utf8mb4"}
	if !s.ver.mariadb {
		args = append(args, "--set-gtid-purged=OFF")
	}
	args = append(args, extra...)
	cmd := exec.Command("docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("mysqldump 失败: %v\n%s", err, stderr.String())
	}
	return stdout.Bytes()
}

// ---------- 比对 ----------

var reAutoInc = regexp.MustCompile(` AUTO_INCREMENT=\d+`)

func normalizeCreate(s string) string {
	return reAutoInc.ReplaceAllString(s, "")
}

func definerless(s string) string { return stripDefiner(s) }

type myObjects struct {
	tables, views, routines, triggers, events map[string]string
	columns                                   map[string][]string // 表 → 全部列（含生成列、不可见列）
}

func showOne(t testing.TB, c *sql.Conn, q string) map[string]string {
	t.Helper()
	rows, err := c.QueryContext(context.Background(), q)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	if !rows.Next() {
		t.Fatalf("%s 没有结果", q)
	}
	vals := make([]sql.NullString, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	for i, c := range cols {
		m[strings.ToLower(c)] = vals[i].String
	}
	return m
}

func queryStrings(t testing.TB, c *sql.Conn, q string, args ...any) [][]string {
	t.Helper()
	rows, err := c.QueryContext(context.Background(), q, args...)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out [][]string
	for rows.Next() {
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		row := make([]string, len(cols))
		for i, v := range vals {
			row[i] = v.String
		}
		out = append(out, row)
	}
	return out
}

func loadObjects(t testing.TB, c *sql.Conn, db string) *myObjects {
	t.Helper()
	myExec(t, c, "USE "+quoteMySQLIdent(db), "SET SESSION sql_mode = ''")
	o := &myObjects{tables: map[string]string{}, views: map[string]string{}, routines: map[string]string{}, triggers: map[string]string{}, events: map[string]string{}, columns: map[string][]string{}}
	for _, r := range queryStrings(t, c, "SELECT TABLE_NAME, TABLE_TYPE FROM information_schema.TABLES WHERE TABLE_SCHEMA = ?", db) {
		if r[1] == "VIEW" {
			o.views[r[0]] = definerless(showOne(t, c, "SHOW CREATE VIEW "+quoteMySQLIdent(r[0]))["create view"])
		} else {
			o.tables[r[0]] = normalizeCreate(showOne(t, c, "SHOW CREATE TABLE "+quoteMySQLIdent(r[0]))["create table"])
		}
	}
	for _, r := range queryStrings(t, c, "SELECT TABLE_NAME, COLUMN_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME, ORDINAL_POSITION", db) {
		o.columns[r[0]] = append(o.columns[r[0]], r[1])
	}
	for _, r := range queryStrings(t, c, "SELECT ROUTINE_NAME, ROUTINE_TYPE FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = ?", db) {
		m := showOne(t, c, "SHOW CREATE "+r[1]+" "+quoteMySQLIdent(r[0]))
		o.routines[r[1]+" "+r[0]] = m["sql_mode"] + "\n" + definerless(m["create "+strings.ToLower(r[1])])
	}
	for _, r := range queryStrings(t, c, "SELECT TRIGGER_NAME, ACTION_ORDER, EVENT_OBJECT_TABLE FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA = ?", db) {
		m := showOne(t, c, "SHOW CREATE TRIGGER "+quoteMySQLIdent(r[0]))
		o.triggers[r[0]] = r[1] + " " + r[2] + "\n" + m["sql_mode"] + "\n" + definerless(m["sql original statement"])
	}
	for _, r := range queryStrings(t, c, "SELECT EVENT_NAME, STATUS FROM information_schema.EVENTS WHERE EVENT_SCHEMA = ?", db) {
		m := showOne(t, c, "SHOW CREATE EVENT "+quoteMySQLIdent(r[0]))
		o.events[r[0]] = r[1] + "\n" + m["sql_mode"] + "\n" + m["time_zone"] + "\n" + definerless(m["create event"])
	}
	return o
}

func compareMaps(t testing.TB, what string, a, b map[string]string) {
	t.Helper()
	for k, v := range a {
		w, ok := b[k]
		if !ok {
			t.Errorf("%s %q 还原后不存在", what, k)
			continue
		}
		if v != w {
			t.Errorf("%s %q 不一致:\n源: %s\n后: %s", what, k, v, w)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			t.Errorf("%s %q 是多出来的", what, k)
		}
	}
}

// exactRows 用二进制协议（预处理语句）读出全部列（含生成列、不可见列）的精确值，排序后返回。
// FLOAT/DOUBLE 以位模式比较，不经过任何文本格式化，独立于 TableChecksum。
func exactRows(t testing.TB, c *sql.Conn, db, table string, cols []string) []string {
	t.Helper()
	myExec(t, c, "SET SESSION time_zone = '+00:00'", "SET NAMES utf8mb4")
	qs := make([]string, len(cols))
	for i, col := range cols {
		qs[i] = quoteMySQLIdent(col)
	}
	rows, err := c.QueryContext(context.Background(), "SELECT "+strings.Join(qs, ", ")+" FROM "+quoteMySQLIdent(db)+"."+quoteMySQLIdent(table)+" WHERE ? = 1", 1)
	if err != nil {
		t.Fatalf("%s.%s: %v", db, table, err)
	}
	defer rows.Close()
	var out []string
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		for _, v := range vals {
			switch x := v.(type) {
			case nil:
				b.WriteString("NULL")
			case float32:
				fmt.Fprintf(&b, "f32:%08x", math.Float32bits(x))
			case float64:
				fmt.Fprintf(&b, "f64:%016x", math.Float64bits(x))
			case []byte:
				b.WriteString("b:" + hex.EncodeToString(x))
			default:
				fmt.Fprintf(&b, "%T:%v", x, x)
			}
			b.WriteByte('|')
		}
		out = append(out, b.String())
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// compareDBs 全面比对两个库：结构、视图、例程、触发器、事件、逐行精确数据、TableChecksum。
func compareDBs(t testing.TB, ca *sql.Conn, dbA string, cb *sql.Conn, dbB string, skipData map[string]bool) {
	t.Helper()
	oa, ob := loadObjects(t, ca, dbA), loadObjects(t, cb, dbB)
	compareMaps(t, "表", oa.tables, ob.tables)
	compareMaps(t, "视图", oa.views, ob.views)
	compareMaps(t, "例程", oa.routines, ob.routines)
	compareMaps(t, "触发器", oa.triggers, ob.triggers)
	compareMaps(t, "事件", oa.events, ob.events)
	if t.Failed() {
		t.FailNow()
	}
	for tb := range oa.tables {
		if skipData[tb] {
			continue
		}
		ra := exactRows(t, ca, dbA, tb, oa.columns[tb])
		rb := exactRows(t, cb, dbB, tb, ob.columns[tb])
		if len(ra) != len(rb) {
			t.Fatalf("表 %s 行数不同: %d vs %d", tb, len(ra), len(rb))
		}
		for i := range ra {
			if ra[i] != rb[i] {
				t.Fatalf("表 %s 数据不同:\n源: %.1500s\n后: %.1500s", tb, ra[i], rb[i])
			}
		}
		na, ha, err := TableChecksum(context.Background(), ca, MySQL, dbA, tb)
		if err != nil {
			t.Fatal(err)
		}
		nb, hb, err := TableChecksum(context.Background(), cb, MySQL, dbB, tb)
		if err != nil {
			t.Fatal(err)
		}
		if na != nb || ha != hb || na != int64(len(ra)) {
			t.Fatalf("表 %s 校验和不同: %d %s vs %d %s（精确比对 %d 行）", tb, na, ha, nb, hb, len(ra))
		}
	}
}

func checkMyManifest(t testing.TB, c *sql.Conn, db string, dm DBManifest) {
	t.Helper()
	for _, tm := range dm.Tables {
		if tm.Hash == "" {
			continue
		}
		n, h, err := TableChecksum(context.Background(), c, MySQL, db, tm.Name)
		if err != nil {
			t.Fatal(err)
		}
		if n != tm.Rows || h != tm.Hash {
			t.Fatalf("表 %s 与清单不一致: %d %s vs 清单 %d %s", tm.Name, n, h, tm.Rows, tm.Hash)
		}
	}
}

func exportMy(t testing.TB, c *sql.Conn, opts ExportOptions) ([]byte, *Manifest) {
	t.Helper()
	opts.Dialect = MySQL
	if opts.Now == nil {
		opts.Now = fixedNow
	}
	var buf bytes.Buffer
	man, err := Export(context.Background(), c, &buf, opts)
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	return buf.Bytes(), man
}

func importMy(t testing.TB, c *sql.Conn, data []byte, opts ImportOptions) *ImportResult {
	t.Helper()
	opts.Dialect = MySQL
	res, err := Import(context.Background(), c, bytes.NewReader(data), opts)
	if err != nil {
		t.Fatalf("导入失败: %v\n%+v", err, res)
	}
	if res.Errors != 0 {
		t.Fatalf("导入有错误: %+v", res.ErrorList)
	}
	return res
}
