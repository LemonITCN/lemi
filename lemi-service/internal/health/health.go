// Package health 是连接健康检查（docs/spec.md §14）：后台按每个连接自己的间隔建立临时连接（经 SSH/代理，不进连接池）
// 并 Ping，记录延迟与错误；连续失败达到阈值判定下线、成功一次判定恢复，只在状态变化时通知；
// 每次检查结果经实时通道（消息类型 health）推给该空间能看到这个连接的成员。检查记录保留 30 天。
package health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
	"github.com/LemonITCN/lemi/lemi-service/internal/conns"
	"github.com/LemonITCN/lemi/lemi-service/internal/driver"
	"github.com/LemonITCN/lemi/lemi-service/internal/notify"
	"github.com/LemonITCN/lemi/lemi-service/internal/store"
	"github.com/jmoiron/sqlx"
)

// 状态。
const (
	StatusUnknown  = "unknown"
	StatusUp       = "up"
	StatusDown     = "down"
	StatusDisabled = "disabled" // 只出现在 Status() 与列表里：检查已关闭
)

// 可在测试里改短。
var (
	// TickInterval 调度循环检查到期连接的频率。
	TickInterval = time.Second
	// ReloadInterval 重新读取连接列表与配置的频率（配置接口修改时会立即重读）。
	ReloadInterval = 15 * time.Second
	// Retention 检查记录保留时长。
	Retention = 30 * 24 * time.Hour
	// PruneInterval 清理过期记录的频率。
	PruneInterval = time.Hour
)

// MaxConcurrent 全局同时进行的检查数上限。
const MaxConcurrent = 8

// Notifier 是通知模块（*notify.Service 实现）。
type Notifier interface {
	Send(ctx context.Context, wsID string, channelIDs []string, ev notify.Event)
	CheckChannels(ctx context.Context, wsID string, ids []string) error
	FilterChannels(ctx context.Context, wsID string, ids []string) []string
}

// Broadcaster 是实时推送（*realtime.Hub 实现）。
type Broadcaster interface {
	Broadcast(t string, data any, filter func(userID string) bool)
}

// Checker 检查一个连接：成功返回 nil。ctx 带本次检查的超时。
type Checker func(ctx context.Context, c *conns.Connection) error

// Deps 是健康检查服务的依赖。
type Deps struct {
	Store    *store.Store
	Conns    *conns.Service
	Accounts *accounts.Service
	Notify   Notifier
	Hub      Broadcaster
	Log      *slog.Logger
	Location *time.Location // 时间线按天 / 按小时切分用的时区（为空用本机时区）
}

// Service 健康检查服务。
type Service struct {
	st     *store.Store
	conns  *conns.Service
	acc    *accounts.Service
	notify Notifier
	hub    Broadcaster
	log    *slog.Logger
	loc    *time.Location

	// Checker 实际的检查函数（默认 OpenTemp + Ping），测试里可以替换。
	Checker Checker
	now     func() time.Time

	mu      sync.Mutex
	sched   map[string]*entry  // 连接 ID → 调度信息（只含已启用的连接）
	flights map[string]*flight // 正在进行的检查
	stateMu sync.Mutex         // 串行化「读状态 → 计算 → 写状态」
	sem     chan struct{}
	wake    chan struct{}
	wg      sync.WaitGroup
	runCtx  context.Context
}

type entry struct {
	wsID     string
	interval time.Duration
	due      time.Time
}

type flight struct {
	done chan struct{}
	res  *CheckResult
	err  error
}

// New 创建健康检查服务。
func New(d Deps) *Service {
	s := &Service{st: d.Store, conns: d.Conns, acc: d.Accounts, notify: d.Notify, hub: d.Hub, log: d.Log, loc: d.Location,
		now: time.Now, sched: map[string]*entry{}, flights: map[string]*flight{},
		sem: make(chan struct{}, MaxConcurrent), wake: make(chan struct{}, 1)}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.loc == nil {
		s.loc = time.Local
	}
	s.Checker = s.defaultCheck
	return s
}

