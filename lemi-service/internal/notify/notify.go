// Package notify 是通知模块（docs/spec.md §15）：工作空间的通知渠道（邮件、Webhook、钉钉、企业微信、飞书），
// 实例级「系统邮件服务器」，以及登录锁定、新设备登录等安全事件的邮件提醒。
//
// 发送一律异步：失败按 RetryDelays 重试，每次发送（含重试次数、最终结果、错误）写进 notify_logs，保留 30 天。
// 出站地址一律经 netsafe.Egress 检查（允许内网与否实时读系统设置）；只有实例管理员配置的系统邮件服务器允许内网
// （仍拒绝云元数据地址），因为只有实例管理员能改它。
package notify

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
	"github.com/LemonITCN/lemi/lemi-service/internal/crypt"
	"github.com/LemonITCN/lemi/lemi-service/internal/ids"
	"github.com/LemonITCN/lemi/lemi-service/internal/netsafe"
	"github.com/LemonITCN/lemi/lemi-service/internal/settings"
	"github.com/LemonITCN/lemi/lemi-service/internal/store"
)

// 事件。
const (
	EvBackupSuccess = "backup_success"
	EvBackupFailed  = "backup_failed"
	EvRestoreDone   = "restore_done"
	EvRestoreFailed = "restore_failed"
	EvVerifyFailed  = "verify_failed"
	EvConnDown      = "conn_down"
	EvConnUp        = "conn_up"
	EvTaskResult    = "task_result"
	EvSecurity      = "security"
	EvTest          = "test"
)

// 级别。
const (
	LevelInfo    = "info"
	LevelWarning = "warning"
	LevelError   = "error"
)

// Event 是一条要发出的通知。
type Event struct {
	Kind    string // 上面的事件
	Level   string // info | warning | error
	Title   string
	Message string
	Link    string // 站内相对路径（如 /w/<空间>/backups）或绝对地址；相对路径用 PublicURL 补全，PublicURL 为空时不带链接
}

// 可在测试里改短。
var (
	// RetryDelays 发送失败后的重试间隔（重试 len(RetryDelays) 次）。
	RetryDelays = []time.Duration{2 * time.Second, 10 * time.Second, 30 * time.Second}
	// Retention 发送记录保留时长。
	Retention = 30 * 24 * time.Hour
	// SendTimeout 单次发送的超时。
	SendTimeout = 30 * time.Second
)

const (
	maxTitleRunes   = 200
	maxMessageRunes = 4000
	maxChannelsPer  = 20 // 一个计划 / 一个连接最多关联的渠道数
	maxConcurrent   = 16 // 同时进行中的发送
)

// Deps 是通知服务的依赖。
type Deps struct {
	Store     *store.Store
	Accounts  *accounts.Service
	Settings  *settings.Service
	Box       *crypt.Box
	Audit     *audit.Log
	Log       *slog.Logger
	PublicURL string // 对外地址，用于补全相对链接
}

// Service 通知服务。
type Service struct {
	st        *store.Store
	acc       *accounts.Service
	set       *settings.Service
	box       *crypt.Box
	audit     *audit.Log
	log       *slog.Logger
	publicURL string

	// Egress 工作空间渠道的出站检查（允许内网 = 系统设置 AllowPrivateEgress，每次连接实时读取）。
	Egress netsafe.Egress
	// SystemEgress 系统邮件服务器的出站检查：允许内网，仍拒绝云元数据地址。
	SystemEgress netsafe.Egress

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	sem    chan struct{}
	now    func() time.Time

	clientMu sync.Mutex
	client   *http.Client
}

// New 创建通知服务，并把上次退出时还在「发送中」的记录标为失败。
func New(d Deps) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	log := d.Log
	if log == nil {
		log = slog.Default()
	}
	s := &Service{st: d.Store, acc: d.Accounts, set: d.Settings, box: d.Box, audit: d.Audit, log: log,
		publicURL: strings.TrimRight(strings.TrimSpace(d.PublicURL), "/"),
		ctx:       ctx, cancel: cancel, sem: make(chan struct{}, maxConcurrent), now: time.Now}
	s.Egress = netsafe.Egress{AllowPrivate: func() bool { return s.set.System(context.Background()).AllowPrivateEgress }}
	s.SystemEgress = netsafe.Egress{AllowPrivate: func() bool { return true }}
	_, err := s.st.Exec(context.Background(), `UPDATE notify_logs SET status = 'failed', error = ?, finished_at = ? WHERE status = 'sending'`,
		"服务重启，发送中断", store.Now())
	if err != nil {
		s.log.Warn("整理未完成的通知记录失败", "err", err)
	}
	return s
}

