package driver

import (
	"context"
	"database/sql"
	sqldriver "database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/sqlclass"
)

// Result 是一条语句的执行结果。
type Result struct {
	Statement    string        `json:"statement"`
	Kind         string        `json:"kind"` // rows | exec | error
	Columns      []Column      `json:"columns,omitempty"`
	Rows         [][]any       `json:"rows,omitempty"`
	HasMore      bool          `json:"has_more,omitempty"`
	Affected     int64         `json:"affected"`
	LastInsertID int64         `json:"last_insert_id,omitempty"`
	DurationMS   int64         `json:"duration_ms"`
	Error        string        `json:"error,omitempty"`
	Class        sqlclass.Info `json:"class"`
	Editable     *Editable     `json:"editable,omitempty"`
}

// Editable 说明结果能否在表格里直接修改。
type Editable struct {
	OK       bool     `json:"ok"`
	Reason   string   `json:"reason,omitempty"`
	Database string   `json:"db,omitempty"`
	Table    string   `json:"table,omitempty"`
	Key      []string `json:"key,omitempty"`
}

// ErrReadOnly 只读会话里执行写语句。
var ErrReadOnly = errors.New("当前是只读会话，不能执行会修改数据或结构的语句")

// ErrBusy 会话正在执行别的语句。
var ErrBusy = errors.New("该控制台正在执行语句，请等待完成或先取消")

// Session 是固定在一条数据库连接上的会话（控制台标签页、智能体一轮执行、表格提交）。
type Session struct {
	ID       string
	conn     SQLConn
	c        *sql.Conn
	hooks    SessionHooks
	opts     SessionOptions
	serverID string

	mu       sync.Mutex // 同一时间只执行一条语句
	stateMu  sync.Mutex
	running  context.CancelFunc
	inTx     bool
	roOpen   bool // 只读包裹事务尚未结束（游标还开着）
	rows     *sql.Rows
	rowsStop context.CancelFunc // 游标的上下文（游标关闭时取消）
	cols     []Column
	kinds    []string
	LastUsed time.Time
	closed   bool
	// CellLimit 单元格截断上限（字节），0 = 默认 64KB
	CellLimit int
}

// OpenSession 从数据源取一条专用连接并初始化。
func OpenSession(ctx context.Context, conn SQLConn, id string, o SessionOptions) (*Session, error) {
	db := conn.DB()
	if o.ReadOnly {
		db = conn.ReadDB()
	}
	c, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	h := conn.Hooks()
	if err := h.Init(ctx, c, o); err != nil {
		_ = c.Raw(func(any) error { return sqldriver.ErrBadConn }) // 初始化失败的连接不再放回池里
		c.Close()
		return nil, err
	}
	s := &Session{ID: id, conn: conn, c: c, hooks: h, opts: o, LastUsed: time.Now()}
	s.serverID = h.SessionID(ctx, c)
	return s, nil
}

// Options 会话选项。
func (s *Session) Options() SessionOptions { return s.opts }

// InTx 是否有用户开启的未提交事务。
func (s *Session) InTx() bool {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.inTx
}

// Database 当前库。
func (s *Session) Database(ctx context.Context) string { return s.hooks.Current(ctx, s.c) }

// Use 切换当前库。
func (s *Session) Use(ctx context.Context, db string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.closeCursor(); err != nil {
		return err
	}
	return s.hooks.Use(ctx, s.c, db)
}

func (s *Session) closeCursor() error {
	var err error
	if s.rows != nil {
		err = s.rows.Close()
		s.rows = nil
	}
	if s.rowsStop != nil {
		s.rowsStop()
		s.rowsStop = nil
	}
	if s.roOpen {
		_, end := s.hooks.WrapReadOnly()
		if end != "" {
			if _, e := s.c.ExecContext(context.Background(), end); e != nil && err == nil {
				err = e
			}
		}
		s.roOpen = false
	}
	return err
}

