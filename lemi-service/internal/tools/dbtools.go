package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/conns"
	"github.com/LemonITCN/lemi/lemi-service/internal/driver"
	"github.com/LemonITCN/lemi/lemi-service/internal/results"
	"github.com/LemonITCN/lemi/lemi-service/internal/sqlclass"
	"github.com/LemonITCN/lemi/lemi-service/internal/sqlsplit"
)

func init() {
	r := Default
	r.Register(&Tool{
		Name: "list_connections", Title: "可用连接", Kind: KindRead,
		Description: "列出当前工作空间里你可以访问的数据库连接：ID、名称、引擎、环境标签（prod 为生产库）、你在该连接上的 AI 档位（只读 / 读写·逐条确认 / 读写·增删改自动 / 完全放开）与库表白名单。开始任何数据库工作前先调用它。",
		Schema:      obj(map[string]any{}),
		Handler:     listConnections,
	})
	r.Register(&Tool{
		Name: "list_databases", Title: "库列表", Kind: KindRead,
		Description: "列出连接上的库（MySQL 的 schema；SQLite 只有 main），带表数量与大小。",
		Schema:      obj(map[string]any{"connection_id": str("连接 ID")}, "connection_id"),
		Handler:     listDatabases,
	})
	r.Register(&Tool{
		Name: "list_tables", Title: "表列表", Kind: KindRead,
		Description: "列出库里的表与视图，带估计行数、大小、注释。pattern 可按名称过滤（支持 * 与 % 通配，不区分大小写）。",
		Schema: obj(map[string]any{
			"connection_id": str("连接 ID"),
			"db":            str("库名（SQLite 用 main）"),
			"pattern":       str("可选：表名过滤，例如 order* 或 %user%"),
		}, "connection_id", "db"),
		Handler: listTables,
	})
	r.Register(&Tool{
		Name: "describe_tables", Title: "表结构", Kind: KindRead,
		Description: "一次查看多张表（最多 20 张）的完整结构：列（类型、可空、默认值、注释、主键、自增）、索引、外键、被哪些表引用；include_ddl=true 时附上建表语句。写查询前先用它弄清列名与关联关系。",
		Schema: obj(map[string]any{
			"connection_id": str("连接 ID"),
			"db":            str("库名（SQLite 用 main）"),
			"tables":        arr(str("表名"), "表名列表"),
			"include_ddl":   boolean("是否附上建表语句（默认否）"),
		}, "connection_id", "db", "tables"),
		Handler: describeTables,
	})
	r.Register(&Tool{
		Name: "search_schema", Title: "搜索结构", Kind: KindRead,
		Description: "按名称或注释在连接的全部库里搜索表与列（例如找所有包含 user_id 的表、注释里有「订单」的表）。不指定 connection_id 时搜索全部可见连接。",
		Schema: obj(map[string]any{
			"query":         str("关键字（不区分大小写，按包含匹配）"),
			"connection_id": str("可选：只搜索这个连接"),
			"db":            str("可选：只搜索这个库"),
			"limit":         integer("最多返回多少条（默认 100）"),
		}, "query"),
		Handler: searchSchema,
	})
	r.Register(&Tool{
		Name: "run_query", Title: "执行查询", Kind: KindRead,
		Description: "执行一条只读查询（SELECT / WITH / SHOW / DESCRIBE / EXPLAIN）。在数据库层强制只读，超时按系统设置（默认 60 秒）。" +
			"完整结果（最多 10 万行）保存在服务端并返回结果 ID，你只会看到前 preview_rows 行（默认 50，最多 500）；需要更多时用 read_result 分页读取、过滤、排序，" +
			"画图用 create_chart(result_id)，写报告用 ```lemi-table <result_id>```，数据不必经过你。能在数据库里聚合的就在 SQL 里聚合（GROUP BY、COUNT、SUM），不要把大量原始行拉出来。",
		Schema: obj(map[string]any{
			"connection_id": str("连接 ID"),
			"sql":           str("一条只读 SQL"),
			"db":            str("可选：在哪个库里执行（SQLite 用 main）"),
			"preview_rows":  integer("给你看的行数（默认 50，最多 500）"),
			"title":         str("可选：这个结果的简短说明（报告与界面里显示）"),
		}, "connection_id", "sql"),
		Handler: runQuery,
	})
	r.Register(&Tool{
		Name: "read_result", Title: "读取结果", Kind: KindRead,
		Description: "按结果 ID 读取已保存的查询结果或被截断的长输出。查询结果可分页（offset/limit，limit 最多 500）、只取某些列、按条件过滤（filters）、排序（order_by），返回过滤后的总行数；文本结果按行分页（limit 最多 2000 行）。",
		Schema: obj(map[string]any{
			"result_id": str("结果 ID"),
			"offset":    integer("从第几行开始（0 起）"),
			"limit":     integer("读多少行"),
			"columns":   arr(str("列名"), "可选：只取这些列"),
			"filters": arr(obj(map[string]any{
				"column": str("列名"),
				"op":     enum("运算", "=", "!=", ">", ">=", "<", "<=", "contains", "starts_with", "is_null", "not_null"),
				"value":  map[string]any{"type": []string{"string", "number", "boolean", "null"}, "description": "比较的值"},
			}, "column", "op"), "可选：过滤条件（全部满足）"),
			"order_by": arr(obj(map[string]any{"column": str("列名"), "desc": boolean("是否降序")}, "column"), "可选：排序"),
		}, "result_id"),
		Handler: readResult,
	})
	r.Register(&Tool{
		Name: "explain_query", Title: "执行计划", Kind: KindRead,
		Description: "查看一条语句的执行计划（MySQL EXPLAIN FORMAT=JSON，SQLite EXPLAIN QUERY PLAN），不真正执行语句。用来分析慢查询、判断索引是否命中。",
		Schema: obj(map[string]any{
			"connection_id": str("连接 ID"),
			"sql":           str("要分析的语句"),
			"db":            str("可选：库名"),
		}, "connection_id", "sql"),
		Handler: explainQuery,
	})
	r.Register(&Tool{
		Name: "execute_sql", Title: "执行写入", Kind: KindWrite, Destructive: true,
		Description: "执行会修改数据或结构的 SQL（INSERT / UPDATE / DELETE / DDL 等，可多条，遇错即停）。按连接的 AI 档位决定是否需要用户确认：" +
			"逐条确认档每条都确认；增删改自动档的增删改自动执行、DDL 与没有 WHERE 的修改需要确认；完全放开档全自动。" +
			"执行前先用 run_query 确认影响范围，并在 reason 里写清楚为什么要改、会影响什么。DDL 的结果里会附上执行前的表结构。",
		Schema: obj(map[string]any{
			"connection_id": str("连接 ID"),
			"sql":           str("要执行的 SQL（可多条，分号分隔）"),
			"db":            str("可选：在哪个库里执行"),
			"reason":        str("为什么要执行、预计影响（审批卡片上展示给用户）"),
		}, "connection_id", "sql", "reason"),
		Handler: executeSQL,
	})
	r.Alias("query", "run_query")
	r.Alias("sql", "run_query")
	r.Alias("select", "run_query")
	r.Alias("execute", "execute_sql")
	r.Alias("exec", "execute_sql")
	r.Alias("describe_table", "describe_tables")
	r.Alias("describe", "describe_tables")
	r.Alias("explain", "explain_query")
	r.Alias("tables", "list_tables")
	r.Alias("databases", "list_databases")
	r.Alias("connections", "list_connections")
	r.Alias("get_result", "read_result")
}

