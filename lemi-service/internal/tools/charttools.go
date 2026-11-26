package tools

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
	"github.com/LemonITCN/lemi/lemi-service/internal/charts"
	"github.com/LemonITCN/lemi/lemi-service/internal/driver"
)

func init() {
	r := Default
	r.Register(&Tool{
		Name: "create_chart", Title: "生成图表", Kind: KindRead,
		Description: "用查询结果生成 ECharts 图表并保存，返回图表 ID；在回复或报告里写 ```lemi-chart <图表ID>``` 引用它。" +
			"数据来源二选一：result_id（run_query 返回的结果 ID），或 connection_id + sql（只读查询，按需在 SQL 里先聚合）。数据由服务端按 dataset 填充，不要在 option 里写数据。" +
			"option 是 ECharts option 模板（JSON，不能有函数）：写 title、legend、xAxis、yAxis、series[].type 等；encode 把结果列映射到系列，" +
			"例如 {\"x\":\"month\",\"y\":[\"sales\",\"cost\"]}（多个 y 会生成多条系列），饼图用 {\"x\":\"name\",\"y\":\"value\"}。" +
			"支持 bar line pie scatter effectScatter radar heatmap funnel gauge sankey tree treemap sunburst boxplot candlestick graph parallel themeRiver pictorialBar，" +
			"以及 3D 的 bar3D scatter3D surface line3D（需要 grid3D、xAxis3D、yAxis3D、zAxis3D，缺省会补）。单图最多 5 万个数据点。",
		Schema: obj(map[string]any{
			"title":         str("图表标题"),
			"option":        anyObj("ECharts option 模板（不含数据）"),
			"encode":        anyObj("可选：列映射，例如 {\"x\":\"day\",\"y\":[\"pv\",\"uv\"]}"),
			"columns":       arr(str("列名"), "可选：只用结果里的这些列"),
			"result_id":     str("数据来源：结果 ID"),
			"connection_id": str("数据来源：连接 ID（与 sql 一起用）"),
			"sql":           str("数据来源：只读查询"),
			"db":            str("可选：查询在哪个库里执行"),
		}, "title", "option"),
		Handler: createChart,
	})
	r.Register(&Tool{
		Name: "create_diagram", Title: "生成图", Kind: KindRead,
		Description: "保存一张 Mermaid 图（流程图 flowchart、时序图 sequenceDiagram、ER 图 erDiagram、状态图、甘特图、思维导图等），返回图 ID；在回复或报告里写 ```lemi-diagram <图ID>``` 引用。" +
			"code 只写 Mermaid 代码本身，不要包 ``` 围栏。数据库表关系图优先用 er_diagram 自动生成。",
		Schema: obj(map[string]any{
			"title":        str("标题"),
			"code":         str("Mermaid 代码"),
			"diagram_type": str("可选：图的类型（flowchart / sequence / er / state / gantt / mindmap …）"),
		}, "title", "code"),
		Handler: createDiagram,
	})
	r.Register(&Tool{
		Name: "er_diagram", Title: "ER 图", Kind: KindRead,
		Description: "按外键为指定库（或其中几张表）自动生成 Mermaid ER 图并保存，返回图 ID 与代码；在回复或报告里写 ```lemi-diagram <图ID>``` 引用。不指定 tables 时取库里的全部表（最多 60 张）。",
		Schema: obj(map[string]any{
			"connection_id": str("连接 ID"),
			"db":            str("库名（SQLite 用 main）"),
			"tables":        arr(str("表名"), "可选：只画这些表"),
			"title":         str("可选：标题"),
		}, "connection_id", "db"),
		Handler: erDiagram,
	})
	r.Alias("chart", "create_chart")
	r.Alias("plot", "create_chart")
	r.Alias("mermaid", "create_diagram")
	r.Alias("diagram", "create_diagram")
	r.Alias("erdiagram", "er_diagram")
}