// defaultCheck 建立临时连接（经 SSH/代理，不进连接池）并 Ping。SSH 主机指纹未确认等都算失败。
func (s *Service) defaultCheck(ctx context.Context, c *conns.Connection) error {
	dc, closeFn, err := s.conns.OpenTemp(ctx, c)
	if err != nil {
		return err
	}
	defer closeFn()
	return dc.Ping(ctx)
}

// ---------- 配置 ----------

// Config 是一个连接的健康检查配置。
type Config struct {
	Enabled       bool     `json:"enabled"`
	IntervalSec   int      `json:"interval_sec"`
	TimeoutSec    int      `json:"timeout_sec"`
	FailThreshold int      `json:"fail_threshold"`
	Channels      []string `json:"channels"`
}

// DefaultConfig 没有配置行的连接用的默认值：启用、60 秒、超时 10 秒、连续失败 3 次判定下线。
func DefaultConfig() Config {
	return Config{Enabled: true, IntervalSec: 60, TimeoutSec: 10, FailThreshold: 3, Channels: []string{}}
}

type configRow struct {
	ConnectionID  string `db:"connection_id"`
	Enabled       bool   `db:"enabled"`
	IntervalSec   int    `db:"interval_sec"`
	TimeoutSec    int    `db:"timeout_sec"`
	FailThreshold int    `db:"fail_threshold"`
	Channels      string `db:"channels"`
	UpdatedBy     string `db:"updated_by"`
	UpdatedAt     int64  `db:"updated_at"`
}

func (r configRow) config() Config {
	c := Config{Enabled: r.Enabled, IntervalSec: r.IntervalSec, TimeoutSec: r.TimeoutSec, FailThreshold: r.FailThreshold}
	_ = json.Unmarshal([]byte(r.Channels), &c.Channels)
	if c.Channels == nil {
		c.Channels = []string{}
	}
	d := DefaultConfig()
	if c.IntervalSec < 10 {
		c.IntervalSec = d.IntervalSec
	}
	if c.TimeoutSec <= 0 {
		c.TimeoutSec = d.TimeoutSec
	}
	if c.FailThreshold <= 0 {
		c.FailThreshold = d.FailThreshold
	}
	return c
}

// Config 连接的健康检查配置；custom 表示有保存过的配置（否则是默认值）。
func (s *Service) Config(ctx context.Context, connID string) (Config, bool, error) {
	var r configRow
	err := s.st.GetW(ctx, &r, `SELECT * FROM health_configs WHERE connection_id = ?`, connID)
	if errors.Is(err, store.ErrNotFound) {
		return DefaultConfig(), false, nil
	}
	if err != nil {
		return Config{}, false, err
	}
	return r.config(), true, nil
}

// validate 校验配置。
func (c *Config) validate() error {
	if c.IntervalSec < 10 || c.IntervalSec > 86400 {
		return apperr.Bad("检查间隔应在 10 秒到 1 天之间")
	}
	if c.TimeoutSec < 1 || c.TimeoutSec > 120 {
		return apperr.Bad("超时应在 1 到 120 秒之间")
	}
	if c.TimeoutSec > c.IntervalSec {
		return apperr.Bad("超时不能大于检查间隔")
	}
	if c.FailThreshold < 1 || c.FailThreshold > 100 {
		return apperr.Bad("下线判定次数应在 1 到 100 之间")
	}
	seen := map[string]bool{}
	var ch []string
	for _, id := range c.Channels {
		if id = strings.TrimSpace(id); id != "" && !seen[id] {
			seen[id] = true
			ch = append(ch, id)
		}
	}
	if ch == nil {
		ch = []string{}
	}
	c.Channels = ch
	return nil
}

