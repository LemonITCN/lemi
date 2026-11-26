package conns

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
	"github.com/LemonITCN/lemi/lemi-service/internal/driver"
	"github.com/LemonITCN/lemi/lemi-service/internal/ids"
	"github.com/LemonITCN/lemi/lemi-service/internal/sqlclass"
	"github.com/LemonITCN/lemi/lemi-service/internal/sqlsplit"
	"github.com/LemonITCN/lemi/lemi-service/internal/store"
)

// Console 是一个登记在册的会话（控制台标签页、智能体、MCP）。
type Console struct {
	ID          string
	UserID      string
	Username    string
	ConnID      string
	WorkspaceID string
	Source      string
	ReadOnly    bool
	Dialect     string
	S           *driver.Session
	Created     time.Time
}

type registry struct {
	mu sync.Mutex
	m  map[string]*Console
}

func newRegistry() *registry { return &registry{m: map[string]*Console{}} }

func (r *registry) add(c *Console) {
	r.mu.Lock()
	r.m[c.ID] = c
	r.mu.Unlock()
}

func (r *registry) get(id string) *Console {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.m[id]
}

func (r *registry) remove(id string) *Console {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.m[id]
	delete(r.m, id)
	return c
}

func (r *registry) usesConn(connID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.m {
		if c.ConnID == connID {
			return true
		}
	}
	return false
}

func (r *registry) closeConn(connID string) {
	r.mu.Lock()
	var list []*Console
	for id, c := range r.m {
		if c.ConnID == connID {
			list = append(list, c)
			delete(r.m, id)
		}
	}
	r.mu.Unlock()
	for _, c := range list {
		c.S.Close()
	}
}

func (r *registry) closeAll() {
	r.mu.Lock()
	list := make([]*Console, 0, len(r.m))
	for _, c := range r.m {
		list = append(list, c)
	}
	r.m = map[string]*Console{}
	r.mu.Unlock()
	for _, c := range list {
		c.S.Close()
	}
}

// reap 关闭空闲超过 idle 的会话；有未提交事务的会话回滚并记审计。
func (r *registry) reap(idle time.Duration, au *audit.Log) {
	r.mu.Lock()
	var list []*Console
	for id, c := range r.m {
		if !c.S.Running() && time.Since(c.S.LastUsed) > idle {
			list = append(list, c)
			delete(r.m, id)
		}
	}
	r.mu.Unlock()
	for _, c := range list {
		if c.S.InTx() && au != nil {
			au.Record(audit.Entry{UserID: c.UserID, Username: c.Username, WorkspaceID: c.WorkspaceID, ConnectionID: c.ConnID, Source: audit.SrcSystem,
				Action: "sql.tx_rolled_back_idle"}, map[string]any{"session": c.ID})
		}
		c.S.Close()
	}
}

// OpenConsole 为用户在连接上开一个会话。用户对连接只有读权限、或连接设为只读时，会话在数据库层强制只读。
func (s *Service) OpenConsole(ctx context.Context, u *accounts.User, c *Connection, level, database, source string, forceReadOnly bool, timeoutSec int) (*Console, error) {
	sc, err := s.SQL(ctx, c)
	if err != nil {
		return nil, err
	}
	ro := forceReadOnly || !CanWrite(c, level)
	if timeoutSec == 0 {
		timeoutSec = s.set.System(ctx).QueryTimeoutSec
	}
	id := ids.New()
	ds, err := driver.OpenSession(ctx, sc, id, driver.SessionOptions{ReadOnly: ro, Database: database, Timeout: timeoutSec})
	if err != nil {
		return nil, apperr.Wrap("session_failed", err)
	}
	cs := &Console{ID: id, UserID: u.ID, Username: u.Username, ConnID: c.ID, WorkspaceID: c.WorkspaceID, Source: source, ReadOnly: ro, Dialect: sc.Dialect(), S: ds, Created: time.Now()}
	s.sessions.add(cs)
	return cs, nil
}

// Console 取用户自己的会话。
func (s *Service) Console(u *accounts.User, id string) (*Console, error) {
	cs := s.sessions.get(id)
	if cs == nil || cs.UserID != u.ID {
		return nil, apperr.New(404, "session_gone", "会话已失效（空闲超时或连接配置已修改），请重新打开")
	}
	return cs, nil
}

// CloseConsole 关闭会话。
func (s *Service) CloseConsole(id string) {
	if cs := s.sessions.remove(id); cs != nil {
		cs.S.Close()
	}
}

// ExecOptions 执行选项。
type ExecOptions struct {
	MaxRows     int    // 首批行数
	StopOnError bool   // 脚本遇错即停（默认 true）
	Source      string // ui | agent | mcp | task
	IP          string
	// Check 每条语句执行前的检查（智能体档位、白名单、审批）。返回错误则不执行该语句。
	Check func(ctx context.Context, stmt string, info sqlclass.Info) error
	// Params 只有单条语句时使用的参数
	Params []any
}

