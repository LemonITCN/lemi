package sqlclass

import (
	"reflect"
	"testing"
)

func TestCTENames(t *testing.T) {
	cases := map[string][]string{
		"WITH a AS (SELECT 1), `b` (x) AS (SELECT * FROM t) SELECT * FROM a JOIN b":      {"a", "b"},
		"WITH RECURSIVE r AS (SELECT 1 UNION ALL SELECT n FROM r) SELECT * FROM r":       {"r"},
		"SELECT * FROM t GROUP BY a WITH ROLLUP":                                         nil,
		"SELECT * FROM (WITH x AS MATERIALIZED (SELECT 1) SELECT * FROM x) y":            {"x"},
		"WITH paid AS NOT MATERIALIZED (SELECT * FROM orders) SELECT COUNT(*) FROM paid": {"paid"},
	}
	for sql, want := range cases {
		if got := CTENames(sql, "mysql"); !reflect.DeepEqual(got, want) {
			t.Errorf("%s：得到 %v，应为 %v", sql, got, want)
		}
	}
}

func TestFromInsideFunctions(t *testing.T) {
	for _, sql := range []string{
		"SELECT EXTRACT(YEAR FROM created_at), COUNT(*) FROM orders GROUP BY 1",
		"SELECT TRIM(LEADING 'x' FROM name) FROM orders",
		"SELECT SUBSTRING(name FROM 2 FOR 3) FROM orders",
	} {
		info := Classify(sql, "mysql")
		if len(info.Tables) != 1 || info.Tables[0].Table != "orders" || !info.TablesComplete {
			t.Errorf("%s：表引用应只有 orders，实际 %+v", sql, info.Tables)
		}
	}
	info := Classify("SELECT * FROM a WHERE id IN (SELECT a_id FROM b)", "mysql")
	if len(info.Tables) != 2 {
		t.Fatalf("子查询里的表也要识别：%+v", info.Tables)
	}
}