// SetConfig 保存连接的健康检查配置并立即生效；关闭检查时状态回到「未知」。
func (s *Service) SetConfig(ctx context.Context, c *conns.Connection, cfg Config, userID string) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	if s.notify != nil {
		if err := s.notify.CheckChannels(ctx, c.WorkspaceID, cfg.Channels); err != nil {
			return err
		}
	} else if len(cfg.Channels) > 0 {
		return apperr.Bad("通知模块不可用")
	}
	ch, _ := json.Marshal(cfg.Channels)
	err := s.st.Tx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO health_configs (connection_id, enabled, interval_sec, timeout_sec, fail_threshold, channels, updated_by, updated_at)
			VALUES (?,?,?,?,?,?,?,?) ON CONFLICT(connection_id) DO UPDATE SET enabled = excluded.enabled, interval_sec = excluded.interval_sec,
			timeout_sec = excluded.timeout_sec, fail_threshold = excluded.fail_threshold, channels = excluded.channels,
			updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
			c.ID, cfg.Enabled, cfg.IntervalSec, cfg.TimeoutSec, cfg.FailThreshold, string(ch), userID, store.Now())
		if err != nil {
			return err
		}
		if !cfg.Enabled {
			_, err = tx.ExecContext(ctx, `DELETE FROM health_state WHERE connection_id = ?`, c.ID)
		}
		return err
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	if !cfg.Enabled {
		delete(s.sched, c.ID)
	} else {
		iv := time.Duration(cfg.IntervalSec) * time.Second
		e := s.sched[c.ID]
		if e == nil {
			e = &entry{wsID: c.WorkspaceID, due: s.now()}
			s.sched[c.ID] = e
		}
		if limit := s.now().Add(iv); e.due.After(limit) {
			e.due = limit
		}
		e.interval = iv
	}
	s.mu.Unlock()
	s.poke()
	return nil
}

func (s *Service) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// ---------- 状态 ----------

// State 是一个连接的当前健康状态。
type State struct {
	ConnectionID  string `db:"connection_id" json:"connection_id"`
	Status        string `db:"status" json:"status"`
	Fails         int    `db:"fails" json:"fails"`
	LastCheckAt   int64  `db:"last_check_at" json:"last_check_at"`
	LastOKAt      int64  `db:"last_ok_at" json:"last_ok_at"`
	LastLatencyMS int64  `db:"last_latency_ms" json:"last_latency_ms"`
	LastError     string `db:"last_error" json:"last_error"`
	ChangedAt     int64  `db:"changed_at" json:"changed_at"`
}

func (s *Service) state(ctx context.Context, connID string) (State, error) {
	var st State
	err := s.st.GetW(ctx, &st, `SELECT * FROM health_state WHERE connection_id = ?`, connID)
	if errors.Is(err, store.ErrNotFound) {
		return State{ConnectionID: connID, Status: StatusUnknown}, nil
	}
	return st, err
}

// Status 连接的当前状态（unknown | up | down | disabled）、最近一次检查时间（Unix 毫秒）与延迟（毫秒）。智能体工具用。
func (s *Service) Status(ctx context.Context, connID string) (status string, lastCheckAt int64, latencyMS int64) {
	cfg, _, err := s.Config(ctx, connID)
	if err != nil {
		return StatusUnknown, 0, 0
	}
	st, err := s.state(ctx, connID)
	if err != nil {
		return StatusUnknown, 0, 0
	}
	if !cfg.Enabled {
		return StatusDisabled, st.LastCheckAt, st.LastLatencyMS
	}
	return st.Status, st.LastCheckAt, st.LastLatencyMS
}

// ---------- 调度 ----------

// Run 后台调度循环，直到 ctx 结束（结束前等进行中的检查收尾）。
func (s *Service) Run(ctx context.Context) {
	s.mu.Lock()
	s.runCtx = ctx
	s.mu.Unlock()
	s.reload(ctx)
	s.Prune(ctx)
	tick := time.NewTicker(TickInterval)
	reload := time.NewTicker(ReloadInterval)
	prune := time.NewTicker(PruneInterval)
	defer func() {
		tick.Stop()
		reload.Stop()
		prune.Stop()
		s.wg.Wait()
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.dispatchDue(ctx)
		case <-reload.C:
			s.reload(ctx)
		case <-s.wake:
			s.reload(ctx)
			s.dispatchDue(ctx)
		case <-prune.C:
			s.Prune(ctx)
		}
	}
}

