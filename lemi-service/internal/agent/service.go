package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/crypt"
	"github.com/LemonITCN/lemi/lemi-service/internal/ids"
	"github.com/LemonITCN/lemi/lemi-service/internal/llm"
	"github.com/LemonITCN/lemi/lemi-service/internal/realtime"
	"github.com/LemonITCN/lemi/lemi-service/internal/settings"
	"github.com/LemonITCN/lemi/lemi-service/internal/store"
	"github.com/LemonITCN/lemi/lemi-service/internal/tools"
)

// Service 是智能体服务。
type Service struct {
	st    *store.Store
	set   *settings.Service
	acc   *accounts.Service
	box   *crypt.Box
	hub   *realtime.Hub
	log   *slog.Logger
	Tools *tools.Services

	Clock Clock
	Retry RetryPolicy
	TZ    *time.Location
	// TestProvider 测试用：不读供应商配置，直接用它（模型引用为 test/<模型 ID>）
	TestProvider llm.Provider
	// TitleGen 首条消息后后台生成标题
	TitleGen bool
	// MaxSteps 覆盖系统设置里的步数上限（测试用，0 = 用系统设置）
	MaxSteps int

	mu     sync.Mutex
	runs   map[string]*Run
	byConv map[string]*Run
	convMu sync.Mutex
	provMu sync.Mutex
	// provCache 供应商实例缓存
	provCache map[string]*cachedProvider
	wg        sync.WaitGroup
	closed    bool
}

// New 创建智能体服务。
func New(st *store.Store, set *settings.Service, acc *accounts.Service, box *crypt.Box, hub *realtime.Hub, log *slog.Logger, ts *tools.Services) *Service {
	return &Service{st: st, set: set, acc: acc, box: box, hub: hub, log: log, Tools: ts, Clock: realClock{}, Retry: DefaultRetry, TZ: time.Local, TitleGen: true,
		runs: map[string]*Run{}, byConv: map[string]*Run{}, provCache: map[string]*cachedProvider{}}
}

// SendInput 是用户发来的一条消息。
type SendInput struct {
	Text     string         `json:"text"`
	Delivery string         `json:"delivery"` // steer（默认：运行中在下一个步骤边界插入）| queue（排队到本轮结束后）
	Model    string         `json:"model"`
	Effort   string         `json:"effort"`
	Mode     string         `json:"mode"`
	Context  *ClientContext `json:"context"`
}

// SendResult 是发送结果。
type SendResult struct {
	RunID   string   `json:"run_id"`
	Message *Message `json:"message,omitempty"`
	Steered bool     `json:"steered,omitempty"`
	Queued  bool     `json:"queued,omitempty"`
}

