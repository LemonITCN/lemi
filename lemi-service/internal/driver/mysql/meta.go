package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/LemonITCN/lemi/lemi-service/internal/driver"
)

var systemDBs = map[string]bool{"mysql": true, "sys": true, "information_schema": true, "performance_schema": true}

// Databases 全部库。
func (c *Conn) Databases(ctx context.Context) ([]driver.DatabaseInfo, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT SCHEMA_NAME, DEFAULT_CHARACTER_SET_NAME, DEFAULT_COLLATION_NAME FROM information_schema.SCHEMATA ORDER BY SCHEMA_NAME`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []driver.DatabaseInfo
	for rows.Next() {
		var d driver.DatabaseInfo
		if err := rows.Scan(&d.Name, &d.Charset, &d.Collation); err != nil {
			return nil, err
		}
		d.System = systemDBs[strings.ToLower(d.Name)]
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 表数量与大小（失败不影响列表）
	if r2, err := c.db.QueryContext(ctx, `SELECT TABLE_SCHEMA, COUNT(*), COALESCE(SUM(DATA_LENGTH + INDEX_LENGTH), 0) FROM information_schema.TABLES GROUP BY TABLE_SCHEMA`); err == nil {
		defer r2.Close()
		m := map[string][2]int64{}
		for r2.Next() {
			var n string
			var cnt, size int64
			if r2.Scan(&n, &cnt, &size) == nil {
				m[n] = [2]int64{cnt, size}
			}
		}
		for i := range out {
			if v, ok := m[out[i].Name]; ok {
				out[i].Tables, out[i].SizeBytes = int(v[0]), v[1]
			}
		}
	}
	return out, nil
}

func nstr(s sql.NullString) string { return s.String }

// Objects 一个库里的全部对象。
func (c *Conn) Objects(ctx context.Context, db string) (*driver.ObjectList, error) {
	out := &driver.ObjectList{Tables: []driver.ObjectInfo{}, Views: []driver.ObjectInfo{}, Procedures: []driver.ObjectInfo{}, Functions: []driver.ObjectInfo{}, Triggers: []driver.ObjectInfo{}, Events: []driver.ObjectInfo{}}
	rows, err := c.db.QueryContext(ctx, `SELECT TABLE_NAME, TABLE_TYPE, COALESCE(TABLE_COMMENT, ''), COALESCE(TABLE_ROWS, 0), COALESCE(DATA_LENGTH + INDEX_LENGTH, 0), COALESCE(ENGINE, ''), COALESCE(UPDATE_TIME, CREATE_TIME, '')
		FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME`, db)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var o driver.ObjectInfo
		var typ string
		var upd sql.NullString
		if err := rows.Scan(&o.Name, &typ, &o.Comment, &o.Rows, &o.SizeBytes, &o.Engine, &upd); err != nil {
			rows.Close()
			return nil, err
		}
		o.UpdatedAt = nstr(upd)
		if typ == "VIEW" {
			o.Comment, o.Rows, o.SizeBytes = "", 0, 0
			out.Views = append(out.Views, o)
		} else {
			out.Tables = append(out.Tables, o)
		}
	}
	rows.Close()
	if r, err := c.db.QueryContext(ctx, `SELECT ROUTINE_NAME, ROUTINE_TYPE, COALESCE(ROUTINE_COMMENT, ''), COALESCE(DTD_IDENTIFIER, '') FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = ? ORDER BY ROUTINE_NAME`, db); err == nil {
		for r.Next() {
			var o driver.ObjectInfo
			var typ string
			if r.Scan(&o.Name, &typ, &o.Comment, &o.Detail) == nil {
				if typ == "FUNCTION" {
					out.Functions = append(out.Functions, o)
				} else {
					o.Detail = ""
					out.Procedures = append(out.Procedures, o)
				}
			}
		}
		r.Close()
	}
	if r, err := c.db.QueryContext(ctx, `SELECT TRIGGER_NAME, EVENT_OBJECT_TABLE, CONCAT(ACTION_TIMING, ' ', EVENT_MANIPULATION) FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA = ? ORDER BY TRIGGER_NAME`, db); err == nil {
		for r.Next() {
			var o driver.ObjectInfo
			if r.Scan(&o.Name, &o.Table, &o.Detail) == nil {
				out.Triggers = append(out.Triggers, o)
			}
		}
		r.Close()
	}
	if r, err := c.db.QueryContext(ctx, `SELECT EVENT_NAME, COALESCE(EVENT_COMMENT, ''), STATUS FROM information_schema.EVENTS WHERE EVENT_SCHEMA = ? ORDER BY EVENT_NAME`, db); err == nil {
		for r.Next() {
			var o driver.ObjectInfo
			if r.Scan(&o.Name, &o.Comment, &o.Detail) == nil {
				out.Events = append(out.Events, o)
			}
		}
		r.Close()
	}
	return out, nil
}

// Table 表的完整结构。
func (c *Conn) Table(ctx context.Context, db, table string) (*driver.TableInfo, error) {
	t := &driver.TableInfo{Database: db, Name: table, Kind: "table", Columns: []driver.ColumnInfo{}, Indexes: []driver.IndexInfo{}, ForeignKeys: []driver.ForeignKeyInfo{}, Referenced: []driver.ForeignKeyInfo{}, Triggers: []driver.ObjectInfo{}}
	var typ string
	var engine, coll, comment sql.NullString
	var rowsEst, autoInc sql.NullInt64
	err := c.db.QueryRowContext(ctx, `SELECT TABLE_TYPE, ENGINE, TABLE_COLLATION, TABLE_COMMENT, TABLE_ROWS, AUTO_INCREMENT FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?`, db, table).
		Scan(&typ, &engine, &coll, &comment, &rowsEst, &autoInc)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("表 %s.%s 不存在", db, table)
	}
	if err != nil {
		return nil, err
	}
	if typ == "VIEW" {
		t.Kind = "view"
	}
	t.Engine, t.Collation, t.Comment, t.RowsEst, t.AutoInc = engine.String, coll.String, comment.String, rowsEst.Int64, autoInc.Int64
	if i := strings.Index(t.Collation, "_"); i > 0 {
		t.Charset = t.Collation[:i]
	}
	rows, err := c.db.QueryContext(ctx, `SELECT COLUMN_NAME, COLUMN_TYPE, DATA_TYPE, IS_NULLABLE, COLUMN_DEFAULT, EXTRA, COLUMN_COMMENT,
		CHARACTER_SET_NAME, COLLATION_NAME, CHARACTER_MAXIMUM_LENGTH, NUMERIC_PRECISION, NUMERIC_SCALE, COLUMN_KEY, COALESCE(GENERATION_EXPRESSION, '')
		FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? ORDER BY ORDINAL_POSITION`, db, table)
	if err != nil {
		// MariaDB 10.1 及更早没有 GENERATION_EXPRESSION
		rows, err = c.db.QueryContext(ctx, `SELECT COLUMN_NAME, COLUMN_TYPE, DATA_TYPE, IS_NULLABLE, COLUMN_DEFAULT, EXTRA, COLUMN_COMMENT,
			CHARACTER_SET_NAME, COLLATION_NAME, CHARACTER_MAXIMUM_LENGTH, NUMERIC_PRECISION, NUMERIC_SCALE, COLUMN_KEY, ''
			FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? ORDER BY ORDINAL_POSITION`, db, table)
		if err != nil {
			return nil, err
		}
	}
	for rows.Next() {
		var col driver.ColumnInfo
		var nullable, extra, key string
		var def, cs, cl sql.NullString
		var maxLen, prec, scale sql.NullInt64
		if err := rows.Scan(&col.Name, &col.Type, &col.DataType, &nullable, &def, &extra, &col.Comment, &cs, &cl, &maxLen, &prec, &scale, &key, &col.GenerationExpr); err != nil {
			rows.Close()
			return nil, err
		}
		col.Nullable = nullable == "YES"
		col.Kind = ColumnKind(col.DataType)
		if strings.HasPrefix(strings.ToLower(col.Type), "tinyint(1)") {
			col.Kind = "bool"
		}
		if def.Valid {
			v := def.String
			col.Default = &v
			lx := strings.ToLower(extra)
			if strings.Contains(lx, "default_generated") || strings.HasPrefix(strings.ToUpper(v), "CURRENT_TIMESTAMP") || strings.Contains(v, "(") {
				col.DefaultIsExpr = true
			}
		}
		col.Extra = extra
		lx := strings.ToLower(extra)
		col.AutoIncrement = strings.Contains(lx, "auto_increment")
		if col.GenerationExpr != "" || strings.Contains(lx, "generated") && (strings.Contains(lx, "virtual") || strings.Contains(lx, "stored")) {
			if strings.Contains(lx, "stored") || strings.Contains(lx, "persistent") {
				col.Generated = "stored"
			} else {
				col.Generated = "virtual"
			}
		}
		col.Invisible = strings.Contains(lx, "invisible")
		col.Charset, col.Collation = cs.String, cl.String
		col.Length, col.Precision, col.Scale = maxLen.Int64, int(prec.Int64), int(scale.Int64)
		col.Unsigned = strings.Contains(strings.ToLower(col.Type), "unsigned")
		col.PrimaryKey = key == "PRI"
		if col.Kind == "enum" || col.Kind == "set" {
			col.EnumValues = parseEnum(col.Type)
		}
		t.Columns = append(t.Columns, col)
	}
	rows.Close()
	if len(t.Columns) == 0 {
		return nil, fmt.Errorf("表 %s.%s 不存在或没有权限读取结构", db, table)
	}
	// 索引
	ir, err := c.db.QueryContext(ctx, `SELECT INDEX_NAME, NON_UNIQUE, COLUMN_NAME, SUB_PART, INDEX_TYPE, COALESCE(INDEX_COMMENT, '') FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? ORDER BY INDEX_NAME = 'PRIMARY' DESC, INDEX_NAME, SEQ_IN_INDEX`, db, table)
	if err == nil {
		idx := map[string]*driver.IndexInfo{}
		var order []string
		for ir.Next() {
			var name, typ, cmt string
			var nonUnique int
			var col sql.NullString
			var sub sql.NullInt64
			if ir.Scan(&name, &nonUnique, &col, &sub, &typ, &cmt) != nil {
				continue
			}
			x, ok := idx[name]
			if !ok {
				x = &driver.IndexInfo{Name: name, Unique: nonUnique == 0, Primary: name == "PRIMARY", Type: typ, Comment: cmt}
				idx[name] = x
				order = append(order, name)
			}
			colName := col.String
			if !col.Valid {
				colName = "(表达式)"
			}
			x.Columns = append(x.Columns, colName)
			x.Lengths = append(x.Lengths, int(sub.Int64))
		}
		ir.Close()
		for _, n := range order {
			t.Indexes = append(t.Indexes, *idx[n])
		}
	}
	// 外键
	t.ForeignKeys = c.fks(ctx, `k.TABLE_SCHEMA = ? AND k.TABLE_NAME = ?`, db, table)
	t.Referenced = c.fks(ctx, `k.REFERENCED_TABLE_SCHEMA = ? AND k.REFERENCED_TABLE_NAME = ?`, db, table)
	// 触发器
	if r, err := c.db.QueryContext(ctx, `SELECT TRIGGER_NAME, CONCAT(ACTION_TIMING, ' ', EVENT_MANIPULATION) FROM information_schema.TRIGGERS WHERE EVENT_OBJECT_SCHEMA = ? AND EVENT_OBJECT_TABLE = ?`, db, table); err == nil {
		for r.Next() {
			var o driver.ObjectInfo
			if r.Scan(&o.Name, &o.Detail) == nil {
				o.Table = table
				t.Triggers = append(t.Triggers, o)
			}
		}
		r.Close()
	}
	// 行键：主键，否则第一个全部列非空的唯一索引
	for _, ix := range t.Indexes {
		if ix.Primary {
			t.Key, t.KeyKind = ix.Columns, "primary"
			break
		}
	}
	if len(t.Key) == 0 {
		notNull := map[string]bool{}
		for _, c := range t.Columns {
			notNull[c.Name] = !c.Nullable
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
	}
	if ddl, err := c.DDL(ctx, db, t.Kind, table); err == nil {
		t.DDL = ddl
	}
	return t, nil
}

func (c *Conn) fks(ctx context.Context, where string, args ...any) []driver.ForeignKeyInfo {
	out := []driver.ForeignKeyInfo{}
	rows, err := c.db.QueryContext(ctx, `SELECT k.CONSTRAINT_NAME, k.TABLE_NAME, k.COLUMN_NAME, k.REFERENCED_TABLE_SCHEMA, k.REFERENCED_TABLE_NAME, k.REFERENCED_COLUMN_NAME,
		COALESCE(r.DELETE_RULE, ''), COALESCE(r.UPDATE_RULE, '')
		FROM information_schema.KEY_COLUMN_USAGE k
		LEFT JOIN information_schema.REFERENTIAL_CONSTRAINTS r ON r.CONSTRAINT_SCHEMA = k.CONSTRAINT_SCHEMA AND r.CONSTRAINT_NAME = k.CONSTRAINT_NAME AND r.TABLE_NAME = k.TABLE_NAME
		WHERE k.REFERENCED_TABLE_NAME IS NOT NULL AND `+where+` ORDER BY k.CONSTRAINT_NAME, k.ORDINAL_POSITION`, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	idx := map[string]int{}
	for rows.Next() {
		var name, tbl, col, rdb, rt, rc, del, upd string
		if rows.Scan(&name, &tbl, &col, &rdb, &rt, &rc, &del, &upd) != nil {
			continue
		}
		key := tbl + "\x00" + name
		i, ok := idx[key]
		if !ok {
			out = append(out, driver.ForeignKeyInfo{Name: name, Table: tbl, RefDB: rdb, RefTable: rt, OnDelete: del, OnUpdate: upd})
			i = len(out) - 1
			idx[key] = i
		}
		out[i].Columns = append(out[i].Columns, col)
		out[i].RefColumns = append(out[i].RefColumns, rc)
	}
	return out
}

func parseEnum(t string) []string {
	i, j := strings.Index(t, "("), strings.LastIndex(t, ")")
	if i < 0 || j <= i {
		return nil
	}
	body := t[i+1 : j]
	var out []string
	var cur strings.Builder
	in := false
	for k := 0; k < len(body); k++ {
		ch := body[k]
		switch {
		case ch == '\'' && !in:
			in = true
		case ch == '\'' && in:
			if k+1 < len(body) && body[k+1] == '\'' {
				cur.WriteByte('\'')
				k++
			} else {
				in = false
				out = append(out, cur.String())
				cur.Reset()
			}
		case ch == '\\' && in && k+1 < len(body):
			cur.WriteByte(body[k+1])
			k++
		case in:
			cur.WriteByte(ch)
		}
	}
	return out
}

// DDL 对象的创建语句。kind：table | view | procedure | function | trigger | event。
func (c *Conn) DDL(ctx context.Context, db, kind, name string) (string, error) {
	q := QuoteIdent(db) + "." + QuoteIdent(name)
	var stmt string
	col := 1
	switch kind {
	case "table":
		stmt = "SHOW CREATE TABLE " + q
	case "view":
		stmt = "SHOW CREATE VIEW " + q
	case "procedure":
		stmt, col = "SHOW CREATE PROCEDURE "+q, 2
	case "function":
		stmt, col = "SHOW CREATE FUNCTION "+q, 2
	case "trigger":
		stmt, col = "SHOW CREATE TRIGGER "+q, 2
	case "event":
		stmt, col = "SHOW CREATE EVENT "+q, 3
	default:
		return "", fmt.Errorf("不支持的对象类型 %q", kind)
	}
	rows, err := c.db.QueryContext(ctx, stmt)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	if !rows.Next() {
		return "", fmt.Errorf("%s 不存在", name)
	}
	vals := make([]sql.NullString, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return "", err
	}
	if col >= len(vals) {
		col = len(vals) - 1
	}
	if !vals[col].Valid {
		return "", fmt.Errorf("没有权限查看 %s 的定义", name)
	}
	return vals[col].String, nil
}

// Completion 供编辑器补全的结构快照：表 → 列。
func Completion(ctx context.Context, db *sql.DB, database string) (map[string][]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT TABLE_NAME, COLUMN_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME, ORDINAL_POSITION`, database)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var t, c string
		if rows.Scan(&t, &c) == nil {
			out[t] = append(out[t], c)
		}
	}
	return out, nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
