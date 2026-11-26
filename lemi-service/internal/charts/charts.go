package charts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/driver"
	"github.com/LemonITCN/lemi/lemi-service/internal/ids"
	"github.com/LemonITCN/lemi/lemi-service/internal/results"
	"github.com/LemonITCN/lemi/lemi-service/internal/store"
)

// 对象种类。
const (
	KindECharts = "echarts"
	KindMermaid = "mermaid"
)

// Chart 是保存的图表或图。
type Chart struct {
	ID          string `db:"id" json:"id"`
	WorkspaceID string `db:"workspace_id" json:"workspace_id"`
	UserID      string `db:"user_id" json:"user_id"`
	Kind        string `db:"kind" json:"kind"`
	Title       string `db:"title" json:"title"`
	SpecJSON    string `db:"spec" json:"-"`
	SourceJSON  string `db:"source" json:"-"`
	DataJSON    string `db:"data" json:"-"`
	CreatedAt   int64  `db:"created_at" json:"created_at"`
	UpdatedAt   int64  `db:"updated_at" json:"updated_at"`
}

// Spec 是图表模板（ECharts）或图代码（Mermaid）。
type Spec struct {
	Option  map[string]any `json:"option,omitempty"`
	Encode  map[string]any `json:"encode,omitempty"`
	Columns []string       `json:"columns,omitempty"` // 只取结果的这些列（空 = 全部）
	Code    string         `json:"code,omitempty"`
	Diagram string         `json:"diagram,omitempty"` // er | flowchart | sequence | ...
}

// Source 是数据来源（刷新时重新执行）。
type Source struct {
	ConnectionID string `json:"connection_id,omitempty"`
	Database     string `json:"db,omitempty"`
	SQL          string `json:"sql,omitempty"`
	ResultID     string `json:"result_id,omitempty"`
}

// Dataset 是 ECharts dataset 快照。
type Dataset struct {
	Dimensions  []string `json:"dimensions"`
	Source      [][]any  `json:"source"`
	Rows        int      `json:"rows"`
	Truncated   bool     `json:"truncated,omitempty"`
	RefreshedAt int64    `json:"refreshed_at"`
}

// Runner 重新执行图表的查询（由装配处注入：只读会话、以当前用户身份）。
type Runner func(ctx context.Context, u *accounts.User, src Source) ([]driver.Column, [][]any, bool, error)

// Service 图表存储。
type Service struct {
	st     *store.Store
	Runner Runner
}

// New 创建图表服务。
func New(st *store.Store) *Service { return &Service{st: st} }

// ErrNotFound 图表不存在。
var ErrNotFound = errors.New("图表不存在")

// Decode 解析图表的各个 JSON 字段。
func (c *Chart) Decode() (Spec, Source, *Dataset) {
	var sp Spec
	var src Source
	var ds Dataset
	_ = json.Unmarshal([]byte(c.SpecJSON), &sp)
	_ = json.Unmarshal([]byte(c.SourceJSON), &src)
	_ = json.Unmarshal([]byte(c.DataJSON), &ds)
	return sp, src, &ds
}

func numericKind(k string) bool {
	switch k {
	case "int", "decimal", "float", "year", "bool":
		return true
	}
	return false
}

// BuildDataset 用结果集构造 dataset：数值列转成数字，按 MaxPoints 截断行数。
func BuildDataset(cols []driver.Column, rows [][]any, pick []string) (*Dataset, error) {
	idx := make([]int, 0, len(cols))
	if len(pick) == 0 {
		for i := range cols {
			idx = append(idx, i)
		}
	} else {
		byName := map[string]int{}
		for i, c := range cols {
			byName[strings.ToLower(c.Name)] = i
		}
		for _, p := range pick {
			i, ok := byName[strings.ToLower(strings.TrimSpace(p))]
			if !ok {
				names := make([]string, len(cols))
				for k, c := range cols {
					names[k] = c.Name
				}
				return nil, fmt.Errorf("结果里没有列 %q（可用列：%s）", p, strings.Join(names, ", "))
			}
			idx = append(idx, i)
		}
	}
	ds := &Dataset{Rows: len(rows), RefreshedAt: store.Now()}
	numeric := make([]bool, len(idx))
	ncols := 0
	for k, i := range idx {
		ds.Dimensions = append(ds.Dimensions, cols[i].Name)
		numeric[k] = numericKind(cols[i].Kind)
		if numeric[k] {
			ncols++
		}
	}
	if ncols == 0 {
		ncols = 1
	}
	maxRows := MaxPoints / ncols
	if len(rows) > maxRows {
		rows = rows[:maxRows]
		ds.Truncated = true
	}
	ds.Source = make([][]any, 0, len(rows))
	for _, r := range rows {
		out := make([]any, len(idx))
		for k, i := range idx {
			v := r[i]
			if v == nil {
				out[k] = nil
				continue
			}
			if numeric[k] {
				if f, ok := results.Number(v); ok {
					out[k] = f
					continue
				}
			}
			out[k] = results.Cell(v)
		}
		ds.Source = append(ds.Source, out)
	}
	return ds, nil
}