// Close 停止全部发送（进行中的重试放弃并记为失败），最多等 10 秒。
func (s *Service) Close() error {
	s.cancel()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
	}
	return nil
}

// wait 等待全部异步发送结束（测试用）。
func (s *Service) wait() { s.wg.Wait() }

// Run 后台清理过期的发送记录，直到 ctx 结束。
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		s.Prune(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Prune 删除超过保留期的发送记录。
func (s *Service) Prune(ctx context.Context) {
	cut := s.now().Add(-Retention).UnixMilli()
	if _, err := s.st.Exec(ctx, `DELETE FROM notify_logs WHERE created_at < ?`, cut); err != nil {
		s.log.Warn("清理通知记录失败", "err", err)
	}
}

// httpClient 带出站检查的 HTTP 客户端：不跟随重定向（避免把令牌与自定义头带去别的地址），不复用连接
// （「允许内网」设置改了立即生效）。
func (s *Service) httpClient() *http.Client {
	s.clientMu.Lock()
	defer s.clientMu.Unlock()
	if s.client == nil {
		tr := s.Egress.Transport()
		tr.DisableKeepAlives = true
		tr.ResponseHeaderTimeout = SendTimeout
		s.client = &http.Client{Transport: tr, Timeout: SendTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return s.client
}

// ---------- 对外发送 ----------

// Send 异步发给空间里指定的渠道：只发属于 wsID、已启用的渠道，未知 ID 忽略；失败重试并记录。
func (s *Service) Send(ctx context.Context, wsID string, channelIDs []string, ev Event) {
	chIDs := dedupe(channelIDs)
	if wsID == "" || len(chIDs) == 0 || s.ctx.Err() != nil {
		return
	}
	ev = s.normalizeEvent(ev)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		list, err := s.channelsByIDs(s.ctx, wsID, chIDs)
		if err != nil {
			s.log.Error("读取通知渠道失败", "workspace", wsID, "err", err)
			return
		}
		for i := range list {
			ch := &list[i]
			if !ch.Enabled || !ch.Wants(ev.Kind) {
				continue
			}
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				s.deliver(ch, ev)
			}()
		}
	}()
}

// CheckChannels 校验渠道都存在且属于该空间（保存备份计划、健康检查配置时用）。
func (s *Service) CheckChannels(ctx context.Context, wsID string, channelIDs []string) error {
	chIDs := dedupe(channelIDs)
	if len(chIDs) == 0 {
		return nil
	}
	if len(chIDs) > maxChannelsPer {
		return apperr.Bad("最多选择 %d 个通知渠道", maxChannelsPer)
	}
	list, err := s.channelsByIDs(ctx, wsID, chIDs)
	if err != nil {
		return err
	}
	if len(list) != len(chIDs) {
		return apperr.Bad("通知渠道不存在或不属于该工作空间")
	}
	return nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range in {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func (s *Service) normalizeEvent(ev Event) Event {
	switch ev.Level {
	case LevelInfo, LevelWarning, LevelError:
	default:
		ev.Level = LevelInfo
	}
	if ev.Kind == "" {
		ev.Kind = EvTest
	}
	ev.Title = truncRunes(oneLine(ev.Title), maxTitleRunes)
	if ev.Title == "" {
		ev.Title = eventName(ev.Kind)
	}
	ev.Message = truncRunes(strings.ReplaceAll(ev.Message, "\r\n", "\n"), maxMessageRunes)
	ev.Link = s.resolveLink(ev.Link)
	return ev
}

// resolveLink 相对路径用 PublicURL 补全（PublicURL 为空时不带链接）；绝对地址只接受 http/https。
func (s *Service) resolveLink(link string) string {
	link = strings.TrimSpace(link)
	if link == "" || strings.ContainsAny(link, "\r\n") {
		return ""
	}
	if strings.HasPrefix(link, "/") && !strings.HasPrefix(link, "//") {
		if s.publicURL == "" {
			return ""
		}
		return s.publicURL + link
	}
	u, err := url.Parse(link)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return u.String()
}

// deliver 发到一个渠道：失败按 RetryDelays 重试，过程与结果写进发送记录。
func (s *Service) deliver(ch *Channel, ev Event) {
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-s.ctx.Done():
		return
	}
	logID := s.logStart(ch.WorkspaceID, ch.ID, ch.Name, ch.Kind, ev)
	attempts := 0
	var err error
	for i := 0; ; i++ {
		attempts++
		err = s.sendOnce(s.ctx, ch, ev)
		if err == nil || isPermanent(err) || i >= len(RetryDelays) {
			break
		}
		s.logProgress(logID, attempts, err)
		select {
		case <-time.After(RetryDelays[i]):
		case <-s.ctx.Done():
			err = errors.New("服务停止，放弃重试：" + err.Error())
		}
		if s.ctx.Err() != nil {
			break
		}
	}
	s.logFinish(logID, attempts, err)
	if err != nil {
		s.log.Warn("通知发送失败", "channel", ch.ID, "kind", ch.Kind, "event", ev.Kind, "attempts", attempts, "err", err)
	}
}

// sendOnce 发一次（单次超时 SendTimeout）。
func (s *Service) sendOnce(ctx context.Context, ch *Channel, ev Event) error {
	ctx, cancel := context.WithTimeout(ctx, SendTimeout)
	defer cancel()
	cfg, err := s.decryptConfig(ch)
	if err != nil {
		return permanent(errors.New("渠道密钥无法解密（主密钥不匹配或数据被篡改），请重新填写"))
	}
	return s.dispatch(ctx, ch.Kind, cfg, ev)
}

// permanentErr 是重试也不会成功的错误（配置错误、地址被拦截）。
type permanentErr struct{ err error }

func (e *permanentErr) Error() string { return e.err.Error() }
func (e *permanentErr) Unwrap() error { return e.err }

func permanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentErr{err: err}
}

func isPermanent(err error) bool {
	var p *permanentErr
	return errors.As(err, &p) || errors.Is(err, netsafe.ErrBlocked)
}

// ---------- 发送记录 ----------

// LogEntry 是一条发送记录。
type LogEntry struct {
	ID          string `db:"id" json:"id"`
	WorkspaceID string `db:"workspace_id" json:"workspace_id"`
	ChannelID   string `db:"channel_id" json:"channel_id"`
	ChannelName string `db:"channel_name" json:"channel_name"`
	Kind        string `db:"kind" json:"kind"`
	Event       string `db:"event" json:"event"`
	Level       string `db:"level" json:"level"`
	Title       string `db:"title" json:"title"`
	Message     string `db:"message" json:"message"`
	Status      string `db:"status" json:"status"`
	Attempts    int    `db:"attempts" json:"attempts"`
	Error       string `db:"error" json:"error"`
	CreatedAt   int64  `db:"created_at" json:"created_at"`
	FinishedAt  int64  `db:"finished_at" json:"finished_at"`
}

func (s *Service) logStart(wsID, channelID, channelName, kind string, ev Event) string {
	id := ids.New()
	var ws any
	if wsID != "" {
		ws = wsID
	}
	_, err := s.st.Exec(context.Background(), `INSERT INTO notify_logs (id, workspace_id, channel_id, channel_name, kind, event, level, title, message, status, attempts, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,'sending',0,?)`, id, ws, channelID, channelName, kind, ev.Kind, ev.Level, ev.Title, truncRunes(ev.Message, 2000), store.Now())
	if err != nil {
		s.log.Warn("写通知记录失败", "err", err)
	}
	return id
}

func (s *Service) logProgress(id string, attempts int, err error) {
	_, _ = s.st.Exec(context.Background(), `UPDATE notify_logs SET attempts = ?, error = ? WHERE id = ?`, attempts, errText(err), id)
}

func (s *Service) logFinish(id string, attempts int, err error) {
	status := "ok"
	if err != nil {
		status = "failed"
	}
	_, e := s.st.Exec(context.Background(), `UPDATE notify_logs SET status = ?, attempts = ?, error = ?, finished_at = ? WHERE id = ?`,
		status, attempts, errText(err), store.Now(), id)
	if e != nil {
		s.log.Warn("写通知记录失败", "err", e)
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return truncRunes(err.Error(), 1000)
}

// Logs 空间的发送记录（按时间倒序）。cursor 为上一页最后一条的 ID；channelID 非空时只看该渠道。
func (s *Service) Logs(ctx context.Context, wsID, channelID, cursor string, limit int) ([]LogEntry, string, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := `SELECT id, IFNULL(workspace_id, '') AS workspace_id, channel_id, channel_name, kind, event, level, title, message, status, attempts, error, created_at, finished_at
		FROM notify_logs WHERE workspace_id = ?`
	args := []any{wsID}
	if channelID != "" {
		q += ` AND channel_id = ?`
		args = append(args, channelID)
	}
	if cursor != "" {
		q += ` AND id < ?`
		args = append(args, cursor)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	out := []LogEntry{}
	if err := s.st.Select(ctx, &out, q, args...); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) == limit {
		next = out[len(out)-1].ID
	}
	return out, next, nil
}

// ---------- 文本小工具 ----------

func truncRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

// truncBytes 按 UTF-8 字节截断（不切断字符）。
func truncBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	const ell = "…"
	cut := n - len(ell)
	if cut < 0 {
		cut = 0
	}
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + ell
}

func oneLine(s string) string {
	s = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ", "\t", " ").Replace(s)
	return strings.TrimSpace(s)
}