// reload 重新读取全部连接与配置：新连接加入调度，删除的、关闭检查的移出。
func (s *Service) reload(ctx context.Context) {
	list, err := s.conns.ListAll(ctx)
	if err != nil {
		s.log.Warn("健康检查读取连接列表失败", "err", err)
		return
	}
	var rows []configRow
	if err := s.st.Select(ctx, &rows, `SELECT * FROM health_configs`); err != nil {
		s.log.Warn("健康检查读取配置失败", "err", err)
		return
	}
	cfgs := map[string]Config{}
	for _, r := range rows {
		cfgs[r.ConnectionID] = r.config()
	}
	type last struct {
		ConnectionID string `db:"connection_id"`
		LastCheckAt  int64  `db:"last_check_at"`
	}
	var lasts []last
	_ = s.st.Select(ctx, &lasts, `SELECT connection_id, last_check_at FROM health_state`)
	lastAt := map[string]int64{}
	for _, l := range lasts {
		lastAt[l.ConnectionID] = l.LastCheckAt
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	next := map[string]*entry{}
	for _, c := range list {
		cfg, ok := cfgs[c.ID]
		if !ok {
			cfg = DefaultConfig()
		}
		if !cfg.Enabled {
			continue
		}
		iv := time.Duration(cfg.IntervalSec) * time.Second
		e := s.sched[c.ID]
		if e == nil {
			// 新加入调度：接着上次检查的节奏；从没检查过或已经过期的，在一小段随机时间内分散开（避免启动时一齐检查）
			spread := min(iv, 10*time.Second)
			due := now.Add(time.Duration(rand.Int64N(int64(spread) + 1)))
			if at := lastAt[c.ID]; at > 0 {
				if d := time.UnixMilli(at).Add(iv); d.After(due) {
					due = d
				}
			}
			e = &entry{due: due}
		} else if limit := now.Add(iv); e.due.After(limit) {
			e.due = limit
		}
		e.wsID, e.interval = c.WorkspaceID, iv
		next[c.ID] = e
	}
	s.sched = next
}

// dispatchDue 启动到期的检查（并发上限由信号量控制）。
func (s *Service) dispatchDue(ctx context.Context) {
	now := s.now()
	var due []string
	s.mu.Lock()
	for id, e := range s.sched {
		if !now.Before(e.due) && s.flights[id] == nil {
			due = append(due, id)
			e.due = now.Add(e.interval)
		}
	}
	s.mu.Unlock()
	for _, id := range due {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			_, _ = s.runCheck(ctx, id, false)
		}()
	}
}

// forget 连接已删除：移出调度。
func (s *Service) forget(connID string) {
	s.mu.Lock()
	delete(s.sched, connID)
	s.mu.Unlock()
}

// Prune 删除超过保留期的检查记录。
func (s *Service) Prune(ctx context.Context) {
	cut := s.now().Add(-Retention).UnixMilli()
	if _, err := s.st.Exec(ctx, `DELETE FROM health_checks WHERE at < ?`, cut); err != nil {
		s.log.Warn("清理健康检查记录失败", "err", err)
	}
}

// ---------- 检查 ----------

// CheckResult 是一次检查的结果（也是实时消息 health 的数据）。
type CheckResult struct {
	WorkspaceID  string `json:"workspace_id"`
	ConnectionID string `json:"connection_id"`
	At           int64  `json:"at"`
	OK           bool   `json:"ok"`
	LatencyMS    int64  `json:"latency_ms"`
	Error        string `json:"error"`
	Status       string `json:"status"`  // 本次检查之后的状态
	Prev         string `json:"prev"`    // 本次检查之前的状态
	Changed      bool   `json:"changed"` // 状态是否变化（下线 / 恢复）
	Fails        int    `json:"fails"`   // 连续失败次数
	Manual       bool   `json:"manual"`
}

// CheckNow 立即检查一个连接（同步）。同一连接已有检查在进行时等它的结果，不重复建连接。
func (s *Service) CheckNow(ctx context.Context, connID string) (*CheckResult, error) {
	res, err := s.runCheck(ctx, connID, true)
	if err == nil && res == nil {
		return nil, apperr.Bad("检查没有完成")
	}
	return res, err
}