// CreateConversation 新建会话。
func (s *Service) CreateConversation(ctx context.Context, wsID string, u *accounts.User, cfg ConvSettings) (*Conversation, error) {
	if cfg.Mode != ModePlan {
		cfg.Mode = ModeNormal
	}
	c := &Conversation{WorkspaceID: wsID, UserID: u.ID, Kind: KindChat, Cfg: cfg}
	if err := s.createConv(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

// applyCfg 应用发送时附带的设置。
func applyCfg(c *Conversation, in SendInput) bool {
	changed := false
	if in.Model != "" && in.Model != c.Cfg.Model {
		c.Cfg.Model, changed = in.Model, true
	}
	if in.Effort != "" && in.Effort != c.Cfg.Effort {
		c.Cfg.Effort, changed = in.Effort, true
	}
	if (in.Mode == ModePlan || in.Mode == ModeNormal) && in.Mode != c.Cfg.Mode {
		c.Cfg.Mode, changed = in.Mode, true
	}
	return changed
}

// Send 发送用户消息：会话空闲时开始一轮执行；正在执行时按 delivery 插话或排队。
func (s *Service) Send(ctx context.Context, u *accounts.User, convID, connID string, in SendInput) (*SendResult, error) {
	in.Text = strings.TrimSpace(in.Text)
	if in.Text == "" {
		return nil, errors.New("消息不能为空")
	}
	if len(in.Text) > 200<<10 {
		return nil, errors.New("消息太长")
	}
	conv, err := s.userConv(ctx, u.ID, convID)
	if err != nil {
		return nil, err
	}
	if conv.Kind != KindChat {
		return nil, errors.New("这个会话不能继续对话")
	}
	if _, err := s.acc.Require(ctx, conv.WorkspaceID, u, accounts.WSMember); err != nil {
		return nil, errors.New("需要工作空间「成员」及以上的角色才能使用智能体")
	}
	if applyCfg(conv, in) {
		if err := s.saveMeta(ctx, conv); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	if r := s.byConv[conv.ID]; r != nil {
		st := r.State()
		if st == RunRunning || st == RunWaiting {
			s.mu.Unlock()
			r.mu.Lock()
			if in.Delivery == "queue" {
				r.queued = append(r.queued, in)
			} else {
				r.steer = append(r.steer, in)
			}
			r.mu.Unlock()
			if in.Delivery == "queue" {
				r.emit("queued", map[string]any{"text": in.Text})
				return &SendResult{RunID: r.ID, Queued: true}, nil
			}
			r.emit("steer_queued", map[string]any{"text": in.Text})
			return &SendResult{RunID: r.ID, Steered: true}, nil
		}
	}
	s.mu.Unlock()
	run, msg, err := s.begin(ctx, conv, u, connID, []SendInput{in})
	if err != nil {
		return nil, err
	}
	return &SendResult{RunID: run.ID, Message: msg}, nil
}

// begin 写入用户消息并开始一轮执行。
func (s *Service) begin(ctx context.Context, conv *Conversation, u *accounts.User, connID string, inputs []SendInput) (*Run, *Message, error) {
	env := s.baseEnv(ctx, conv, u, nil, false)
	first := conv.System == ""
	if first {
		var cc *ClientContext
		if len(inputs) > 0 {
			cc = inputs[0].Context
		}
		facts := s.currentFacts(env, conv, cc, false)
		conv.System = s.buildSystem(ctx, env, conv, u, facts)
		conv.St.Env = facts
	}
	var msg *Message
	for i, in := range inputs {
		parts := []StoredPart{{Part: llm.Part{Type: "text", Text: in.Text}}}
		if !(first && i == 0) {
			parts = s.userParts(env, conv, in)
		}
		m, err := s.appendMessage(ctx, conv.ID, "user", parts, nil)
		if err != nil {
			return nil, nil, err
		}
		if msg == nil {
			msg = m
		}
	}
	if conv.Title == "" && len(inputs) > 0 {
		conv.Title = autoTitle(inputs[0].Text)
		_ = s.saveMeta(ctx, conv)
		if s.TitleGen {
			go s.genTitle(conv.ID, conv.WorkspaceID, conv.Cfg.Model, conv.Title, inputs[0].Text, u.ID)
		}
	}
	if err := s.saveState(ctx, conv); err != nil {
		return nil, nil, err
	}
	run, err := s.startRun(conv, u, connID, false, nil)
	if err != nil {
		return nil, nil, err
	}
	run.emit("user_message", msg)
	go s.execute(run, u)
	return run, msg, nil
}

// startRun 登记一轮运行。
func (s *Service) startRun(conv *Conversation, u *accounts.User, connID string, unattended bool, grant tools.Grant) (*Run, error) {
	return s.startRunCtx(context.Background(), conv, u, connID, unattended, grant)
}

func (s *Service) startRunCtx(parent context.Context, conv *Conversation, u *accounts.User, connID string, unattended bool, grant tools.Grant) (*Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("服务正在停止")
	}
	if r := s.byConv[conv.ID]; r != nil && (r.State() == RunRunning || r.State() == RunWaiting) {
		return nil, errors.New("这个会话正在执行")
	}
	rctx, cancel := context.WithCancel(parent)
	run := &Run{ID: ids.New(), ConvID: conv.ID, UserID: u.ID, WorkspaceID: conv.WorkspaceID, ConnID: connID, Unattended: unattended, grant: grant,
		s: s, ctx: rctx, cancel: cancel, done: make(chan struct{}), subs: map[chan struct{}]struct{}{}, state: RunRunning,
		pending: map[string]*interaction{}, started: time.Now()}
	if _, err := s.st.Exec(context.Background(), `INSERT INTO agent_runs (id, conversation_id, user_id, status, started_at) VALUES (?,?,?,?,?)`,
		run.ID, conv.ID, u.ID, RunRunning, store.Now()); err != nil {
		cancel()
		return nil, err
	}
	s.runs[run.ID] = run
	s.byConv[conv.ID] = run
	s.wg.Add(1)
	run.emit("run_start", map[string]any{"run_id": run.ID, "conversation_id": conv.ID})
	return run, nil
}

// finishRun 收尾：错误写进会话、没来得及插入的插话入库、记录运行结果、推送结束事件、处理排队消息。
func (s *Service) finishRun(run *Run, u *accounts.User, status, errMsg string) {
	defer s.wg.Done()
	ctx := context.Background()
	if status == RunError && errMsg != "" {
		_, _ = s.appendMessage(ctx, run.ConvID, "assistant", []StoredPart{{Part: llm.Part{Type: "text"}, Status: StatusError, Data: mustJSON(map[string]any{"error": errMsg})}},
			map[string]any{"error": true})
	}
	conv, _ := s.getConv(ctx, run.ConvID)
	if left := run.takeSteer(); len(left) > 0 && conv != nil {
		if status == RunDone {
			run.mu.Lock()
			run.queued = append(left, run.queued...)
			run.mu.Unlock()
		} else {
			for _, in := range left {
				_, _ = s.appendMessage(ctx, run.ConvID, "user", []StoredPart{{Part: llm.Part{Type: "text", Text: in.Text}}}, map[string]any{"not_run": true})
			}
		}
	}
	_, _ = s.st.Exec(ctx, `UPDATE agent_runs SET status = ?, error = ?, finished_at = ? WHERE id = ?`, status, errMsg, store.Now(), run.ID)
	run.mu.Lock()
	queued := run.queued
	run.queued = nil
	run.finished = time.Now()
	run.mu.Unlock()
	var next *Run
	if len(queued) > 0 && conv != nil {
		s.mu.Lock()
		if s.byConv[run.ConvID] == run {
			delete(s.byConv, run.ConvID)
		}
		s.mu.Unlock()
		run.setState(status, errMsg)
		if status == RunDone || status == RunStopped {
			if r, _, err := s.begin(ctx, conv, u, run.ConnID, queued); err == nil {
				next = r
			}
		}
	}
	if next != nil {
		run.emit("next_run", map[string]any{"run_id": next.ID})
	}
	run.setState(status, errMsg)
	run.emit("run_done", map[string]any{"status": status, "error": errMsg})
	run.cancel()
	close(run.done)
	s.mu.Lock()
	if s.byConv[run.ConvID] == run {
		delete(s.byConv, run.ConvID)
	}
	s.mu.Unlock()
	s.gc()
}

// GetRun 取运行（只能取自己的）。
func (s *Service) GetRun(userID, runID string) (*Run, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[runID]
	if !ok || r.UserID != userID {
		return nil, false
	}
	return r, true
}

// ActiveRun 会话正在执行的运行。
func (s *Service) ActiveRun(convID string) *Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byConv[convID]
}

// Stop 停止运行：先让工具协作取消（取消数据库查询），5 秒后强制中断。
func (s *Service) Stop(userID, runID string) error {
	r, ok := s.GetRun(userID, runID)
	if !ok {
		return errors.New("运行不存在")
	}
	r.cancel()
	return nil
}

// Respond 回应审批、提问或死循环确认。
func (s *Service) Respond(userID, runID, interactionID string, rep Reply) error {
	r, ok := s.GetRun(userID, runID)
	if !ok {
		return errors.New("运行不存在或已结束")
	}
	switch rep.Reply {
	case "once", "always", "reject", "":
	default:
		return errors.New("reply 应为 once、always 或 reject")
	}
	return r.respond(interactionID, rep)
}

// gc 清理结束超过 30 分钟的运行。
func (s *Service) gc() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, r := range s.runs {
		r.mu.Lock()
		old := !r.finished.IsZero() && time.Since(r.finished) > 30*time.Minute
		r.mu.Unlock()
		if old {
			delete(s.runs, id)
		}
	}
}

