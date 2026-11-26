package sqldump

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------- 服务器版本 ----------

type myVersion struct {
	text                string
	mariadb             bool
	major, minor, patch int
}

func parseMyVersion(s string) myVersion {
	v := myVersion{text: s, mariadb: strings.Contains(strings.ToLower(s), "mariadb")}
	base := s
	if i := strings.IndexByte(base, '-'); i >= 0 {
		base = base[:i]
	}
	parts := strings.SplitN(base, ".", 3)
	nums := []*int{&v.major, &v.minor, &v.patch}
	for i, p := range parts {
		n, _ := strconv.Atoi(strings.TrimFunc(p, func(r rune) bool { return r < '0' || r > '9' }))
		*nums[i] = n
	}
	return v
}

func (v myVersion) num() int { return v.major*10000 + v.minor*100 + v.patch }

func (v myVersion) atLeast(a, b, c int) bool { return v.num() >= a*10000+b*100+c }

// short 是注释头里括号中的版本，与 Navicat 一样：8.0.36 / 11.4.4-MariaDB。
func (v myVersion) short() string {
	s := fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch)
	if v.mariadb {
		s += "-MariaDB"
	}
	return s
}

func (v myVersion) serverType() string {
	if v.mariadb {
		return "MariaDB"
	}
	return "MySQL"
}

// ---------- 列 ----------

type colKind int

const (
	kNum    colKind = iota // 整数、DECIMAL：原文不加引号
	kFloat                 // FLOAT：用 col*1e0 读出精确值
	kDouble                // DOUBLE：同上
	kString                // 字符串、ENUM、SET、JSON、TIME 等：单引号转义
	kDate                  // DATE/DATETIME/TIMESTAMP：CAST(col AS CHAR) 读文本（不受驱动 parseTime 影响）
	kYear                  // YEAR：不加引号
	kBinary                // 二进制：0x..
	kBit                   // BIT：b'..'
	kGeom                  // GEOMETRY：内部格式参与哈希，ST_AsWKB 用于导出
)

type myCol struct {
	name, dataType, colType, extra string
	kind                           colKind
	skip                           bool // 生成列与 MariaDB 系统版本列：不导数据、不参与哈希
}

func kindOf(dataType string) colKind {
	switch strings.ToLower(dataType) {
	case "tinyint", "smallint", "mediumint", "int", "integer", "bigint", "decimal", "numeric", "dec", "fixed":
		return kNum
	case "float":
		return kFloat
	case "double", "real", "double precision":
		return kDouble
	case "bit":
		return kBit
	case "binary", "varbinary", "tinyblob", "blob", "mediumblob", "longblob":
		return kBinary
	case "date", "datetime", "timestamp":
		return kDate
	case "year":
		return kYear
	case "geometry", "point", "linestring", "polygon", "multipoint", "multilinestring", "multipolygon",
		"geometrycollection", "geomcollection":
		return kGeom
	}
	return kString
}

