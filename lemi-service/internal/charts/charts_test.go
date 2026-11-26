package charts

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/LemonITCN/lemi/lemi-service/internal/driver"
)

func TestSanitize(t *testing.T) {
	var opt map[string]any
	_ = json.Unmarshal([]byte(`{
		"title": {"text": "销量", "link": "https://evil.example", "subtext": "x"},
		"tooltip": {"formatter": "<img src=x onerror=alert(1)>{b}", "extraCssText": "background:url(https://evil.example)"},
		"toolbox": {"feature": {"saveAsImage": {}}},
		"dataset": {"source": [[1, 2]]},
		"graphic": [{"type": "image", "style": {"image": "https://evil.example/a.png"}}, {"type": "text", "style": {"text": "水印"}}],
		"series": [
			{"type": "bar", "symbol": "image://https://evil.example/s.png", "label": {"formatter": "<b>{c}</b> 件"}, "tooltip": {"show": true}},
			{"type": "scatter3D"}
		],
		"backgroundColor": {"image": "https://evil.example/bg.png", "repeat": "repeat"}
	}`), &opt)
	out, warns, err := Sanitize(opt)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(out)
	s := string(b)
	for _, bad := range []string{"evil.example", "<img", "<b>", "extraCssText", "toolbox", `"dataset"`} {
		if strings.Contains(s, bad) {
			t.Errorf("清洗后不应包含 %s：%s", bad, s)
		}
	}
	if !strings.Contains(s, `"formatter":"{c} 件"`) || strings.Count(s, `"renderMode":"richText"`) != 2 || !strings.Contains(s, "水印") {
		t.Fatalf("清洗结果不对：%s", s)
	}
	if len(warns) == 0 {
		t.Fatal("应给出清洗提示")
	}
	if !Is3D(out) {
		t.Fatal("应识别 3D 图表")
	}
	_, _, err = Sanitize(map[string]any{"series": []any{map[string]any{"type": "custom", "renderItem": "x"}}})
	if err == nil {
		t.Fatal("不支持的系列类型应报错")
	}
	big := make([]any, MaxPoints+1)
	for i := range big {
		big[i] = float64(i)
	}
	if _, _, err := Sanitize(map[string]any{"series": []any{map[string]any{"type": "line", "data": big}}}); err == nil {
		t.Fatal("内联数据超过上限应报错")
	}
	// 只数最外层数据项：散点 [x,y] 对不算两个点
	pairs := make([]any, 30000)
	for i := range pairs {
		pairs[i] = []any{float64(i), float64(i)}
	}
	if _, _, err := Sanitize(map[string]any{"series": []any{map[string]any{"type": "scatter", "data": pairs}}}); err != nil {
		t.Fatalf("3 万个散点不应超限：%v", err)
	}
}

func TestDatasetAndEncode(t *testing.T) {
	cols := []driver.Column{{Name: "month", Kind: "string"}, {Name: "sales", Kind: "decimal"}, {Name: "cost", Kind: "int"}}
	rows := [][]any{{"1月", "100.5", "80"}, {"2月", "120", nil}}
	ds, err := BuildDataset(cols, rows, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ds.Source[0][1] != 100.5 || ds.Source[1][2] != nil || ds.Source[0][0] != "1月" {
		t.Fatalf("数值列应转成数字：%v", ds.Source)
	}
	opt := Render(Spec{Option: map[string]any{"series": []any{map[string]any{"type": "line", "smooth": true}}}, Encode: map[string]any{"x": "month", "y": []any{"sales", "cost"}}}, ds)
	series := opt["series"].([]any)
	if len(series) != 2 || opt["xAxis"] == nil || series[1].(map[string]any)["smooth"] != true {
		t.Fatalf("多个 y 列应复制系列并补坐标轴：%v", opt)
	}
	pie := Render(Spec{Encode: map[string]any{"type": "pie", "x": "month", "y": "sales"}}, ds)
	enc := pie["series"].([]any)[0].(map[string]any)["encode"].(map[string]any)
	if enc["itemName"] != "month" || enc["value"] != "sales" || pie["xAxis"] != nil {
		t.Fatalf("饼图的 encode 应为 itemName/value：%v", pie)
	}
	if _, err := BuildDataset(cols, rows, []string{"nope"}); err == nil {
		t.Fatal("不存在的列应报错")
	}
	code, err := SanitizeMermaid("```mermaid\n%%{init: {'securityLevel':'loose'}}%%\nflowchart LR\nA-->B\nclick A \"https://evil.example\"\n```")
	if err != nil || strings.Contains(code, "init") || strings.Contains(code, "click") || !strings.Contains(code, "A-->B") {
		t.Fatalf("Mermaid 清洗不对：%q %v", code, err)
	}
}
