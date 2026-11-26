package sqldump

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

const baseSQLMode = "NO_AUTO_VALUE_ON_ZERO"

type myTable struct {
	name, typ, engine string
}

type myRoutine struct {
	name, typ string // FUNCTION / PROCEDURE
}

type myTrigger struct {
	name, table string
}

type myDB struct {
	name     string
	tables   []myTable // 表与序列，按名字
	views    []string
	routines []myRoutine
	events   []string
	triggers []myTrigger
}

type mysqlExporter struct {
	ctx  context.Context
	conn *sql.Conn
	o    *out
	opts ExportOptions
	ver  myVersion
	man  *Manifest
	prog progressor

	curMode string // 文件里当前生效的 sql_mode
	stmt    []byte // 合并插入缓冲
	row     []byte
}

func exportMySQL(ctx context.Context, conn *sql.Conn, w io.Writer, opts ExportOptions) (*Manifest, error) {
	if len(opts.Databases) == 0 {
		return nil, errNoDatabases
	}
	if len(opts.Tables) > 0 && len(opts.Databases) > 1 {
		return nil, errors.New("只导部分表时只能选一个库")
	}
	e := &mysqlExporter{ctx: ctx, conn: conn, o: newOut(w), opts: opts, curMode: baseSQLMode}
	e.prog.fn = opts.Progress
	started := opts.Now()
	var verText string
	if err := conn.QueryRowContext(ctx, "SELECT VERSION()").Scan(&verText); err != nil {
		return nil, isCanceled(ctx, err)
	}
	e.ver = parseMyVersion(verText)
	e.man = &Manifest{Tool: ToolName, Dialect: string(MySQL), ServerVersion: verText, StartedAt: started.UnixMilli()}

	saved, err := saveMySession(ctx, conn)
	if err != nil {
		return nil, isCanceled(ctx, err)
	}
	defer saved.restore(conn)
	if err := setReadSession(ctx, conn); err != nil {
		return nil, isCanceled(ctx, err)
	}
	for _, q := range []string{
		"SET SESSION sql_quote_show_create = 1",
		"SET SESSION net_write_timeout = 3600",
		"SET SESSION TRANSACTION ISOLATION LEVEL REPEATABLE READ",
		"START TRANSACTION WITH CONSISTENT SNAPSHOT",
	} {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			return nil, isCanceled(ctx, fmt.Errorf("%s: %w", q, err))
		}
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(c, "COMMIT")
	}()

	dbs := make([]*myDB, 0, len(opts.Databases))
	for _, name := range opts.Databases {
		d, err := e.loadDB(name)
		if err != nil {
			return nil, isCanceled(ctx, err)
		}
		dbs = append(dbs, d)
		e.prog.p.TablesTotal += len(d.tables)
	}

	e.o.header(headerInfo{
		connName:    opts.ConnectionName,
		serverType:  e.ver.serverType(),
		versionNum:  strconv.Itoa(e.ver.num()),
		versionText: e.ver.short(),
		host:        opts.HostLabel,
		schema:      strings.Join(opts.Databases, ", "),
		now:         started,
	})
	e.o.str("SET NAMES utf8mb4;\nSET FOREIGN_KEY_CHECKS = 0;\nSET time_zone = '+00:00';\nSET SQL_MODE = '" + baseSQLMode + "';\n")

	sections := len(dbs) > 1 || opts.CreateDatabase
	for _, d := range dbs {
		if err := e.dumpDB(d, sections); err != nil {
			return nil, isCanceled(ctx, err)
		}
	}
	e.ensureMode(baseSQLMode)
	e.o.str("\nSET FOREIGN_KEY_CHECKS = 1;\n")
	finished := opts.Now()
	e.o.footer(finished)
	if err := e.o.flush(); err != nil {
		return nil, err
	}
	e.man.FinishedAt = finished.UnixMilli()
	e.man.Bytes = e.o.n
	e.prog.p.Bytes = e.o.n
	e.prog.emit(true)
	return e.man, nil
}