var cartesian = map[string]bool{"bar": true, "line": true, "scatter": true, "effectScatter": true, "boxplot": true, "candlestick": true, "heatmap": true, "pictorialBar": true}

// Render 把模板与 dataset 合成最终的 ECharts option（深拷贝，不改模板）。
func Render(sp Spec, ds *Dataset) map[string]any {
	var opt map[string]any
	b, _ := json.Marshal(sp.Option)
	_ = json.Unmarshal(b, &opt)
	if opt == nil {
		opt = map[string]any{}
	}
	if ds != nil {
		src := make([]any, 0, len(ds.Source)+1)
		hdr := make([]any, len(ds.Dimensions))
		for i, d := range ds.Dimensions {
			hdr[i] = d
		}
		src = append(src, hdr)
		for _, r := range ds.Source {
			src = append(src, r)
		}
		opt["dataset"] = map[string]any{"source": src}
	}
	applyEncode(opt, sp.Encode)
	// 直角坐标系列缺坐标轴时补默认轴；3D 系列补 3D 坐标系
	needAxes, need3D := false, false
	for _, s := range asList(opt["series"]) {
		if m, ok := s.(map[string]any); ok {
			t, _ := m["type"].(string)
			if cartesian[t] {
				needAxes = true
			}
			if t == "bar3D" || t == "scatter3D" || t == "line3D" || t == "surface" {
				need3D = true
			}
		}
	}
	if needAxes {
		if _, ok := opt["xAxis"]; !ok {
			opt["xAxis"] = map[string]any{"type": "category"}
		}
		if _, ok := opt["yAxis"]; !ok {
			opt["yAxis"] = map[string]any{}
		}
	}
	if need3D {
		for _, k := range []string{"grid3D", "xAxis3D", "yAxis3D", "zAxis3D"} {
			if _, ok := opt[k]; !ok {
				if k == "grid3D" {
					opt[k] = map[string]any{}
				} else {
					opt[k] = map[string]any{"type": "value"}
				}
			}
		}
		if x, ok := opt["xAxis3D"].(map[string]any); ok && ds != nil && len(ds.Dimensions) > 0 {
			if _, has := x["type"]; !has {
				x["type"] = "category"
			}
		}
	}
	return opt
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
	case []string:
		return x
	}
	return nil
}

// applyEncode 把 encode 映射套到系列上：没有系列时按 y 列生成；单个系列、多个 y 列时按列复制系列。
func applyEncode(opt map[string]any, enc map[string]any) {
	if len(enc) == 0 {
		return
	}
	base := map[string]any{}
	for k, v := range enc {
		if k != "type" {
			base[k] = v
		}
	}
	ys := strList(enc["y"])
	series := asList(opt["series"])
	mk := func(t string, y string) map[string]any {
		e := map[string]any{}
		for k, v := range base {
			e[k] = v
		}
		if y != "" {
			e["y"] = y
		}
		if t == "pie" || t == "funnel" {
			if x, ok := e["x"]; ok {
				e["itemName"] = x
				delete(e, "x")
			}
			if yy, ok := e["y"]; ok {
				e["value"] = yy
				delete(e, "y")
			}
		}
		return e
	}
	if len(series) == 0 {
		t, _ := enc["type"].(string)
		if t == "" {
			t = "bar"
		}
		if len(ys) == 0 {
			opt["series"] = []any{map[string]any{"type": t, "encode": mk(t, "")}}
			return
		}
		var out []any
		for _, y := range ys {
			out = append(out, map[string]any{"type": t, "name": y, "encode": mk(t, y)})
		}
		opt["series"] = out
		return
	}
	if len(series) == 1 && len(ys) > 1 {
		tmpl, _ := series[0].(map[string]any)
		if tmpl != nil {
			if _, has := tmpl["encode"]; !has {
				t, _ := tmpl["type"].(string)
				var out []any
				for _, y := range ys {
					s := map[string]any{}
					for k, v := range tmpl {
						s[k] = v
					}
					if _, has := s["name"]; !has {
						s["name"] = y
					}
					s["encode"] = mk(t, y)
					out = append(out, s)
				}
				opt["series"] = out
				return
			}
		}
	}
	for i, s := range series {
		m, ok := s.(map[string]any)
		if !ok {
			continue
		}
		if _, has := m["encode"]; has {
			continue
		}
		t, _ := m["type"].(string)
		y := ""
		if len(ys) > 0 {
			y = ys[min(i, len(ys)-1)]
		}
		m["encode"] = mk(t, y)
	}
	opt["series"] = series
}

