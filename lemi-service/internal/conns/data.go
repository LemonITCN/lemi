package conns

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
	"github.com/LemonITCN/lemi/lemi-service/internal/driver"
	"github.com/LemonITCN/lemi/lemi-service/internal/sqlclass"
	"github.com/LemonITCN/lemi/lemi-service/internal/sqlsplit"
)

// DataQuery 是表格页的查询条件。
type DataQuery struct {
	Database string `json:"db"`
	Table    string `json:"table"`
	Where    string `json:"where"`
	OrderBy  string `json:"order_by"`
	Limit    int    `json:"limit"`
	Offset   int    `json:"offset"`
}

// fragmentOK 用户输入的 WHERE / ORDER BY 片段只能是一个表达式，不能带分号拼第二条语句。
func fragmentOK(frag, dialect string) error {
	if strings.TrimSpace(frag) == "" {
		return nil
	}
	if n := len(sqlsplit.Split(frag+";", sqlsplit.Dialect(dialect))); n > 1 {
		return apperr.Bad("筛选条件里不能包含多条语句")
	}
	return nil
}

func qualified(sc driver.SQLConn, db, table string) string {
	if db == "" {
		return sc.QuoteIdent(table)
	}
	return sc.QuoteIdent(db) + "." + sc.QuoteIdent(table)
}

// TableData 查询表格页数据（永远在只读会话里执行：筛选条件是用户写的 SQL 片段）。
func (s *Service) TableData(ctx context.Context, u *accounts.User, c *Connection, level string, q DataQuery) (*driver.Result, *driver.TableInfo, error) {
	sc, err := s.SQL(ctx, c)
	if err != nil {
		return nil, nil, err
	}
	if err := fragmentOK(q.Where, sc.Dialect()); err != nil {
		return nil, nil, err
	}
	if err := fragmentOK(q.OrderBy, sc.Dialect()); err != nil {
		return nil, nil, err
	}
	t, err := sc.Table(ctx, q.Database, q.Table)
	if err != nil {
		return nil, nil, apperr.Wrap("table_failed", err)
	}
	if q.Limit <= 0 {
		q.Limit = 500
	}
	max := s.set.System(ctx).MaxResultRows
	if q.Limit > max {
		q.Limit = max
	}
	if q.Offset < 0 {
		q.Offset = 0
	}
	sel := "*"
	if t.KeyKind == "rowid" {
		sel = "rowid, *"
	}
	stmt := "SELECT " + sel + " FROM " + qualified(sc, q.Database, q.Table)
	if w := strings.TrimSpace(q.Where); w != "" {
		stmt += " WHERE " + w
	}
	if o := strings.TrimSpace(q.OrderBy); o != "" {
		stmt += " ORDER BY " + o
	}
	stmt += " LIMIT " + strconv.Itoa(q.Limit+1)
	if q.Offset > 0 {
		stmt += " OFFSET " + strconv.Itoa(q.Offset)
	}
	cs, err := s.OpenConsole(ctx, u, c, level, "", audit.SrcGrid, true, 0)
	if err != nil {
		return nil, nil, err
	}
	defer s.CloseConsole(cs.ID)
	start := time.Now()
	res, err := cs.S.Exec(ctx, stmt, nil, q.Limit+1)
	if err != nil {
		return nil, nil, apperr.Wrap("query_failed", err)
	}
	if len(res.Rows) > q.Limit {
		res.Rows = res.Rows[:q.Limit]
		res.HasMore = true
	} else {
		res.HasMore = false
	}
	res.DurationMS = time.Since(start).Milliseconds()
	annotate(res, t, q.Database)
	return res, t, nil
}

