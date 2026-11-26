package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/LemonITCN/lemi/lemi-service/internal/driver"
)

// Databases SQLite 只有 main。
func (c *Conn) Databases(ctx context.Context) ([]driver.DatabaseInfo, error) {
	d := driver.DatabaseInfo{Name: "main"}
	if fi, err := os.Stat(c.path); err == nil {
		d.SizeBytes = fi.Size()
	}
	_ = c.ro.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'").Scan(&d.Tables)
	return []driver.DatabaseInfo{d}, nil
}

func checkDB(db string) error {
	if db != "" && db != "main" {
		return errors.New("SQLite 连接只有 main 库")
	}
	return nil
}

// Objects 全部对象。
func (c *Conn) Objects(ctx context.Context, db string) (*driver.ObjectList, error) {
	if err := checkDB(db); err != nil {
		return nil, err
	}
	out := &driver.ObjectList{Tables: []driver.ObjectInfo{}, Views: []driver.ObjectInfo{}, Procedures: []driver.ObjectInfo{}, Functions: []driver.ObjectInfo{}, Triggers: []driver.ObjectInfo{}, Events: []driver.ObjectInfo{}}
	rows, err := c.ro.QueryContext(ctx, "SELECT type, name, tbl_name FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var typ, name, tbl string
		if err := rows.Scan(&typ, &name, &tbl); err != nil {
			return nil, err
		}
		switch typ {
		case "table":
			out.Tables = append(out.Tables, driver.ObjectInfo{Name: name})
		case "view":
			out.Views = append(out.Views, driver.ObjectInfo{Name: name})
		case "trigger":
			out.Triggers = append(out.Triggers, driver.ObjectInfo{Name: name, Table: tbl})
		}
	}
	return out, rows.Err()
}