// Exec 执行一条语句；返回结果集时先取 firstBatch 行，游标保留在会话里供 Fetch 续取。
func (s *Session) Exec(ctx context.Context, stmt string, args []any, firstBatch int) (*Result, error) {
	if !s.mu.TryLock() {
		return nil, ErrBusy
	}
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("会话已关闭")
	}
	s.LastUsed = time.Now()
	if err := s.closeCursor(); err != nil {
		return nil, err
	}
	class := sqlclass.Classify(stmt, s.conn.Dialect())
	res := &Result{Statement: stmt, Class: class}
	if s.opts.ReadOnly && !class.ReadOnlyOK() {
		return nil, ErrReadOnly
	}
	if s.conn.Dialect() == "sqlite" && (class.Keyword == "ATTACH" || class.Keyword == "DETACH" || (class.Keyword == "VACUUM" && class.Dangerous != "") || class.Dangerous == "加载扩展") {
		return nil, errors.New("出于安全原因，Lemi 不允许在 SQLite 连接上执行 ATTACH、DETACH、VACUUM INTO 与 load_extension")
	}
	if s.conn.Dialect() == "mysql" && class.Kind == sqlclass.Admin && class.Dangerous == "从文件导入" && strings.Contains(strings.ToUpper(stmt), "LOCAL") {
		return nil, errors.New("出于安全原因，Lemi 不允许 LOAD DATA LOCAL INFILE")
	}
	rctx := ctx
	var cancel context.CancelFunc
	if s.opts.Timeout > 0 {
		rctx, cancel = context.WithTimeout(ctx, time.Duration(s.opts.Timeout)*time.Second)
	} else {
		rctx, cancel = context.WithCancel(ctx)
	}
	s.stateMu.Lock()
	s.running = cancel
	s.stateMu.Unlock()
	defer func() {
		s.stateMu.Lock()
		s.running = nil
		s.stateMu.Unlock()
		cancel()
	}()
	// 事务控制语句：记录状态
	upper := class.Keyword
	start := time.Now()
	wrapped := false
	if s.opts.ReadOnly && !s.InTx() && class.Kind != sqlclass.Tx {
		if begin, _ := s.hooks.WrapReadOnly(); begin != "" {
			if _, err := s.c.ExecContext(rctx, begin); err != nil {
				return nil, err
			}
			wrapped = true
			s.roOpen = true
		}
	}
	finishWrap := func() {
		if wrapped && s.rows == nil {
			_ = s.closeCursor()
		}
	}
	if class.ReturnsRows || class.Kind == sqlclass.Call {
		// 游标要在本次请求之后继续存活（Fetch 续取），所以查询用独立的上下文；
		// 首批读取期间，请求取消或超时会取消它。
		qctx, qcancel := context.WithCancel(context.Background())
		stop := make(chan struct{})
		go func() {
			select {
			case <-rctx.Done():
				qcancel()
			case <-stop:
			}
		}()
		s.stateMu.Lock()
		s.running = qcancel
		s.stateMu.Unlock()
		rows, err := s.c.QueryContext(qctx, stmt, args...)
		if err != nil {
			close(stop)
			qcancel()
			finishWrap()
			return nil, s.wrapErr(rctx, err)
		}
		cols, kinds, err := s.columns(rows)
		if err != nil || len(cols) == 0 {
			close(stop)
			rows.Close()
			qcancel()
			finishWrap()
			if err != nil {
				return nil, err
			}
			// CALL 没有结果集、或驱动把非查询语句当成查询
			res.Kind = "exec"
			res.DurationMS = time.Since(start).Milliseconds()
			return res, nil
		}
		s.rows, s.rowsStop, s.cols, s.kinds = rows, qcancel, cols, kinds
		data, more, err := s.read(firstBatch)
		close(stop)
		res.Kind, res.Columns, res.Rows, res.HasMore = "rows", cols, data, more
		res.DurationMS = time.Since(start).Milliseconds()
		if err != nil {
			_ = s.closeCursor()
			return nil, s.wrapErr(rctx, err)
		}
		if !more {
			_ = s.closeCursor()
		}
		return res, nil
	}
	r, err := s.c.ExecContext(rctx, stmt, args...)
	res.DurationMS = time.Since(start).Milliseconds()
	finishWrap()
	if err != nil {
		return nil, s.wrapErr(rctx, err)
	}
	res.Kind = "exec"
	res.Affected, _ = r.RowsAffected()
	if id, err := r.LastInsertId(); err == nil {
		res.LastInsertID = id
	}
	switch {
	case upper == "BEGIN" || (upper == "START" && class.Kind == sqlclass.Tx):
		s.setTx(true)
	case upper == "COMMIT" || upper == "ROLLBACK" || upper == "END":
		if !strings.Contains(strings.ToUpper(stmt), " TO ") {
			s.setTx(false)
		}
	case class.Kind == sqlclass.DDL && s.conn.Dialect() == "mysql":
		s.setTx(false) // MySQL 的 DDL 会隐式提交
	}
	return res, nil
}