// loadDB 列出一个库里要导出的对象。
func (e *mysqlExporter) loadDB(name string) (*myDB, error) {
	ctx, conn := e.ctx, e.conn
	var n int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = ?", name).Scan(&n); err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, fmt.Errorf("库 %s 不存在", name)
	}
	d := &myDB{name: name}
	want := map[string]bool{}
	for _, t := range e.opts.Tables {
		want[t] = true
	}
	filter := len(want) > 0
	found := map[string]bool{}

	rows, err := conn.QueryContext(ctx, "SELECT TABLE_NAME, TABLE_TYPE, IFNULL(ENGINE, '') FROM information_schema.TABLES WHERE TABLE_SCHEMA = ?", name)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t myTable
		if err := rows.Scan(&t.name, &t.typ, &t.engine); err != nil {
			rows.Close()
			return nil, err
		}
		if filter && !want[t.name] {
			continue
		}
		found[t.name] = true
		switch strings.ToUpper(t.typ) {
		case "VIEW", "SYSTEM VIEW":
			d.views = append(d.views, t.name)
		default:
			d.tables = append(d.tables, t)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for t := range want {
		if !found[t] {
			return nil, fmt.Errorf("表 %s.%s 不存在", name, t)
		}
	}
	sort.Slice(d.tables, func(i, j int) bool { return d.tables[i].name < d.tables[j].name })

	if !filter {
		rows, err = conn.QueryContext(ctx, "SELECT ROUTINE_NAME, ROUTINE_TYPE FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = ?", name)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var r myRoutine
			if err := rows.Scan(&r.name, &r.typ); err != nil {
				rows.Close()
				return nil, err
			}
			r.typ = strings.ToUpper(r.typ)
			if r.typ == "FUNCTION" || r.typ == "PROCEDURE" {
				d.routines = append(d.routines, r)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		sort.Slice(d.routines, func(i, j int) bool {
			if d.routines[i].typ != d.routines[j].typ {
				return d.routines[i].typ == "FUNCTION"
			}
			return d.routines[i].name < d.routines[j].name
		})

		rows, err = conn.QueryContext(ctx, "SELECT EVENT_NAME FROM information_schema.EVENTS WHERE EVENT_SCHEMA = ?", name)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var ev string
			if err := rows.Scan(&ev); err != nil {
				rows.Close()
				return nil, err
			}
			d.events = append(d.events, ev)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		sort.Strings(d.events)
	}

	rows, err = conn.QueryContext(ctx, `SELECT TRIGGER_NAME, EVENT_OBJECT_TABLE FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA = ?
		ORDER BY EVENT_OBJECT_TABLE, ACTION_TIMING, EVENT_MANIPULATION, ACTION_ORDER, TRIGGER_NAME`, name)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t myTrigger
		if err := rows.Scan(&t.name, &t.table); err != nil {
			rows.Close()
			return nil, err
		}
		if filter && !want[t.table] {
			continue
		}
		d.triggers = append(d.triggers, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(d.triggers, func(i, j int) bool { return d.triggers[i].table < d.triggers[j].table })
	return d, nil
}

func (e *mysqlExporter) exec(q string) error {
	_, err := e.conn.ExecContext(e.ctx, q)
	return err
}

// showCreate 执行 SHOW CREATE ...，返回按列名取值的结果。
func (e *mysqlExporter) showCreate(q string) (map[string]sql.NullString, error) {
	rows, err := e.conn.QueryContext(e.ctx, q)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", q, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%s 没有返回结果（可能没有权限）", q)
	}
	vals := make([]sql.NullString, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, err
	}
	m := map[string]sql.NullString{}
	for i, c := range cols {
		m[strings.ToLower(c)] = vals[i]
	}
	return m, rows.Err()
}

func (e *mysqlExporter) definer(s string) string {
	if e.opts.KeepDefiner {
		return s
	}
	return stripDefiner(s)
}

// ensureMode 让文件里当前生效的 sql_mode 等于 m。
func (e *mysqlExporter) ensureMode(m string) {
	if m == e.curMode {
		return
	}
	e.o.str("SET SQL_MODE = " + string(appendMySQLString(nil, []byte(m))) + ";\n")
	e.curMode = m
}

