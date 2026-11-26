package mysql

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/LemonITCN/lemi/lemi-service/internal/driver"
)

// AlterSQL 按表结构设计器的目标结构生成 DDL。old 为 nil 时生成 CREATE TABLE。
func (c *Conn) AlterSQL(ctx context.Context, db string, old *driver.TableInfo, next *driver.TableDesign) ([]string, error) {
	if strings.TrimSpace(next.Name) == "" {
		return nil, errors.New("请填写表名")
	}
	if len(next.Columns) == 0 {
		return nil, errors.New("表至少要有一列")
	}
	seen := map[string]bool{}
	for _, col := range next.Columns {
		n := strings.ToLower(strings.TrimSpace(col.Name))
		if n == "" {
			return nil, errors.New("列名不能为空")
		}
		if seen[n] {
			return nil, fmt.Errorf("列名重复：%s", col.Name)
		}
		seen[n] = true
		if err := checkType(col.Type); err != nil {
			return nil, fmt.Errorf("列 %s：%w", col.Name, err)
		}
	}
	tbl := QuoteIdent(db) + "." + QuoteIdent(next.Name)
	if old == nil {
		return []string{createSQL(tbl, next)}, nil
	}
	var parts []string
	oldCols := map[string]driver.ColumnInfo{}
	for _, oc := range old.Columns {
		oldCols[oc.Name] = oc
	}
	kept := map[string]bool{}
	prev := ""
	for i, col := range next.Columns {
		pos := " FIRST"
		if i > 0 {
			pos = " AFTER " + QuoteIdent(prev)
		}
		def := columnDef(col)
		if col.OrigName == "" {
			parts = append(parts, "ADD COLUMN "+def+pos)
		} else {
			oc, ok := oldCols[col.OrigName]
			if !ok {
				return nil, fmt.Errorf("原列 %s 不存在（表结构可能已被别人修改，请刷新）", col.OrigName)
			}
			kept[col.OrigName] = true
			if col.OrigName != col.Name || columnChanged(oc, col) || positionChanged(old, col.OrigName, i, next) {
				parts = append(parts, "CHANGE COLUMN "+QuoteIdent(col.OrigName)+" "+def+pos)
			}
		}
		prev = col.Name
	}
	for _, oc := range old.Columns {
		if !kept[oc.Name] {
			parts = append(parts, "DROP COLUMN "+QuoteIdent(oc.Name))
		}
	}
	// 主键
	oldPK := pkCols(old)
	var newPK []string
	for _, col := range next.Columns {
		if col.PrimaryKey {
			newPK = append(newPK, col.Name)
		}
	}
	for _, ix := range next.Indexes {
		if ix.Primary {
			newPK = ix.Columns
		}
	}
	if strings.Join(renameAll(oldPK, next), "\x00") != strings.Join(newPK, "\x00") {
		if len(oldPK) > 0 {
			parts = append(parts, "DROP PRIMARY KEY")
		}
		if len(newPK) > 0 {
			parts = append(parts, "ADD PRIMARY KEY ("+quoteList(newPK)+")")
		}
	}
	// 索引：按名称比较，变了就先删后加
	oldIdx := map[string]driver.IndexInfo{}
	for _, ix := range old.Indexes {
		if !ix.Primary {
			oldIdx[ix.Name] = ix
		}
	}
	newIdx := map[string]bool{}
	for _, ix := range next.Indexes {
		if ix.Primary {
			continue
		}
		newIdx[ix.Name] = true
		if o, ok := oldIdx[ix.Name]; ok && indexEqual(o, ix) {
			continue
		}
		if _, ok := oldIdx[ix.Name]; ok {
			parts = append(parts, "DROP INDEX "+QuoteIdent(ix.Name))
		}
		parts = append(parts, "ADD "+indexDef(ix))
	}
	for name := range oldIdx {
		if !newIdx[name] && !isFKIndex(old, name) {
			parts = append(parts, "DROP INDEX "+QuoteIdent(name))
		}
	}
	// 外键
	oldFK := map[string]driver.ForeignKeyInfo{}
	for _, fk := range old.ForeignKeys {
		oldFK[fk.Name] = fk
	}
	newFK := map[string]bool{}
	var fkAdds []string
	for _, fk := range next.ForeignKeys {
		newFK[fk.Name] = true
		if o, ok := oldFK[fk.Name]; ok && fkEqual(o, fk) {
			continue
		}
		if _, ok := oldFK[fk.Name]; ok {
			parts = append(parts, "DROP FOREIGN KEY "+QuoteIdent(fk.Name))
		}
		fkAdds = append(fkAdds, "ADD "+fkDef(db, fk))
	}
	for name := range oldFK {
		if !newFK[name] {
			parts = append([]string{"DROP FOREIGN KEY " + QuoteIdent(name)}, parts...)
		}
	}
	parts = append(parts, fkAdds...)
	// 表选项
	if next.Comment != old.Comment {
		parts = append(parts, "COMMENT = "+QuoteString(next.Comment))
	}
	if next.Engine != "" && !strings.EqualFold(next.Engine, old.Engine) {
		parts = append(parts, "ENGINE = "+safeWord(next.Engine))
	}
	if next.Collation != "" && !strings.EqualFold(next.Collation, old.Collation) {
		cs := next.Charset
		if cs == "" {
			cs = strings.SplitN(next.Collation, "_", 2)[0]
		}
		parts = append(parts, "CHARACTER SET = "+safeWord(cs)+" COLLATE = "+safeWord(next.Collation))
	}
	if next.AutoInc > 0 && next.AutoInc != old.AutoInc {
		parts = append(parts, "AUTO_INCREMENT = "+strconv.FormatInt(next.AutoInc, 10))
	}
	var out []string
	if next.Name != old.Name {
		out = append(out, "RENAME TABLE "+QuoteIdent(db)+"."+QuoteIdent(old.Name)+" TO "+tbl)
	}
	if len(parts) > 0 {
		out = append(out, "ALTER TABLE "+tbl+"\n  "+strings.Join(parts, ",\n  "))
	}
	return out, nil
}