// annotate 给结果列标上原始表列与主键，并给出可编辑判断。
func annotate(res *driver.Result, t *driver.TableInfo, db string) {
	byName := map[string]driver.ColumnInfo{}
	for _, col := range t.Columns {
		byName[col.Name] = col
	}
	key := map[string]bool{}
	for _, k := range t.Key {
		key[k] = true
	}
	for i := range res.Columns {
		col := &res.Columns[i]
		if ci, ok := byName[col.Name]; ok {
			col.Origin, col.Table, col.Database = ci.Name, t.Name, db
			col.Kind, col.Type = ci.Kind, strings.ToUpper(ci.Type)
			nullable := ci.Nullable
			col.Nullable = &nullable
		}
		if key[col.Name] {
			col.PK = true
			if col.Name == "rowid" {
				col.Origin, col.Table = "rowid", t.Name
			}
		}
	}
	switch {
	case t.Kind == "view":
		res.Editable = &driver.Editable{OK: false, Reason: "视图不能直接修改"}
	case len(t.Key) == 0:
		res.Editable = &driver.Editable{OK: false, Reason: "该表没有主键或非空唯一键，不能直接修改"}
	default:
		res.Editable = &driver.Editable{OK: true, Database: db, Table: t.Name, Key: t.Key}
	}
}

// CountRows 精确行数。
func (s *Service) CountRows(ctx context.Context, u *accounts.User, c *Connection, level string, q DataQuery) (int64, error) {
	sc, err := s.SQL(ctx, c)
	if err != nil {
		return 0, err
	}
	if err := fragmentOK(q.Where, sc.Dialect()); err != nil {
		return 0, err
	}
	stmt := "SELECT COUNT(*) FROM " + qualified(sc, q.Database, q.Table)
	if w := strings.TrimSpace(q.Where); w != "" {
		stmt += " WHERE " + w
	}
	cs, err := s.OpenConsole(ctx, u, c, level, "", audit.SrcGrid, true, 0)
	if err != nil {
		return 0, err
	}
	defer s.CloseConsole(cs.ID)
	res, err := cs.S.Exec(ctx, stmt, nil, 1)
	if err != nil {
		return 0, apperr.Wrap("query_failed", err)
	}
	if len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		return 0, nil
	}
	n, _ := strconv.ParseInt(fmt.Sprint(res.Rows[0][0]), 10, 64)
	return n, nil
}

// EditableForQuery 判断控制台查询结果能否回写（§9.4）。
func (s *Service) EditableForQuery(ctx context.Context, c *Connection, cs *Console, res *driver.Result) {
	if res.Kind != "rows" {
		return
	}
	sel, reason := sqlclass.ParseSimpleSelect(res.Statement, cs.Dialect)
	if sel == nil {
		res.Editable = &driver.Editable{OK: false, Reason: reason}
		return
	}
	sc, err := s.SQL(ctx, c)
	if err != nil {
		return
	}
	db := sel.Database
	if db == "" {
		db = cs.S.Database(ctx)
	}
	t, err := sc.Table(ctx, db, sel.Table)
	if err != nil {
		res.Editable = &driver.Editable{OK: false, Reason: "无法读取表结构"}
		return
	}
	// 结果列 → 原始列
	origin := map[string]string{}
	star := false
	for _, col := range sel.Cols {
		if col.Star {
			star = true
		} else {
			origin[col.Alias] = col.Name
		}
	}
	byName := map[string]driver.ColumnInfo{}
	for _, col := range t.Columns {
		byName[col.Name] = col
	}
	have := map[string]bool{}
	for i := range res.Columns {
		rc := &res.Columns[i]
		on := origin[rc.Name]
		if on == "" && star {
			on = rc.Name
		}
		if ci, ok := byName[on]; ok {
			rc.Origin, rc.Table, rc.Database = ci.Name, t.Name, db
			rc.Kind = ci.Kind
			have[ci.Name] = true
		}
	}
	if len(t.Key) == 0 || t.KeyKind == "rowid" {
		res.Editable = &driver.Editable{OK: false, Reason: "该表没有主键或非空唯一键，结果不能直接修改"}
		return
	}
	for _, k := range t.Key {
		if !have[k] {
			res.Editable = &driver.Editable{OK: false, Reason: "结果里没有完整的主键列（" + strings.Join(t.Key, ", ") + "），不能直接修改"}
			return
		}
	}
	for i := range res.Columns {
		for _, k := range t.Key {
			if res.Columns[i].Origin == k {
				res.Columns[i].PK = true
			}
		}
	}
	res.Editable = &driver.Editable{OK: true, Database: db, Table: t.Name, Key: t.Key}
}