// ErrSkipped 被 Check 拒绝的语句。
var ErrSkipped = errors.New("未执行")

// Exec 执行一段 SQL（可含多条语句），逐条返回结果；每条都记审计与查询历史。
func (s *Service) Exec(ctx context.Context, cs *Console, sql string, o ExecOptions) []*driver.Result {
	stmts := sqlsplit.Split(sql, sqlsplit.Dialect(cs.Dialect))
	if o.MaxRows <= 0 {
		o.MaxRows = 500
	}
	var out []*driver.Result
	sys := s.set.System(ctx)
	for i, st := range stmts {
		info := sqlclass.Classify(st.Text, cs.Dialect)
		var res *driver.Result
		var err error
		if o.Check != nil {
			err = o.Check(ctx, st.Text, info)
		}
		start := time.Now()
		if err == nil {
			var params []any
			if len(stmts) == 1 {
				params = o.Params
			}
			res, err = cs.S.Exec(ctx, st.Text, params, o.MaxRows)
		}
		if err != nil {
			res = &driver.Result{Statement: st.Text, Kind: "error", Error: friendlyDBErr(err), Class: info, DurationMS: time.Since(start).Milliseconds()}
		}
		if res.Kind == "rows" && i < len(stmts)-1 && res.HasMore {
			// 不是最后一条：游标会被下一条关闭，标记为不可续取
			res.HasMore = false
		}
		out = append(out, res)
		s.record(ctx, cs, st.Text, info, res, o, sys.AuditSQLMaxLength)
		if res.Kind == "error" && (o.StopOnError || len(stmts) == 1) {
			break
		}
		if ctx.Err() != nil {
			break
		}
	}
	return out
}

func friendlyDBErr(err error) string {
	msg := err.Error()
	if errors.Is(err, driver.ErrReadOnly) {
		return msg
	}
	return msg
}

// record 写审计与查询历史。
func (s *Service) record(ctx context.Context, cs *Console, stmt string, info sqlclass.Info, res *driver.Result, o ExecOptions, maxLen int) {
	text := stmt
	if len(text) > maxLen {
		text = text[:maxLen] + "…"
	}
	src := o.Source
	if src == "" {
		src = audit.SrcUI
	}
	rows := res.Affected
	if res.Kind == "rows" {
		rows = int64(len(res.Rows))
	}
	if s.audit != nil {
		s.audit.Record(audit.Entry{UserID: cs.UserID, Username: cs.Username, WorkspaceID: cs.WorkspaceID, ConnectionID: cs.ConnID, Source: src,
			Action: "sql." + string(info.Kind), Target: info.Keyword, IP: o.IP, OK: res.Kind != "error", Error: res.Error, DurationMS: res.DurationMS},
			map[string]any{"sql": text, "rows": rows, "session": cs.ID})
	}
	if src == audit.SrcUI {
		_, _ = s.st.Exec(context.WithoutCancel(ctx), `INSERT INTO query_history (user_id, connection_id, database, sql, source, at, duration_ms, ok, rows, error) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			cs.UserID, cs.ConnID, "", text, src, store.Now(), res.DurationMS, res.Kind != "error", rows, truncate(res.Error, 500))
	}
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// History 查询历史。
type History struct {
	ID         int64  `db:"id" json:"id"`
	Database   string `db:"database" json:"database"`
	SQL        string `db:"sql" json:"sql"`
	At         int64  `db:"at" json:"at"`
	DurationMS int64  `db:"duration_ms" json:"duration_ms"`
	OK         bool   `db:"ok" json:"ok"`
	Rows       int64  `db:"rows" json:"rows"`
	Error      string `db:"error" json:"error"`
}

// HistoryList 用户在连接上的查询历史（可搜索）。
func (s *Service) HistoryList(ctx context.Context, userID, connID, q string, before int64, limit int) ([]History, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	sqlq := `SELECT id, database, sql, at, duration_ms, ok, rows, error FROM query_history WHERE user_id = ? AND connection_id = ?`
	args := []any{userID, connID}
	if q = strings.TrimSpace(q); q != "" {
		sqlq += ` AND sql LIKE ? ESCAPE '\'`
		args = append(args, "%"+strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)+"%")
	}
	if before > 0 {
		sqlq += ` AND id < ?`
		args = append(args, before)
	}
	sqlq += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	var out []History
	err := s.st.Select(ctx, &out, sqlq, args...)
	return out, err
}

// PruneHistory 每个用户每个连接只保留最近 5000 条。
func (s *Service) PruneHistory(ctx context.Context) {
	_, _ = s.st.Exec(ctx, `DELETE FROM query_history WHERE id IN (
		SELECT id FROM (SELECT id, ROW_NUMBER() OVER (PARTITION BY user_id, connection_id ORDER BY id DESC) AS rn FROM query_history) WHERE rn > 5000)`)
}