// cleanMode 去掉新版本已经不认识、且对例程行为没有影响的模式，保证跨版本还原时 SET 不报错。
func cleanMode(m string) string {
	parts := strings.Split(m, ",")
	out := parts[:0]
	for _, p := range parts {
		if p == "NO_AUTO_CREATE_USER" || p == "" {
			continue
		}
		out = append(out, p)
	}
	return strings.Join(out, ",")
}

func (e *mysqlExporter) dumpDB(d *myDB, sections bool) error {
	ctx, o := e.ctx, e.o
	qdb := quoteMySQLIdent(d.name)
	if err := e.exec("USE " + qdb); err != nil {
		return err
	}
	dm := DBManifest{Name: d.name}
	e.prog.p.Database = d.name
	if sections {
		m, err := e.showCreate("SHOW CREATE DATABASE " + qdb)
		if err != nil {
			return err
		}
		create := m["create database"].String
		if strings.HasPrefix(create, "CREATE DATABASE ") && !strings.HasPrefix(create, "CREATE DATABASE IF NOT EXISTS ") {
			create = "CREATE DATABASE IF NOT EXISTS " + create[len("CREATE DATABASE "):]
		}
		o.section("Database structure for " + d.name)
		o.str(create + ";\nUSE " + qdb + ";\n")
	}

	for _, t := range d.tables {
		if err := ctx.Err(); err != nil {
			return err
		}
		e.prog.p.Table = t.name
		e.prog.emit(true)
		tm, err := e.dumpTable(d.name, t)
		if err != nil {
			return fmt.Errorf("导出 %s.%s: %w", d.name, t.name, err)
		}
		dm.Tables = append(dm.Tables, tm)
		e.prog.p.TablesDone++
		e.prog.p.Bytes = o.n
		e.prog.emit(true)
	}

	if !e.opts.NoStructure {
		for _, r := range d.routines {
			if err := e.dumpRoutine(r); err != nil {
				return err
			}
			dm.Routines++
		}
		e.ensureMode(baseSQLMode)
		views, err := e.orderViews(d)
		if err != nil {
			return err
		}
		for _, v := range views {
			m, err := e.showCreate("SHOW CREATE VIEW " + quoteMySQLIdent(v))
			if err != nil {
				return err
			}
			o.section("View structure for " + v)
			if !e.opts.NoDrop {
				o.str("DROP VIEW IF EXISTS " + quoteMySQLIdent(v) + ";\n")
			}
			o.str(e.definer(m["create view"].String) + ";\n")
			dm.Views++
		}
		for _, ev := range d.events {
			if err := e.dumpEvent(ev); err != nil {
				return err
			}
			dm.Events++
		}
		for _, tr := range d.triggers {
			if err := e.dumpTrigger(tr); err != nil {
				return err
			}
			dm.Triggers++
		}
		e.ensureMode(baseSQLMode)
	}
	e.man.Databases = append(e.man.Databases, dm)
	return o.err
}

