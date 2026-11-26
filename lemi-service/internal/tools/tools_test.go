package tools_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/aitest"
	"github.com/LemonITCN/lemi/lemi-service/internal/conns"
	"github.com/LemonITCN/lemi/lemi-service/internal/sqlclass"
	"github.com/LemonITCN/lemi/lemi-service/internal/tools"
)

func call(t *testing.T, env *tools.Env, name string, args any) *tools.Result {
	t.Helper()
	tl, ok := tools.Default.Get(name)
	if !ok {
		t.Fatalf("没有工具 %s", name)
	}
	b, _ := json.Marshal(args)
	return tools.Default.Call(env, tl, b)
}

func mustOK(t *testing.T, r *tools.Result) *tools.Result {
	t.Helper()
	if r.IsError {
		t.Fatalf("工具出错：%s", r.Text)
	}
	return r
}

func mustErr(t *testing.T, r *tools.Result, contains string) {
	t.Helper()
	if !r.IsError {
		t.Fatalf("应当出错，实际成功：%s", r.Text)
	}
	if contains != "" && !strings.Contains(r.Text, contains) {
		t.Fatalf("错误应包含 %q，实际：%s", contains, r.Text)
	}
}

func TestEffectiveAI(t *testing.T) {
	c := &conns.Connection{ID: "c1", AILevel: conns.AIFull}
	cases := []struct {
		user      string
		readOnly  bool
		grant     tools.Grant
		plan      bool
		allowFull bool
		want      string
	}{
		{conns.LevelManage, false, nil, false, true, conns.AIFull},
		{conns.LevelManage, false, nil, false, false, conns.AIWriteAuto}, // 完全放开需要实例管理员允许
		{conns.LevelRead, false, nil, false, true, conns.AIRead},         // 查看者 / 个人收紧为只读
		{conns.LevelNone, false, nil, false, true, conns.AIHidden},
		{conns.LevelWrite, true, nil, false, true, conns.AIRead}, // 连接设为只读
		{conns.LevelWrite, false, tools.Grant{"c1": conns.AIRead}, false, true, conns.AIRead},
		{conns.LevelWrite, false, tools.Grant{"other": conns.AIFull}, false, true, conns.AIHidden}, // 授权里没有 = 不可见
		{conns.LevelWrite, false, nil, true, true, conns.AIRead},                                   // 计划模式
	}
	for i, tc := range cases {
		cc := *c
		cc.ReadOnly = tc.readOnly
		if got := tools.EffectiveAI(&cc, tc.user, tc.grant, tc.plan, tc.allowFull); got != tc.want {
			t.Errorf("用例 %d：得到 %s，应为 %s", i, got, tc.want)
		}
	}
	hidden := &conns.Connection{ID: "c2", AILevel: conns.AIHidden}
	if got := tools.EffectiveAI(hidden, conns.LevelManage, nil, false, true); got != conns.AIHidden {
		t.Fatalf("不可见的连接对管理员也不可见：%s", got)
	}
	if tools.MaxGrantLevel(conns.AIWriteConfirm) != conns.AIRead || tools.MaxGrantLevel(conns.AIWriteAuto) != conns.AIWriteAuto {
		t.Fatal("授权最高档位计算不对")
	}
}

func TestHiddenConnectionInvisible(t *testing.T) {
	f := aitest.New(t)
	vis := f.SQLite(t, "vis", conns.AIRead, nil)
	hid := f.SQLite(t, "hid", conns.AIHidden, nil)
	env := f.Env(f.Owner)
	r := mustOK(t, call(t, env, "list_connections", map[string]any{}))
	if !strings.Contains(r.Text, vis.ID) || strings.Contains(r.Text, hid.ID) {
		t.Fatalf("list_connections 应只列出可见连接：%s", r.Text)
	}
	mustErr(t, call(t, env, "run_query", map[string]any{"connection_id": hid.ID, "sql": "SELECT 1"}), "不可见")
	mustErr(t, call(t, env, "list_tables", map[string]any{"connection_id": hid.ID, "db": "main"}), "不可见")
	mustErr(t, call(t, env, "describe_tables", map[string]any{"connection_id": hid.ID, "db": "main", "tables": []string{"users"}}), "不可见")
	r = mustOK(t, call(t, env, "search_schema", map[string]any{"query": "email"}))
	if strings.Contains(r.Text, hid.ID) {
		t.Fatalf("search_schema 不应搜索不可见的连接：%s", r.Text)
	}
	// 另一个工作空间的连接也不可见
	env2 := f.Env(f.Owner)
	env2.WorkspaceID = "01jzzzzzzzzzzzzzzzzzzzzzzz"
	mustErr(t, call(t, env2, "run_query", map[string]any{"connection_id": vis.ID, "sql": "SELECT 1"}), "不可见")
}