func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func listConnections(env *Env, _ json.RawMessage) (*Result, error) {
	list, err := env.Visible()
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return &Result{Text: "当前工作空间里没有你可以访问的连接（连接的 AI 档位可能是「不可见」，或者你没有权限）。", Data: map[string]any{"connections": []any{}}}, nil
	}
	var b strings.Builder
	var data []map[string]any
	for _, a := range list {
		c := a.Conn
		env := c.Env
		if env == "" {
			env = "-"
		}
		fmt.Fprintf(&b, "- %s「%s」 引擎 %s，环境 %s，AI 档位 %s", c.ID, c.Name, c.Driver, env, Label(a.AI))
		if c.ReadOnly {
			b.WriteString("，连接设为只读")
		}
		if len(a.Allow) > 0 {
			b.WriteString("，库表白名单：" + strings.Join(a.Allow, ", "))
		}
		if c.Notes != "" {
			n := []rune(c.Notes)
			if len(n) > 200 {
				n = append(n[:200], '…')
			}
			b.WriteString("，备注：" + strings.ReplaceAll(string(n), "\n", " "))
		}
		b.WriteString("\n")
		data = append(data, map[string]any{"id": c.ID, "name": c.Name, "driver": c.Driver, "env": c.Env, "ai_level": a.AI, "read_only": c.ReadOnly, "allow": a.Allow})
	}
	return &Result{Text: b.String(), Data: map[string]any{"connections": data}}, nil
}

func (env *Env) sql(a *ConnAccess) (driver.SQLConn, error) {
	sc, err := env.Svc.Conns.SQL(env.Ctx, a.Conn)
	if err != nil {
		return nil, fail("打开连接「%s」失败：%s", a.Conn.Name, errText(err))
	}
	return sc, nil
}