// Create 保存图表或图。
func (s *Service) Create(ctx context.Context, wsID, userID, kind, title string, sp Spec, src Source, ds *Dataset) (*Chart, error) {
	now := store.Now()
	c := &Chart{ID: ids.New(), WorkspaceID: wsID, UserID: userID, Kind: kind, Title: strings.TrimSpace(title), CreatedAt: now, UpdatedAt: now}
	if c.Title == "" {
		c.Title = "未命名"
	}
	if r := []rune(c.Title); len(r) > 200 {
		c.Title = string(r[:200])
	}
	b, _ := json.Marshal(sp)
	c.SpecJSON = string(b)
	b, _ = json.Marshal(src)
	c.SourceJSON = string(b)
	if ds == nil {
		ds = &Dataset{}
	}
	b, _ = json.Marshal(ds)
	c.DataJSON = string(b)
	if err := s.st.Add(ctx, "ai_charts", c); err != nil {
		return nil, err
	}
	return c, nil
}

// Get 取图表。
func (s *Service) Get(ctx context.Context, id string) (*Chart, error) {
	if !ids.Valid(id) {
		return nil, ErrNotFound
	}
	var c Chart
	if err := s.st.GetW(ctx, &c, `SELECT * FROM ai_charts WHERE id = ?`, id); err != nil {
		return nil, ErrNotFound
	}
	return &c, nil
}

// SaveData 更新数据快照（刷新后）。
func (s *Service) SaveData(ctx context.Context, c *Chart, ds *Dataset) error {
	b, _ := json.Marshal(ds)
	c.DataJSON = string(b)
	c.UpdatedAt = store.Now()
	_, err := s.st.Exec(ctx, `UPDATE ai_charts SET data = ?, updated_at = ? WHERE id = ?`, c.DataJSON, c.UpdatedAt, c.ID)
	return err
}

// View 给前端（与报告快照）的图表描述：ECharts 给最终 option，Mermaid 给代码。
func (s *Service) View(c *Chart) map[string]any {
	sp, src, ds := c.Decode()
	out := map[string]any{"id": c.ID, "kind": c.Kind, "title": c.Title, "created_at": c.CreatedAt, "updated_at": c.UpdatedAt,
		"refreshable": src.SQL != "" && src.ConnectionID != ""}
	if c.Kind == KindMermaid {
		out["code"] = sp.Code
		out["diagram"] = sp.Diagram
		return out
	}
	out["option"] = Render(sp, ds)
	out["gl"] = Is3D(sp.Option)
	out["rows"] = ds.Rows
	out["truncated"] = ds.Truncated
	out["refreshed_at"] = ds.RefreshedAt
	if src.ConnectionID != "" {
		out["source"] = map[string]any{"connection_id": src.ConnectionID, "db": src.Database, "sql": src.SQL}
	}
	return out
}

// Refresh 重新执行查询并更新数据快照。
func (s *Service) Refresh(ctx context.Context, u *accounts.User, c *Chart) error {
	if c.Kind != KindECharts {
		return errors.New("只有图表可以刷新")
	}
	sp, src, _ := c.Decode()
	if src.SQL == "" || src.ConnectionID == "" {
		return errors.New("这个图表没有可重新执行的查询")
	}
	if s.Runner == nil {
		return errors.New("刷新功能不可用")
	}
	cols, rows, truncated, err := s.Runner(ctx, u, src)
	if err != nil {
		return err
	}
	ds, err := BuildDataset(cols, rows, sp.Columns)
	if err != nil {
		return err
	}
	ds.Truncated = ds.Truncated || truncated
	return s.SaveData(ctx, c, ds)
}
