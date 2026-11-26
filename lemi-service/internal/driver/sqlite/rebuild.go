package sqlite

// 表结构设计器在 SQLite 上需要「重建表」时（改类型、可空、默认值、主键、外键、列顺序）的做法。
//
// SQLite 的 ALTER TABLE 能力有限，官方给的是 12 步：新建表 → 复制数据 → 删旧表 → 新表改名 → 重建索引与触发器。
// 设计器的模型里没有 CHECK、表级 UNIQUE、COLLATE、生成列表达式、STRICT / WITHOUT ROWID、部分索引与表达式索引、
// 触发器，如果只按模型生成 CREATE TABLE，这些都会被悄悄丢掉。这里的做法：
//
//  1. 新表先用「原列名」建（改名的列先不改名），原 DDL 里的列级 CHECK / UNIQUE / COLLATE / 生成列子句与表级
//     CHECK / UNIQUE 约束、表选项原样带上——它们引用的都是原列名，原样就成立。
//  2. 数据复制后删旧表，新表改回原表名；原有索引与触发器用 sqlite_master 里保存的原 SQL 原样重建。
//  3. 最后再执行改列名、改表名：ALTER TABLE ... RENAME 会由 SQLite 自己改写触发器、索引、CHECK、视图里的引用，
//     比我们自己改写 SQL 文本可靠。
//
// 引用了要删除的列的约束、索引随列一起去掉；触发器或生成列引用了要删除的列时拒绝，请用户先处理。

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/LemonITCN/lemi/lemi-service/internal/driver"
)

// ---------- 词法 ----------

type tokKind byte

const (
	tkWord   tokKind = 'w' // 不带引号的标识符 / 关键字 / 数字
	tkIdent  tokKind = 'i' // "x"、`x`、[x]
	tkString tokKind = 's' // 'x'
	tkOpen   tokKind = '('
	tkClose  tokKind = ')'
	tkComma  tokKind = ','
	tkOther  tokKind = 'o'
)

type token struct {
	kind       tokKind
	start, end int // 在原文中的位置
	text       string
}

// upper 关键字比较用。
func (t token) upper() string {
	if t.kind != tkWord {
		return ""
	}
	return strings.ToUpper(t.text)
}

// name 标识符的名字（去掉引号）。
func (t token) name() string {
	switch t.kind {
	case tkWord:
		return t.text
	case tkIdent:
		s := t.text
		if len(s) >= 2 {
			q := s[0]
			inner := s[1 : len(s)-1]
			switch q {
			case '"':
				return strings.ReplaceAll(inner, `""`, `"`)
			case '`':
				return strings.ReplaceAll(inner, "``", "`")
			case '[':
				return inner
			}
		}
	}
	return ""
}

func isWordByte(ch byte) bool {
	return ch == '_' || ch == '$' || ch >= 0x80 || (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z')
}

// lex 把 SQL 切成记号，跳过空白与注释。引号不成对时返回错误。
func lex(s string) ([]token, error) {
	var out []token
	for i := 0; i < len(s); {
		ch := s[i]
		switch {
		case ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' || ch == '\f':
			i++
		case ch == '-' && i+1 < len(s) && s[i+1] == '-':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case ch == '/' && i+1 < len(s) && s[i+1] == '*':
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				return nil, errors.New("注释没有结束")
			}
			i += j + 4
		case ch == '\'' || ch == '"' || ch == '`':
			j := i + 1
			for {
				if j >= len(s) {
					return nil, errors.New("引号不成对")
				}
				if s[j] == ch {
					if j+1 < len(s) && s[j+1] == ch {
						j += 2
						continue
					}
					break
				}
				j++
			}
			k := tkIdent
			if ch == '\'' {
				k = tkString
			}
			out = append(out, token{kind: k, start: i, end: j + 1, text: s[i : j+1]})
			i = j + 1
		case ch == '[':
			j := strings.IndexByte(s[i:], ']')
			if j < 0 {
				return nil, errors.New("方括号不成对")
			}
			out = append(out, token{kind: tkIdent, start: i, end: i + j + 1, text: s[i : i+j+1]})
			i += j + 1
		case ch == '(':
			out = append(out, token{kind: tkOpen, start: i, end: i + 1, text: "("})
			i++
		case ch == ')':
			out = append(out, token{kind: tkClose, start: i, end: i + 1, text: ")"})
			i++
		case ch == ',':
			out = append(out, token{kind: tkComma, start: i, end: i + 1, text: ","})
			i++
		case isWordByte(ch):
			j := i
			for j < len(s) && isWordByte(s[j]) {
				j++
			}
			out = append(out, token{kind: tkWord, start: i, end: j, text: s[i:j]})
			i = j
		default:
			out = append(out, token{kind: tkOther, start: i, end: i + 1, text: s[i : i+1]})
			i++
		}
	}
	return out, nil
}