func (e *mysqlExporter) dumpTable(db string, t myTable) (TableManifest, error) {
	o := e.o
	tm := TableManifest{Name: t.name}
	q := quoteMySQLIdent(t.name)
	isSeq := strings.EqualFold(t.typ, "SEQUENCE")
	switch strings.ToLower(t.engine) {
	case "", "innodb", "ndbcluster", "ndb", "tokudb", "rocksdb":
	default:
		if !isSeq {
			e.man.Warnings = append(e.man.Warnings, fmt.Sprintf("表 %s.%s 使用 %s 引擎，不是事务表，导出时不在一致性快照内", db, t.name, t.engine))
		}
	}
	if !e.opts.NoStructure {
		// MariaDB 对序列执行 SHOW CREATE TABLE 返回的是表形式（丢了 START/INCREMENT 等参数），要用 SHOW CREATE SEQUENCE。
		show := "SHOW CREATE TABLE "
		if isSeq {
			show = "SHOW CREATE SEQUENCE "
		}
		m, err := e.showCreate(show + q)
		if err != nil {
			return tm, err
		}
		create := m["create table"].String
		if isSeq {
			o.section("Sequence structure for " + t.name)
			if !e.opts.NoDrop {
				o.str("DROP SEQUENCE IF EXISTS " + q + ";\n")
			}
		} else {
			o.section("Table structure for " + t.name)
			if !e.opts.NoDrop {
				o.str("DROP TABLE IF EXISTS " + q + ";\n")
			}
		}
		o.str(create + ";\n")
	}
	if isSeq {
		if !e.opts.NoData {
			var next string
			if err := e.conn.QueryRowContext(e.ctx, "SELECT next_not_cached_value FROM "+quoteMySQLIdent(db)+"."+q).Scan(&next); err != nil {
				return tm, err
			}
			o.str("SELECT SETVAL(" + q + ", " + next + ", 0);\n")
		}
		return tm, o.err
	}
	if e.opts.NoData {
		return tm, o.err
	}
	cols, err := loadMyColumns(e.ctx, e.conn, db, t.name)
	if err != nil {
		return tm, err
	}
	var prefix []byte
	prefix = append(prefix, "INSERT INTO "...)
	prefix = append(prefix, q...)
	prefix = append(prefix, " ("...)
	first := true
	for _, c := range cols {
		if c.skip {
			continue
		}
		if !first {
			prefix = append(prefix, ", "...)
		}
		first = false
		prefix = append(prefix, quoteMySQLIdent(c.name)...)
	}
	prefix = append(prefix, ") VALUES "...)

	o.section("Records of " + t.name)
	o.str("BEGIN;\n")
	var h rowHasher
	chunkStart := o.n
	e.stmt = e.stmt[:0]
	flushStmt := func() {
		if len(e.stmt) > 0 {
			e.stmt = append(e.stmt, ";\n"...)
			o.write(e.stmt)
			e.stmt = e.stmt[:0]
			if o.n-chunkStart >= txnChunkBytes {
				o.str("COMMIT;\nBEGIN;\n")
				chunkStart = o.n
			}
		}
	}
	query := "SELECT " + selectList(cols) + " FROM " + quoteMySQLIdent(db) + "." + q
	err = rawQuery(e.ctx, e.conn, query, func(n int, next func([]driver.Value) error) error {
		dest := make([]driver.Value, n)
		for {
			if err := next(dest); err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}
				return err
			}
			if err := hashMyRow(&h, cols, dest); err != nil {
				return err
			}
			row := append(e.row[:0], '(')
			i := 0
			firstVal := true
			for ci := range cols {
				c := &cols[ci]
				if c.skip {
					continue
				}
				if !firstVal {
					row = append(row, ", "...)
				}
				firstVal = false
				var err error
				row, err = appendMyLiteral(row, c, dest[i:])
				if err != nil {
					return fmt.Errorf("列 %s: %w", c.name, err)
				}
				i++
				if c.kind == kGeom {
					i++
				}
			}
			row = append(row, ')')
			e.row = row
			if e.opts.ExtendedInsert {
				if len(e.stmt) == 0 {
					e.stmt = append(e.stmt, prefix...)
				} else {
					e.stmt = append(e.stmt, ", "...)
				}
				e.stmt = append(e.stmt, row...)
				if len(e.stmt) >= e.opts.MaxStatementBytes {
					flushStmt()
				}
			} else {
				e.stmt = append(e.stmt, prefix...)
				e.stmt = append(e.stmt, row...)
				flushStmt()
			}
			if o.err != nil {
				return o.err
			}
			e.prog.p.Rows++
			if h.rows%1024 == 0 {
				if err := e.ctx.Err(); err != nil {
					return err
				}
				e.prog.p.Bytes = o.n
				e.prog.emit(false)
			}
		}
	})
	if err != nil {
		return tm, err
	}
	flushStmt()
	o.str("COMMIT;\n")
	if cap(e.stmt) > 4*e.opts.MaxStatementBytes {
		e.stmt = nil
	}
	if cap(e.row) > 1<<20 {
		e.row = nil
	}
	tm.Rows = h.rows
	tm.Hash = h.String()
	return tm, o.err
}