func TestWhitelist(t *testing.T) {
	f := aitest.New(t)
	c := f.SQLite(t, "wl", conns.AIWriteAuto, []string{"main.orders"})
	env := f.Env(f.Owner)
	r := mustOK(t, call(t, env, "list_tables", map[string]any{"connection_id": c.ID, "db": "main"}))
	if !strings.Contains(r.Text, "orders") || strings.Contains(r.Text, "users") {
		t.Fatalf("白名单外的表不应出现在结构工具里：%s", r.Text)
	}
	r = mustOK(t, call(t, env, "describe_tables", map[string]any{"connection_id": c.ID, "db": "main", "tables": []string{"orders", "users"}}))
	if !strings.Contains(r.Text, "amount") || !strings.Contains(r.Text, "不在 AI 库表白名单内") || strings.Contains(r.Text, "email") {
		t.Fatalf("describe_tables 应隐藏白名单外的表：%s", r.Text)
	}
	mustOK(t, call(t, env, "run_query", map[string]any{"connection_id": c.ID, "sql": "SELECT status, COUNT(*) AS n FROM orders GROUP BY status"}))
	mustErr(t, call(t, env, "run_query", map[string]any{"connection_id": c.ID, "sql": "SELECT * FROM users"}), "白名单")
	mustErr(t, call(t, env, "run_query", map[string]any{"connection_id": c.ID, "sql": "SELECT o.id FROM orders o JOIN users u ON u.id = o.user_id"}), "白名单")
	mustErr(t, call(t, env, "run_query", map[string]any{"connection_id": c.ID, "sql": "SELECT * FROM orders WHERE user_id IN (SELECT id FROM users)"}), "白名单")
	mustErr(t, call(t, env, "run_query", map[string]any{"connection_id": c.ID, "sql": "SELECT name FROM sqlite_master"}), "白名单")
	mustErr(t, call(t, env, "run_query", map[string]any{"connection_id": c.ID, "sql": "PRAGMA table_info(users)"}), "白名单")
	// 公共表表达式的名字不是表
	mustOK(t, call(t, env, "run_query", map[string]any{"connection_id": c.ID, "sql": "WITH paid AS (SELECT * FROM orders WHERE status = 'paid') SELECT COUNT(*) FROM paid"}))
	// 写入也受白名单约束
	mustErr(t, call(t, env, "execute_sql", map[string]any{"connection_id": c.ID, "sql": "DELETE FROM users WHERE id = 1", "reason": "测试"}), "白名单")

	a, err := env.Access(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	stmt := "SELECT EXTRACT(YEAR FROM created_at) FROM orders"
	if err := a.CheckTables(stmt, "mysql", "main", sqlclass.Classify(stmt, "mysql")); err != nil {
		t.Fatalf("EXTRACT(... FROM 列) 不是表引用：%v", err)
	}
	stmt = "SELECT * FROM main.orders, main.users"
	if err := a.CheckTables(stmt, "sqlite", "main", sqlclass.Classify(stmt, "sqlite")); err == nil {
		t.Fatal("逗号连接的白名单外表应拒绝")
	}
}

func TestReadOnlyLevelAndDatabaseLayer(t *testing.T) {
	f := aitest.New(t)
	c := f.SQLite(t, "ro", conns.AIRead, nil)
	env := f.Env(f.Owner)
	mustErr(t, call(t, env, "execute_sql", map[string]any{"connection_id": c.ID, "sql": "DELETE FROM orders", "reason": "测试"}), "只读")
	mustErr(t, call(t, env, "run_query", map[string]any{"connection_id": c.ID, "sql": "DELETE FROM orders"}), "只执行只读查询")
	mustErr(t, call(t, env, "run_query", map[string]any{"connection_id": c.ID, "sql": "SELECT 1; DELETE FROM orders"}), "一次只能执行一条")
	// 数据库层强制只读：run_query 用的会话即使绕过语句分类直接写也会失败
	cs, err := f.Conns.OpenConsole(context.Background(), f.Owner, c, conns.LevelManage, "", "agent", true, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Conns.CloseConsole(cs.ID)
	if _, err := cs.S.Conn().ExecContext(context.Background(), "DELETE FROM orders"); err == nil {
		t.Fatal("只读会话在数据库层应拒绝写入")
	}
	var n int
	_ = cs.S.Conn().QueryRowContext(context.Background(), "SELECT COUNT(*) FROM orders").Scan(&n)
	if n != 120 {
		t.Fatalf("数据不应被修改：%d", n)
	}
}

func TestWriteConfirmApprovals(t *testing.T) {
	f := aitest.New(t)
	c := f.SQLite(t, "wc", conns.AIWriteConfirm, nil)
	env := f.Env(f.Owner)
	var asked []*tools.ApprovalRequest
	reply := func(req *tools.ApprovalRequest) error { return nil }
	env.Approve = func(ctx context.Context, req *tools.ApprovalRequest) error {
		asked = append(asked, req)
		return reply(req)
	}
	r := mustOK(t, call(t, env, "execute_sql", map[string]any{"connection_id": c.ID, "sql": "UPDATE orders SET status = 'x' WHERE id = 1; UPDATE orders SET status = 'y' WHERE id = 2", "reason": "修正状态"}))
	if len(asked) != 2 || asked[0].Class != "dml" || asked[0].Reason == "" || asked[0].Explanation != "修正状态" || !strings.HasPrefix(asked[0].Key, "execute_sql|"+c.ID+"|dml") {
		t.Fatalf("逐条确认档每条写入都要审批：%+v", asked)
	}
	if !strings.Contains(r.Text, "影响 1 行") {
		t.Fatalf("结果应有影响行数：%s", r.Text)
	}
	// 拒绝（不带说明）：结束本步
	reply = func(*tools.ApprovalRequest) error { return tools.ErrRejected }
	r = call(t, env, "execute_sql", map[string]any{"connection_id": c.ID, "sql": "DELETE FROM orders WHERE id = 3", "reason": "删"})
	if !r.Rejected || !r.Halt {
		t.Fatalf("不带说明的拒绝应结束本步：%+v", r)
	}
	// 拒绝（带说明）：作为工具错误回灌
	reply = func(*tools.ApprovalRequest) error { return &tools.RejectedError{Message: "先备份"} }
	r = call(t, env, "execute_sql", map[string]any{"connection_id": c.ID, "sql": "DELETE FROM orders WHERE id = 3", "reason": "删"})
	if !r.Rejected || r.Halt || !strings.Contains(r.Text, "先备份") {
		t.Fatalf("带说明的拒绝应回灌说明：%+v", r)
	}
	// 被拒绝的语句没有执行
	q := mustOK(t, call(t, env, "run_query", map[string]any{"connection_id": c.ID, "sql": "SELECT COUNT(*) FROM orders WHERE id = 3"}))
	if !strings.Contains(q.Text, "\n1\n") {
		t.Fatalf("被拒绝的删除不应执行：%s", q.Text)
	}
	// 无人值守（定时任务）：需要确认的一律拒绝
	env.Approval = tools.ApprovalUnattended
	mustErr(t, call(t, env, "execute_sql", map[string]any{"connection_id": c.ID, "sql": "UPDATE orders SET status = 'z' WHERE id = 4", "reason": "x"}), "无人值守")
	// MCP：逐条确认档不能写
	env.Approval = tools.ApprovalDelegated
	mustErr(t, call(t, env, "execute_sql", map[string]any{"connection_id": c.ID, "sql": "UPDATE orders SET status = 'z' WHERE id = 4", "reason": "x"}), "MCP")
}

func TestWriteAutoAndFull(t *testing.T) {
	f := aitest.New(t)
	c := f.SQLite(t, "wa", conns.AIWriteAuto, nil)
	env := f.Env(f.Owner)
	n := 0
	env.Approve = func(ctx context.Context, req *tools.ApprovalRequest) error { n++; return nil }
	mustOK(t, call(t, env, "execute_sql", map[string]any{"connection_id": c.ID, "sql": "UPDATE orders SET status = 'x' WHERE id = 1", "reason": "x"}))
	if n != 0 {
		t.Fatal("增删改自动档的增删改不需要确认")
	}
	mustOK(t, call(t, env, "execute_sql", map[string]any{"connection_id": c.ID, "sql": "CREATE TABLE t1 (a INTEGER)", "reason": "x"}))
	if n != 1 {
		t.Fatal("增删改自动档的 DDL 需要确认")
	}
	mustOK(t, call(t, env, "execute_sql", map[string]any{"connection_id": c.ID, "sql": "UPDATE orders SET status = 'all'", "reason": "x"}))
	if n != 2 {
		t.Fatal("没有 WHERE 的修改需要确认")
	}
	// 完全放开：实例管理员没允许时按增删改自动处理
	full := f.SQLite(t, "full", conns.AIFull, nil)
	n = 0
	mustOK(t, call(t, env, "execute_sql", map[string]any{"connection_id": full.ID, "sql": "CREATE TABLE t2 (a INTEGER)", "reason": "x"}))
	if n != 1 {
		t.Fatal("没有允许完全放开时 DDL 仍需确认")
	}
	f.AllowFull(t, true)
	mustOK(t, call(t, env, "execute_sql", map[string]any{"connection_id": full.ID, "sql": "CREATE TABLE t3 (a INTEGER)", "reason": "x"}))
	if n != 1 {
		t.Fatal("完全放开档全自动")
	}
	// DDL 的结果附上执行前的表结构
	r := mustOK(t, call(t, env, "execute_sql", map[string]any{"connection_id": full.ID, "sql": "ALTER TABLE orders ADD COLUMN note TEXT", "reason": "x"}))
	if !strings.Contains(r.Text, "执行前的相关表结构") || !strings.Contains(r.Text, "CREATE TABLE orders") {
		t.Fatalf("DDL 应附上执行前的表结构：%s", r.Text)
	}
	// 计划模式禁写
	env.Plan = true
	mustErr(t, call(t, env, "execute_sql", map[string]any{"connection_id": full.ID, "sql": "DELETE FROM orders WHERE id = 1", "reason": "x"}), "计划模式")
}

func TestViewerReadOnlyAndGrantCap(t *testing.T) {
	f := aitest.New(t)
	f.AllowFull(t, true)
	c := f.SQLite(t, "v", conns.AIFull, nil)
	env := f.Env(f.Viewer)
	a, err := env.Access(c.ID)
	if err != nil || a.AI != conns.AIRead {
		t.Fatalf("查看者即使连接档位是完全放开也只能读：%v %v", a, err)
	}
	mustOK(t, call(t, env, "run_query", map[string]any{"connection_id": c.ID, "sql": "SELECT COUNT(*) FROM users"}))
	mustErr(t, call(t, env, "execute_sql", map[string]any{"connection_id": c.ID, "sql": "DELETE FROM users WHERE id = 1", "reason": "x"}), "只读")
	// 授权档位上限（MCP）
	env = f.Env(f.Owner)
	env.Approval = tools.ApprovalDelegated
	env.Grant = tools.Grant{c.ID: conns.AIRead}
	mustErr(t, call(t, env, "execute_sql", map[string]any{"connection_id": c.ID, "sql": "DELETE FROM users WHERE id = 1", "reason": "x"}), "只读")
	env.Grant = tools.Grant{c.ID: conns.AIWriteAuto}
	mustOK(t, call(t, env, "execute_sql", map[string]any{"connection_id": c.ID, "sql": "CREATE TABLE mcp_t (a INTEGER)", "reason": "x"})) // DDL 由外部智能体确认
	env.Grant = tools.Grant{}
	mustErr(t, call(t, env, "run_query", map[string]any{"connection_id": c.ID, "sql": "SELECT 1"}), "不可见")
}

func TestQueryResultsAndReadResult(t *testing.T) {
	f := aitest.New(t)
	c := f.SQLite(t, "q", conns.AIRead, nil)
	env := f.Env(f.Owner)
	env.PreviewRows = 5
	r := mustOK(t, call(t, env, "run_query", map[string]any{"connection_id": c.ID, "sql": "SELECT id, amount, status FROM orders ORDER BY id", "title": "全部订单"}))
	data := r.Data.(map[string]any)
	rid := data["result_id"].(string)
	if data["row_count"].(int) != 120 || strings.Count(r.Text, "\n") > 12 {
		t.Fatalf("模型只应看到前几行：%s", r.Text)
	}
	r = mustOK(t, call(t, env, "read_result", map[string]any{"result_id": rid, "filters": []map[string]any{{"column": "status", "op": "=", "value": "refunded"}},
		"order_by": []map[string]any{{"column": "amount", "desc": true}}, "limit": 3, "columns": []string{"id", "amount"}}))
	if !strings.Contains(r.Text, "符合条件 17 行") || !strings.Contains(r.Text, "119\t1190.5") {
		t.Fatalf("read_result 过滤排序不对：%s", r.Text)
	}
	// 别人的结果读不到
	env2 := f.Env(f.Member)
	mustErr(t, call(t, env2, "read_result", map[string]any{"result_id": rid}), "不存在")
	// preview_rows 上限 500
	r = mustOK(t, call(t, env, "run_query", map[string]any{"connection_id": c.ID, "sql": "SELECT * FROM orders", "preview_rows": 1000}))
	if strings.Count(r.Text, "\n") < 120 {
		t.Fatalf("要求更多预览行时应给出：%d 行", strings.Count(r.Text, "\n"))
	}
}

func TestOutputTruncation(t *testing.T) {
	f := aitest.New(t)
	env := f.Env(f.Owner)
	big := strings.Repeat("一行很长的输出\n", 3000)
	tl := &tools.Tool{Name: "test_big", Kind: tools.KindRead, Schema: map[string]any{"type": "object", "properties": map[string]any{}},
		Handler: func(env *tools.Env, args json.RawMessage) (*tools.Result, error) {
			return &tools.Result{Text: big}, nil
		}}
	r := tools.Default.Call(env, tl, json.RawMessage(`{}`))
	if r.Truncated == "" || strings.Count(r.Text, "\n") > tools.MaxOutputLines+5 || !strings.Contains(r.Text, "read_result") {
		t.Fatalf("超过 2000 行应截断并存为结果：%d 行", strings.Count(r.Text, "\n"))
	}
	r2 := mustOK(t, call(t, env, "read_result", map[string]any{"result_id": r.Truncated, "offset": 2990, "limit": 100}))
	if !strings.Contains(r2.Text, "共 3001 行") {
		t.Fatalf("文本结果应能按行分页：%s", r2.Text[:200])
	}
}

func TestRepair(t *testing.T) {
	tl, _ := tools.Default.Get("run_query")
	fixed, problems := tools.Repair(tl.Schema, json.RawMessage(`{"connection_id":"c","sql":"select 1","preview_rows":"20","extra":1,"title":null}`))
	if len(problems) > 0 {
		t.Fatalf("应能修复：%v", problems)
	}
	var m map[string]any
	_ = json.Unmarshal(fixed, &m)
	if m["preview_rows"] != float64(20) || m["extra"] != nil || m["title"] != nil {
		t.Fatalf("修复结果不对：%s", fixed)
	}
	dt, _ := tools.Default.Get("describe_tables")
	fixed, problems = tools.Repair(dt.Schema, json.RawMessage(`{"connection_id":"c","db":"main","tables":"[\"a\",\"b\"]","include_ddl":"false",}`))
	if len(problems) > 0 || !strings.Contains(string(fixed), `"tables":["a","b"]`) || !strings.Contains(string(fixed), `"include_ddl":false`) {
		t.Fatalf("字符串化的数组、字符串布尔、结尾逗号应修复：%s %v", fixed, problems)
	}
	fixed, problems = tools.Repair(dt.Schema, json.RawMessage(`{"connection_id":"c","db":"main","tables":"users"}`))
	if len(problems) > 0 || !strings.Contains(string(fixed), `"tables":["users"]`) {
		t.Fatalf("单个值应包成数组：%s %v", fixed, problems)
	}
	_, problems = tools.Repair(tl.Schema, json.RawMessage(`{"sql":1.5,"preview_rows":"abc"}`))
	if len(problems) < 2 {
		t.Fatalf("修不好的应列出问题：%v", problems)
	}
	_, problems = tools.Repair(tl.Schema, json.RawMessage(`{"connection_id":`))
	if len(problems) != 1 || !strings.Contains(problems[0], "JSON") {
		t.Fatalf("非法 JSON 应报错：%v", problems)
	}
	rr, _ := tools.Default.Get("read_result")
	fixed, problems = tools.Repair(rr.Schema, json.RawMessage(`{"result_id":"x","filters":[{"column":"a","op":"CONTAINS","value":"b"}]}`))
	if len(problems) > 0 || !strings.Contains(string(fixed), `"op":"contains"`) {
		t.Fatalf("枚举大小写应修正：%s %v", fixed, problems)
	}
	// 通过 Call 时，修不好的参数作为工具错误回灌（附原参数）
	f := aitest.New(t)
	r := tools.Default.Call(f.Env(f.Owner), tl, json.RawMessage(`{"sql":"select 1"}`))
	if !r.IsError || !strings.Contains(r.Text, "缺少必填字段") || !strings.Contains(r.Text, "原参数") {
		t.Fatalf("应回灌可读错误：%s", r.Text)
	}
}

func TestResolveToolName(t *testing.T) {
	f := aitest.New(t)
	list := tools.Default.List(f.Env(f.Owner), tools.ProfileAgent)
	for in, want := range map[string]string{"run_query": "run_query", "Run_Query": "run_query", "RUNQUERY": "run_query", "query": "run_query", "TodoWrite": "todo_write", "describe_table": "describe_tables"} {
		tl, ok := tools.Resolve(in, list)
		if !ok || tl.Name != want {
			t.Errorf("%s 应修正为 %s", in, want)
		}
	}
	if _, ok := tools.Resolve("delete_everything", list); ok {
		t.Fatal("未知工具不应匹配")
	}
	// 各类调用方的工具集
	names := func(p tools.Profile) string {
		var out []string
		for _, tl := range tools.Default.List(f.Env(f.Owner), p) {
			out = append(out, tl.Name)
		}
		return "," + strings.Join(out, ",") + ","
	}
	mcp := names(tools.ProfileMCP)
	for _, n := range []string{"workspace_", "todo_write", ",task,", "ask_user", "list_backups"} {
		if strings.Contains(mcp, n) {
			t.Errorf("MCP 不应有 %s：%s", n, mcp)
		}
	}
	sub := names(tools.ProfileSub)
	for _, n := range []string{"execute_sql", ",task,", "ask_user", "report_write", "todo_write"} {
		if strings.Contains(sub, n) {
			t.Errorf("子智能体不应有 %s：%s", n, sub)
		}
	}
	if strings.Contains(names(tools.ProfileAgent), "workspace_") {
		t.Error("浏览器不在线时不应提供 workspace_* 工具")
	}
}

type fakeBackup struct{ got []string }

func (b *fakeBackup) AIListBackups(ctx context.Context, u *accounts.User, ws string, ids []string, limit int) (any, error) {
	b.got = ids
	return map[string]any{"backups": len(ids)}, nil
}
func (b *fakeBackup) AIBackupStatus(ctx context.Context, u *accounts.User, ws string, ids []string) (any, error) {
	return nil, nil
}
func (b *fakeBackup) AIHealthStatus(ctx context.Context, u *accounts.User, ws string, ids []string) (any, error) {
	b.got = ids
	return map[string]any{"ok": true}, nil
}

func TestChartsAndReports(t *testing.T) {
	f := aitest.New(t)
	c := f.SQLite(t, "ch", conns.AIRead, nil)
	env := f.Env(f.Owner)
	r := mustOK(t, call(t, env, "run_query", map[string]any{"connection_id": c.ID, "sql": "SELECT city, COUNT(*) AS n FROM users GROUP BY city ORDER BY city"}))
	rid := r.Data.(map[string]any)["result_id"].(string)
	r = mustOK(t, call(t, env, "create_chart", map[string]any{"title": "城市分布", "result_id": rid, "encode": map[string]any{"x": "city", "y": "n"},
		"option": map[string]any{"series": []any{map[string]any{"type": "bar"}}, "tooltip": map[string]any{"formatter": "<b>{b}</b>: {c}"},
			"graphic": []any{map[string]any{"type": "image", "style": map[string]any{"image": "https://evil.example/x.png"}}}}}))
	cid := r.Data.(map[string]any)["chart_id"].(string)
	ch, err := f.Tools.Charts.Get(context.Background(), cid)
	if err != nil {
		t.Fatal(err)
	}
	view := f.Tools.Charts.View(ch)
	b, _ := json.Marshal(view)
	s := string(b)
	if strings.Contains(s, "<b>") || !strings.Contains(s, `"renderMode":"richText"`) || strings.Contains(s, "evil.example") || !strings.Contains(s, `"北京"`) || !strings.Contains(s, `"encode":{"x":"city","y":"n"}`) {
		t.Fatalf("图表清洗或填充不对：%s", s)
	}
	mustErr(t, call(t, env, "create_chart", map[string]any{"title": "x", "result_id": rid, "option": map[string]any{"series": []any{map[string]any{"type": "custom"}}}}), "不支持的系列类型")
	mustErr(t, call(t, env, "create_chart", map[string]any{"title": "x", "result_id": rid, "encode": map[string]any{"x": "nope"}, "option": map[string]any{}}), "不在数据里")
	// 3D
	mustOK(t, call(t, env, "create_chart", map[string]any{"title": "3D", "connection_id": c.ID, "sql": "SELECT id, user_id, amount FROM orders LIMIT 20",
		"option": map[string]any{"series": []any{map[string]any{"type": "scatter3D", "encode": map[string]any{"x": "id", "y": "user_id", "z": "amount"}}}}}))
	// 报告：引用块做快照，导出 Markdown 时表格内嵌
	r = mustOK(t, call(t, env, "er_diagram", map[string]any{"connection_id": c.ID, "db": "main"}))
	did := r.Data.(map[string]any)["diagram_id"].(string)
	if !strings.Contains(r.Text, "orders }o--|| users") {
		t.Fatalf("ER 图应按外键生成关系：%s", r.Text)
	}
	body := fmt.Sprintf("## 结论\n北京用户最多。\n\n```lemi-chart %s```\n\n```lemi-table %s```\n\n```lemi-diagram %s\n表关系\n```\n", cid, rid, did)
	r = mustOK(t, call(t, env, "report_write", map[string]any{"title": "用户分布", "body": body}))
	repID := r.Data.(map[string]any)["report_id"].(string)
	if strings.Contains(r.Text, "注意") {
		t.Fatalf("引用都应找到：%s", r.Text)
	}
	mustOK(t, call(t, env, "report_write", map[string]any{"report_id": repID, "edits": []map[string]any{{"old": "北京用户最多。", "new": "北京、上海、深圳用户各占三分之一。"}}}))
	rp, _ := f.Tools.Reports.Get(context.Background(), repID)
	sn := rp.Decode()
	if len(sn.Charts) != 1 || len(sn.Tables) != 1 || len(sn.Diagrams) != 1 || !strings.Contains(rp.Body, "三分之一") {
		t.Fatalf("报告快照不对：%+v", sn)
	}
	md := f.Tools.Reports.Markdown(rp)
	if !strings.Contains(md, "| city | n |") || !strings.Contains(md, "```mermaid") || strings.Contains(md, "lemi-table") {
		t.Fatalf("Markdown 导出不对：%s", md)
	}
	// 查看者不能写报告
	mustErr(t, call(t, f.Env(f.Viewer), "report_write", map[string]any{"title": "x", "body": "y"}), "权限")
}

func TestBackupToolsOptional(t *testing.T) {
	f := aitest.New(t)
	for _, tl := range tools.Default.List(f.Env(f.Owner), tools.ProfileAgent) {
		if tl.Name == "list_backups" {
			t.Fatal("没有备份模块时不应出现备份工具")
		}
	}
	vis := f.SQLite(t, "b1", conns.AIRead, nil)
	hid := f.SQLite(t, "b2", conns.AIHidden, nil)
	fb := &fakeBackup{}
	f.Tools.Backup = fb
	env := f.Env(f.Owner)
	found := false
	for _, tl := range tools.Default.List(env, tools.ProfileMCP) {
		if tl.Name == "list_backups" {
			found = true
		}
	}
	if !found {
		t.Fatal("有备份模块时应出现备份工具")
	}
	mustOK(t, call(t, env, "list_backups", map[string]any{}))
	if len(fb.got) != 1 || fb.got[0] != vis.ID {
		t.Fatalf("只应把可见连接交给备份模块：%v", fb.got)
	}
	mustErr(t, call(t, env, "health_status", map[string]any{"connection_id": hid.ID}), "不可见")
}