// Change 是表格里的一处修改。
type Change struct {
	Op     string         `json:"op"` // update | insert | delete
	Key    map[string]any `json:"key"`
	Set    map[string]any `json:"set"`
	Old    map[string]any `json:"old"`
	Values map[string]any `json:"values"`
}

// ApplyRequest 是提交变更集的请求。
type ApplyRequest struct {
	Database string   `json:"db"`
	Table    string   `json:"table"`
	Changes  []Change `json:"changes"`
	DryRun   bool     `json:"dry_run"`
	Session  string   `json:"session"` // 在控制台会话里执行（手动事务模式下不自动提交）
}

// ApplyResult 是提交结果。
type ApplyResult struct {
	Statements []string `json:"statements"`
	Applied    int      `json:"applied"`
	FailedAt   int      `json:"failed_at"` // -1 表示全部成功
	Error      string   `json:"error,omitempty"`
	InsertIDs  []int64  `json:"insert_ids,omitempty"`
}

type stmt struct {
	sql      string
	args     []any
	display  string
	needOne  bool
	isInsert bool
}

const defaultMarker = "\x00lemi-default\x00"

// decodeValue 把前端的值转成参数：null → NULL，{"$bin": base64} → []byte，{"$default": true} → DEFAULT。
func decodeValue(v any) (any, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case string:
		return x, nil
	case json.Number:
		return x.String(), nil
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), nil
	case bool:
		if x {
			return "1", nil
		}
		return "0", nil
	case map[string]any:
		if b, ok := x["$bin"].(string); ok {
			raw, err := base64.StdEncoding.DecodeString(b)
			if err != nil {
				return nil, apperr.Bad("二进制值格式不正确")
			}
			return raw, nil
		}
		if h, ok := x["$hex"].(string); ok {
			raw, err := hex.DecodeString(strings.TrimPrefix(strings.TrimPrefix(h, "0x"), "0X"))
			if err != nil {
				return nil, apperr.Bad("十六进制值格式不正确")
			}
			return raw, nil
		}
		if d, ok := x["$default"].(bool); ok && d {
			return defaultMarker, nil
		}
	}
	return nil, apperr.Bad("不支持的单元格值")
}

func literal(sc driver.SQLConn, v any) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case []byte:
		if sc.Dialect() == "sqlite" {
			return "X'" + hex.EncodeToString(x) + "'"
		}
		if len(x) == 0 {
			return "''"
		}
		return "0x" + hex.EncodeToString(x)
	case string:
		if x == defaultMarker {
			return "DEFAULT"
		}
		return sc.QuoteString(x)
	}
	return sc.QuoteString(fmt.Sprint(v))
}

// comparable 旧值能否用于乐观并发比较（浮点、JSON、空间、被截断的值不比较）。
func comparable(kind string, v any) bool {
	switch kind {
	case "float", "json", "geometry":
		return false
	}
	if m, ok := v.(map[string]any); ok {
		if _, t := m["$trunc"]; t {
			return false
		}
		if b, ok := m["$bin"].(string); ok {
			if l, ok := m["len"].(json.Number); ok {
				n, _ := l.Int64()
				raw, _ := base64.StdEncoding.DecodeString(b)
				return int64(len(raw)) == n
			}
		}
	}
	return true
}

