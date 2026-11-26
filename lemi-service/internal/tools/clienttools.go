package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/sqlclass"
	"github.com/LemonITCN/lemi/lemi-service/internal/sqlsplit"
)

// 浏览器端工具：服务端做权限与审批，经 WebSocket client_call 让用户的浏览器在主工作区执行，结果回传。
// 参数与返回的 JSON 约定见 docs/spec.md §24.6。

func init() {
	r := Default
	r.Register(&Tool{
		Name: "workspace_open_table", Title: "在工作区打开表", Kind: KindClient,
		Description: "在用户的主工作区打开一张表的表格页，并设置筛选（WHERE 片段）、排序（ORDER BY 片段）与显示的列。用于把分析结果直接展示给用户看（例如「把有问题的订单打开给我看」）。",
		Schema: obj(map[string]any{
			"connection_id": str("连接 ID"),
			"db":            str("库名（SQLite 用 main）"),
			"table":         str("表名"),
			"where":         str("可选：WHERE 条件片段（不含 WHERE 关键字）"),
			"order_by":      str("可选：ORDER BY 片段（不含 ORDER BY 关键字）"),
			"columns":       arr(str("列名"), "可选：只显示这些列"),
		}, "connection_id", "db", "table"),
		Handler: wsOpenTable,
	})
	r.Register(&Tool{
		Name: "workspace_run_sql", Title: "在工作区执行 SQL", Kind: KindClient, Destructive: true,
		Description: "把 SQL 写进用户主工作区的控制台并执行，结果显示在主表格里（用户能直接看到并继续编辑）。写语句仍按连接的 AI 档位确认。" +
			"只是要数据给你分析时用 run_query，不要用这个。",
		Schema: obj(map[string]any{
			"connection_id": str("连接 ID"),
			"sql":           str("SQL（可多条）"),
			"db":            str("可选：库名"),
			"title":         str("可选：标签标题（简短说明这个查询做什么，例如「按状态统计订单」）"),
			"reason":        str("可选：为什么要执行（写语句的审批卡片上显示）"),
		}, "connection_id", "sql"),
		Handler: wsRunSQL,
	})
	r.Register(&Tool{
		Name: "workspace_update_view", Title: "修改当前视图", Kind: KindClient,
		Description: "修改用户当前（或指定）标签页的筛选、排序、显示的列，或定位到某一行。",
		Schema: obj(map[string]any{
			"tab_id":   str("可选：标签 ID（默认当前标签）"),
			"where":    str("可选：新的 WHERE 片段（空字符串表示清除）"),
			"order_by": str("可选：新的 ORDER BY 片段"),
			"columns":  arr(str("列名"), "可选：显示的列"),
			"goto_row": integer("可选：定位到第几行（0 起）"),
		}),
		Handler: wsUpdateView,
	})
	r.Register(&Tool{
		Name: "workspace_read_view", Title: "读取当前视图", Kind: KindClient,
		Description: "读取用户正在看的标签页：连接、库、表、筛选、排序、控制台 SQL、选中的行与单元格、可见行样本。用户说「这几行」「当前这张表」时先用它。",
		Schema: obj(map[string]any{
			"tab_id":   str("可选：标签 ID（默认当前标签）"),
			"max_rows": integer("可见行样本最多几行（默认 20，最多 100）"),
		}),
		Handler: wsReadView,
	})
}

func (env *Env) visibleIDs() []string {
	list, _ := env.Visible()
	out := make([]string, 0, len(list))
	for _, a := range list {
		out = append(out, a.Conn.ID)
	}
	return out
}

func (env *Env) callClient(tool string, args map[string]any) (map[string]any, error) {
	if env.Client == nil || !env.Client.Online() {
		return nil, fail("用户的浏览器不在线，不能操作工作区")
	}
	args["visible_connections"] = env.visibleIDs()
	raw, err := env.Client.Call(env.Ctx, tool, args)
	if err != nil {
		return nil, fail("浏览器执行失败：%s", errText(err))
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		out = map[string]any{"raw": string(raw)}
	}
	return out, nil
}

func fmtTime(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	return time.UnixMilli(ms).Format("2006-01-02 15:04")
}

func fragmentOK(frag, dialect string) error {
	if strings.TrimSpace(frag) == "" {
		return nil
	}
	if len(sqlsplit.Split(frag+";", sqlsplit.Dialect(dialect))) > 1 {
		return fail("筛选与排序片段里不能有多条语句")
	}
	return nil
}

func wsOpenTable(env *Env, args json.RawMessage) (*Result, error) {
	var in struct {
		ConnectionID string   `json:"connection_id"`
		DB           string   `json:"db"`
		Table        string   `json:"table"`
		Where        string   `json:"where"`
		OrderBy      string   `json:"order_by"`
		Columns      []string `json:"columns"`
	}
	if err := bind(args, &in); err != nil {
		return nil, err
	}
	a, err := env.Access(in.ConnectionID)
	if err != nil {
		return nil, err
	}
	if !a.CanSeeTable(in.DB, in.Table) {
		return nil, fail("表 %s.%s 不存在或不在 AI 库表白名单内", in.DB, in.Table)
	}
	dialect := "mysql"
	if a.Conn.Driver == "sqlite" {
		dialect = "sqlite"
	}
	if err := fragmentOK(in.Where, dialect); err != nil {
		return nil, err
	}
	if err := fragmentOK(in.OrderBy, dialect); err != nil {
		return nil, err
	}
	out, err := env.callClient("workspace_open_table", map[string]any{"connection_id": a.Conn.ID, "db": in.DB, "table": in.Table,
		"where": in.Where, "order_by": in.OrderBy, "columns": in.Columns})
	if err != nil {
		return nil, err
	}
	text := fmt.Sprintf("已在工作区打开 %s.%s（标签 %v），已加载 %v 行", in.DB, in.Table, out["tab_id"], out["rows_loaded"])
	if t, ok := out["total"]; ok && t != nil {
		text += fmt.Sprintf("，共 %v 行", t)
	}
	if e, ok := out["error"].(string); ok && e != "" {
		text += "；页面报错：" + e
	}
	return &Result{Text: text + "。", Data: out}, nil
}