func listDatabases(env *Env, args json.RawMessage) (*Result, error) {
	var in struct {
		ConnectionID string `json:"connection_id"`
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
	list, err := sc.Databases(env.Ctx)
	if err != nil {
		return nil, fail("读取库列表失败：%s", errText(err))
	}
	var b strings.Builder
	var data []driver.DatabaseInfo
	for _, d := range list {
		if !a.CanSeeDB(d.Name) {
			continue
		}
		data = append(data, d)
		fmt.Fprintf(&b, "- %s", d.Name)
		if d.System {
			b.WriteString("（系统库）")
		}
		if d.Tables > 0 {
			fmt.Fprintf(&b, " %d 张表", d.Tables)
		}
		if d.SizeBytes > 0 {
			fmt.Fprintf(&b, " %s", humanBytes(d.SizeBytes))
		}
		if d.Charset != "" {
			fmt.Fprintf(&b, " %s", d.Charset)
		}
		b.WriteString("\n")
	}
	if len(data) == 0 {
		return &Result{Text: "没有可见的库。"}, nil
	}
	return &Result{Text: b.String(), Data: map[string]any{"databases": data}}, nil
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func matchPattern(pattern, name string) bool {
	p := strings.ToLower(strings.TrimSpace(pattern))
	if p == "" {
		return true
	}
	p = strings.ReplaceAll(strings.ReplaceAll(p, "%", "*"), "_", "?")
	if !strings.ContainsAny(p, "*?") {
		return strings.Contains(strings.ToLower(name), p)
	}
	ok, _ := path.Match(p, strings.ToLower(name))
	return ok
}

func listTables(env *Env, args json.RawMessage) (*Result, error) {
	var in struct {
		ConnectionID string `json:"connection_id"`
		DB           string `json:"db"`
		Pattern      string `json:"pattern"`
	}
	if err := bind(args, &in); err != nil {
		return nil, err
	}
	a, err := env.Access(in.ConnectionID)
	if err != nil {
		return nil, err
	}
	if !a.CanSeeDB(in.DB) {
		return nil, fail("库 %s 不存在或不在 AI 库表白名单内", in.DB)
	}
	sc, err := env.sql(a)
	if err != nil {
		return nil, err
	}
	objs, err := sc.Objects(env.Ctx, in.DB)
	if err != nil {
		return nil, fail("读取表列表失败：%s", errText(err))
	}
	var b strings.Builder
	var data []map[string]any
	n := 0
	add := func(kind string, list []driver.ObjectInfo) {
		for _, o := range list {
			if !a.CanSeeTable(in.DB, o.Name) || !matchPattern(in.Pattern, o.Name) {
				continue
			}
			n++
			if n > 1000 {
				continue
			}
			fmt.Fprintf(&b, "- %s", o.Name)
			if kind == "view" {
				b.WriteString("（视图）")
			}
			if o.Rows > 0 {
				fmt.Fprintf(&b, " 约 %d 行", o.Rows)
			}
			if o.SizeBytes > 0 {
				fmt.Fprintf(&b, " %s", humanBytes(o.SizeBytes))
			}
			if o.Comment != "" {
				fmt.Fprintf(&b, " — %s", strings.ReplaceAll(o.Comment, "\n", " "))
			}
			b.WriteString("\n")
			data = append(data, map[string]any{"name": o.Name, "kind": kind, "rows": o.Rows, "size_bytes": o.SizeBytes, "comment": o.Comment})
		}
	}
	add("table", objs.Tables)
	add("view", objs.Views)
	if n == 0 {
		return &Result{Text: "没有匹配的表。"}, nil
	}
	if n > 1000 {
		fmt.Fprintf(&b, "…共 %d 张，只列出前 1000 张，请用 pattern 缩小范围\n", n)
	}
	return &Result{Text: fmt.Sprintf("库 %s 共 %d 张表/视图（行数为估计值）：\n", in.DB, n) + b.String(), Data: map[string]any{"tables": data}}, nil
}

func describeTables(env *Env, args json.RawMessage) (*Result, error) {
	var in struct {
		ConnectionID string   `json:"connection_id"`
		DB           string   `json:"db"`
		Tables       []string `json:"tables"`
		IncludeDDL   bool     `json:"include_ddl"`
	}
	if err := bind(args, &in); err != nil {
		return nil, err
	}
	if len(in.Tables) == 0 {
		return nil, fail("tables 不能为空")
	}
	if len(in.Tables) > 20 {
		return nil, fail("一次最多 20 张表，请分批查看")
	}
	a, err := env.Access(in.ConnectionID)
	if err != nil {
		return nil, err
	}
	sc, err := env.sql(a)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	var data []*driver.TableInfo
	for _, name := range in.Tables {
		if !a.CanSeeTable(in.DB, name) {
			fmt.Fprintf(&b, "## %s\n表不存在或不在 AI 库表白名单内\n\n", name)
			continue
		}
		t, err := sc.Table(env.Ctx, in.DB, name)
		if err != nil {
			fmt.Fprintf(&b, "## %s\n读取失败：%s\n\n", name, errText(err))
			continue
		}
		kind := "表"
		if t.Kind == "view" {
			kind = "视图"
		}
		fmt.Fprintf(&b, "## %s.%s（%s", in.DB, t.Name, kind)
		if t.RowsEst > 0 {
			fmt.Fprintf(&b, "，约 %d 行", t.RowsEst)
		}
		if t.Engine != "" {
			fmt.Fprintf(&b, "，%s", t.Engine)
		}
		b.WriteString("）")
		if t.Comment != "" {
			b.WriteString(" " + t.Comment)
		}
		b.WriteString("\n列：\n")
		for _, c := range t.Columns {
			fmt.Fprintf(&b, "- %s %s", c.Name, c.Type)
			if !c.Nullable {
				b.WriteString(" NOT NULL")
			}
			if c.PrimaryKey {
				b.WriteString(" 主键")
			}
			if c.AutoIncrement {
				b.WriteString(" 自增")
			}
			if c.Default != nil {
				fmt.Fprintf(&b, " 默认 %s", *c.Default)
			}
			if c.Generated != "" {
				fmt.Fprintf(&b, " 生成列(%s)", c.Generated)
			}
			if len(c.EnumValues) > 0 {
				fmt.Fprintf(&b, " 取值 %s", strings.Join(c.EnumValues, "/"))
			}
			if c.Comment != "" {
				b.WriteString(" — " + strings.ReplaceAll(c.Comment, "\n", " "))
			}
			b.WriteString("\n")
		}
		if len(t.Indexes) > 0 {
			b.WriteString("索引：")
			for i, x := range t.Indexes {
				if i > 0 {
					b.WriteString("；")
				}
				switch {
				case x.Primary:
					b.WriteString("PRIMARY")
				case x.Unique:
					b.WriteString("UNIQUE " + x.Name)
				default:
					b.WriteString(x.Name)
					if x.Type != "" && x.Type != "BTREE" {
						b.WriteString(" " + x.Type)
					}
				}
				b.WriteString("(" + strings.Join(x.Columns, ", ") + ")")
			}
			b.WriteString("\n")
		}
		for _, fk := range t.ForeignKeys {
			fmt.Fprintf(&b, "外键 %s：(%s) → %s(%s)", fk.Name, strings.Join(fk.Columns, ", "), strings.Trim(fk.RefDB+"."+fk.RefTable, "."), strings.Join(fk.RefColumns, ", "))
			if fk.OnDelete != "" {
				b.WriteString(" ON DELETE " + fk.OnDelete)
			}
			b.WriteString("\n")
		}
		for _, fk := range t.Referenced {
			if a.CanSeeTable(in.DB, fk.Table) {
				fmt.Fprintf(&b, "被引用：%s(%s) → (%s)\n", fk.Table, strings.Join(fk.Columns, ", "), strings.Join(fk.RefColumns, ", "))
			}
		}
		if in.IncludeDDL {
			ddl := t.DDL
			if ddl == "" {
				kindName := "table"
				if t.Kind == "view" {
					kindName = "view"
				}
				ddl, _ = sc.DDL(env.Ctx, in.DB, kindName, t.Name)
			}
			if ddl != "" {
				b.WriteString("DDL：\n" + ddl + "\n")
			}
		}
		b.WriteString("\n")
		t.DDL = ""
		data = append(data, t)
	}
	return &Result{Text: b.String(), Data: map[string]any{"tables": data}}, nil
}

func searchSchema(env *Env, args json.RawMessage) (*Result, error) {
	var in struct {
		Query        string `json:"query"`
		ConnectionID string `json:"connection_id"`
		DB           string `json:"db"`
		Limit        int    `json:"limit"`
	}
	if err := bind(args, &in); err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(in.Query))
	if q == "" {
		return nil, fail("query 不能为空")
	}
	if in.Limit <= 0 || in.Limit > 500 {
		in.Limit = 100
	}
	var targets []*ConnAccess
	if in.ConnectionID != "" {
		a, err := env.Access(in.ConnectionID)
		if err != nil {
			return nil, err
		}
		targets = []*ConnAccess{a}
	} else {
		list, err := env.Visible()
		if err != nil {
			return nil, err
		}
		targets = list
	}
	var b strings.Builder
	found := 0
	ctx, cancel := context.WithTimeout(env.Ctx, 60*time.Second)
	defer cancel()
	for _, a := range targets {
		if found >= in.Limit {
			break
		}
		sc, err := env.Svc.Conns.SQL(ctx, a.Conn)
		if err != nil {
			fmt.Fprintf(&b, "（连接「%s」打不开：%s）\n", a.Conn.Name, errText(err))
			continue
		}
		dbs, err := sc.Databases(ctx)
		if err != nil {
			continue
		}
		scanned := 0
		for _, d := range dbs {
			if d.System || !a.CanSeeDB(d.Name) || (in.DB != "" && !strings.EqualFold(in.DB, d.Name)) {
				continue
			}
			if found >= in.Limit || scanned >= 50 {
				break
			}
			scanned++
			objs, err := sc.Objects(ctx, d.Name)
			if err != nil {
				continue
			}
			for _, o := range append(append([]driver.ObjectInfo(nil), objs.Tables...), objs.Views...) {
				if !a.CanSeeTable(d.Name, o.Name) {
					continue
				}
				if strings.Contains(strings.ToLower(o.Name), q) || strings.Contains(strings.ToLower(o.Comment), q) {
					found++
					fmt.Fprintf(&b, "表 %s / %s.%s", a.Conn.ID, d.Name, o.Name)
					if o.Comment != "" {
						b.WriteString(" — " + o.Comment)
					}
					b.WriteString("\n")
				}
			}
			cols, err := env.Svc.Conns.Completion(ctx, sc, d.Name)
			if err != nil {
				continue
			}
			names := make([]string, 0, len(cols))
			for t := range cols {
				names = append(names, t)
			}
			sort.Strings(names)
			for _, t := range names {
				if !a.CanSeeTable(d.Name, t) {
					continue
				}
				for _, c := range cols[t] {
					if strings.Contains(strings.ToLower(c), q) {
						found++
						if found <= in.Limit {
							fmt.Fprintf(&b, "列 %s / %s.%s.%s\n", a.Conn.ID, d.Name, t, c)
						}
					}
				}
			}
			// MySQL：列注释也搜
			if sc.Dialect() == "mysql" {
				rows, err := sc.ReadDB().QueryContext(ctx, `SELECT TABLE_NAME, COLUMN_NAME, COLUMN_COMMENT FROM information_schema.COLUMNS
					WHERE TABLE_SCHEMA = ? AND LOWER(COLUMN_COMMENT) LIKE ? LIMIT 200`, d.Name, "%"+strings.NewReplacer("%", "\\%", "_", "\\_").Replace(q)+"%")
				if err == nil {
					for rows.Next() {
						var t, c, cm string
						if rows.Scan(&t, &c, &cm) == nil && a.CanSeeTable(d.Name, t) && !strings.Contains(strings.ToLower(c), q) {
							found++
							if found <= in.Limit {
								fmt.Fprintf(&b, "列 %s / %s.%s.%s — %s\n", a.Conn.ID, d.Name, t, c, cm)
							}
						}
					}
					rows.Close()
				}
			}
		}
	}
	if found == 0 {
		return &Result{Text: "没有找到匹配「" + in.Query + "」的表或列。"}, nil
	}
	head := fmt.Sprintf("找到 %d 处匹配（格式：类别 连接ID / 库.表[.列]）：\n", found)
	if found > in.Limit {
		head = fmt.Sprintf("找到超过 %d 处匹配，只列出前 %d 处（格式：类别 连接ID / 库.表[.列]）：\n", in.Limit, in.Limit)
	}
	return &Result{Text: head + b.String()}, nil
}

// QueryOutput 是一次只读查询的完整结果。
type QueryOutput struct {
	Columns    []driver.Column
	Rows       [][]any
	Truncated  bool
	DurationMS int64
	DB         string
	Statement  string
}

func cellBytes(v any) int {
	switch x := v.(type) {
	case string:
		return len(x) + 3
	case driver.BinValue:
		return len(x.B64) + 20
	case driver.TruncValue:
		return len(x.Text) + 20
	}
	return 5
}

// RunSelect 在数据库层强制只读的会话里执行一条查询并读完整结果（最多 10 万行、64 MB）。
func (env *Env) RunSelect(a *ConnAccess, db, stmt string) (*QueryOutput, error) {
	sc, err := env.sql(a)
	if err != nil {
		return nil, err
	}
	stmts := sqlsplit.Split(stmt, sqlsplit.Dialect(sc.Dialect()))
	if len(stmts) == 0 {
		return nil, fail("SQL 为空")
	}
	if len(stmts) > 1 {
		return nil, fail("一次只能执行一条查询（收到 %d 条）", len(stmts))
	}
	stmt = stmts[0].Text
	info := sqlclass.Classify(stmt, sc.Dialect())
	if info.Kind != sqlclass.Read || !info.ReturnsRows {
		return nil, fail("这里只执行只读查询（这条是 %s %s）；修改数据或结构请用 execute_sql", info.Kind, info.Keyword)
	}
	if !a.CanSeeDB(db) && db != "" {
		return nil, fail("库 %s 不在 AI 库表白名单内", db)
	}
	sys := env.Svc.Settings.System(env.Ctx)
	cs, err := env.Svc.Conns.OpenConsole(env.Ctx, env.User, a.Conn, a.UserLevel, db, env.Source, true, sys.AgentTimeoutSec)
	if err != nil {
		return nil, fail("打开只读会话失败：%s", errText(err))
	}
	defer env.Svc.Conns.CloseConsole(cs.ID)
	cur := db
	if cur == "" {
		cur = cs.S.Database(env.Ctx)
	}
	if err := a.CheckTables(stmt, sc.Dialect(), cur, info); err != nil {
		return nil, err
	}
	start := time.Now()
	res := env.Svc.Conns.Exec(env.Ctx, cs, stmt, conns.ExecOptions{MaxRows: 1000, Source: env.Source, IP: env.IP})
	if len(res) == 0 {
		return nil, fail("没有执行任何语句")
	}
	r := res[0]
	if r.Kind == "error" {
		return nil, fail("查询出错：%s", r.Error)
	}
	limit := sys.MaxResultRows
	if limit <= 0 || limit > results.MaxRows {
		limit = results.MaxRows
	}
	rows := r.Rows
	size := 0
	for _, row := range rows {
		for _, c := range row {
			size += cellBytes(c)
		}
	}
	more := r.HasMore
	truncated := false
	for more {
		if len(rows) >= limit || size >= results.MaxBytes {
			truncated = true
			break
		}
		batch, m, err := cs.S.Fetch(env.Ctx, min(5000, limit-len(rows)))
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil, err
			}
			return nil, fail("读取结果出错：%s", errText(err))
		}
		for _, row := range batch {
			for _, c := range row {
				size += cellBytes(c)
			}
		}
		rows = append(rows, batch...)
		more = m
	}
	if len(rows) > limit {
		rows = rows[:limit]
		truncated = true
	}
	return &QueryOutput{Columns: r.Columns, Rows: rows, Truncated: truncated, DurationMS: time.Since(start).Milliseconds(), DB: cur, Statement: stmt}, nil
}

