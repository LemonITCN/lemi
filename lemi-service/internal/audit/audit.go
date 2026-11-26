// Package audit 记录审计日志：登录、权限与配置变更、每一条执行的 SQL、导出下载还原等。
package audit

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/store"
)

// 来源。
const (
	SrcUI     = "ui"
	SrcGrid   = "grid"
	SrcAgent  = "agent"
	SrcMCP    = "mcp"
	SrcTask   = "task"
	SrcSystem = "system"
	SrcCLI    = "cli"
)

// Entry 是一条审计记录。
type Entry struct {
	ID           int64  `db:"id" json:"id"`
	At           int64  `db:"at" json:"at"`
	UserID       string `db:"user_id" json:"user_id"`
	Username     string `db:"username" json:"username"`
	WorkspaceID  string `db:"workspace_id" json:"workspace_id"`
	ConnectionID string `db:"connection_id" json:"connection_id"`
	Source       string `db:"source" json:"source"`
	Action       string `db:"action" json:"action"`
	Target       string `db:"target" json:"target"`
	Detail       string `db:"detail" json:"detail"`
	IP           string `db:"ip" json:"ip"`
	OK           bool   `db:"ok" json:"ok"`
	Error        string `db:"error" json:"error"`
	DurationMS   int64  `db:"duration_ms" json:"duration_ms"`
}

// Log 审计服务。写入走异步队列，不拖慢请求；队列满时同步写。
type Log struct {
	st  *store.Store
	log *slog.Logger
	ch  chan Entry
}

// New 创建审计服务并启动写入协程。
func New(st *store.Store, log *slog.Logger) *Log {
	l := &Log{st: st, log: log, ch: make(chan Entry, 4096)}
	go l.loop()
	return l
}

func (l *Log) loop() {
	batch := make([]Entry, 0, 128)
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	flush := func() {
		if len(batch) == 0 {
			return
		}
		for _, e := range batch {
			l.write(e)
		}
		batch = batch[:0]
	}
	for {
		select {
		case e, ok := <-l.ch:
			if !ok {
				flush()
				return
			}
			batch = append(batch, e)
			if len(batch) >= 128 {
				flush()
			}
		case <-tick.C:
			flush()
		}
	}
}

func (l *Log) write(e Entry) {
	_, err := l.st.Exec(context.Background(), `INSERT INTO audit_log (at, user_id, username, workspace_id, connection_id, source, action, target, detail, ip, ok, error, duration_ms)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, e.At, e.UserID, e.Username, e.WorkspaceID, e.ConnectionID, e.Source, e.Action, e.Target, e.Detail, e.IP, e.OK, e.Error, e.DurationMS)
	if err != nil {
		l.log.Error("写审计日志失败", "action", e.Action, "err", err)
	}
}

// Record 写一条审计。detail 可以是任意可 JSON 化的值。
func (l *Log) Record(e Entry, detail any) {
	if e.At == 0 {
		e.At = time.Now().UnixMilli()
	}
	if detail != nil {
		if b, err := json.Marshal(detail); err == nil {
			e.Detail = string(b)
		}
	}
	if e.Detail == "" {
		e.Detail = "{}"
	}
	if len(e.Error) > 2000 {
		e.Error = e.Error[:2000]
	}
	select {
	case l.ch <- e:
	default:
		l.write(e)
	}
}

// Flush 等待队列写完（测试与退出时用）。
func (l *Log) Flush() {
	for i := 0; i < 50 && len(l.ch) > 0; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(600 * time.Millisecond)
}

// Query 检索条件。
type Query struct {
	WorkspaceID  string
	UserID       string
	ConnectionID string
	Source       string
	Action       string // 前缀匹配
	Text         string // 在目标与详情中搜索
	Before       int64  // 游标：id 小于它
	Since, Until int64
	Limit        int
}

// List 检索审计记录（按 id 倒序）。
func (l *Log) List(ctx context.Context, q Query) ([]Entry, error) {
	var conds []string
	var args []any
	add := func(c string, a ...any) { conds = append(conds, c); args = append(args, a...) }
	if q.WorkspaceID != "" {
		add("workspace_id = ?", q.WorkspaceID)
	}
	if q.UserID != "" {
		add("user_id = ?", q.UserID)
	}
	if q.ConnectionID != "" {
		add("connection_id = ?", q.ConnectionID)
	}
	if q.Source != "" {
		add("source = ?", q.Source)
	}
	if q.Action != "" {
		add("action LIKE ? ESCAPE '\\'", escapeLike(q.Action)+"%")
	}
	if q.Text != "" {
		p := "%" + escapeLike(q.Text) + "%"
		add("(target LIKE ? ESCAPE '\\' OR detail LIKE ? ESCAPE '\\')", p, p)
	}
	if q.Before > 0 {
		add("id < ?", q.Before)
	}
	if q.Since > 0 {
		add("at >= ?", q.Since)
	}
	if q.Until > 0 {
		add("at < ?", q.Until)
	}
	if q.Limit <= 0 || q.Limit > 500 {
		q.Limit = 100
	}
	sql := "SELECT * FROM audit_log"
	if len(conds) > 0 {
		sql += " WHERE " + strings.Join(conds, " AND ")
	}
	sql += " ORDER BY id DESC LIMIT ?"
	args = append(args, q.Limit)
	var out []Entry
	err := l.st.Select(ctx, &out, sql, args...)
	return out, err
}

func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// Prune 删除超过保留期的记录。
func (l *Log) Prune(ctx context.Context, days int) {
	cut := time.Now().Add(-time.Duration(days) * 24 * time.Hour).UnixMilli()
	_, _ = l.st.Exec(ctx, `DELETE FROM audit_log WHERE at < ?`, cut)
}