func wsRunSQL(env *Env, args json.RawMessage) (*Result, error) {
	var in struct {
		ConnectionID string `json:"connection_id"`
		SQL          string `json:"sql"`
		DB           string `json:"db"`
		Title        string `json:"title"`
		Reason       string `json:"reason"`
	}
	if err := bind(args, &in); err != nil {
		return nil, err
	}
	a, err := env.Access(in.ConnectionID)
	if err != nil {
		return nil, err
	}
	sc, err := env.sql(a)
	if err != nil {
		return nil, err
	}
	stmts := sqlsplit.Split(in.SQL, sqlsplit.Dialect(sc.Dialect()))
	if len(stmts) == 0 {
		return nil, fail("SQL 为空")
	}
	db := in.DB
	if db == "" && sc.Dialect() == "sqlite" {
		db = "main"
	}
	writes := 0
	for _, st := range stmts {
		info := sqlclass.Classify(st.Text, sc.Dialect())
		if err := a.CheckTables(st.Text, sc.Dialect(), db, info); err != nil {
			return nil, err
		}
		if info.Keyword == "USE" {
			return nil, fail("请用 db 参数指定库，不要在 SQL 里写 USE")
		}
		if info.IsWrite() {
			writes++
			if err := env.checkWrite(env.Ctx, "workspace_run_sql", a, st.Text, db, info, in.Reason, sc); err != nil {
				return nil, err
			}
		}
	}
	title := strings.TrimSpace(in.Title)
	if r := []rune(title); len(r) > 40 {
		title = string(r[:40])
	}
	out, err := env.callClient("workspace_run_sql", map[string]any{"connection_id": a.Conn.ID, "db": in.DB, "sql": in.SQL, "title": title, "approved": writes > 0})
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(out["results"])
	text := fmt.Sprintf("已在工作区控制台执行（标签 %v「%v」）。结果：%s", out["tab_id"], out["title"], string(b))
	return &Result{Text: text, Data: out}, nil
}

func wsUpdateView(env *Env, args json.RawMessage) (*Result, error) {
	var in map[string]any
	if err := bind(args, &in); err != nil {
		return nil, err
	}
	for _, k := range []string{"where", "order_by"} {
		if s, ok := in[k].(string); ok {
			if err := fragmentOK(s, "mysql"); err != nil {
				return nil, err
			}
		}
	}
	out, err := env.callClient("workspace_update_view", in)
	if err != nil {
		return nil, err
	}
	if cid, _ := out["connection_id"].(string); cid != "" {
		if _, err := env.Access(cid); err != nil {
			return nil, fail("这个标签的连接对智能体不可见")
		}
	}
	if e, ok := out["error"].(string); ok && e != "" {
		return nil, fail("修改视图失败：%s", e)
	}
	return &Result{Text: fmt.Sprintf("已更新标签 %v，已加载 %v 行。", out["tab_id"], out["rows_loaded"]), Data: out}, nil
}

func wsReadView(env *Env, args json.RawMessage) (*Result, error) {
	var in struct {
		TabID   string `json:"tab_id"`
		MaxRows int    `json:"max_rows"`
	}
	if err := bind(args, &in); err != nil {
		return nil, err
	}
	if in.MaxRows <= 0 {
		in.MaxRows = 20
	}
	if in.MaxRows > 100 {
		in.MaxRows = 100
	}
	out, err := env.callClient("workspace_read_view", map[string]any{"tab_id": in.TabID, "max_rows": in.MaxRows})
	if err != nil {
		return nil, err
	}
	if e, ok := out["error"].(string); ok && e != "" {
		return nil, fail("读取视图失败：%s", e)
	}
	// 浏览器回传的是用户能看到的数据：仍按智能体权限过滤
	cid, _ := out["connection_id"].(string)
	if cid != "" {
		a, err := env.Access(cid)
		if err != nil {
			return &Result{Text: "用户当前标签的连接对智能体不可见，无法读取内容。", Data: map[string]any{"tab_id": out["tab_id"], "kind": out["kind"]}}, nil
		}
		db, _ := out["db"].(string)
		table, _ := out["table"].(string)
		if table != "" && !a.CanSeeTable(db, table) {
			return &Result{Text: "用户当前标签的表不在 AI 库表白名单内，无法读取内容。", Data: map[string]any{"tab_id": out["tab_id"], "kind": out["kind"]}}, nil
		}
		if len(a.Allow) > 0 {
			if sqlText, _ := out["sql"].(string); sqlText != "" {
				info := sqlclass.Classify(sqlText, map[bool]string{true: "sqlite", false: "mysql"}[a.Conn.Driver == "sqlite"])
				if a.CheckTables(sqlText, map[bool]string{true: "sqlite", false: "mysql"}[a.Conn.Driver == "sqlite"], db, info) != nil {
					delete(out, "visible_rows_sample")
					delete(out, "selection")
					out["note"] = "控制台语句引用了白名单外的表，数据已隐去"
				}
			}
		}
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	return &Result{Text: "用户当前的视图：\n" + string(b), Data: out}, nil
}