func renameAll(cols []string, next *driver.TableDesign) []string {
	m := map[string]string{}
	for _, c := range next.Columns {
		if c.OrigName != "" {
			m[c.OrigName] = c.Name
		}
	}
	out := make([]string, len(cols))
	for i, c := range cols {
		if n, ok := m[c]; ok {
			out[i] = n
		} else {
			out[i] = c
		}
	}
	return out
}

func pkCols(t *driver.TableInfo) []string {
	for _, ix := range t.Indexes {
		if ix.Primary {
			return ix.Columns
		}
	}
	return nil
}

func isFKIndex(t *driver.TableInfo, name string) bool {
	for _, fk := range t.ForeignKeys {
		if fk.Name == name {
			return true
		}
	}
	return false
}

func positionChanged(old *driver.TableInfo, orig string, newPos int, next *driver.TableDesign) bool {
	// 与原顺序比较：只看保留下来的列之间的相对顺序
	var oldOrder []string
	keep := map[string]bool{}
	for _, c := range next.Columns {
		if c.OrigName != "" {
			keep[c.OrigName] = true
		}
	}
	for _, c := range old.Columns {
		if keep[c.Name] {
			oldOrder = append(oldOrder, c.Name)
		}
	}
	var newOrder []string
	for _, c := range next.Columns {
		if c.OrigName != "" {
			newOrder = append(newOrder, c.OrigName)
		}
	}
	for i := range newOrder {
		if newOrder[i] == orig {
			return i >= len(oldOrder) || oldOrder[i] != orig
		}
	}
	return false
}