// Close 停止全部运行并等待收尾。
func (s *Service) Close() error {
	s.mu.Lock()
	s.closed = true
	for _, r := range s.runs {
		r.cancel()
	}
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
	}
	return nil
}

// Recover 进程启动时：上次残留的运行标为失败，没执行完的工具调用补上「丢失」结果（回放时连同调用一起丢掉），
// 中断的压缩标为失败。
func (s *Service) Recover(ctx context.Context) {
	var rows []runRow
	if err := s.st.Select(ctx, &rows, `SELECT * FROM agent_runs WHERE status IN ('running','waiting')`); err != nil {
		return
	}
	for _, r := range rows {
		_, _ = s.st.Exec(ctx, `UPDATE agent_runs SET status = ?, error = ?, finished_at = ? WHERE id = ?`, RunError, "服务重启，运行中断", store.Now(), r.ID)
		msgs, err := s.loadMessages(ctx, r.ConversationID)
		if err != nil {
			continue
		}
		have := map[string]bool{}
		for _, m := range msgs {
			if m.Role == "tool" {
				for _, p := range m.Parts {
					have[p.CallID] = true
				}
			}
		}
		var lost []StoredPart
		for _, m := range msgs {
			if m.Role != "assistant" {
				continue
			}
			for _, p := range m.Parts {
				if p.Type == "tool_call" && !have[p.CallID] {
					lost = append(lost, StoredPart{Part: llm.Part{Type: "tool_result", CallID: p.CallID, Name: p.Name, Text: "服务重启，这个工具没有执行完", IsError: true}, Status: StatusLost, Title: p.Title})
				}
			}
		}
		if len(lost) > 0 {
			_, _ = s.appendMessage(ctx, r.ConversationID, "tool", lost, map[string]any{"recovered": true})
		}
		_, _ = s.appendMessage(ctx, r.ConversationID, "assistant", []StoredPart{{Part: llm.Part{Type: "text"}, Status: StatusError, Data: mustJSON(map[string]any{"error": "服务重启，上一轮运行被中断"})}},
			map[string]any{"error": true})
		if conv, err := s.getConv(ctx, r.ConversationID); err == nil && conv.St.Compacting {
			conv.St.Compacting = false
			_ = s.saveState(ctx, conv)
		}
	}
	// 没有运行记录但标着正在压缩的会话
	var convs []convRow
	_ = s.st.Select(ctx, &convs, `SELECT * FROM agent_conversations WHERE state LIKE '%"compacting":true%'`)
	for i := range convs {
		c := decodeConv(&convs[i])
		c.St.Compacting = false
		_ = s.saveState(ctx, c)
	}
	if len(rows) > 0 {
		s.log.Info("已把上次残留的智能体运行标为失败", "count", len(rows))
	}
}