func (s *Service) runCheck(ctx context.Context, connID string, manual bool) (*CheckResult, error) {
	s.mu.Lock()
	if f := s.flights[connID]; f != nil {
		s.mu.Unlock()
		if !manual {
			return nil, nil
		}
		select {
		case <-f.done:
			return f.res, f.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f := &flight{done: make(chan struct{})}
	s.flights[connID] = f
	bg := s.runCtx
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.flights, connID)
		s.mu.Unlock()
		close(f.done)
	}()
	if manual {
		// 「立即检查」不受浏览器断开影响（否则会被记成一次失败），但服务停止时随之取消
		if bg == nil {
			bg = context.Background()
		}
		mctx, cancel := context.WithCancel(bg)
		defer cancel()
		ctx = mctx
	}
	f.res, f.err = s.check(ctx, connID, manual)
	return f.res, f.err
}

func (s *Service) check(ctx context.Context, connID string, manual bool) (*CheckResult, error) {
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	c, err := s.conns.Get(ctx, connID)
	if err != nil {
		if ae, ok := apperr.As(err); ok && ae.Status == 404 {
			s.forget(connID)
		}
		return nil, err
	}
	cfg, _, err := s.Config(ctx, connID)
	if err != nil {
		return nil, err
	}
	if !cfg.Enabled && !manual {
		s.forget(connID)
		return nil, nil
	}
	timeout := time.Duration(cfg.TimeoutSec) * time.Second
	start := time.Now()
	cerr := s.runChecker(ctx, c, timeout)
	latency := time.Since(start)
	if ctx.Err() != nil { // 服务停止：不记这次
		return nil, ctx.Err()
	}
	errText := ""
	if cerr != nil {
		errText = s.redact(c, cerr.Error())
	}
	return s.record(ctx, c, cfg, cerr == nil, latency, errText, manual)
}

// runChecker 整次检查受超时限制：检查函数不理会 ctx 时也按时返回（它在后台自行结束）。
func (s *Service) runChecker(ctx context.Context, c *conns.Connection, timeout time.Duration) error {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		defer func() {
			if v := recover(); v != nil {
				done <- fmt.Errorf("检查出错：%v", v)
			}
		}()
		done <- s.Checker(cctx, c)
	}()
	timeoutErr := fmt.Errorf("检查超时（%d 秒内没有完成）", int(timeout.Seconds()))
	select {
	case err := <-done:
		if err != nil && cctx.Err() != nil && ctx.Err() == nil {
			return timeoutErr
		}
		return err
	case <-cctx.Done():
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return timeoutErr
	}
}

// redact 去掉错误文本里可能出现的口令、私钥口令（防御性：驱动一般不会把口令写进错误），并截断。
func (s *Service) redact(c *conns.Connection, text string) string {
	var secrets []string
	if d, ok := driver.Get(c.Driver); ok {
		if p, err := s.conns.DecryptParams(c); err == nil {
			for _, k := range d.Info().SecretKeys() {
				if v, _ := p[k].(string); v != "" {
					secrets = append(secrets, v)
				}
			}
		}
	}
	if nc, err := s.conns.DecryptNetwork(c); err == nil {
		secrets = append(secrets, nc.SSH.Password, nc.SSH.Passphrase, nc.Proxy.Password)
	}
	for _, v := range secrets {
		if len(v) >= 3 {
			text = strings.ReplaceAll(text, v, "******")
		}
	}
	if utf8.RuneCountInString(text) > 500 {
		text = string([]rune(text)[:500]) + "…"
	}
	return text
}