// ---------- CREATE TABLE 解析 ----------

// tableDDL 是解析后的 CREATE TABLE。
type tableDDL struct {
	cols        map[string][]string // 小写列名 → 要保留的列级子句（CHECK、UNIQUE、COLLATE、生成列）
	generated   map[string]bool     // 小写列名 → 是生成列
	constraints []string            // 要保留的表级约束（CHECK、UNIQUE，含 CONSTRAINT 名）
	options     string              // 右括号后面的表选项：WITHOUT ROWID、STRICT
}

var tableConstraintKW = map[string]bool{"CONSTRAINT": true, "PRIMARY": true, "UNIQUE": true, "CHECK": true, "FOREIGN": true}

// parseTableDDL 解析 sqlite_master 里保存的 CREATE TABLE。
func parseTableDDL(ddl string) (*tableDDL, error) {
	toks, err := lex(ddl)
	if err != nil {
		return nil, err
	}
	open := -1
	for i, t := range toks {
		if t.kind == tkOpen {
			open = i
			break
		}
		if t.upper() == "AS" {
			return nil, errors.New("CREATE TABLE ... AS SELECT 建的表没有列定义")
		}
	}
	if open < 0 {
		return nil, errors.New("读不懂表定义")
	}
	depth, close := 0, -1
	for i := open; i < len(toks); i++ {
		switch toks[i].kind {
		case tkOpen:
			depth++
		case tkClose:
			depth--
		}
		if depth == 0 {
			close = i
			break
		}
	}
	if close < 0 {
		return nil, errors.New("表定义的括号不成对")
	}
	out := &tableDDL{cols: map[string][]string{}, generated: map[string]bool{}}
	out.options = strings.TrimSpace(ddl[toks[close].end:])
	// 按顶层逗号切分
	var items [][]token
	cur := []token{}
	depth = 0
	for _, t := range toks[open+1 : close] {
		switch t.kind {
		case tkOpen:
			depth++
		case tkClose:
			depth--
		case tkComma:
			if depth == 0 {
				items = append(items, cur)
				cur = []token{}
				continue
			}
		}
		cur = append(cur, t)
	}
	items = append(items, cur)
	for _, it := range items {
		if len(it) == 0 {
			continue
		}
		if tableConstraintKW[it[0].upper()] {
			// 表级约束：保留 CHECK 与 UNIQUE（主键、外键由设计器的模型生成）
			kw := it[0].upper()
			if kw == "CONSTRAINT" && len(it) >= 3 {
				kw = it[2].upper()
			}
			if kw == "CHECK" || kw == "UNIQUE" {
				out.constraints = append(out.constraints, ddl[it[0].start:it[len(it)-1].end])
			}
			continue
		}
		name := it[0].name()
		if name == "" {
			continue
		}
		keep, gen := columnClauses(ddl, it[1:])
		out.cols[strings.ToLower(name)] = keep
		out.generated[strings.ToLower(name)] = gen
	}
	return out, nil
}

var columnConstraintKW = map[string]bool{
	"CONSTRAINT": true, "PRIMARY": true, "NOT": true, "NULL": true, "UNIQUE": true, "CHECK": true, "DEFAULT": true,
	"COLLATE": true, "REFERENCES": true, "GENERATED": true, "AS": true,
}

