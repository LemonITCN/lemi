// Package charts 保存智能体生成的图表（ECharts option 模板 + 结果集填充的 dataset）与图（Mermaid），
// 并在保存前清洗 option（docs/spec.md §16.6）：只允许 JSON、formatter 去 HTML、tooltip 强制 richText、
// 去掉外部图片与链接、限制数据点数。
package charts

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// MaxPoints 单个图表最多的数据点。
const MaxPoints = 50000

// 允许的系列类型（2D 与 echarts-gl 3D）。
var seriesTypes = map[string]bool{
	"bar": true, "line": true, "pie": true, "scatter": true, "effectScatter": true, "radar": true, "tree": true, "treemap": true,
	"sunburst": true, "boxplot": true, "candlestick": true, "heatmap": true, "funnel": true, "gauge": true, "sankey": true,
	"graph": true, "parallel": true, "themeRiver": true, "pictorialBar": true,
	"bar3D": true, "scatter3D": true, "surface": true, "line3D": true,
}

// SeriesTypes 返回允许的系列类型（给工具说明用）。
func SeriesTypes() []string {
	out := make([]string, 0, len(seriesTypes))
	for k := range seriesTypes {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// 允许的顶层组件。
var topKeys = map[string]bool{
	"title": true, "legend": true, "grid": true, "xAxis": true, "yAxis": true, "polar": true, "radiusAxis": true, "angleAxis": true,
	"radar": true, "dataZoom": true, "visualMap": true, "tooltip": true, "axisPointer": true, "parallel": true, "parallelAxis": true,
	"singleAxis": true, "calendar": true, "graphic": true, "series": true, "color": true, "backgroundColor": true, "textStyle": true,
	"animation": true, "animationDuration": true, "animationEasing": true, "darkMode": true, "aria": true,
	"grid3D": true, "xAxis3D": true, "yAxis3D": true, "zAxis3D": true, "light": true,
}

var tagRe = regexp.MustCompile(`<[^>]*>`)

// 会让浏览器去访问外部地址或打开链接的键。
var dropKeys = map[string]bool{"link": true, "sublink": true, "target": true, "sublinkTarget": true, "url": true, "href": true,
	"extraCssText": true, "className": true, "appendToBody": true, "appendTo": true, "renderItem": true, "dataset": true}

// Sanitize 清洗 ECharts option。返回清洗后的 option、提示（被去掉的内容）与错误（无法使用的配置）。
func Sanitize(opt map[string]any) (map[string]any, []string, error) {
	var warns []string
	warn := func(format string, a ...any) {
		if len(warns) < 20 {
			warns = append(warns, fmt.Sprintf(format, a...))
		}
	}
	out := map[string]any{}
	keys := make([]string, 0, len(opt))
	for k := range opt {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if k == "dataset" {
			continue // 数据由服务端从结果集填充
		}
		if !topKeys[k] {
			warn("去掉了不支持的配置项 %s", k)
			continue
		}
		out[k] = opt[k]
	}
	points := 0
	nodes := 0
	var walk func(key string, v any, depth int) (any, bool)
	walk = func(key string, v any, depth int) (any, bool) {
		nodes++
		if depth > 40 || nodes > 400000 {
			return nil, false
		}
		switch x := v.(type) {
		case map[string]any:
			m := map[string]any{}
			for k, vv := range x {
				if dropKeys[k] {
					warn("去掉了 %s", k)
					continue
				}
				if k == "formatter" {
					if s, ok := vv.(string); ok {
						clean := tagRe.ReplaceAllString(s, "")
						if strings.ContainsAny(clean, "<>") {
							warn("formatter 含 HTML，已去掉")
							continue
						}
						if clean != s {
							warn("formatter 里的 HTML 标签已去掉")
						}
						m[k] = clean
						continue
					}
					if _, ok := vv.(map[string]any); !ok {
						continue
					}
				}
				if k == "image" || k == "backgroundImage" {
					if s, ok := vv.(string); ok && !strings.HasPrefix(s, "data:image/") {
						warn("去掉了外部图片 %s", k)
						continue
					}
				}
				nv, ok := walk(k, vv, depth+1)
				if ok {
					m[k] = nv
				}
			}
			if key == "tooltip" {
				m["renderMode"] = "richText"
			}
			return m, true
		case []any:
			arr := make([]any, 0, len(x))
			childKey := key
			if key == "data" {
				childKey = "data_item" // 只数最外层的数据项
			}
			for _, vv := range x {
				nv, ok := walk(childKey, vv, depth+1)
				if ok {
					arr = append(arr, nv)
				}
			}
			if key == "data" {
				points += len(arr)
			}
			return arr, true
		case string:
			low := strings.ToLower(strings.TrimSpace(x))
			if strings.HasPrefix(low, "image://") && !strings.HasPrefix(low, "image://data:image/") {
				warn("去掉了外部图片符号")
				return nil, false
			}
			if strings.HasPrefix(low, "javascript:") {
				return nil, false
			}
			return x, true
		case float64, bool, nil, int, int64:
			return x, true
		}
		return nil, false
	}
	for k, v := range out {
		nv, ok := walk(k, v, 0)
		if !ok {
			delete(out, k)
			continue
		}
		out[k] = nv
	}
	if nodes > 400000 {
		return nil, warns, fmt.Errorf("图表配置过大")
	}
	if points > MaxPoints {
		return nil, warns, fmt.Errorf("图表内联数据超过 %d 个点，请改用 result_id / sql 由服务端填充数据，并在查询里先聚合", MaxPoints)
	}
	// 强制 tooltip richText（顶层没有 tooltip 时也加上，图表默认可悬停查看）
	if _, ok := out["tooltip"]; !ok {
		out["tooltip"] = map[string]any{"renderMode": "richText"}
	}
	// graphic 里只保留图形与文字，图片元素整个去掉
	if g, ok := out["graphic"]; ok {
		out["graphic"] = stripGraphicImages(g)
	}
	// 系列类型白名单
	series := asList(out["series"])
	for i, s := range series {
		m, ok := s.(map[string]any)
		if !ok {
			return nil, warns, fmt.Errorf("series[%d] 不是对象", i)
		}
		t, _ := m["type"].(string)
		if t == "" {
			return nil, warns, fmt.Errorf("series[%d] 缺少 type", i)
		}
		if !seriesTypes[t] {
			return nil, warns, fmt.Errorf("不支持的系列类型 %q（可用：%s）", t, strings.Join(SeriesTypes(), ", "))
		}
	}
	return out, warns, nil
}

func stripGraphicImages(g any) any {
	switch x := g.(type) {
	case []any:
		var out []any
		for _, e := range x {
			if v := stripGraphicImages(e); v != nil {
				out = append(out, v)
			}
		}
		return out
	case map[string]any:
		if t, _ := x["type"].(string); t == "image" {
			return nil
		}
		if el, ok := x["elements"]; ok {
			x["elements"] = stripGraphicImages(el)
		}
		if ch, ok := x["children"]; ok {
			x["children"] = stripGraphicImages(ch)
		}
		if st, ok := x["style"].(map[string]any); ok {
			delete(st, "image")
		}
		return x
	}
	return g
}

func asList(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case map[string]any:
		return []any{x}
	}
	return nil
}

// Is3D 判断 option 是否用了 echarts-gl。
func Is3D(opt map[string]any) bool {
	for _, s := range asList(opt["series"]) {
		if m, ok := s.(map[string]any); ok {
			switch m["type"] {
			case "bar3D", "scatter3D", "surface", "line3D":
				return true
			}
		}
	}
	return false
}

// SanitizeMermaid 清洗 Mermaid 代码：去掉 init 指令（可能改安全级别）与 click 交互（跳转、回调）。
func SanitizeMermaid(code string) (string, error) {
	code = strings.TrimSpace(code)
	code = strings.TrimPrefix(code, "```mermaid")
	code = strings.TrimSuffix(strings.TrimSpace(code), "```")
	code = strings.TrimSpace(code)
	if code == "" {
		return "", fmt.Errorf("图的代码为空")
	}
	if len(code) > 100<<10 {
		return "", fmt.Errorf("图的代码超过 100 KB")
	}
	var lines []string
	for _, l := range strings.Split(code, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "%%{") {
			continue
		}
		if strings.HasPrefix(strings.ToLower(t), "click ") || strings.HasPrefix(strings.ToLower(t), "href ") {
			continue
		}
		lines = append(lines, l)
	}
	return strings.Join(lines, "\n"), nil
}