func createChart(env *Env, args json.RawMessage) (*Result, error) {
	var in struct {
		Title        string         `json:"title"`
		Option       map[string]any `json:"option"`
		Encode       map[string]any `json:"encode"`
		Columns      []string       `json:"columns"`
		ResultID     string         `json:"result_id"`
		ConnectionID string         `json:"connection_id"`
		SQL          string         `json:"sql"`
		DB           string         `json:"db"`
	}
	if err := bind(args, &in); err != nil {
		return nil, err
	}
	opt, warns, err := charts.Sanitize(in.Option)
	if err != nil {
		return nil, fail("图表配置不可用：%s", err.Error())
	}
	var cols []driver.Column
	var rows [][]any
	var src charts.Source
	truncated := false
	switch {
	case strings.TrimSpace(in.ResultID) != "":
		m, err := env.Svc.Results.Get(env.Ctx, strings.ToLower(strings.TrimSpace(in.ResultID)), env.User.ID, env.WorkspaceID)
		if err != nil || m.Kind != "rows" {
			return nil, fail("结果 %s 不存在或已过期（结果保留 7 天）", in.ResultID)
		}
		if m.ConnectionID != "" {
			if _, err := env.Access(m.ConnectionID); err != nil {
				return nil, fail("结果所属的连接当前对智能体不可见")
			}
		}
		d, err := env.Svc.Results.Load(m)
		if err != nil {
			return nil, fail("%s", err.Error())
		}
		cols, rows, truncated = d.Columns, d.Rows, m.Truncated
		src = charts.Source{ConnectionID: m.ConnectionID, Database: m.Database, SQL: m.SQL, ResultID: m.ID}
	case strings.TrimSpace(in.SQL) != "":
		a, err := env.Access(in.ConnectionID)
		if err != nil {
			return nil, err
		}
		out, err := env.RunSelect(a, in.DB, in.SQL)
		if err != nil {
			return nil, err
		}
		m, _, _, err := env.saveQuery(a, out, in.Title, 0)
		if err != nil {
			return nil, err
		}
		cols, rows, truncated = out.Columns, out.Rows, out.Truncated
		src = charts.Source{ConnectionID: a.Conn.ID, Database: out.DB, SQL: out.Statement, ResultID: m.ID}
	default:
		return nil, fail("请提供数据来源：result_id，或 connection_id + sql")
	}
	ds, err := charts.BuildDataset(cols, rows, in.Columns)
	if err != nil {
		return nil, fail("%s", err.Error())
	}
	ds.Truncated = ds.Truncated || truncated
	// encode 里引用的列必须存在
	dims := map[string]bool{}
	for _, d := range ds.Dimensions {
		dims[strings.ToLower(d)] = true
	}
	for k, v := range in.Encode {
		if k == "type" {
			continue
		}
		for _, name := range strList(v) {
			if !dims[strings.ToLower(name)] {
				return nil, fail("encode.%s 引用的列 %q 不在数据里（可用列：%s）", k, name, strings.Join(ds.Dimensions, ", "))
			}
		}
	}
	sp := charts.Spec{Option: opt, Encode: in.Encode, Columns: in.Columns}
	if s, _ := charts.Render(sp, ds)["series"].([]any); len(s) == 0 {
		return nil, fail("图表没有系列：请在 option.series 里写系列类型，或用 encode 指定 x / y 列")
	}
	c, err := env.Svc.Charts.Create(env.Ctx, env.WorkspaceID, env.User.ID, charts.KindECharts, in.Title, sp, src, ds)
	if err != nil {
		return nil, err
	}
	env.Svc.Audit.Record(audit.Entry{UserID: env.User.ID, Username: env.User.Username, WorkspaceID: env.WorkspaceID, ConnectionID: src.ConnectionID,
		Source: env.Source, Action: "ai.chart_create", Target: c.ID, IP: env.IP, OK: true}, map[string]any{"title": c.Title})
	var b strings.Builder
	fmt.Fprintf(&b, "图表已保存：%s「%s」，数据 %d 行", c.ID, c.Title, ds.Rows)
	if ds.Truncated {
		b.WriteString("（超过上限的部分没有画出，建议在 SQL 里先聚合）")
	}
	fmt.Fprintf(&b, "，维度：%s。\n在回复或报告里用 ```lemi-chart %s``` 引用。", strings.Join(ds.Dimensions, ", "), c.ID)
	if len(warns) > 0 {
		b.WriteString("\n清洗提示：" + strings.Join(warns, "；"))
	}
	data := map[string]any{"chart_id": c.ID, "title": c.Title, "rows": ds.Rows}
	if env.Source == audit.SrcMCP {
		// MCP 客户端看不到 Lemi 的界面：附上最终的 option，客户端可以自己渲染
		ob, _ := json.Marshal(charts.Render(sp, ds))
		b.WriteString("\n\n（MCP 客户端无法直接显示图表，下面是完整的 ECharts option，可在 Lemi 网页里查看该图表）\n")
		b.Write(ob)
	}
	return &Result{Text: b.String(), Data: data}, nil
}