// Table 表结构。
func (c *Conn) Table(ctx context.Context, db, table string) (*driver.TableInfo, error) {
	if err := checkDB(db); err != nil {
		return nil, err
	}
	t := &driver.TableInfo{Database: "main", Name: table, Kind: "table", Columns: []driver.ColumnInfo{}, Indexes: []driver.IndexInfo{}, ForeignKeys: []driver.ForeignKeyInfo{}, Referenced: []driver.ForeignKeyInfo{}, Triggers: []driver.ObjectInfo{}}
	var typ, ddl sql.NullString
	err := c.ro.QueryRowContext(ctx, "SELECT type, sql FROM sqlite_master WHERE name = ? AND type IN ('table','view')", table).Scan(&typ, &ddl)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("表 %s 不存在", table)
	}
	if err != nil {
		return nil, err
	}
	t.Kind, t.DDL = typ.String, ddl.String
	upper := strings.ToUpper(ddl.String)
	t.WithoutRowid = strings.Contains(upper, "WITHOUT ROWID")
	autoinc := strings.Contains(upper, "AUTOINCREMENT")
	rows, err := c.ro.QueryContext(ctx, "SELECT cid, name, type, \"notnull\", dflt_value, pk, hidden FROM pragma_table_xinfo(?)", table)
	if err != nil {
		return nil, err
	}
	type pkc struct {
		name string
		pos  int
	}
	var pks []pkc
	for rows.Next() {
		var cid, notnull, pk, hidden int
		var name, ctype string
		var def sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &def, &pk, &hidden); err != nil {
			rows.Close()
			return nil, err
		}
		col := driver.ColumnInfo{Name: name, Type: ctype, DataType: strings.ToLower(strings.SplitN(ctype, "(", 2)[0]), Kind: ColumnKind(ctype), Nullable: notnull == 0 && pk == 0}
		if def.Valid {
			v := def.String
			if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
				v = strings.ReplaceAll(v[1:len(v)-1], "''", "'")
			} else if !isNumber(v) && !strings.EqualFold(v, "NULL") {
				col.DefaultIsExpr = true
			}
			col.Default = &v
		}
		switch hidden {
		case 2:
			col.Generated = "virtual"
		case 3:
			col.Generated = "stored"
		}
		if pk > 0 {
			col.PrimaryKey = true
			pks = append(pks, pkc{name, pk})
			if autoinc && strings.EqualFold(ctype, "INTEGER") {
				col.AutoIncrement = true
			}
		}
		t.Columns = append(t.Columns, col)
	}
	rows.Close()
	if len(pks) > 0 {
		key := make([]string, len(pks))
		for _, p := range pks {
			if p.pos-1 < len(key) {
				key[p.pos-1] = p.name
			}
		}
		t.Indexes = append(t.Indexes, driver.IndexInfo{Name: "PRIMARY", Columns: key, Unique: true, Primary: true})
		t.Key, t.KeyKind = key, "primary"
	}
	// 索引
	ir, err := c.ro.QueryContext(ctx, "SELECT name, \"unique\", origin FROM pragma_index_list(?)", table)
	if err == nil {
		var idx []driver.IndexInfo
		for ir.Next() {
			var name, origin string
			var uniq int
			if ir.Scan(&name, &uniq, &origin) == nil && origin != "pk" {
				idx = append(idx, driver.IndexInfo{Name: name, Unique: uniq == 1})
			}
		}
		ir.Close()
		for i := range idx {
			cr, err := c.ro.QueryContext(ctx, "SELECT name FROM pragma_index_info(?) ORDER BY seqno", idx[i].Name)
			if err != nil {
				continue
			}
			for cr.Next() {
				var n sql.NullString
				if cr.Scan(&n) == nil {
					if n.Valid {
						idx[i].Columns = append(idx[i].Columns, n.String)
					} else {
						idx[i].Columns = append(idx[i].Columns, "(表达式)")
					}
					idx[i].Lengths = append(idx[i].Lengths, 0)
				}
			}
			cr.Close()
			t.Indexes = append(t.Indexes, idx[i])
		}
	}
	// 外键
	fr, err := c.ro.QueryContext(ctx, "SELECT id, \"table\", \"from\", \"to\", on_update, on_delete FROM pragma_foreign_key_list(?) ORDER BY id, seq", table)
	if err == nil {
		m := map[int]int{}
		for fr.Next() {
			var id int
			var rt, from, onu, ond string
			var to sql.NullString
			if fr.Scan(&id, &rt, &from, &to, &onu, &ond) != nil {
				continue
			}
			i, ok := m[id]
			if !ok {
				t.ForeignKeys = append(t.ForeignKeys, driver.ForeignKeyInfo{Name: "fk_" + strconv.Itoa(id), Table: table, RefDB: "main", RefTable: rt, OnUpdate: onu, OnDelete: ond})
				i = len(t.ForeignKeys) - 1
				m[id] = i
			}
			t.ForeignKeys[i].Columns = append(t.ForeignKeys[i].Columns, from)
			t.ForeignKeys[i].RefColumns = append(t.ForeignKeys[i].RefColumns, to.String)
		}
		fr.Close()
	}
	// 被引用
	if tr, err := c.ro.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type = 'table' AND name <> ?", table); err == nil {
		var others []string
		for tr.Next() {
			var n string
			if tr.Scan(&n) == nil {
				others = append(others, n)
			}
		}
		tr.Close()
		for _, o := range others {
			r, err := c.ro.QueryContext(ctx, "SELECT id, \"table\", \"from\", \"to\" FROM pragma_foreign_key_list(?) ORDER BY id, seq", o)
			if err != nil {
				continue
			}
			m := map[int]int{}
			for r.Next() {
				var id int
				var rt, from string
				var to sql.NullString
				if r.Scan(&id, &rt, &from, &to) != nil || !strings.EqualFold(rt, table) {
					continue
				}
				i, ok := m[id]
				if !ok {
					t.Referenced = append(t.Referenced, driver.ForeignKeyInfo{Name: "fk_" + strconv.Itoa(id), Table: o, RefDB: "main", RefTable: table})
					i = len(t.Referenced) - 1
					m[id] = i
				}
				t.Referenced[i].Columns = append(t.Referenced[i].Columns, from)
				t.Referenced[i].RefColumns = append(t.Referenced[i].RefColumns, to.String)
			}
			r.Close()
		}
	}
	// 触发器
	if r, err := c.ro.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type = 'trigger' AND tbl_name = ?", table); err == nil {
		for r.Next() {
			var n string
			if r.Scan(&n) == nil {
				t.Triggers = append(t.Triggers, driver.ObjectInfo{Name: n, Table: table})
			}
		}
		r.Close()
	}
	// 没有主键时：唯一非空索引，否则 rowid
	if len(t.Key) == 0 && t.Kind == "table" {
		notNull := map[string]bool{}
		for _, col := range t.Columns {
			notNull[col.Name] = !col.Nullable
		}
		for _, ix := range t.Indexes {
			if !ix.Unique {
				continue
			}
			ok := true
			for _, col := range ix.Columns {
				if !notNull[col] {
					ok = false
				}
			}
			if ok {
				t.Key, t.KeyKind = ix.Columns, "unique"
				break
			}
		}
		if len(t.Key) == 0 && !t.WithoutRowid {
			t.Key, t.KeyKind = []string{"rowid"}, "rowid"
		}
	}
	_ = c.ro.QueryRowContext(ctx, "SELECT seq FROM sqlite_sequence WHERE name = ?", table).Scan(&t.AutoInc)
	return t, nil
}