func loadMyColumns(ctx context.Context, conn *sql.Conn, db, table string) ([]myCol, error) {
	rows, err := conn.QueryContext(ctx, `SELECT COLUMN_NAME, DATA_TYPE, COLUMN_TYPE, EXTRA FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? ORDER BY ORDINAL_POSITION`, db, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []myCol
	for rows.Next() {
		var c myCol
		if err := rows.Scan(&c.name, &c.dataType, &c.colType, &c.extra); err != nil {
			return nil, err
		}
		c.kind = kindOf(c.dataType)
		ex := strings.ToUpper(c.extra)
		if strings.Contains(ex, "VIRTUAL GENERATED") || strings.Contains(ex, "STORED GENERATED") ||
			strings.Contains(ex, "PERSISTENT GENERATED") || ex == "VIRTUAL" || ex == "PERSISTENT" ||
			strings.Contains(ex, "ROW START") || strings.Contains(ex, "ROW END") {
			c.skip = true
		}
		cols = append(cols, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("表 %s.%s 不存在或没有列", db, table)
	}
	return cols, nil
}

// selectList 返回读取数据用的 SELECT 列表；GEOMETRY 列占两列（内部格式、WKB）。
func selectList(cols []myCol) string {
	var b strings.Builder
	n := 0
	add := func(s string) {
		if n > 0 {
			b.WriteString(", ")
		}
		b.WriteString(s)
		n++
	}
	for _, c := range cols {
		if c.skip {
			continue
		}
		q := quoteMySQLIdent(c.name)
		switch c.kind {
		case kFloat, kDouble:
			add("(" + q + " * 1e0)")
		case kDate:
			add("CAST(" + q + " AS CHAR)")
		case kGeom:
			add(q)
			add("ST_AsWKB(" + q + ")")
		default:
			add(q)
		}
	}
	if n == 0 {
		return "1"
	}
	return b.String()
}

// ---------- 值 ----------

func asBytes(v driver.Value) ([]byte, bool) {
	switch x := v.(type) {
	case []byte:
		return x, true
	case string:
		return []byte(x), true
	}
	return nil, false
}

func toFloat64(v driver.Value) (float64, error) {
	switch x := v.(type) {
	case float64:
		return x, nil
	case float32:
		return float64(x), nil
	case int64:
		return float64(x), nil
	case []byte:
		return strconv.ParseFloat(string(x), 64)
	}
	return 0, fmt.Errorf("浮点列收到意外的值类型 %T", v)
}

func appendInt(b []byte, v driver.Value) ([]byte, error) {
	switch x := v.(type) {
	case int64:
		return strconv.AppendInt(b, x, 10), nil
	case uint64:
		return strconv.AppendUint(b, x, 10), nil
	case []byte:
		return append(b, x...), nil
	case string:
		return append(b, x...), nil
	}
	return b, fmt.Errorf("数值列收到意外的值类型 %T", v)
}

// appendMyLiteral 把一个值写成 SQL 字面量。vals 从 c 对应的位置开始（GEOMETRY 占两个）。
func appendMyLiteral(b []byte, c *myCol, vals []driver.Value) ([]byte, error) {
	v := vals[0]
	if v == nil {
		return append(b, "NULL"...), nil
	}
	switch c.kind {
	case kNum, kYear:
		return appendInt(b, v)
	case kFloat:
		f, err := toFloat64(v)
		if err != nil {
			return b, err
		}
		return appendFloat32Literal(b, float32(f)), nil
	case kDouble:
		f, err := toFloat64(v)
		if err != nil {
			return b, err
		}
		return appendFloat64Literal(b, f), nil
	case kBinary:
		x, _ := asBytes(v)
		if len(x) == 0 {
			return append(b, "''"...), nil
		}
		b = append(b, "0x"...)
		return hexUpper(b, x), nil
	case kBit:
		x, _ := asBytes(v)
		return appendBits(b, x), nil
	case kGeom:
		raw, _ := asBytes(v)
		wkb, ok := asBytes(vals[1])
		if !ok || len(raw) < 4 {
			if len(raw) == 0 {
				return append(b, "''"...), nil
			}
			b = append(b, "0x"...)
			return hexUpper(b, raw), nil
		}
		b = append(b, "ST_GeomFromWKB(0x"...)
		b = hexUpper(b, wkb)
		b = append(b, ", "...)
		b = strconv.AppendUint(b, uint64(binary.LittleEndian.Uint32(raw[:4])), 10)
		return append(b, ')'), nil
	}
	x, ok := asBytes(v)
	if !ok {
		switch y := v.(type) {
		case int64:
			x = strconv.AppendInt(nil, y, 10)
		case uint64:
			x = strconv.AppendUint(nil, y, 10)
		case float64:
			x = strconv.AppendFloat(nil, y, 'g', -1, 64)
		case time.Time:
			return b, errors.New("连接开启了 parseTime，导出需要关闭")
		default:
			return b, fmt.Errorf("字符串列收到意外的值类型 %T", v)
		}
	}
	return appendMySQLString(b, x), nil
}

// hashMyValue 把一个值加进行哈希。
func hashMyValue(h *rowHasher, c *myCol, vals []driver.Value) error {
	v := vals[0]
	if v == nil {
		h.null()
		return nil
	}
	switch c.kind {
	case kNum, kYear:
		var tmp [24]byte
		b, err := appendInt(tmp[:0], v)
		if err != nil {
			return err
		}
		h.val('i', b)
	case kFloat:
		f, err := toFloat64(v)
		if err != nil {
			return err
		}
		var tmp [4]byte
		binary.BigEndian.PutUint32(tmp[:], math.Float32bits(float32(f)))
		h.val('f', tmp[:])
	case kDouble:
		f, err := toFloat64(v)
		if err != nil {
			return err
		}
		var tmp [8]byte
		binary.BigEndian.PutUint64(tmp[:], math.Float64bits(f))
		h.val('d', tmp[:])
	case kBinary:
		x, _ := asBytes(v)
		h.val('b', x)
	case kBit:
		x, _ := asBytes(v)
		h.val('B', x)
	case kGeom:
		x, _ := asBytes(v)
		h.val('G', x)
	default:
		x, ok := asBytes(v)
		if !ok {
			if t, isTime := v.(time.Time); isTime {
				_ = t
				return errors.New("连接开启了 parseTime，校验需要关闭")
			}
			x = []byte(fmt.Sprint(v))
		}
		h.val('s', x)
	}
	return nil
}

// ---------- 会话 ----------

// mySession 记录会话变量，结束时恢复。
type mySession struct {
	charsetClient, charsetResults, collationConn sql.NullString
	timeZone, sqlMode                            sql.NullString
	db                                           sql.NullString
	isolation                                    string
	netWriteTimeout                              sql.NullInt64
	autocommit                                   sql.NullInt64
}

func saveMySession(ctx context.Context, conn *sql.Conn) (*mySession, error) {
	s := &mySession{}
	err := conn.QueryRowContext(ctx, `SELECT @@session.character_set_client, @@session.character_set_results,
		@@session.collation_connection, @@session.time_zone, @@session.sql_mode, DATABASE(),
		@@session.net_write_timeout, @@session.autocommit`).Scan(
		&s.charsetClient, &s.charsetResults, &s.collationConn, &s.timeZone, &s.sqlMode, &s.db, &s.netWriteTimeout, &s.autocommit)
	if err != nil {
		return nil, err
	}
	var iso sql.NullString
	if err := conn.QueryRowContext(ctx, "SELECT @@session.transaction_isolation").Scan(&iso); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		_ = conn.QueryRowContext(ctx, "SELECT @@session.tx_isolation").Scan(&iso)
	}
	s.isolation = strings.ReplaceAll(iso.String, "-", " ")
	return s, nil
}

// restore 尽量恢复会话（失败忽略：调用方本来就应该用完关闭这条连接）。
func (s *mySession) restore(conn *sql.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	lit := func(v sql.NullString) string {
		if !v.Valid {
			return "NULL"
		}
		return string(appendMySQLString(nil, []byte(v.String)))
	}
	// 先把 sql_mode 设回空，保证下面的反斜杠转义被正确解析。
	stmts := []string{
		"SET SESSION sql_mode = ''",
		"SET SESSION character_set_client = " + lit(s.charsetClient) + ", character_set_results = " + lit(s.charsetResults) +
			", collation_connection = " + lit(s.collationConn),
		"SET SESSION time_zone = " + lit(s.timeZone),
	}
	if s.netWriteTimeout.Valid {
		stmts = append(stmts, "SET SESSION net_write_timeout = "+strconv.FormatInt(s.netWriteTimeout.Int64, 10))
	}
	if s.autocommit.Valid {
		stmts = append(stmts, "SET SESSION autocommit = "+strconv.FormatInt(s.autocommit.Int64, 10))
	}
	switch s.isolation {
	case "READ UNCOMMITTED", "READ COMMITTED", "REPEATABLE READ", "SERIALIZABLE":
		stmts = append(stmts, "SET SESSION TRANSACTION ISOLATION LEVEL "+s.isolation)
	}
	if s.db.Valid && s.db.String != "" {
		stmts = append(stmts, "USE "+quoteMySQLIdent(s.db.String))
	}
	stmts = append(stmts, "SET SESSION sql_mode = "+lit(s.sqlMode))
	for _, q := range stmts {
		if _, err := conn.ExecContext(ctx, q); err != nil && ctx.Err() != nil {
			return
		}
	}
}

// setReadSession 设置读数据用的会话：utf8mb4、UTC、空 sql_mode（SHOW CREATE 用反引号，CHAR 不补空格）。
func setReadSession(ctx context.Context, conn *sql.Conn) error {
	for _, q := range []string{
		"SET NAMES utf8mb4",
		"SET SESSION time_zone = '+00:00'",
		"SET SESSION sql_mode = ''",
	} {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("%s: %w", q, err)
		}
	}
	return nil
}