// record 写检查记录、推进状态机；状态变化时通知并推送。
//
// 状态机：unknown → up（一次成功）；unknown / up → down（连续失败达到阈值）；down → up（一次成功）。
// 只有变成 down（下线）与 down → up（恢复）时发通知。
func (s *Service) record(ctx context.Context, c *conns.Connection, cfg Config, ok bool, latency time.Duration, errText string, manual bool) (*CheckResult, error) {
	s.stateMu.Lock()
	st, err := s.state(ctx, c.ID)
	if err != nil {
		s.stateMu.Unlock()
		return nil, err
	}
	now := s.now().UnixMilli()
	prev := st.Status
	prevChanged := st.ChangedAt
	if ok {
		st.Fails = 0
		st.LastOKAt = now
		st.Status = StatusUp
	} else {
		st.Fails++
		if st.Fails >= cfg.FailThreshold {
			st.Status = StatusDown
		}
	}
	st.LastCheckAt, st.LastLatencyMS, st.LastError = now, latency.Milliseconds(), errText
	changed := st.Status != prev
	if changed {
		st.ChangedAt = now
	}
	err = s.st.Tx(ctx, func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO health_checks (connection_id, at, ok, latency_ms, error, status, manual) VALUES (?,?,?,?,?,?,?)`,
			c.ID, now, ok, st.LastLatencyMS, errText, st.Status, manual); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO health_state (connection_id, status, fails, last_check_at, last_ok_at, last_latency_ms, last_error, changed_at)
			VALUES (?,?,?,?,?,?,?,?) ON CONFLICT(connection_id) DO UPDATE SET status = excluded.status, fails = excluded.fails,
			last_check_at = excluded.last_check_at, last_ok_at = excluded.last_ok_at, last_latency_ms = excluded.last_latency_ms,
			last_error = excluded.last_error, changed_at = excluded.changed_at`,
			c.ID, st.Status, st.Fails, st.LastCheckAt, st.LastOKAt, st.LastLatencyMS, st.LastError, st.ChangedAt)
		return err
	})
	s.stateMu.Unlock()
	if err != nil {
		// 检查期间连接被删除时外键约束失败，属正常情况
		if _, gerr := s.conns.Get(ctx, c.ID); gerr != nil {
			s.forget(c.ID)
			return nil, gerr
		}
		return nil, err
	}
	res := &CheckResult{WorkspaceID: c.WorkspaceID, ConnectionID: c.ID, At: now, OK: ok, LatencyMS: st.LastLatencyMS, Error: errText,
		Status: st.Status, Prev: prev, Changed: changed, Fails: st.Fails, Manual: manual}
	s.broadcast(c, res)
	if changed {
		s.notifyChange(c, cfg, res, prevChanged)
	}
	return res, nil
}

func (s *Service) broadcast(c *conns.Connection, res *CheckResult) {
	if s.hub == nil {
		return
	}
	ws := c.WorkspaceID
	s.hub.Broadcast("health", res, func(userID string) bool {
		ctx := context.Background()
		if s.acc.RoleIn(ctx, ws, userID) == "" {
			return false
		}
		return s.conns.Access(ctx, &accounts.User{ID: userID}, c) != conns.LevelNone
	})
}

// notifyChange 状态变化时发通知：变成 down 发 conn_down；从 down 恢复发 conn_up（unknown → up 不算恢复）。
func (s *Service) notifyChange(c *conns.Connection, cfg Config, res *CheckResult, prevChangedAt int64) {
	if s.notify == nil || len(cfg.Channels) == 0 {
		return
	}
	link := "/w/" + c.WorkspaceID + "/health?connection=" + c.ID
	switch {
	case res.Status == StatusDown:
		msg := fmt.Sprintf("连接「%s」连续 %d 次健康检查失败，已判定为下线。", c.Name, res.Fails)
		if res.Error != "" {
			msg += "\n最近一次错误：" + res.Error
		}
		s.notify.Send(context.Background(), c.WorkspaceID, cfg.Channels, notify.Event{Kind: notify.EvConnDown, Level: notify.LevelError,
			Title: "连接「" + c.Name + "」已下线", Message: msg, Link: link})
	case res.Status == StatusUp && res.Prev == StatusDown:
		msg := fmt.Sprintf("连接「%s」健康检查恢复正常，延迟 %d 毫秒。", c.Name, res.LatencyMS)
		if prevChangedAt > 0 {
			msg += "下线持续了 " + humanDuration(time.Duration(res.At-prevChangedAt)*time.Millisecond) + "。"
		}
		s.notify.Send(context.Background(), c.WorkspaceID, cfg.Channels, notify.Event{Kind: notify.EvConnUp, Level: notify.LevelInfo,
			Title: "连接「" + c.Name + "」已恢复", Message: msg, Link: link})
	}
}

func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d 秒", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d 小时 %d 分钟", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%d 天 %d 小时", int(d.Hours())/24, int(d.Hours())%24)
}