func (env *Env) previewRows(want int) int {
	n := env.PreviewRows
	if n <= 0 {
		n = env.Svc.Settings.System(env.Ctx).AgentPreviewRows
	}
	if want > 0 {
		n = want
	}
	if n > 500 {
		n = 500
	}
	if n <= 0 {
		n = 50
	}
	return n
}

// saveQuery 把查询结果存为结果集，返回给模型的文本与给界面的数据。
func (env *Env) saveQuery(a *ConnAccess, out *QueryOutput, title string, preview int) (*results.Meta, string, map[string]any, error) {
	m, err := env.Svc.Results.SaveRows(env.Ctx, results.Meta{WorkspaceID: env.WorkspaceID, UserID: env.User.ID, ConnectionID: a.Conn.ID, Database: out.DB,
		SQL: out.Statement, Title: title, Source: env.Source}, out.Columns, out.Rows, out.Truncated)
	if err != nil {
		return nil, "", nil, err
	}
	n := min(preview, len(out.Rows))
	var b strings.Builder
	total := fmt.Sprintf("%d 行", len(out.Rows))
	if out.Truncated {
		total = fmt.Sprintf("至少 %d 行（达到上限，后面还有，没有全部读出）", len(out.Rows))
	}
	fmt.Fprintf(&b, "结果 ID：%s\n共 %s，%d 列，耗时 %d ms。", m.ID, total, len(out.Columns), out.DurationMS)
	if n < len(out.Rows) {
		fmt.Fprintf(&b, "下面是前 %d 行；其余用 read_result 读取。", n)
	}
	b.WriteString("\n列：")
	for i, c := range out.Columns {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(c.Name + " " + c.Type)
	}
	b.WriteString("\n\n")
	b.WriteString(results.TextTable(out.Columns, out.Rows[:n], 300))
	data := map[string]any{"result_id": m.ID, "connection_id": a.Conn.ID, "db": out.DB, "sql": out.Statement, "columns": out.Columns,
		"rows": out.Rows[:min(n, 100)], "row_count": len(out.Rows), "truncated": out.Truncated, "duration_ms": out.DurationMS, "title": title}
	return m, b.String(), data, nil
}