func isNumber(s string) bool {
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

// DDL 对象定义。
func (c *Conn) DDL(ctx context.Context, db, kind, name string) (string, error) {
	if err := checkDB(db); err != nil {
		return "", err
	}
	var s sql.NullString
	err := c.ro.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE name = ?", name).Scan(&s)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("%s 不存在", name)
	}
	if err != nil {
		return "", err
	}
	return s.String + ";", nil
}

var typeRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_ ]*(\(\s*\d+\s*(,\s*\d+\s*)?\))?$`)

// AlterSQL 表结构设计器：新建表直接 CREATE；修改时能用 ALTER TABLE 完成的（改名、加列、删列、改列名）就用，
// 其他修改按 SQLite 官方 12 步流程重建表（建新表 → 复制数据 → 删旧表 → 改名 → 重建索引与触发器）。
func (c *Conn) AlterSQL(ctx context.Context, db string, old *driver.TableInfo, next *driver.TableDesign) ([]string, error) {
	if err := checkDB(db); err != nil {
		return nil, err
	}
	if strings.TrimSpace(next.Name) == "" || len(next.Columns) == 0 {
		return nil, errors.New("请填写表名并至少保留一列")
	}
	for _, col := range next.Columns {
		if strings.TrimSpace(col.Name) == "" {
			return nil, errors.New("列名不能为空")
		}
		if col.Type != "" && !typeRe.MatchString(strings.TrimSpace(col.Type)) {
			return nil, fmt.Errorf("列 %s 的类型不正确", col.Name)
		}
	}
	if old == nil {
		return []string{createSQL(next.Name, next)}, nil
	}
	// 判断是否只需要简单修改
	simple := true
	oldCols := map[string]driver.ColumnInfo{}
	for _, oc := range old.Columns {
		oldCols[oc.Name] = oc
	}
	var stmts []string
	kept := map[string]bool{}
	tbl := QuoteIdent(old.Name)
	for i, col := range next.Columns {
		if col.OrigName == "" {
			if col.PrimaryKey || (col.Default != nil && col.DefaultIsExpr) || (!col.Nullable && col.Default == nil) {
				simple = false
			}
			stmts = append(stmts, "ALTER TABLE "+tbl+" ADD COLUMN "+columnDef(col, false))
			continue
		}
		oc, ok := oldCols[col.OrigName]
		if !ok {
			return nil, fmt.Errorf("原列 %s 不存在（请刷新）", col.OrigName)
		}
		kept[col.OrigName] = true
		if !strings.EqualFold(oc.Type, col.Type) || oc.Nullable != col.Nullable || oc.PrimaryKey != col.PrimaryKey || !sameDefault(oc, col) {
			simple = false
		}
		if col.OrigName != col.Name {
			stmts = append(stmts, "ALTER TABLE "+tbl+" RENAME COLUMN "+QuoteIdent(col.OrigName)+" TO "+QuoteIdent(col.Name))
		}
		// 顺序改变需要重建
		if i < len(old.Columns) && old.Columns[i].Name != col.OrigName {
			simple = false
		}
	}
	for _, oc := range old.Columns {
		if !kept[oc.Name] {
			if oc.PrimaryKey {
				simple = false
			}
			stmts = append(stmts, "ALTER TABLE "+tbl+" DROP COLUMN "+QuoteIdent(oc.Name))
		}
	}
	if len(next.ForeignKeys) != len(old.ForeignKeys) {
		simple = false
	}
	idxStmts, idxSimple := indexChanges(old, next)
	if !idxSimple {
		simple = false
	}
	if simple {
		if next.Name != old.Name {
			stmts = append(stmts, "ALTER TABLE "+tbl+" RENAME TO "+QuoteIdent(next.Name))
		}
		return append(stmts, idxStmts...), nil
	}
	// 12 步重建
	tmp := "_lemi_new_" + next.Name
	var copyCols, fromCols []string
	for _, col := range next.Columns {
		if col.OrigName != "" {
			copyCols = append(copyCols, QuoteIdent(col.Name))
			fromCols = append(fromCols, QuoteIdent(col.OrigName))
		}
	}
	out := []string{
		"PRAGMA foreign_keys = OFF",
		"BEGIN",
		createSQL(tmp, next),
		"INSERT INTO " + QuoteIdent(tmp) + " (" + strings.Join(copyCols, ", ") + ") SELECT " + strings.Join(fromCols, ", ") + " FROM " + tbl,
		"DROP TABLE " + tbl,
		"ALTER TABLE " + QuoteIdent(tmp) + " RENAME TO " + QuoteIdent(next.Name),
	}
	for _, ix := range next.Indexes {
		if !ix.Primary {
			out = append(out, createIndex(next.Name, ix))
		}
	}
	out = append(out, "PRAGMA foreign_key_check("+c.QuoteString(next.Name)+")", "COMMIT", "PRAGMA foreign_keys = ON")
	return out, nil
}

func sameDefault(o driver.ColumnInfo, n driver.ColumnDesign) bool {
	if (o.Default == nil) != (n.Default == nil) {
		return false
	}
	return o.Default == nil || *o.Default == *n.Default
}

func indexChanges(old *driver.TableInfo, next *driver.TableDesign) ([]string, bool) {
	oldIdx := map[string]driver.IndexInfo{}
	for _, ix := range old.Indexes {
		if !ix.Primary && !strings.HasPrefix(ix.Name, "sqlite_autoindex_") {
			oldIdx[ix.Name] = ix
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, ix := range next.Indexes {
		if ix.Primary {
			continue
		}
		seen[ix.Name] = true
		if o, ok := oldIdx[ix.Name]; ok {
			if o.Unique == ix.Unique && strings.Join(o.Columns, ",") == strings.Join(ix.Columns, ",") {
				continue
			}
			out = append(out, "DROP INDEX "+QuoteIdent(ix.Name))
		}
		out = append(out, createIndex(next.Name, ix))
	}
	for name := range oldIdx {
		if !seen[name] {
			out = append(out, "DROP INDEX "+QuoteIdent(name))
		}
	}
	return out, true
}

func createIndex(table string, ix driver.IndexInfo) string {
	var cols []string
	for _, c := range ix.Columns {
		cols = append(cols, QuoteIdent(c))
	}
	u := ""
	if ix.Unique {
		u = "UNIQUE "
	}
	return "CREATE " + u + "INDEX " + QuoteIdent(ix.Name) + " ON " + QuoteIdent(table) + " (" + strings.Join(cols, ", ") + ")"
}

func columnDef(c driver.ColumnDesign, inlinePK bool) string {
	s := QuoteIdent(c.Name)
	if t := strings.TrimSpace(c.Type); t != "" {
		s += " " + t
	}
	if inlinePK {
		s += " PRIMARY KEY"
		if c.AutoIncrement {
			s += " AUTOINCREMENT"
		}
	}
	if !c.Nullable && !inlinePK {
		s += " NOT NULL"
	}
	if c.Default != nil {
		d := *c.Default
		switch {
		case strings.EqualFold(d, "NULL"):
			s += " DEFAULT NULL"
		case c.DefaultIsExpr:
			s += " DEFAULT (" + d + ")"
		case isNumber(d):
			s += " DEFAULT " + d
		default:
			s += " DEFAULT '" + strings.ReplaceAll(d, "'", "''") + "'"
		}
	}
	return s
}

func createSQL(name string, d *driver.TableDesign) string {
	var pk []string
	for _, c := range d.Columns {
		if c.PrimaryKey {
			pk = append(pk, c.Name)
		}
	}
	var lines []string
	for _, c := range d.Columns {
		inline := len(pk) == 1 && c.PrimaryKey && strings.EqualFold(strings.TrimSpace(c.Type), "INTEGER")
		lines = append(lines, columnDef(c, inline))
	}
	if len(pk) > 1 || (len(pk) == 1 && !inlinePKUsed(d, pk[0])) {
		var q []string
		for _, p := range pk {
			q = append(q, QuoteIdent(p))
		}
		lines = append(lines, "PRIMARY KEY ("+strings.Join(q, ", ")+")")
	}
	for _, fk := range d.ForeignKeys {
		var a, b []string
		for _, x := range fk.Columns {
			a = append(a, QuoteIdent(x))
		}
		for _, x := range fk.RefColumns {
			b = append(b, QuoteIdent(x))
		}
		s := "FOREIGN KEY (" + strings.Join(a, ", ") + ") REFERENCES " + QuoteIdent(fk.RefTable) + " (" + strings.Join(b, ", ") + ")"
		for _, act := range []struct{ k, v string }{{"ON DELETE", fk.OnDelete}, {"ON UPDATE", fk.OnUpdate}} {
			switch strings.ToUpper(act.v) {
			case "CASCADE", "SET NULL", "SET DEFAULT", "RESTRICT":
				s += " " + act.k + " " + strings.ToUpper(act.v)
			}
		}
		lines = append(lines, s)
	}
	return "CREATE TABLE " + QuoteIdent(name) + " (\n  " + strings.Join(lines, ",\n  ") + "\n)"
}

func inlinePKUsed(d *driver.TableDesign, pk string) bool {
	for _, c := range d.Columns {
		if c.Name == pk {
			return strings.EqualFold(strings.TrimSpace(c.Type), "INTEGER")
		}
	}
	return false
}

// Status 文件与页面统计。
func (c *Conn) Status(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	for _, p := range []string{"page_count", "page_size", "freelist_count", "journal_mode", "auto_vacuum", "encoding", "user_version", "schema_version"} {
		var v sql.NullString
		if c.ro.QueryRowContext(ctx, "PRAGMA "+p).Scan(&v) == nil {
			out[p] = v.String
		}
	}
	if fi, err := os.Stat(c.path); err == nil {
		out["file_size"] = strconv.FormatInt(fi.Size(), 10)
	}
	return out, nil
}

// Variables 编译选项。
func (c *Conn) Variables(ctx context.Context) (map[string]string, error) {
	rows, err := c.ro.QueryContext(ctx, "PRAGMA compile_options")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var s string
		if rows.Scan(&s) == nil {
			k, v, _ := strings.Cut(s, "=")
			if v == "" {
				v = "1"
			}
			out[k] = v
		}
	}
	return out, nil
}

// Processes SQLite 没有进程列表。
func (c *Conn) Processes(ctx context.Context) ([]map[string]any, error) { return []map[string]any{}, nil }

// KillProcess 不支持。
func (c *Conn) KillProcess(ctx context.Context, id string, queryOnly bool) error {
	return driver.ErrUnsupported
}

// TableSizes 用 dbstat 虚拟表统计（未编译 dbstat 时返回行数估计）。
func (c *Conn) TableSizes(ctx context.Context, limit int) ([]map[string]any, error) {
	rows, err := c.ro.QueryContext(ctx, "SELECT name, SUM(pgsize) AS total_bytes FROM dbstat GROUP BY name ORDER BY total_bytes DESC LIMIT ?", limit)
	if err != nil {
		return nil, fmt.Errorf("当前 SQLite 不支持 dbstat 统计：%w", err)
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var n string
		var b int64
		if rows.Scan(&n, &b) == nil {
			out = append(out, map[string]any{"db": "main", "name": n, "total_bytes": b})
		}
	}
	return out, nil
}

// SlowQueries 不支持。
func (c *Conn) SlowQueries(ctx context.Context, limit int) ([]map[string]any, error) {
	return nil, driver.ErrUnsupported
}

// IntegrityCheck 完整性检查。
func (c *Conn) IntegrityCheck(ctx context.Context) ([]string, error) {
	rows, err := c.ro.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if rows.Scan(&s) == nil {
			out = append(out, s)
		}
	}
	return out, nil
}