// columnClauses 从列定义（去掉列名后的记号）里挑出要保留的子句：UNIQUE、CHECK、COLLATE、生成列。
// 类型、NOT NULL、DEFAULT、PRIMARY KEY 由设计器的模型生成；列级 REFERENCES 已经在外键列表里，由模型生成表级外键。
func columnClauses(src string, toks []token) (keep []string, generated bool) {
	type seg struct{ from, to int } // 记号下标 [from, to)
	var segs []seg
	start := -1
	depth := 0
	for i, t := range toks {
		switch t.kind {
		case tkOpen:
			depth++
			continue
		case tkClose:
			depth--
			continue
		}
		if depth != 0 || !columnConstraintKW[t.upper()] {
			continue
		}
		prev, next := "", ""
		if i > 0 {
			prev = toks[i-1].upper()
		}
		if i+1 < len(toks) {
			next = toks[i+1].upper()
		}
		kw := t.upper()
		// 这些关键字出现在别的子句内部，不是新子句的开头
		if (kw == "NULL" && (prev == "NOT" || prev == "SET")) || (kw == "DEFAULT" && prev == "SET") ||
			(kw == "NOT" && next == "DEFERRABLE") || (kw == "AS" && prev == "ALWAYS") {
			continue
		}
		// CONSTRAINT 名后面紧跟的子句与它算一段
		if start >= 0 && toks[start].upper() == "CONSTRAINT" && i == start+2 {
			continue
		}
		if start >= 0 {
			segs = append(segs, seg{start, i})
		}
		start = i
	}
	if start >= 0 {
		segs = append(segs, seg{start, len(toks)})
	}
	for _, s := range segs {
		kw := toks[s.from].upper()
		if kw == "CONSTRAINT" && s.from+2 < s.to {
			kw = toks[s.from+2].upper()
		}
		switch kw {
		case "UNIQUE", "CHECK", "COLLATE":
		case "GENERATED", "AS":
			generated = true
		default:
			continue
		}
		keep = append(keep, src[toks[s.from].start:toks[s.to-1].end])
	}
	return keep, generated
}

// refsAny 文本里是否以标识符形式引用了 names 中的某个名字（小写）。函数名（后面紧跟左括号）与字符串不算。
func refsAny(text string, names map[string]bool) (string, bool) {
	if len(names) == 0 {
		return "", false
	}
	toks, err := lex(text)
	if err != nil {
		// 读不懂时按引用了处理，宁可拒绝也不要建出坏结构
		for n := range names {
			return n, true
		}
	}
	for i, t := range toks {
		if t.kind != tkWord && t.kind != tkIdent {
			continue
		}
		if t.kind == tkWord && i+1 < len(toks) && toks[i+1].kind == tkOpen {
			continue
		}
		if n := strings.ToLower(t.name()); names[n] {
			return n, true
		}
	}
	return "", false
}

// ---------- 重建 ----------

type schemaObj struct{ name, sql string }