func (s *Service) buildChanges(sc driver.SQLConn, t *driver.TableInfo, req *ApplyRequest) ([]stmt, error) {
	if len(t.Key) == 0 {
		return nil, apperr.Bad("该表没有主键或非空唯一键，不能直接修改")
	}
	if t.Kind == "view" {
		return nil, apperr.Bad("视图不能直接修改")
	}
	cols := map[string]driver.ColumnInfo{}
	for _, col := range t.Columns {
		cols[col.Name] = col
	}
	isKey := func(name string) bool {
		for _, k := range t.Key {
			if k == name {
				return true
			}
		}
		return false
	}
	tbl := qualified(sc, req.Database, req.Table)
	hooks := sc.Hooks()
	where := func(ch Change, withOld bool) (string, []any, string, error) {
		var parts, disp []string
		var args []any
		for _, k := range t.Key {
			v, ok := ch.Key[k]
			if !ok {
				return "", nil, "", apperr.Bad("缺少主键列 %s 的值", k)
			}
			dv, err := decodeValue(v)
			if err != nil {
				return "", nil, "", err
			}
			q := sc.QuoteIdent(k)
			if k == "rowid" && t.KeyKind == "rowid" {
				q = "rowid"
			}
			parts = append(parts, hooks.NullSafeEq(q))
			disp = append(disp, q+" = "+literal(sc, dv))
			args = append(args, dv)
		}
		if withOld {
			for name, ov := range ch.Old {
				ci, ok := cols[name]
				if !ok || isKey(name) || !comparable(ci.Kind, ov) {
					continue
				}
				// SQLite 的日期时间是文本，驱动解析后再格式化可能与原文写法不同（T 分隔、时区后缀），不做旧值比较
				if sc.Dialect() == "sqlite" && (ci.Kind == "date" || ci.Kind == "datetime") {
					continue
				}
				dv, err := decodeValue(ov)
				if err != nil || dv == defaultMarker {
					continue
				}
				q := sc.QuoteIdent(name)
				parts = append(parts, hooks.NullSafeEq(q))
				args = append(args, dv)
			}
		}
		return strings.Join(parts, " AND "), args, strings.Join(disp, " AND "), nil
	}
	var out []stmt
	for _, ch := range req.Changes {
		switch ch.Op {
		case "update":
			if len(ch.Set) == 0 {
				continue
			}
			var sets, disp []string
			var args []any
			for _, name := range sortedNames(ch.Set) {
				ci, ok := cols[name]
				if !ok {
					return nil, apperr.Bad("列 %s 不存在（表结构可能已变化，请刷新）", name)
				}
				if ci.Generated != "" {
					return nil, apperr.Bad("列 %s 是生成列，不能修改", name)
				}
				dv, err := decodeValue(ch.Set[name])
				if err != nil {
					return nil, err
				}
				q := sc.QuoteIdent(name)
				if dv == defaultMarker {
					sets = append(sets, q+" = DEFAULT")
				} else {
					sets = append(sets, q+" = ?")
					args = append(args, dv)
				}
				disp = append(disp, q+" = "+literal(sc, dv))
			}
			w, wargs, wdisp, err := where(ch, true)
			if err != nil {
				return nil, err
			}
			out = append(out, stmt{sql: "UPDATE " + tbl + " SET " + strings.Join(sets, ", ") + " WHERE " + w, args: append(args, wargs...),
				display: "UPDATE " + tbl + " SET " + strings.Join(disp, ", ") + " WHERE " + wdisp + ";", needOne: true})
		case "delete":
			w, wargs, wdisp, err := where(ch, false)
			if err != nil {
				return nil, err
			}
			out = append(out, stmt{sql: "DELETE FROM " + tbl + " WHERE " + w, args: wargs, display: "DELETE FROM " + tbl + " WHERE " + wdisp + ";", needOne: true})
		case "insert":
			var names, marks, disp []string
			var args []any
			for _, name := range sortedNames(ch.Values) {
				ci, ok := cols[name]
				if !ok {
					if name == "rowid" {
						continue
					}
					return nil, apperr.Bad("列 %s 不存在", name)
				}
				if ci.Generated != "" {
					continue
				}
				dv, err := decodeValue(ch.Values[name])
				if err != nil {
					return nil, err
				}
				if dv == defaultMarker {
					continue
				}
				names = append(names, sc.QuoteIdent(name))
				marks = append(marks, "?")
				args = append(args, dv)
				disp = append(disp, literal(sc, dv))
			}
			var q, d string
			if len(names) == 0 {
				if sc.Dialect() == "sqlite" {
					q = "INSERT INTO " + tbl + " DEFAULT VALUES"
				} else {
					q = "INSERT INTO " + tbl + " () VALUES ()"
				}
				d = q + ";"
			} else {
				q = "INSERT INTO " + tbl + " (" + strings.Join(names, ", ") + ") VALUES (" + strings.Join(marks, ", ") + ")"
				d = "INSERT INTO " + tbl + " (" + strings.Join(names, ", ") + ") VALUES (" + strings.Join(disp, ", ") + ");"
			}
			out = append(out, stmt{sql: q, args: args, display: d, isInsert: true})
		default:
			return nil, apperr.Bad("未知的修改类型 %q", ch.Op)
		}
	}
	if len(out) == 0 {
		return nil, apperr.Bad("没有需要提交的修改")
	}
	return out, nil
}