func (s *Session) setTx(v bool) {
	s.stateMu.Lock()
	s.inTx = v
	s.stateMu.Unlock()
}

func (s *Session) wrapErr(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("执行超时（超过 %d 秒），已取消", s.opts.Timeout)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return errors.New("已取消")
	}
	return err
}

func (s *Session) columns(rows *sql.Rows) ([]Column, []string, error) {
	cts, err := rows.ColumnTypes()
	if err != nil {
		return nil, nil, err
	}
	cols := make([]Column, len(cts))
	kinds := make([]string, len(cts))
	for i, ct := range cts {
		t := strings.ToUpper(ct.DatabaseTypeName())
		kinds[i] = s.conn.ColumnKind(t)
		cols[i] = Column{Name: ct.Name(), Type: t, Kind: kinds[i]}
		if n, ok := ct.Nullable(); ok {
			cols[i].Nullable = &n
		}
	}
	return cols, kinds, nil
}

func (s *Session) read(n int) ([][]any, bool, error) {
	if s.rows == nil {
		return nil, false, nil
	}
	if n <= 0 {
		n = 500
	}
	out := make([][]any, 0, min(n, 1024))
	vals := make([]any, len(s.cols))
	ptrs := make([]any, len(s.cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for len(out) < n {
		if !s.rows.Next() {
			err := s.rows.Err()
			return out, false, err
		}
		if err := s.rows.Scan(ptrs...); err != nil {
			return out, false, err
		}
		row := make([]any, len(vals))
		for i, v := range vals {
			row[i] = EncodeCell(v, s.kinds[i], s.CellLimit)
		}
		out = append(out, row)
	}
	// 读满了：再看一下有没有下一行（不消耗）做不到，只能告诉前端可能还有
	return out, true, nil
}

// Fetch 从保留的游标继续取 n 行。
func (s *Session) Fetch(ctx context.Context, n int) ([][]any, bool, error) {
	if !s.mu.TryLock() {
		return nil, false, ErrBusy
	}
	defer s.mu.Unlock()
	s.LastUsed = time.Now()
	if s.rows == nil {
		return nil, false, nil
	}
	data, more, err := s.read(n)
	if err != nil || !more {
		_ = s.closeCursor()
	}
	return data, more, err
}

// Columns 当前游标的列。
func (s *Session) Columns() []Column { return s.cols }

// Cancel 取消正在执行的语句。
func (s *Session) Cancel(ctx context.Context) error {
	s.stateMu.Lock()
	cancel := s.running
	s.stateMu.Unlock()
	if cancel == nil {
		return nil
	}
	var err error
	if s.serverID != "" {
		err = s.hooks.Cancel(ctx, s.conn.DB(), s.serverID)
	}
	cancel()
	return err
}

// Running 是否正在执行。
func (s *Session) Running() bool {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.running != nil
}

// Begin / Commit / Rollback 手动事务。
func (s *Session) Begin(ctx context.Context) error {
	_, err := s.Exec(ctx, "BEGIN", nil, 0)
	return err
}

func (s *Session) Commit(ctx context.Context) error {
	_, err := s.Exec(ctx, "COMMIT", nil, 0)
	return err
}

func (s *Session) Rollback(ctx context.Context) error {
	_, err := s.Exec(ctx, "ROLLBACK", nil, 0)
	return err
}

// Conn 底层连接（备份、导入导出等需要直接使用连接的模块用）。调用方负责不与 Exec 并发。
func (s *Session) Conn() *sql.Conn { return s.c }

// Close 回滚未提交事务并释放连接。
func (s *Session) Close() {
	s.Cancel(context.Background())
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	_ = s.closeCursor()
	if s.InTx() {
		_, _ = s.c.ExecContext(context.Background(), "ROLLBACK")
	}
	if s.opts.ReadOnly || s.opts.Database != "" {
		// 会话改过连接状态（只读、默认库），不放回连接池，直接丢弃
		_ = s.c.Raw(func(any) error { return sqldriver.ErrBadConn })
	}
	_ = s.c.Close()
}