func runQuery(env *Env, args json.RawMessage) (*Result, error) {
	var in struct {
		ConnectionID string `json:"connection_id"`
		SQL          string `json:"sql"`
		DB           string `json:"db"`
		PreviewRows  int    `json:"preview_rows"`
		Title        string `json:"title"`
	}
	if err := bind(args, &in); err != nil {
		return nil, err
	}
	a, err := env.Access(in.ConnectionID)
	if err != nil {
		return nil, err
	}
	out, err := env.RunSelect(a, in.DB, in.SQL)
	if err != nil {
		return nil, err
	}
	_, text, data, err := env.saveQuery(a, out, in.Title, env.previewRows(in.PreviewRows))
	if err != nil {
		return nil, err
	}
	return &Result{Text: text, Data: data}, nil
}

func readResult(env *Env, args json.RawMessage) (*Result, error) {
	var in struct {
		ResultID string            `json:"result_id"`
		Offset   int               `json:"offset"`
		Limit    int               `json:"limit"`
		Columns  []string          `json:"columns"`
		Filters  []results.Filter  `json:"filters"`
		OrderBy  []results.Order   `json:"order_by"`
	}
	if err := bind(args, &in); err != nil {
		return nil, err
	}
	m, err := env.Svc.Results.Get(env.Ctx, strings.ToLower(strings.TrimSpace(in.ResultID)), env.User.ID, env.WorkspaceID)
	if err != nil {
		return nil, fail("%s", err.Error())
	}
	// 结果来自的连接如果现在对智能体不可见（档位改了、授权收紧了），不再给读
	if m.ConnectionID != "" {
		if _, err := env.Access(m.ConnectionID); err != nil {
			return nil, fail("结果所属的连接当前对智能体不可见")
		}
	}
	d, err := env.Svc.Results.Load(m)
	if err != nil {
		return nil, fail("%s", err.Error())
	}
	if m.Kind == "text" {
		if in.Limit <= 0 || in.Limit > MaxOutputLines {
			in.Limit = 500
		}
		text, total := results.TextLines(d.Text, in.Offset, in.Limit)
		end := min(in.Offset+in.Limit, total)
		return &Result{Text: fmt.Sprintf("（第 %d–%d 行，共 %d 行）\n%s", in.Offset+1, end, total, text)}, nil
	}
	if in.Limit <= 0 {
		in.Limit = 50
	}
	if in.Limit > 500 {
		in.Limit = 500
	}
	cols, rows, total, err := results.Apply(d, results.Query{Offset: in.Offset, Limit: in.Limit, Columns: in.Columns, Filters: in.Filters, Order: in.OrderBy})
	if err != nil {
		return nil, fail("%s", err.Error())
	}
	head := fmt.Sprintf("结果 %s：符合条件 %d 行（结果共 %d 行", m.ID, total, m.RowCount)
	if m.Truncated {
		head += "，查询时达到上限"
	}
	head += fmt.Sprintf("），这里是第 %d–%d 行。\n", in.Offset+1, in.Offset+len(rows))
	return &Result{Text: head + results.TextTable(cols, rows, 500), Data: map[string]any{"result_id": m.ID, "columns": cols, "rows": rows, "total": total}}, nil
}