func sortedNames(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// 稳定顺序，生成的 SQL 可复现
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Apply 提交变更集：全部语句在一个事务里执行，任一失败整体回滚；修改与删除要求恰好影响一行（乐观并发）。
// 如果指定了控制台会话且会话处于手动事务中，语句在该事务里执行、不自动提交。
func (s *Service) Apply(ctx context.Context, u *accounts.User, c *Connection, level string, req *ApplyRequest, ip string) (*ApplyResult, error) {
	sc, err := s.SQL(ctx, c)
	if err != nil {
		return nil, err
	}
	t, err := sc.Table(ctx, req.Database, req.Table)
	if err != nil {
		return nil, apperr.Wrap("table_failed", err)
	}
	stmts, err := s.buildChanges(sc, t, req)
	if err != nil {
		return nil, err
	}
	res := &ApplyResult{FailedAt: -1}
	for _, st := range stmts {
		res.Statements = append(res.Statements, st.display)
	}
	if req.DryRun {
		return res, nil
	}
	if !CanWrite(c, level) {
		return nil, apperr.Forbidden("你对这个连接只有读取权限")
	}
	var cs *Console
	own := false
	if req.Session != "" {
		cs, err = s.Console(u, req.Session)
		if err != nil {
			return nil, err
		}
		if cs.ConnID != c.ID || cs.ReadOnly {
			return nil, apperr.Bad("会话与连接不匹配或是只读会话")
		}
	}
	if cs == nil || !cs.S.InTx() {
		cs2, err := s.OpenConsole(ctx, u, c, level, req.Database, audit.SrcGrid, false, 0)
		if err != nil {
			return nil, err
		}
		cs, own = cs2, true
		defer s.CloseConsole(cs.ID)
		if _, err := cs.S.Exec(ctx, "BEGIN", nil, 0); err != nil {
			return nil, apperr.Wrap("tx_failed", err)
		}
	}
	sys := s.set.System(ctx)
	for i, st := range stmts {
		r, err := cs.S.Exec(ctx, st.sql, st.args, 0)
		info := sqlclass.Classify(st.sql, cs.Dialect)
		if err == nil && st.needOne && r.Affected != 1 {
			if r.Affected == 0 {
				err = fmt.Errorf("第 %d 处修改没有生效：该行已被别人修改或删除，请刷新后重试", i+1)
			} else {
				err = fmt.Errorf("第 %d 处修改影响了 %d 行（预期 1 行），已整体撤销", i+1, r.Affected)
			}
		}
		rec := &driver.Result{Kind: "exec", DurationMS: 0}
		if r != nil {
			rec = r
		}
		if err != nil {
			rec = &driver.Result{Kind: "error", Error: err.Error()}
		}
		s.record(ctx, cs, st.display, info, rec, ExecOptions{Source: audit.SrcGrid, IP: ip}, sys.AuditSQLMaxLength)
		if err != nil {
			if own {
				_, _ = cs.S.Exec(context.WithoutCancel(ctx), "ROLLBACK", nil, 0)
			}
			res.FailedAt, res.Error = i, err.Error()
			return res, nil
		}
		if st.isInsert && r != nil {
			res.InsertIDs = append(res.InsertIDs, r.LastInsertID)
		}
		res.Applied++
	}
	if own {
		if _, err := cs.S.Exec(ctx, "COMMIT", nil, 0); err != nil {
			res.FailedAt, res.Error, res.Applied = len(stmts)-1, "提交失败："+err.Error(), 0
			return res, nil
		}
	}
	return res, nil
}