// ---------- 读取 ----------

// rawQuery 用驱动层接口流式读取（文本协议，[]byte 不复制，只在本次回调内有效）。
func rawQuery(ctx context.Context, conn *sql.Conn, query string, fn func(cols int, next func([]driver.Value) error) error) error {
	return conn.Raw(func(dc any) error {
		q, ok := dc.(driver.QueryerContext)
		if !ok {
			return errors.New("数据库驱动不支持 QueryerContext")
		}
		rows, err := q.QueryContext(ctx, query, nil)
		if err != nil {
			return err
		}
		n := len(rows.Columns())
		err = fn(n, rows.Next)
		if cerr := rows.Close(); err == nil {
			err = cerr
		}
		return err
	})
}

// ---------- 校验 ----------

func mysqlChecksum(ctx context.Context, conn *sql.Conn, db, table string) (int64, string, error) {
	saved, err := saveMySession(ctx, conn)
	if err != nil {
		return 0, "", err
	}
	defer saved.restore(conn)
	if err := setReadSession(ctx, conn); err != nil {
		return 0, "", err
	}
	cols, err := loadMyColumns(ctx, conn, db, table)
	if err != nil {
		return 0, "", err
	}
	q := "SELECT " + selectList(cols) + " FROM " + quoteMySQLIdent(db) + "." + quoteMySQLIdent(table)
	var h rowHasher
	err = rawQuery(ctx, conn, q, func(n int, next func([]driver.Value) error) error {
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
		}
	})
	if err != nil {
		return 0, "", isCanceled(ctx, err)
	}
	return h.rows, h.String(), nil
}