func (e *mysqlExporter) dumpRoutine(r myRoutine) error {
	q := quoteMySQLIdent(r.name)
	m, err := e.showCreate("SHOW CREATE " + r.typ + " " + q)
	if err != nil {
		return err
	}
	key := "create function"
	title := "Function structure for "
	if r.typ == "PROCEDURE" {
		key = "create procedure"
		title = "Procedure structure for "
	}
	body := m[key]
	if !body.Valid {
		return fmt.Errorf("读不到 %s %s 的定义（需要 SHOW_ROUTINE 权限或是定义者）", r.typ, r.name)
	}
	o := e.o
	o.section(title + r.name)
	if !e.opts.NoDrop {
		o.str("DROP " + r.typ + " IF EXISTS " + q + ";\n")
	}
	e.ensureMode(cleanMode(m["sql_mode"].String))
	o.str("delimiter ;;\n" + e.definer(body.String) + "\n;;\ndelimiter ;\n")
	return o.err
}

func (e *mysqlExporter) dumpEvent(name string) error {
	q := quoteMySQLIdent(name)
	m, err := e.showCreate("SHOW CREATE EVENT " + q)
	if err != nil {
		return err
	}
	body := m["create event"]
	if !body.Valid {
		return fmt.Errorf("读不到事件 %s 的定义", name)
	}
	o := e.o
	o.section("Event structure for " + name)
	if !e.opts.NoDrop {
		o.str("DROP EVENT IF EXISTS " + q + ";\n")
	}
	e.ensureMode(cleanMode(m["sql_mode"].String))
	tz := m["time_zone"].String
	if tz != "" && tz != "+00:00" {
		o.str("SET time_zone = " + string(appendMySQLString(nil, []byte(tz))) + ";\n")
	}
	o.str("delimiter ;;\n" + e.definer(body.String) + "\n;;\ndelimiter ;\n")
	if tz != "" && tz != "+00:00" {
		o.str("SET time_zone = '+00:00';\n")
	}
	return o.err
}

func (e *mysqlExporter) dumpTrigger(t myTrigger) error {
	q := quoteMySQLIdent(t.name)
	m, err := e.showCreate("SHOW CREATE TRIGGER " + q)
	if err != nil {
		return err
	}
	body := m["sql original statement"]
	if !body.Valid {
		return fmt.Errorf("读不到触发器 %s 的定义", t.name)
	}
	o := e.o
	o.section("Triggers structure for table " + t.table)
	if !e.opts.NoDrop {
		o.str("DROP TRIGGER IF EXISTS " + q + ";\n")
	}
	e.ensureMode(cleanMode(m["sql_mode"].String))
	o.str("delimiter ;;\n" + e.definer(body.String) + "\n;;\ndelimiter ;\n")
	return o.err
}

// orderViews 按依赖拓扑排序视图。MySQL 8.0.13+ 用 VIEW_TABLE_USAGE，其余从定义里找引用。
func (e *mysqlExporter) orderViews(d *myDB) ([]string, error) {
	if len(d.views) < 2 {
		return d.views, nil
	}
	deps := map[string][]string{}
	if !e.ver.mariadb && e.ver.atLeast(8, 0, 13) {
		rows, err := e.conn.QueryContext(e.ctx, `SELECT VIEW_NAME, TABLE_NAME FROM information_schema.VIEW_TABLE_USAGE
			WHERE VIEW_SCHEMA = ? AND TABLE_SCHEMA = ?`, d.name, d.name)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var v, t string
			if err := rows.Scan(&v, &t); err != nil {
				rows.Close()
				return nil, err
			}
			deps[v] = append(deps[v], t)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	} else {
		lower := map[string]string{}
		for _, v := range d.views {
			lower[strings.ToLower(v)] = v
		}
		for _, v := range d.views {
			m, err := e.showCreate("SHOW CREATE VIEW " + quoteMySQLIdent(v))
			if err != nil {
				return nil, err
			}
			def := m["create view"].String
			if i := strings.Index(def, " AS "); i >= 0 {
				def = def[i:]
			}
			for _, id := range backtickIdents(def) {
				if dv, ok := lower[strings.ToLower(id)]; ok && dv != v {
					deps[v] = append(deps[v], dv)
				}
			}
		}
	}
	return topoSort(d.views, deps), nil
}