func columnChanged(o driver.ColumnInfo, n driver.ColumnDesign) bool {
	if !strings.EqualFold(strings.TrimSpace(o.Type), strings.TrimSpace(n.Type)) || o.Nullable != n.Nullable || o.Comment != n.Comment || o.AutoIncrement != n.AutoIncrement {
		return true
	}
	if (o.Default == nil) != (n.Default == nil) || (o.Default != nil && n.Default != nil && *o.Default != *n.Default) {
		return true
	}
	if n.Collation != "" && !strings.EqualFold(n.Collation, o.Collation) {
		return true
	}
	return false
}

// checkType 列类型只允许类型名、括号、数字、逗号、引号枚举值与 unsigned/zerofill 等修饰，防止注入额外语句。
func checkType(t string) error {
	t = strings.TrimSpace(t)
	if t == "" {
		return errors.New("请填写类型")
	}
	in := false
	for i := 0; i < len(t); i++ {
		ch := t[i]
		if ch == '\'' {
			if in && i+1 < len(t) && t[i+1] == '\'' {
				i++
				continue
			}
			in = !in
			continue
		}
		if in {
			continue
		}
		if !(ch == ' ' || ch == '(' || ch == ')' || ch == ',' || ch == '_' || (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z')) {
			return fmt.Errorf("类型里有不允许的字符 %q", ch)
		}
	}
	if in {
		return errors.New("类型里的引号不成对")
	}
	return nil
}

func safeWord(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func columnDef(c driver.ColumnDesign) string {
	var b strings.Builder
	b.WriteString(QuoteIdent(c.Name) + " " + strings.TrimSpace(c.Type))
	if c.Collation != "" {
		cs := c.Charset
		if cs == "" {
			cs = strings.SplitN(c.Collation, "_", 2)[0]
		}
		b.WriteString(" CHARACTER SET " + safeWord(cs) + " COLLATE " + safeWord(c.Collation))
	}
	if c.Nullable {
		b.WriteString(" NULL")
	} else {
		b.WriteString(" NOT NULL")
	}
	if c.Default != nil {
		d := *c.Default
		switch {
		case strings.EqualFold(d, "NULL"):
			b.WriteString(" DEFAULT NULL")
		case c.DefaultIsExpr:
			b.WriteString(" DEFAULT " + exprDefault(d))
		default:
			b.WriteString(" DEFAULT " + QuoteString(d))
		}
	}
	if c.AutoIncrement {
		b.WriteString(" AUTO_INCREMENT")
	}
	if c.Comment != "" {
		b.WriteString(" COMMENT " + QuoteString(c.Comment))
	}
	return b.String()
}

// exprDefault 表达式默认值：CURRENT_TIMESTAMP 系列原样写，其他表达式加括号（8.0.13+ 语法）。
func exprDefault(d string) string {
	u := strings.ToUpper(strings.TrimSpace(d))
	if strings.HasPrefix(u, "CURRENT_TIMESTAMP") || strings.HasPrefix(u, "NOW(") || strings.HasPrefix(u, "LOCALTIME") {
		return strings.TrimSpace(d)
	}
	if strings.HasPrefix(u, "(") {
		return strings.TrimSpace(d)
	}
	return "(" + strings.TrimSpace(d) + ")"
}

func quoteList(cols []string) string {
	q := make([]string, len(cols))
	for i, c := range cols {
		q[i] = QuoteIdent(c)
	}
	return strings.Join(q, ", ")
}

func indexDef(ix driver.IndexInfo) string {
	var cols []string
	for i, c := range ix.Columns {
		s := QuoteIdent(c)
		if i < len(ix.Lengths) && ix.Lengths[i] > 0 {
			s += "(" + strconv.Itoa(ix.Lengths[i]) + ")"
		}
		cols = append(cols, s)
	}
	kind := "INDEX"
	switch strings.ToUpper(ix.Type) {
	case "FULLTEXT":
		kind = "FULLTEXT INDEX"
	case "SPATIAL":
		kind = "SPATIAL INDEX"
	default:
		if ix.Unique {
			kind = "UNIQUE INDEX"
		}
	}
	s := kind + " " + QuoteIdent(ix.Name) + " (" + strings.Join(cols, ", ") + ")"
	if ix.Comment != "" {
		s += " COMMENT " + QuoteString(ix.Comment)
	}
	return s
}

func indexEqual(a, b driver.IndexInfo) bool {
	if a.Unique != b.Unique || strings.Join(a.Columns, ",") != strings.Join(b.Columns, ",") || !strings.EqualFold(a.Type, b.Type) && b.Type != "" {
		return false
	}
	for i := range a.Lengths {
		bl := 0
		if i < len(b.Lengths) {
			bl = b.Lengths[i]
		}
		if a.Lengths[i] != bl {
			return false
		}
	}
	return a.Comment == b.Comment
}

var fkActions = map[string]bool{"RESTRICT": true, "CASCADE": true, "SET NULL": true, "NO ACTION": true, "SET DEFAULT": true}

func fkDef(db string, fk driver.ForeignKeyInfo) string {
	ref := QuoteIdent(fk.RefTable)
	if fk.RefDB != "" && fk.RefDB != db {
		ref = QuoteIdent(fk.RefDB) + "." + ref
	}
	s := "CONSTRAINT " + QuoteIdent(fk.Name) + " FOREIGN KEY (" + quoteList(fk.Columns) + ") REFERENCES " + ref + " (" + quoteList(fk.RefColumns) + ")"
	if a := strings.ToUpper(fk.OnDelete); fkActions[a] {
		s += " ON DELETE " + a
	}
	if a := strings.ToUpper(fk.OnUpdate); fkActions[a] {
		s += " ON UPDATE " + a
	}
	return s
}

func fkEqual(a, b driver.ForeignKeyInfo) bool {
	return strings.Join(a.Columns, ",") == strings.Join(b.Columns, ",") && a.RefTable == b.RefTable &&
		strings.Join(a.RefColumns, ",") == strings.Join(b.RefColumns, ",") &&
		strings.EqualFold(a.OnDelete, b.OnDelete) && strings.EqualFold(a.OnUpdate, b.OnUpdate)
}

func createSQL(tbl string, d *driver.TableDesign) string {
	var lines []string
	var pk []string
	for _, c := range d.Columns {
		lines = append(lines, columnDef(c))
		if c.PrimaryKey {
			pk = append(pk, c.Name)
		}
	}
	for _, ix := range d.Indexes {
		if ix.Primary {
			pk = ix.Columns
		}
	}
	if len(pk) > 0 {
		lines = append(lines, "PRIMARY KEY ("+quoteList(pk)+")")
	}
	for _, ix := range d.Indexes {
		if !ix.Primary {
			lines = append(lines, indexDef(ix))
		}
	}
	db := strings.SplitN(tbl, ".", 2)[0]
	for _, fk := range d.ForeignKeys {
		lines = append(lines, fkDef(strings.Trim(db, "`"), fk))
	}
	s := "CREATE TABLE " + tbl + " (\n  " + strings.Join(lines, ",\n  ") + "\n)"
	if d.Engine != "" {
		s += " ENGINE = " + safeWord(d.Engine)
	}
	if d.Collation != "" {
		cs := d.Charset
		if cs == "" {
			cs = strings.SplitN(d.Collation, "_", 2)[0]
		}
		s += " CHARACTER SET = " + safeWord(cs) + " COLLATE = " + safeWord(d.Collation)
	} else if d.Charset != "" {
		s += " CHARACTER SET = " + safeWord(d.Charset)
	}
	if d.AutoInc > 0 {
		s += " AUTO_INCREMENT = " + strconv.FormatInt(d.AutoInc, 10)
	}
	if d.Comment != "" {
		s += " COMMENT = " + QuoteString(d.Comment)
	}
	return s
}