func hashMyRow(h *rowHasher, cols []myCol, dest []driver.Value) error {
	h.reset()
	i := 0
	for ci := range cols {
		c := &cols[ci]
		if c.skip {
			continue
		}
		if err := hashMyValue(h, c, dest[i:]); err != nil {
			return fmt.Errorf("列 %s: %w", c.name, err)
		}
		i++
		if c.kind == kGeom {
			i++
		}
	}
	h.endRow()
	return nil
}

// ---------- DEFINER ----------

// stripDefiner 去掉 SHOW CREATE 输出头部的 DEFINER=`user`@`host` 子句。只在 CREATE 与对象关键字之间找，不动正文。
func stripDefiner(s string) string {
	i := strings.Index(s, "DEFINER=")
	if i < 0 {
		return s
	}
	head := strings.Fields(strings.ToUpper(s[:i]))
	if len(head) == 0 || head[0] != "CREATE" {
		return s
	}
	for _, w := range head[1:] {
		if !strings.HasPrefix(w, "ALGORITHM=") && w != "OR" && w != "REPLACE" {
			return s
		}
	}
	j := skipUserPart(s, i+len("DEFINER="))
	if j < len(s) && s[j] == '@' {
		j = skipUserPart(s, j+1)
	}
	if j < len(s) && s[j] == '(' && j+1 < len(s) && s[j+1] == ')' { // CURRENT_USER()
		j += 2
	}
	for j < len(s) && (s[j] == ' ' || s[j] == '\t' || s[j] == '\n') {
		j++
	}
	return s[:i] + s[j:]
}

func skipUserPart(s string, j int) int {
	if j >= len(s) {
		return j
	}
	switch q := s[j]; q {
	case '`', '\'', '"':
		j++
		for j < len(s) {
			if s[j] == '\\' && q != '`' {
				j += 2
				continue
			}
			if s[j] == q {
				if j+1 < len(s) && s[j+1] == q {
					j += 2
					continue
				}
				return j + 1
			}
			j++
		}
		return j
	}
	for j < len(s) && s[j] != '@' && s[j] != ' ' && s[j] != '\t' && s[j] != '\n' && s[j] != '(' {
		j++
	}
	return j
}

// ---------- 视图依赖 ----------

// backtickIdents 取出定义里所有反引号标识符（跳过字符串）。
func backtickIdents(s string) []string {
	var out []string
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\'', '"':
			for i++; i < len(s); i++ {
				if s[i] == '\\' {
					i++
					continue
				}
				if s[i] == c {
					if i+1 < len(s) && s[i+1] == c {
						i++
						continue
					}
					break
				}
			}
		case '`':
			var b strings.Builder
			for i++; i < len(s); i++ {
				if s[i] == '`' {
					if i+1 < len(s) && s[i+1] == '`' {
						b.WriteByte('`')
						i++
						continue
					}
					break
				}
				b.WriteByte(s[i])
			}
			out = append(out, b.String())
		}
	}
	return out
}

// topoSort 按依赖排序（依赖在前），同层按名字；有环时剩下的按名字追加。
func topoSort(names []string, deps map[string][]string) []string {
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	in := map[string]bool{}
	for _, n := range sorted {
		in[n] = true
	}
	done := map[string]bool{}
	var out []string
	for len(out) < len(sorted) {
		progress := false
		for _, n := range sorted {
			if done[n] {
				continue
			}
			ok := true
			for _, d := range deps[n] {
				if d != n && in[d] && !done[d] {
					ok = false
					break
				}
			}
			if ok {
				done[n] = true
				out = append(out, n)
				progress = true
			}
		}
		if !progress {
			for _, n := range sorted {
				if !done[n] {
					done[n] = true
					out = append(out, n)
					break
				}
			}
		}
	}
	return out
}
