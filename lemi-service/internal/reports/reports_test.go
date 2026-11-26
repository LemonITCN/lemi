package reports

import (
	"testing"
)

func TestParseBlocks(t *testing.T) {
	body := "# 标题\n\n```lemi-chart 01ABCDEF```\n\n```lemi-table 01xyz\n各城市用户数\n```\n\n```sql\nselect 1\n```\n```lemi-diagram 01QQQ\n```\n"
	bs := ParseBlocks(body)
	if len(bs) != 3 {
		t.Fatalf("应识别 3 个引用块：%+v", bs)
	}
	if bs[0].Kind != "chart" || bs[0].ID != "01abcdef" || bs[0].Start != bs[0].End {
		t.Fatalf("单行引用块不对：%+v", bs[0])
	}
	if bs[1].Kind != "table" || bs[1].Caption != "各城市用户数" || bs[1].End != bs[1].Start+2 {
		t.Fatalf("多行引用块不对：%+v", bs[1])
	}
	if bs[2].Kind != "diagram" || bs[2].ID != "01qqq" {
		t.Fatalf("图引用块不对：%+v", bs[2])
	}
}