func (c *Conn) schemaObjects(ctx context.Context, typ, table string) ([]schemaObj, error) {
	rows, err := c.ro.QueryContext(ctx, "SELECT name, sql FROM sqlite_master WHERE type = ? AND tbl_name = ? AND sql IS NOT NULL ORDER BY rowid", typ, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []schemaObj
	for rows.Next() {
		var o schemaObj
		if err := rows.Scan(&o.name, &o.sql); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// rebuildSQL 生成重建表的语句（见文件开头的说明）。
func (c *Conn) rebuildSQL(ctx context.Context, old *driver.TableInfo, next *driver.TableDesign) ([]string, error) {
	parsed, err := parseTableDDL(old.DDL)
	if err != nil {
		return nil, fmt.Errorf("读取原表定义失败：%w", err)
	}
	// 新表里的列名：保留的列用原名，新列用新名（与原列名冲突时先用临时名）
	oldNames := map[string]bool{}
	for _, oc := range old.Columns {
		oldNames[strings.ToLower(oc.Name)] = true
	}
	kept := map[string]bool{}
	for _, col := range next.Columns {
		if col.OrigName != "" {
			kept[strings.ToLower(col.OrigName)] = true
		}
	}
	dropped := map[string]bool{}
	for _, oc := range old.Columns {
		if !kept[strings.ToLower(oc.Name)] {
			dropped[strings.ToLower(oc.Name)] = true
		}
	}
	type rename struct{ from, to string }
	var renames []rename
	tmpName := map[string]string{} // 最终列名 → 新表里的列名
	used := map[string]bool{}
	for i, col := range next.Columns {
		tn := col.Name
		if col.OrigName != "" {
			tn = col.OrigName
		} else if oldNames[strings.ToLower(col.Name)] {
			tn = "_lemi_col_" + strconv.Itoa(i)
		}
		if used[strings.ToLower(tn)] {
			return nil, fmt.Errorf("列名 %s 重复", col.Name)
		}
		used[strings.ToLower(tn)] = true
		tmpName[col.Name] = tn
		if tn != col.Name {
			renames = append(renames, rename{tn, col.Name})
		}
	}
	mapCols := func(cols []string) []string {
		out := make([]string, len(cols))
		for i, x := range cols {
			if t, ok := tmpName[x]; ok {
				out[i] = t
			} else {
				out[i] = x
			}
		}
		return out
	}

	// 生成列依赖要删除的列：拒绝
	for _, col := range next.Columns {
		if col.OrigName == "" {
			continue
		}
		lc := strings.ToLower(col.OrigName)
		if parsed.generated[lc] {
			if n, ok := refsAny(strings.Join(parsed.cols[lc], " "), dropped); ok {
				return nil, fmt.Errorf("生成列 %s 用到了要删除的列 %s，请先删除或修改这个生成列", col.Name, n)
			}
		}
	}
	// 触发器引用了要删除的列：拒绝（留着会在改名时报错，删掉又会丢逻辑）
	triggers, err := c.schemaObjects(ctx, "trigger", old.Name)
	if err != nil {
		return nil, err
	}
	for _, tr := range triggers {
		if n, ok := refsAny(tr.sql, dropped); ok {
			return nil, fmt.Errorf("触发器 %s 用到了要删除的列 %s，请先修改或删除这个触发器", tr.name, n)
		}
	}
	indexes, err := c.schemaObjects(ctx, "index", old.Name)
	if err != nil {
		return nil, err
	}
	oldIdxSQL := map[string]string{}
	for _, ix := range indexes {
		oldIdxSQL[ix.name] = ix.sql
	}

	// 新表：设计器的列（换成新表列名）+ 原样保留的子句
	d := *next
	d.Columns = make([]driver.ColumnDesign, len(next.Columns))
	extras := make([][]string, len(next.Columns))
	generated := map[string]bool{}
	for i, col := range next.Columns {
		col.Name = tmpName[col.Name]
		if col.OrigName != "" {
			lc := strings.ToLower(col.OrigName)
			extras[i] = parsed.cols[lc]
			if parsed.generated[lc] {
				generated[col.Name] = true
				col.Default = nil // 生成列不能有默认值
			}
		}
		d.Columns[i] = col
	}
	d.ForeignKeys = make([]driver.ForeignKeyInfo, len(next.ForeignKeys))
	for i, fk := range next.ForeignKeys {
		fk.Columns = mapCols(fk.Columns)
		if strings.EqualFold(fk.RefTable, old.Name) || strings.EqualFold(fk.RefTable, next.Name) {
			fk.RefTable = old.Name // 自引用：新表改回原名后再整体改名，SQLite 会一起改
			fk.RefColumns = mapCols(fk.RefColumns)
		}
		d.ForeignKeys[i] = fk
	}
	var constraints []string
	for _, cs := range parsed.constraints {
		if _, ok := refsAny(cs, dropped); ok {
			continue // 约束里的列被删了，约束随之去掉
		}
		constraints = append(constraints, cs)
	}

	tmp := "_lemi_new_" + old.Name
	tbl := QuoteIdent(old.Name)
	var copyTo, copyFrom []string
	for _, col := range d.Columns {
		if col.OrigName != "" && !generated[col.Name] {
			copyTo = append(copyTo, QuoteIdent(col.Name))
			copyFrom = append(copyFrom, QuoteIdent(col.OrigName))
		}
	}
	create := createSQLWith(tmp, &d, extras, constraints, parsed.options)
	out := []string{"PRAGMA foreign_keys = OFF", "BEGIN", create}
	if len(copyTo) > 0 {
		out = append(out, "INSERT INTO "+QuoteIdent(tmp)+" ("+strings.Join(copyTo, ", ")+") SELECT "+strings.Join(copyFrom, ", ")+" FROM "+tbl)
	}
	if hasInlineAutoInc(&d) {
		// 自增计数器：复制数据只会把它推到现有最大值，删掉过的号会被重新用掉；把原表的计数器接过来
		qt, qo := c.QuoteString(tmp), c.QuoteString(old.Name)
		out = append(out,
			"INSERT INTO sqlite_sequence (name, seq) SELECT "+qt+", 0 WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = "+qt+")",
			"UPDATE sqlite_sequence SET seq = max(seq, coalesce((SELECT seq FROM sqlite_sequence WHERE name = "+qo+"), 0)) WHERE name = "+qt,
		)
	}
	out = append(out,
		"DROP TABLE "+tbl,
		// 视图引用的是原表名，此刻原表不存在；用旧式改名避免 SQLite 检查视图时报错（新表没有被任何对象引用，不需要改写）
		"PRAGMA legacy_alter_table = ON",
		"ALTER TABLE "+QuoteIdent(tmp)+" RENAME TO "+tbl,
		"PRAGMA legacy_alter_table = OFF",
	)

	// 索引：没变的用原 SQL，变了的、新加的按模型生成
	oldIdx := map[string]driver.IndexInfo{}
	for _, ix := range old.Indexes {
		oldIdx[ix.Name] = ix
	}
	finalOf := map[string]string{} // 原列名 → 最终列名
	for _, col := range next.Columns {
		if col.OrigName != "" {
			finalOf[col.OrigName] = col.Name
		}
	}
	for _, ix := range next.Indexes {
		if ix.Primary || strings.HasPrefix(ix.Name, "sqlite_autoindex_") {
			continue
		}
		if o, ok := oldIdx[ix.Name]; ok && o.Unique == ix.Unique && oldIdxSQL[ix.Name] != "" {
			same := len(o.Columns) == len(ix.Columns)
			for k := 0; same && k < len(o.Columns); k++ {
				oc := o.Columns[k]
				if f, ok := finalOf[oc]; ok {
					oc = f
				}
				same = oc == ix.Columns[k]
			}
			if same {
				if _, bad := refsAny(oldIdxSQL[ix.Name], dropped); !bad {
					out = append(out, oldIdxSQL[ix.Name])
				}
				continue
			}
		}
		if strings.Contains(strings.Join(ix.Columns, ","), "(表达式)") {
			continue // 表达式索引只能原样保留；列被删了就随之去掉
		}
		ix.Columns = mapCols(ix.Columns)
		out = append(out, createIndex(old.Name, ix))
	}
	for _, tr := range triggers {
		out = append(out, tr.sql)
	}
	// 最后改名：SQLite 会改写触发器、索引、约束、视图里的引用
	cur := map[string]bool{}
	for n := range used {
		cur[n] = true
	}
	for len(renames) > 0 {
		progressed := false
		for i := 0; i < len(renames); i++ {
			r := renames[i]
			if cur[strings.ToLower(r.to)] && !strings.EqualFold(r.from, r.to) {
				continue
			}
			out = append(out, "ALTER TABLE "+tbl+" RENAME COLUMN "+QuoteIdent(r.from)+" TO "+QuoteIdent(r.to))
			delete(cur, strings.ToLower(r.from))
			cur[strings.ToLower(r.to)] = true
			renames = append(renames[:i], renames[i+1:]...)
			i--
			progressed = true
		}
		if !progressed {
			// 互换名字（a↔b）：先把其中一个挪到临时名
			r := renames[0]
			t := "_lemi_swap_" + strconv.Itoa(len(out))
			out = append(out, "ALTER TABLE "+tbl+" RENAME COLUMN "+QuoteIdent(r.from)+" TO "+QuoteIdent(t))
			delete(cur, strings.ToLower(r.from))
			cur[strings.ToLower(t)] = true
			renames[0].from = t
		}
	}
	if next.Name != old.Name {
		out = append(out, "ALTER TABLE "+tbl+" RENAME TO "+QuoteIdent(next.Name))
	}
	out = append(out, "PRAGMA foreign_key_check("+c.QuoteString(next.Name)+")", "COMMIT", "PRAGMA foreign_keys = ON")
	return out, nil
}

// hasInlineAutoInc 新表是否有 INTEGER PRIMARY KEY AUTOINCREMENT 列（与 createSQLWith 的判断一致）。
func hasInlineAutoInc(d *driver.TableDesign) bool {
	n := 0
	var pk driver.ColumnDesign
	for _, c := range d.Columns {
		if c.PrimaryKey {
			n++
			pk = c
		}
	}
	return n == 1 && pk.AutoIncrement && strings.EqualFold(strings.TrimSpace(pk.Type), "INTEGER")
}
