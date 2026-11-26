package sqldump

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
)

type liteObj struct {
	typ, name, tbl, sql string
	seq                 int64 // sqlite_master 的 rowid（创建顺序）
}

type liteTable struct {
	name    string
	sql     string
	virtual bool
}

type liteCol struct {
	name string
}

// liteColumns 返回要导出的列（跳过生成列与虚表隐藏列），以及要不要带上隐式 rowid（返回可用的 rowid 别名，空 = 不带）。
func liteColumns(ctx context.Context, conn *sql.Conn, table string, virtual bool) ([]liteCol, string, error) {
	rows, err := conn.QueryContext(ctx, "SELECT name, type, pk, hidden FROM pragma_table_xinfo(?)", table)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var cols []liteCol
	names := map[string]bool{}
	pkCount := 0
	intPK := false
	for rows.Next() {
		var name, typ string
		var pk, hidden int
		if err := rows.Scan(&name, &typ, &pk, &hidden); err != nil {
			return nil, "", err
		}
		names[strings.ToLower(name)] = true
		if pk > 0 {
			pkCount++
			intPK = strings.EqualFold(strings.TrimSpace(typ), "INTEGER")
		}
		if hidden != 0 {
			continue
		}
		cols = append(cols, liteCol{name: name})
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if len(cols) == 0 {
		return nil, "", fmt.Errorf("表 %s 不存在或没有列", table)
	}
	if virtual || (pkCount == 1 && intPK) {
		return cols, "", nil
	}
	var wr int
	if err := conn.QueryRowContext(ctx, "SELECT wr FROM pragma_table_list WHERE schema = 'main' AND name = ?", table).Scan(&wr); err != nil {
		return nil, "", err
	}
	if wr != 0 {
		return cols, "", nil
	}
	for _, alias := range []string{"rowid", "_rowid_", "oid"} {
		if !names[alias] {
			return cols, alias, nil
		}
	}
	return cols, "", nil
}

func liteSelect(cols []liteCol, rowid string) string {
	parts := make([]string, 0, len(cols)+1)
	if rowid != "" {
		parts = append(parts, rowid)
	}
	for _, c := range cols {
		// 一元 + 去掉声明类型，驱动不会把 DATETIME 列的文本解析成 time.Time；值与存储类型不变。
		parts = append(parts, "+"+quoteSQLiteIdent(c.name))
	}
	return strings.Join(parts, ", ")
}

func appendLiteLiteral(b []byte, v any) ([]byte, error) {
	switch x := v.(type) {
	case nil:
		return append(b, "NULL"...), nil
	case int64:
		return strconv.AppendInt(b, x, 10), nil
	case float64:
		return appendSQLiteReal(b, x), nil
	case string:
		return appendSQLiteString(b, x), nil
	case []byte:
		b = append(b, "X'"...)
		b = hexUpper(b, x)
		return append(b, '\''), nil
	case bool:
		if x {
			return append(b, '1'), nil
		}
		return append(b, '0'), nil
	}
	return b, fmt.Errorf("意外的值类型 %T", v)
}

func hashLiteValue(h *rowHasher, v any) error {
	switch x := v.(type) {
	case nil:
		h.null()
	case int64:
		var tmp [8]byte
		binary.BigEndian.PutUint64(tmp[:], uint64(x))
		h.val('i', tmp[:])
	case float64:
		var tmp [8]byte
		binary.BigEndian.PutUint64(tmp[:], math.Float64bits(x))
		h.val('r', tmp[:])
	case string:
		h.valStr('t', x)
	case []byte:
		h.val('b', x)
	case bool:
		if x {
			h.val('i', []byte{0, 0, 0, 0, 0, 0, 0, 1})
		} else {
			h.val('i', make([]byte, 8))
		}
	default:
		return fmt.Errorf("意外的值类型 %T", v)
	}
	return nil
}

// liteScanRows 逐行读取并回调。
func liteScanRows(ctx context.Context, conn *sql.Conn, query string, fn func([]any) error) error {
	rows, err := conn.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		if err := fn(vals); err != nil {
			return err
		}
	}
	return rows.Err()
}

func liteIsVirtual(ctx context.Context, conn *sql.Conn, table string) (bool, error) {
	var typ string
	err := conn.QueryRowContext(ctx, "SELECT type FROM pragma_table_list WHERE schema = 'main' AND name = ?", table).Scan(&typ)
	if err == sql.ErrNoRows {
		return false, fmt.Errorf("表 %s 不存在", table)
	}
	return typ == "virtual", err
}

func sqliteChecksum(ctx context.Context, conn *sql.Conn, table string) (int64, string, error) {
	virtual, err := liteIsVirtual(ctx, conn, table)
	if err != nil {
		return 0, "", err
	}
	cols, rowid, err := liteColumns(ctx, conn, table, virtual)
	if err != nil {
		return 0, "", err
	}
	var h rowHasher
	err = liteScanRows(ctx, conn, "SELECT "+liteSelect(cols, rowid)+" FROM "+quoteSQLiteIdent(table), func(vals []any) error {
		h.reset()
		for _, v := range vals {
			if err := hashLiteValue(&h, v); err != nil {
				return err
			}
		}
		h.endRow()
		return nil
	})
	if err != nil {
		return 0, "", isCanceled(ctx, err)
	}
	return h.rows, h.String(), nil
}