// genTitle 后台生成标题（3–8 字）。
func (s *Service) genTitle(convID, wsID, model, placeholder, text, userID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	prov, mdl, _, err := s.resolveModel(ctx, wsID, model)
	if err != nil {
		return
	}
	out, _, err := s.collectText(ctx, prov, llm.Request{Model: mdl.ID, System: titlePrompt, MaxTokens: 200, SessionID: convID,
		Messages: []llm.Message{{Role: llm.RoleUser, Parts: []llm.Part{{Type: "text", Text: truncateRunes(text, 2000)}}}}})
	if err != nil {
		return
	}
	t := strings.TrimSpace(strings.Split(strings.TrimSpace(out), "\n")[0])
	t = strings.Trim(t, "\"'“”‘’「」《》#*。.！!？? ")
	if n := len([]rune(t)); n == 0 || n > 30 {
		return
	}
	conv, err := s.getConv(ctx, convID)
	if err != nil || conv.Title != placeholder {
		return // 用户已经改过标题
	}
	conv.Title = t
	if s.saveMeta(ctx, conv) != nil {
		return
	}
	if r := s.ActiveRun(convID); r != nil {
		r.emit("title", map[string]any{"title": t})
	} else if s.hub != nil {
		s.hub.SendToUser(userID, "run_event", map[string]any{"conversation_id": convID, "type": "title", "data": map[string]any{"title": t}})
	}
}