func explainQuery(env *Env, args json.RawMessage) (*Result, error) {
	var in struct {
		ConnectionID string `json:"connection_id"`
		SQL          string `json:"sql"`
		DB           string `json:"db"`
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
	if len(stmts) != 1 {
		return nil, fail("一次只能分析一条语句")
	}
	stmt := stmts[0].Text
	info := sqlclass.Classify(stmt, sc.Dialect())
	if info.Keyword == "EXPLAIN" {
		return nil, fail("直接给要分析的语句，不要自己加 EXPLAIN")
	}
	switch info.Kind {
	case sqlclass.Read, sqlclass.DML:
	default:
		return nil, fail("只能分析查询与增删改语句")
	}
	ex, format := sc.ExplainSQL(stmt)
	sys := env.Svc.Settings.System(env.Ctx)
	cs, err := env.Svc.Conns.OpenConsole(env.Ctx, env.User, a.Conn, a.UserLevel, in.DB, env.Source, true, sys.AgentTimeoutSec)
	if err != nil {
		return nil, fail("打开只读会话失败：%s", errText(err))
	}
	defer env.Svc.Conns.CloseConsole(cs.ID)
	cur := in.DB
	if cur == "" {
		cur = cs.S.Database(env.Ctx)
	}
	if err := a.CheckTables(stmt, sc.Dialect(), cur, info); err != nil {
		return nil, err
	}
	res := env.Svc.Conns.Exec(env.Ctx, cs, ex, conns.ExecOptions{MaxRows: 500, Source: env.Source, IP: env.IP})
	if len(res) == 0 || res[0].Kind == "error" {
		msg := "没有结果"
		if len(res) > 0 {
			msg = res[0].Error
		}
		return nil, fail("获取执行计划失败：%s", msg)
	}
	r := res[0]
	if format == "json" && len(r.Rows) > 0 && len(r.Rows[0]) > 0 {
		return &Result{Text: "执行计划（JSON）：\n" + results.Cell(r.Rows[0][0]), Data: map[string]any{"format": "json", "plan": results.Cell(r.Rows[0][0])}}, nil
	}
	return &Result{Text: "执行计划：\n" + results.TextTable(r.Columns, r.Rows, 500), Data: map[string]any{"format": "table", "columns": r.Columns, "rows": r.Rows}}, nil
}

func executeSQL(env *Env, args json.RawMessage) (*Result, error) {
	var in struct {
		ConnectionID string `json:"connection_id"`
		SQL          string `json:"sql"`
		DB           string `json:"db"`
		Reason       string `json:"reason"`
	}
	if err := bind(args, &in); err != nil {
		return nil, err
	}
	a, err := env.Access(in.ConnectionID)
	if err != nil {
		return nil, err
	}
	if env.Plan {
		return nil, fail("当前是计划模式（只读分析），不能执行写入或 DDL；请先给出方案，等用户切换回普通模式后再执行")
	}
	if !a.CanWrite() {
		return nil, fail("连接「%s」对智能体是%s档位，不能执行写入；只读查询请用 run_query", a.Conn.Name, Label(a.AI))
	}
	sc, err := env.sql(a)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.SQL) == "" {
		return nil, fail("SQL 为空")
	}
	if !a.CanSeeDB(in.DB) && in.DB != "" {
		return nil, fail("库 %s 不在 AI 库表白名单内", in.DB)
	}
	sys := env.Svc.Settings.System(env.Ctx)
	cs, err := env.Svc.Conns.OpenConsole(env.Ctx, env.User, a.Conn, a.UserLevel, in.DB, env.Source, false, sys.AgentTimeoutSec)
	if err != nil {
		return nil, fail("打开会话失败：%s", errText(err))
	}
	defer env.Svc.Conns.CloseConsole(cs.ID)
	var stopErr error
	before := map[string]string{}
	check := func(ctx context.Context, stmt string, info sqlclass.Info) error {
		cur := cs.S.Database(ctx)
		if err := a.CheckTables(stmt, sc.Dialect(), cur, info); err != nil {
			stopErr = err
			return err
		}
		if info.Keyword == "USE" {
			// 切换库：新库也要在白名单里
			parts := strings.Fields(stmt)
			if len(parts) >= 2 {
				target := strings.Trim(strings.TrimSuffix(parts[1], ";"), "`\"[]")
				if !a.CanSeeDB(target) {
					stopErr = fail("库 %s 不在 AI 库表白名单内", target)
					return stopErr
				}
			}
			return nil
		}
		if !info.IsWrite() {
			return nil
		}
		if info.Kind == sqlclass.DDL {
			for k, v := range relatedDDL(ctx, a, sc, cur, info) {
				before[k] = v
			}
		}
		if err := env.checkWrite(ctx, "execute_sql", a, stmt, cur, info, in.Reason, sc); err != nil {
			stopErr = err
			return err
		}
		return nil
	}
	res := env.Svc.Conns.Exec(env.Ctx, cs, in.SQL, conns.ExecOptions{MaxRows: 50, StopOnError: true, Source: env.Source, IP: env.IP, Check: check})
	inTx := cs.S.InTx()
	var b strings.Builder
	var data []map[string]any
	failed := false
	for i, r := range res {
		stmt := r.Statement
		if rs := []rune(stmt); len(rs) > 200 {
			stmt = string(rs[:200]) + "…"
		}
		fmt.Fprintf(&b, "[%d] %s\n    ", i+1, strings.ReplaceAll(stmt, "\n", " "))
		switch r.Kind {
		case "error":
			failed = true
			fmt.Fprintf(&b, "未成功：%s\n", r.Error)
		case "rows":
			fmt.Fprintf(&b, "返回 %d 行（%d ms）\n%s", len(r.Rows), r.DurationMS, results.TextTable(r.Columns, r.Rows, 200))
		default:
			fmt.Fprintf(&b, "成功，影响 %d 行（%d ms）", r.Affected, r.DurationMS)
			if r.LastInsertID > 0 {
				fmt.Fprintf(&b, "，最后插入 ID %d", r.LastInsertID)
			}
			b.WriteString("\n")
		}
		data = append(data, map[string]any{"statement": r.Statement, "kind": r.Kind, "affected": r.Affected, "error": r.Error, "duration_ms": r.DurationMS, "class": r.Class})
	}
	if inTx {
		b.WriteString("注意：语句里开启的事务没有提交，会话关闭时已回滚。需要提交请在同一次调用里写 COMMIT。\n")
	}
	if len(before) > 0 {
		b.WriteString("\n执行前的相关表结构：\n")
		keys := make([]string, 0, len(before))
		for k := range before {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteString("-- " + k + "\n" + before[k] + "\n")
		}
	}
	out := &Result{Text: b.String(), Data: map[string]any{"connection_id": a.Conn.ID, "statements": data}, IsError: failed}
	if stopErr != nil {
		var re *RejectedError
		switch {
		case errors.Is(stopErr, ErrRejected):
			out.Text = "用户拒绝了这次操作。\n" + out.Text
			out.Rejected, out.Halt = true, true
		case errors.As(stopErr, &re):
			out.Text = re.Error() + "\n" + out.Text
			out.Rejected = true
		case errors.Is(stopErr, context.Canceled):
			out.Text = "已中断（用户停止）\n" + out.Text
		}
	}
	return out, nil
}