func exportSQLite(ctx context.Context, conn *sql.Conn, w io.Writer, opts ExportOptions) (*Manifest, error) {
	started := opts.Now()
	var ver string
	if err := conn.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&ver); err != nil {
		return nil, isCanceled(ctx, err)
	}
	man := &Manifest{Tool: ToolName, Dialect: string(SQLite), ServerVersion: ver, StartedAt: started.UnixMilli()}
	// 读事务：整个导出看到同一个快照。
	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		return nil, isCanceled(ctx, err)
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), "COMMIT") }()

	var objs []liteObj
	err := liteScanRows(ctx, conn, "SELECT type, name, tbl_name, IFNULL(sql, ''), rowid FROM sqlite_master", func(v []any) error {
		o := liteObj{}
		o.typ, _ = v[0].(string)
		o.name, _ = v[1].(string)
		o.tbl, _ = v[2].(string)
		o.sql, _ = v[3].(string)
		o.seq, _ = v[4].(int64)
		objs = append(objs, o)
		return nil
	})
	if err != nil {
		return nil, isCanceled(ctx, err)
	}
	sort.Slice(objs, func(i, j int) bool { return objs[i].seq < objs[j].seq })

	kinds := map[string]string{}
	err = liteScanRows(ctx, conn, "SELECT name, type FROM pragma_table_list WHERE schema = 'main'", func(v []any) error {
		n, _ := v[0].(string)
		t, _ := v[1].(string)
		kinds[n] = t
		return nil
	})
	if err != nil {
		return nil, isCanceled(ctx, err)
	}

	want := map[string]bool{}
	for _, t := range opts.Tables {
		want[t] = true
	}
	filter := len(want) > 0
	var tables []liteTable
	var views, indexes, triggers []liteObj
	hasSeq := false
	for _, o := range objs {
		if o.name == "sqlite_sequence" {
			hasSeq = true
		}
		if strings.HasPrefix(strings.ToLower(o.name), "sqlite_") || o.sql == "" {
			continue
		}
		switch o.typ {
		case "table":
			k := kinds[o.name]
			if k == "shadow" || (filter && !want[o.name]) {
				continue
			}
			tables = append(tables, liteTable{name: o.name, sql: o.sql, virtual: k == "virtual"})
		case "view":
			if filter && !want[o.name] {
				continue
			}
			views = append(views, o)
		case "index":
			if filter && !want[o.tbl] {
				continue
			}
			if k := kinds[o.tbl]; k == "shadow" || k == "virtual" {
				continue
			}
			indexes = append(indexes, o)
		case "trigger":
			if filter && !want[o.tbl] {
				continue
			}
			triggers = append(triggers, o)
		}
	}
	sort.Slice(tables, func(i, j int) bool { return tables[i].name < tables[j].name })
	sort.SliceStable(indexes, func(i, j int) bool { return indexes[i].tbl < indexes[j].tbl })
	sort.SliceStable(triggers, func(i, j int) bool { return triggers[i].tbl < triggers[j].tbl })

	o := newOut(w)
	o.header(headerInfo{
		connName:    opts.ConnectionName,
		serverType:  "SQLite",
		versionNum:  liteVersionNum(ver),
		versionText: ver,
		schema:      "main",
		now:         started,
		sqlite:      true,
	})
	o.str("PRAGMA foreign_keys = false;\n")
	prog := progressor{fn: opts.Progress}
	prog.p.Database = "main"
	prog.p.TablesTotal = len(tables)
	dm := DBManifest{Name: "main", Views: len(views), Triggers: len(triggers)}
	if opts.NoStructure {
		dm.Views, dm.Triggers = 0, 0
	}

	var row []byte
	for _, t := range tables {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		prog.p.Table = t.name
		prog.emit(true)
		q := quoteSQLiteIdent(t.name)
		tm := TableManifest{Name: t.name}
		if !opts.NoStructure {
			o.section("Table structure for " + t.name)
			if !opts.NoDrop {
				o.str("DROP TABLE IF EXISTS " + q + ";\n")
			}
			o.str(t.sql + ";\n")
		}
		if !opts.NoData {
			cols, rowid, err := liteColumns(ctx, conn, t.name, t.virtual)
			if err != nil {
				return nil, isCanceled(ctx, fmt.Errorf("导出表 %s: %w", t.name, err))
			}
			names := make([]string, 0, len(cols)+1)
			if rowid != "" {
				names = append(names, rowid)
			}
			for _, c := range cols {
				names = append(names, quoteSQLiteIdent(c.name))
			}
			prefix := "INSERT INTO " + q + " (" + strings.Join(names, ", ") + ") VALUES "
			o.section("Records of " + t.name)
			o.str("BEGIN;\n")
			var h rowHasher
			var stmt []byte
			chunkStart := o.n
			flush := func() {
				if len(stmt) > 0 {
					stmt = append(stmt, ";\n"...)
					o.write(stmt)
					stmt = stmt[:0]
					if o.n-chunkStart >= txnChunkBytes {
						o.str("COMMIT;\nBEGIN;\n")
						chunkStart = o.n
					}
				}
			}
			err = liteScanRows(ctx, conn, "SELECT "+liteSelect(cols, rowid)+" FROM "+q, func(vals []any) error {
				h.reset()
				row = append(row[:0], '(')
				for i, v := range vals {
					if err := hashLiteValue(&h, v); err != nil {
						return err
					}
					if i > 0 {
						row = append(row, ", "...)
					}
					var err error
					if row, err = appendLiteLiteral(row, v); err != nil {
						return err
					}
				}
				h.endRow()
				row = append(row, ')')
				if opts.ExtendedInsert {
					if len(stmt) == 0 {
						stmt = append(stmt, prefix...)
					} else {
						stmt = append(stmt, ", "...)
					}
					stmt = append(stmt, row...)
					if len(stmt) >= opts.MaxStatementBytes {
						flush()
					}
				} else {
					stmt = append(stmt, prefix...)
					stmt = append(stmt, row...)
					flush()
				}
				prog.p.Rows++
				if h.rows%1024 == 0 {
					if err := ctx.Err(); err != nil {
						return err
					}
					prog.p.Bytes = o.n
					prog.emit(false)
				}
				return o.err
			})
			if err != nil {
				return nil, isCanceled(ctx, fmt.Errorf("导出表 %s 的数据: %w", t.name, err))
			}
			flush()
			o.str("COMMIT;\n")
			tm.Rows, tm.Hash = h.rows, h.String()
		}
		dm.Tables = append(dm.Tables, tm)
		prog.p.TablesDone++
		prog.p.Bytes = o.n
		prog.emit(true)
	}

	if hasSeq && !opts.NoData {
		inSet := map[string]bool{}
		for _, t := range tables {
			inSet[t.name] = true
		}
		type seqRow struct {
			name string
			seq  int64
		}
		var seqs []seqRow
		err := liteScanRows(ctx, conn, "SELECT name, seq FROM sqlite_sequence", func(v []any) error {
			n, _ := v[0].(string)
			s, _ := v[1].(int64)
			if inSet[n] {
				seqs = append(seqs, seqRow{n, s})
			}
			return nil
		})
		if err != nil {
			return nil, isCanceled(ctx, err)
		}
		sort.Slice(seqs, func(i, j int) bool { return seqs[i].name < seqs[j].name })
		for _, s := range seqs {
			n := quoteSQLiteString(s.name)
			v := strconv.FormatInt(s.seq, 10)
			o.section("Auto increment value for " + s.name)
			o.str(`UPDATE "sqlite_sequence" SET seq = ` + v + " WHERE name = " + n + ";\n")
			o.str(`INSERT INTO "sqlite_sequence" (name, seq) SELECT ` + n + ", " + v +
				` WHERE NOT EXISTS (SELECT 1 FROM "sqlite_sequence" WHERE name = ` + n + ");\n")
		}
	}

	if !opts.NoStructure {
		last := ""
		for _, ix := range indexes {
			if ix.tbl != last {
				o.section("Indexes structure for table " + ix.tbl)
				last = ix.tbl
			}
			o.str(ix.sql + ";\n")
		}
		for _, v := range views {
			o.section("View structure for " + v.name)
			if !opts.NoDrop {
				o.str("DROP VIEW IF EXISTS " + quoteSQLiteIdent(v.name) + ";\n")
			}
			o.str(v.sql + ";\n")
		}
		for _, tr := range triggers {
			o.section("Triggers structure for table " + tr.tbl)
			if !opts.NoDrop {
				o.str("DROP TRIGGER IF EXISTS " + quoteSQLiteIdent(tr.name) + ";\n")
			}
			o.str(tr.sql + ";\n")
		}
	}
	o.str("\nPRAGMA foreign_keys = true;\n")
	finished := opts.Now()
	o.footer(finished)
	if err := o.flush(); err != nil {
		return nil, err
	}
	man.Databases = []DBManifest{dm}
	man.FinishedAt = finished.UnixMilli()
	man.Bytes = o.n
	prog.p.Bytes = o.n
	prog.emit(true)
	return man, nil
}

// liteVersionNum 3.45.0 → 3045000（与 Navicat 注释头一致）。
func liteVersionNum(v string) string {
	parts := strings.SplitN(v, ".", 3)
	n := 0
	mul := []int{1000000, 1000, 1}
	for i, p := range parts {
		x, _ := strconv.Atoi(p)
		n += x * mul[i]
	}
	return strconv.Itoa(n)
}
