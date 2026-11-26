package conns

import (
	"context"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/driver"
)

// LEMI_TEST_MYSQL_ADDR=127.0.0.1:33184 LEMI_TEST_MYSQL_PASSWORD=... go test ./internal/conns -run MySQL
func mysqlConn(t *testing.T, f *fixture) *Connection {
	addr := os.Getenv("LEMI_TEST_MYSQL_ADDR")
	if addr == "" {
		t.Skip("未设置 LEMI_TEST_MYSQL_ADDR")
	}
	host, port, _ := net.SplitHostPort(addr)
	p, _ := strconv.Atoi(port)
	user := os.Getenv("LEMI_TEST_MYSQL_USER")
	if user == "" {
		user = "root"
	}
	c, err := f.s.Create(context.Background(), f.ws, f.owner.ID, &Input{Name: "mysql", Driver: "mysql",
		Params: map[string]any{"host": host, "port": p, "user": user, "password": os.Getenv("LEMI_TEST_MYSQL_PASSWORD"), "tls_mode": "preferred"}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestMySQLEndToEnd(t *testing.T) {
	f := setup(t)
	c := mysqlConn(t, f)
	ctx := context.Background()
	lvl := f.s.Access(ctx, f.owner, c)
	cs, err := f.s.OpenConsole(ctx, f.owner, c, lvl, "", "ui", false, 30)
	if err != nil {
		t.Fatal(err)
	}
	defer f.s.CloseConsole(cs.ID)
	res := f.s.Exec(ctx, cs, "DROP DATABASE IF EXISTS lemi_conns_test; CREATE DATABASE lemi_conns_test; USE lemi_conns_test;"+
		"CREATE TABLE t (id BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT, name VARCHAR(50) NOT NULL, amount DECIMAL(20,6), f FLOAT, b VARBINARY(10), d DATETIME(3), j JSON, big BIGINT UNSIGNED);"+
		"INSERT INTO t (name, amount, f, b, d, j, big) VALUES ('a', 12345678901234.123456, 0.1, 0x00ff, '2026-11-26 12:00:00.123', '{\"k\": [1,2]}', 18446744073709551615), ('b', NULL, NULL, NULL, NULL, NULL, 0);"+
		"SELECT * FROM t ORDER BY id", ExecOptions{StopOnError: true})
	for _, r := range res {
		if r.Kind == "error" {
			t.Fatalf("%s：%s", r.Statement, r.Error)
		}
	}
	rows := res[len(res)-1].Rows
	if rows[0][2] != "12345678901234.123456" || rows[0][7] != "18446744073709551615" || rows[0][3] != "0.1" {
		t.Fatalf("数值编码丢精度：%v", rows[0])
	}
	if b, ok := rows[0][4].(driver.BinValue); !ok || b.B64 != "AP8=" {
		t.Fatalf("二进制：%#v", rows[0][4])
	}
	// 查询结果可编辑
	r := f.s.Exec(ctx, cs, "SELECT id, name FROM lemi_conns_test.t", ExecOptions{})
	f.s.EditableForQuery(ctx, c, cs, r[0])
	if r[0].Editable == nil || !r[0].Editable.OK || r[0].Editable.Database != "lemi_conns_test" {
		t.Fatalf("应可编辑：%+v", r[0].Editable)
	}
	// 表格提交（含 UPDATE 写入相同值：影响行数按匹配行算）
	ar, err := f.s.Apply(ctx, f.owner, c, lvl, &ApplyRequest{Database: "lemi_conns_test", Table: "t", Changes: []Change{
		{Op: "update", Key: map[string]any{"id": "1"}, Set: map[string]any{"name": "a"}, Old: map[string]any{"name": "a"}},
		{Op: "update", Key: map[string]any{"id": "2"}, Set: map[string]any{"amount": "1.5", "b": map[string]any{"$bin": "AQI="}}},
		{Op: "insert", Values: map[string]any{"name": "c", "big": "42"}},
	}}, "")
	if err != nil || ar.FailedAt != -1 {
		t.Fatalf("提交失败：%+v %v", ar, err)
	}
	// 取消长查询
	cs2, _ := f.s.OpenConsole(ctx, f.owner, c, lvl, "", "ui", false, 0)
	done := make(chan []*driver.Result)
	go func() { done <- f.s.Exec(ctx, cs2, "SELECT SLEEP(20)", ExecOptions{}) }()
	time.Sleep(700 * time.Millisecond)
	if err := cs2.S.Cancel(ctx); err != nil {
		t.Fatalf("取消失败：%v", err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("取消后 5 秒内没有返回")
	}
	f.s.CloseConsole(cs2.ID)
	// 只读会话：数据库层强制
	vlvl := f.s.Access(ctx, f.viewer, c)
	vcs, err := f.s.OpenConsole(ctx, f.viewer, c, vlvl, "lemi_conns_test", "ui", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	r = f.s.Exec(ctx, vcs, "UPDATE t SET name = 'x'", ExecOptions{})
	if r[0].Kind != "error" {
		t.Fatal("只读会话不应能修改")
	}
	if _, err := vcs.S.Conn().ExecContext(ctx, "UPDATE t SET name = 'x'"); err == nil || !strings.Contains(err.Error(), "READ ONLY") {
		t.Fatalf("绕过分类器直接写也应被数据库拒绝：%v", err)
	}
	r = f.s.Exec(ctx, vcs, "SET SESSION TRANSACTION READ WRITE", ExecOptions{})
	if r[0].Kind != "error" {
		t.Fatal("只读会话不能改回读写")
	}
	r = f.s.Exec(ctx, vcs, "SELECT COUNT(*) FROM t", ExecOptions{})
	if r[0].Kind != "rows" || r[0].Rows[0][0] != "3" {
		t.Fatalf("只读会话应能查询：%+v", r[0])
	}
	f.s.CloseConsole(vcs.ID)
	// LOAD DATA LOCAL INFILE 永远不行
	r = f.s.Exec(ctx, cs, "LOAD DATA LOCAL INFILE '/etc/passwd' INTO TABLE t", ExecOptions{})
	if r[0].Kind != "error" {
		t.Fatal("LOAD DATA LOCAL 应被拒绝")
	}
	// 表结构设计器
	sc, _ := f.s.SQL(ctx, c)
	tinfo, err := sc.Table(ctx, "lemi_conns_test", "t")
	if err != nil {
		t.Fatal(err)
	}
	design := driver.TableDesign{Name: "t2", Comment: "改过", Indexes: []driver.IndexInfo{{Name: "idx_name", Columns: []string{"nm"}}}}
	for _, col := range tinfo.Columns {
		cd := driver.ColumnDesign{OrigName: col.Name, Name: col.Name, Type: col.Type, Nullable: col.Nullable, Default: col.Default, AutoIncrement: col.AutoIncrement, PrimaryKey: col.PrimaryKey, Comment: col.Comment}
		if col.Name == "name" {
			cd.Name = "nm"
		}
		if col.Name == "f" {
			continue // 删掉
		}
		design.Columns = append(design.Columns, cd)
	}
	design.Columns = append(design.Columns, driver.ColumnDesign{Name: "added", Type: "int", Nullable: true, Comment: "新列"})
	stmts, err := sc.AlterSQL(ctx, "lemi_conns_test", tinfo, &design)
	if err != nil {
		t.Fatal(err)
	}
	res = f.s.Exec(ctx, cs, strings.Join(stmts, ";\n"), ExecOptions{StopOnError: true})
	for _, x := range res {
		if x.Kind == "error" {
			t.Fatalf("执行设计器生成的语句失败：%s\n%s", x.Error, strings.Join(stmts, "\n"))
		}
	}
	t2, err := sc.Table(ctx, "lemi_conns_test", "t2")
	if err != nil || t2.Comment != "改过" || len(t2.Columns) != 8 {
		t.Fatalf("改表结果不对：%+v %v", t2, err)
	}
	// 监控
	m := sc.(driver.Monitor)
	if ps, err := m.Processes(ctx); err != nil || len(ps) == 0 {
		t.Fatalf("进程列表：%v", err)
	}
	f.s.Exec(ctx, cs, "DROP DATABASE lemi_conns_test", ExecOptions{})
}