func strList(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		var out []string
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func createDiagram(env *Env, args json.RawMessage) (*Result, error) {
	var in struct {
		Title       string `json:"title"`
		Code        string `json:"code"`
		DiagramType string `json:"diagram_type"`
	}
	if err := bind(args, &in); err != nil {
		return nil, err
	}
	code, err := charts.SanitizeMermaid(in.Code)
	if err != nil {
		return nil, fail("%s", err.Error())
	}
	c, err := env.Svc.Charts.Create(env.Ctx, env.WorkspaceID, env.User.ID, charts.KindMermaid, in.Title, charts.Spec{Code: code, Diagram: in.DiagramType}, charts.Source{}, nil)
	if err != nil {
		return nil, err
	}
	return &Result{Text: fmt.Sprintf("图已保存：%s「%s」。在回复或报告里用 ```lemi-diagram %s``` 引用。", c.ID, c.Title, c.ID), Data: map[string]any{"diagram_id": c.ID, "title": c.Title}}, nil
}

var identRe = regexp.MustCompile(`[^A-Za-z0-9_]`)

func mermaidIdent(s string) string {
	t := identRe.ReplaceAllString(s, "_")
	if t == "" || (t[0] >= '0' && t[0] <= '9') {
		t = "_" + t
	}
	return t
}

func mermaidType(c driver.ColumnInfo) string {
	t := c.DataType
	if t == "" {
		t = strings.Fields(c.Type + " x")[0]
	}
	if i := strings.Index(t, "("); i > 0 {
		t = t[:i]
	}
	t = identRe.ReplaceAllString(strings.ToLower(t), "_")
	if t == "" {
		t = "any"
	}
	return t
}

func erDiagram(env *Env, args json.RawMessage) (*Result, error) {
	var in struct {
		ConnectionID string   `json:"connection_id"`
		DB           string   `json:"db"`
		Tables       []string `json:"tables"`
		Title        string   `json:"title"`
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
	names := in.Tables
	if len(names) == 0 {
		objs, err := sc.Objects(env.Ctx, in.DB)
		if err != nil {
			return nil, fail("读取表列表失败：%s", errText(err))
		}
		for _, o := range objs.Tables {
			names = append(names, o.Name)
		}
	}
	var visible []string
	for _, n := range names {
		if a.CanSeeTable(in.DB, n) {
			visible = append(visible, n)
		}
	}
	if len(visible) == 0 {
		return nil, fail("没有可画的表")
	}
	if len(visible) > 60 {
		visible = visible[:60]
	}
	inSet := map[string]bool{}
	for _, n := range visible {
		inSet[strings.ToLower(n)] = true
	}
	var b strings.Builder
	b.WriteString("erDiagram\n")
	var rels []string
	for _, n := range visible {
		t, err := sc.Table(env.Ctx, in.DB, n)
		if err != nil {
			continue
		}
		fkCols := map[string]bool{}
		for _, fk := range t.ForeignKeys {
			for _, c := range fk.Columns {
				fkCols[c] = true
			}
		}
		fmt.Fprintf(&b, "    %s {\n", mermaidIdent(t.Name))
		for i, c := range t.Columns {
			if i >= 40 {
				fmt.Fprintf(&b, "        string more_%d_columns\n", len(t.Columns)-40)
				break
			}
			keys := []string{}
			if c.PrimaryKey {
				keys = append(keys, "PK")
			}
			if fkCols[c.Name] {
				keys = append(keys, "FK")
			}
			line := fmt.Sprintf("        %s %s", mermaidType(c), mermaidIdent(c.Name))
			if len(keys) > 0 {
				line += " " + strings.Join(keys, ",")
			}
			if c.Comment != "" {
				cm := strings.NewReplacer("\"", "'", "\n", " ").Replace(c.Comment)
				if r := []rune(cm); len(r) > 40 {
					cm = string(r[:40])
				}
				line += " \"" + cm + "\""
			}
			b.WriteString(line + "\n")
		}
		b.WriteString("    }\n")
		for _, fk := range t.ForeignKeys {
			if !inSet[strings.ToLower(fk.RefTable)] || (fk.RefDB != "" && !strings.EqualFold(fk.RefDB, in.DB) && a.Conn.Driver != "sqlite") {
				continue
			}
			card := "}o--||"
			for _, c := range t.Columns {
				if len(fk.Columns) > 0 && c.Name == fk.Columns[0] && c.Nullable {
					card = "}o--o|"
				}
			}
			label := fk.Name
			if label == "" {
				label = strings.Join(fk.Columns, ",")
			}
			rels = append(rels, fmt.Sprintf("    %s %s %s : \"%s\"", mermaidIdent(t.Name), card, mermaidIdent(fk.RefTable), strings.ReplaceAll(label, "\"", "'")))
		}
	}
	for _, r := range rels {
		b.WriteString(r + "\n")
	}
	code := b.String()
	title := in.Title
	if title == "" {
		title = in.DB + " ER 图"
	}
	c, err := env.Svc.Charts.Create(env.Ctx, env.WorkspaceID, env.User.ID, charts.KindMermaid, title, charts.Spec{Code: code, Diagram: "er"}, charts.Source{ConnectionID: a.Conn.ID, Database: in.DB}, nil)
	if err != nil {
		return nil, err
	}
	return &Result{Text: fmt.Sprintf("ER 图已保存：%s「%s」，%d 张表、%d 条关系。在回复或报告里用 ```lemi-diagram %s``` 引用。\n\n%s", c.ID, c.Title, len(visible), len(rels), c.ID, code),
		Data: map[string]any{"diagram_id": c.ID, "title": c.Title}}, nil
}
