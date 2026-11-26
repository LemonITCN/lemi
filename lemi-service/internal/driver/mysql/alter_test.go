package mysql

import (
	"context"
	"strings"
	"testing"

	"github.com/LemonITCN/lemi/lemi-service/internal/driver"
)

func sp(s string) *string { return &s }

// 表：id 主键、updated_at 带 ON UPDATE、total 生成列、secret 不可见列
func sampleOld() *driver.TableInfo {
	return &driver.TableInfo{
		Name: "t",
		Columns: []driver.ColumnInfo{
			{Name: "id", Type: "bigint", PrimaryKey: true, AutoIncrement: true},
			{Name: "price", Type: "decimal(10,2)", Nullable: true},
			{Name: "qty", Type: "int", Nullable: true},
			{Name: "total", Type: "decimal(12,2)", Nullable: true, Generated: "virtual", GenerationExpr: "(`price` * `qty`)"},
			{Name: "updated_at", Type: "datetime", Default: sp("CURRENT_TIMESTAMP"), DefaultIsExpr: true, OnUpdate: "CURRENT_TIMESTAMP"},
			{Name: "secret", Type: "varchar(32)", Nullable: true, Invisible: true},
		},
		Indexes: []driver.IndexInfo{{Name: "PRIMARY", Columns: []string{"id"}, Unique: true, Primary: true}},
	}
}

// 设计器把已有列原样带回（包括新增的三个属性）
func sameDesign(old *driver.TableInfo) *driver.TableDesign {
	d := &driver.TableDesign{Name: old.Name}
	for _, c := range old.Columns {
		d.Columns = append(d.Columns, driver.ColumnDesign{
			OrigName: c.Name, Name: c.Name, Type: c.Type, Nullable: c.Nullable, Default: c.Default, DefaultIsExpr: c.DefaultIsExpr,
			AutoIncrement: c.AutoIncrement, PrimaryKey: c.PrimaryKey, OnUpdate: c.OnUpdate, Generated: c.Generated,
			GenerationExpr: c.GenerationExpr, Invisible: c.Invisible,
		})
	}
	return d
}

func alter(t *testing.T, old *driver.TableInfo, d *driver.TableDesign) string {
	t.Helper()
	out, err := (&Conn{}).AlterSQL(context.Background(), "db", old, d)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(out, "\n")
}

func TestAlterNoChange(t *testing.T) {
	if s := alter(t, sampleOld(), sameDesign(sampleOld())); s != "" {
		t.Fatalf("没改动却生成了：%s", s)
	}
}

func TestAlterKeepsOnUpdateWhenCommentChanges(t *testing.T) {
	d := sameDesign(sampleOld())
	d.Columns[4].Comment = "更新时间"
	s := alter(t, sampleOld(), d)
	want := "CHANGE COLUMN `updated_at` `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间'"
	if !strings.Contains(s, want) {
		t.Fatalf("ON UPDATE 丢了：\n%s", s)
	}
}

func TestAlterKeepsInvisible(t *testing.T) {
	d := sameDesign(sampleOld())
	d.Columns[5].Type = "varchar(64)"
	s := alter(t, sampleOld(), d)
	if !strings.Contains(s, "`secret` varchar(64) NULL INVISIBLE") {
		t.Fatalf("INVISIBLE 丢了：\n%s", s)
	}
}

func TestAlterMoveKeepsGeneratedEvenIfDesignOmitsIt(t *testing.T) {
	old := sampleOld()
	d := sameDesign(old)
	// 设计器没带生成列属性（旧界面）也不能把生成列改成普通列
	d.Columns[3].Generated, d.Columns[3].GenerationExpr = "", ""
	// 把 updated_at 挪到 total 前面，total 的位置随之变化
	d.Columns[3], d.Columns[4] = d.Columns[4], d.Columns[3]
	s := alter(t, old, d)
	if !strings.Contains(s, "`total` decimal(12,2) GENERATED ALWAYS AS ((`price` * `qty`)) VIRTUAL NULL") {
		t.Fatalf("生成列定义丢了：\n%s", s)
	}
}

func TestAlterOnUpdateRemovedAndAdded(t *testing.T) {
	d := sameDesign(sampleOld())
	d.Columns[4].OnUpdate = ""
	if s := alter(t, sampleOld(), d); !strings.Contains(s, "CHANGE COLUMN `updated_at`") || strings.Contains(s, "ON UPDATE") {
		t.Fatalf("去掉 ON UPDATE 不对：\n%s", s)
	}
	d = sameDesign(sampleOld())
	d.Columns[4].OnUpdate = "current_timestamp()" // MariaDB 写法，与原值等价，不算变化
	if s := alter(t, sampleOld(), d); s != "" {
		t.Fatalf("等价写法不该算变化：\n%s", s)
	}
	d.Columns[4].OnUpdate = "CURRENT_TIMESTAMP(3)"
	d.Columns[4].Type = "datetime(3)"
	if s := alter(t, sampleOld(), d); !strings.Contains(s, "ON UPDATE CURRENT_TIMESTAMP(3)") {
		t.Fatalf("精度没带上：\n%s", s)
	}
}

func TestAlterRejectsBadExtras(t *testing.T) {
	bad := []driver.ColumnDesign{
		{Name: "a", Type: "datetime", OnUpdate: "CURRENT_TIMESTAMP; DROP TABLE x"},
		{Name: "a", Type: "datetime", OnUpdate: "(SELECT 1)"},
		{Name: "a", Type: "int", Generated: "virtual", GenerationExpr: "1); DROP TABLE x; -- "},
		{Name: "a", Type: "int", Generated: "virtual", GenerationExpr: "1 -- x"},
		{Name: "a", Type: "int", Generated: "virtual", GenerationExpr: "(1"},
		{Name: "a", Type: "int", Generated: "virtual", GenerationExpr: "'a"},
		{Name: "a", Type: "int", Generated: "both", GenerationExpr: "1"},
		{Name: "a", Type: "int", Generated: "virtual"},
	}
	for _, c := range bad {
		if _, err := (&Conn{}).AlterSQL(context.Background(), "db", nil, &driver.TableDesign{Name: "t", Columns: []driver.ColumnDesign{c}}); err == nil {
			t.Errorf("应当拒绝：%+v", c)
		}
	}
	ok := driver.ColumnDesign{Name: "a", Type: "varchar(10)", Generated: "stored", GenerationExpr: "concat('a;b', `x`, \"--\")"}
	if _, err := (&Conn{}).AlterSQL(context.Background(), "db", nil, &driver.TableDesign{Name: "t", Columns: []driver.ColumnDesign{ok}}); err != nil {
		t.Errorf("引号里的分号与横线应当允许：%v", err)
	}
}

func TestOnUpdateOf(t *testing.T) {
	cases := map[string]string{
		"on update CURRENT_TIMESTAMP":                             "CURRENT_TIMESTAMP",
		"DEFAULT_GENERATED on update CURRENT_TIMESTAMP(3)":        "CURRENT_TIMESTAMP(3)",
		"on update current_timestamp()":                           "current_timestamp()",
		"DEFAULT_GENERATED on update CURRENT_TIMESTAMP INVISIBLE": "CURRENT_TIMESTAMP",
		"auto_increment": "",
		"":               "",
	}
	for in, want := range cases {
		if got := onUpdateOf(in); got != want {
			t.Errorf("onUpdateOf(%q) = %q，应为 %q", in, got, want)
		}
	}
}