// ---------- 定时任务 ----------

// TaskRunInput 是一次无人值守运行的参数。
type TaskRunInput struct {
	UserID      string
	WorkspaceID string
	TaskID      string
	Title       string
	Prompt      string
	Model       string
	Effort      string
	Grant       tools.Grant // 连接档位（在任务里再收紧）
}

// TaskRunOutput 是运行结果。
type TaskRunOutput struct {
	ConversationID string
	RunID          string
	Status         string
	Error          string
	Summary        string
	Reports        []string
}

// RunTask 无人值守地运行一次（每次是独立对话），阻塞到结束。需要确认的操作一律拒绝，不能提问。
func (s *Service) RunTask(ctx context.Context, in TaskRunInput) (*TaskRunOutput, error) {
	u, err := s.acc.UserByID(ctx, in.UserID)
	if err != nil || u.Status != "active" {
		return nil, errors.New("任务的运行身份（用户）不存在或已停用")
	}
	if _, err := s.acc.Require(ctx, in.WorkspaceID, u, accounts.WSMember); err != nil {
		return nil, errors.New("任务的运行身份已经不是该工作空间的成员")
	}
	conv := &Conversation{WorkspaceID: in.WorkspaceID, UserID: u.ID, Kind: KindTask, TaskID: in.TaskID, Title: truncateRunes(in.Title, 60),
		Cfg: ConvSettings{Model: in.Model, Effort: in.Effort, Mode: ModeNormal}}
	if in.Grant == nil {
		in.Grant = tools.Grant{}
	}
	env := s.baseEnv(ctx, conv, u, in.Grant, true)
	facts := s.currentFacts(env, conv, nil, false)
	conv.System = s.buildSystem(ctx, env, conv, u, facts)
	conv.St.Env = facts
	if err := s.createConv(ctx, conv); err != nil {
		return nil, err
	}
	msg, err := s.appendMessage(ctx, conv.ID, "user", []StoredPart{{Part: llm.Part{Type: "text", Text: in.Prompt}}}, map[string]any{"task": in.TaskID})
	if err != nil {
		return nil, err
	}
	run, err := s.startRunCtx(ctx, conv, u, "", true, in.Grant)
	if err != nil {
		return nil, err
	}
	run.emit("user_message", msg)
	s.execute(run, u)
	<-run.done
	run.mu.Lock()
	defer run.mu.Unlock()
	return &TaskRunOutput{ConversationID: conv.ID, RunID: run.ID, Status: run.state, Error: run.errMsg, Summary: run.final, Reports: append([]string(nil), run.reports...)}, nil
}

// fmtErr 给用户看的错误。
func fmtErr(err error) string {
	if ae, ok := err.(*llm.APIError); ok {
		return ae.Kind.Label() + "：" + ae.Message
	}
	return fmt.Sprint(err)
}
