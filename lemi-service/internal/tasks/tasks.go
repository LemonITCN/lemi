// Package tasks 是定时智能体任务（docs/spec.md §16.8）：按计划无人值守地运行智能体，每次运行是一段独立对话；
// 连接档位在任务里再收紧（默认只读），需要确认的操作一律拒绝；运行结果（摘要、报告、失败原因）经通知渠道发出。
package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/agent"
	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
	"github.com/LemonITCN/lemi/lemi-service/internal/conns"
	"github.com/LemonITCN/lemi/lemi-service/internal/ids"
	"github.com/LemonITCN/lemi/lemi-service/internal/sched"
	"github.com/LemonITCN/lemi/lemi-service/internal/settings"
	"github.com/LemonITCN/lemi/lemi-service/internal/store"
	"github.com/LemonITCN/lemi/lemi-service/internal/tools"
)

// TaskNotification 是一次运行结束后的通知内容。
type TaskNotification struct {
	WorkspaceID string   `json:"workspace_id"`
	ChannelIDs  []string `json:"channel_ids"`
	TaskID      string   `json:"task_id"`
	TaskName    string   `json:"task_name"`
	RunID       string   `json:"run_id"`
	Status      string   `json:"status"` // done | error | stopped
	Level       string   `json:"level"`  // info | error
	Title       string   `json:"title"`
	Message     string   `json:"message"`
	Summary     string   `json:"summary"`
	Error       string   `json:"error,omitempty"`
	ReportIDs   []string `json:"report_ids,omitempty"`
	Link        string   `json:"link,omitempty"`
	StartedAt   int64    `json:"started_at"`
	FinishedAt  int64    `json:"finished_at"`
}

// TaskNotifier 由通知模块实现：装配时 a.Get("notify") 实现了它就用，否则只记录日志。
type TaskNotifier interface {
	NotifyTask(ctx context.Context, n TaskNotification) error
}

// Task 是一个定时任务。
type Task struct {
	ID          string `db:"id" json:"id"`
	WorkspaceID string `db:"workspace_id" json:"workspace_id"`
	OwnerID     string `db:"owner_id" json:"owner_id"`
	Name        string `db:"name" json:"name"`
	Schedule    string `db:"schedule" json:"-"`
	Prompt      string `db:"prompt" json:"prompt"`
	Model       string `db:"model" json:"model"`
	Effort      string `db:"effort" json:"effort"`
	Connections string `db:"connections" json:"-"`
	Channels    string `db:"channels" json:"-"`
	NotifyOn    string `db:"notify_on" json:"notify_on"`
	Enabled     bool   `db:"enabled" json:"enabled"`
	NextRunAt   int64  `db:"next_run_at" json:"next_run_at"`
	LastRunAt   int64  `db:"last_run_at" json:"last_run_at"`
	LastStatus  string `db:"last_status" json:"last_status"`
	CreatedBy   string `db:"created_by" json:"created_by"`
	CreatedAt   int64  `db:"created_at" json:"created_at"`
	UpdatedAt   int64  `db:"updated_at" json:"updated_at"`
}

// RunRecord 是一次运行。
type RunRecord struct {
	ID             string `db:"id" json:"id"`
	TaskID         string `db:"task_id" json:"task_id"`
	ConversationID string `db:"conversation_id" json:"conversation_id"`
	Trigger        string `db:"trigger" json:"trigger"`
	Status         string `db:"status" json:"status"`
	Summary        string `db:"summary" json:"summary"`
	ReportID       string `db:"report_id" json:"report_id"`
	Error          string `db:"error" json:"error"`
	StartedAt      int64  `db:"started_at" json:"started_at"`
	FinishedAt     int64  `db:"finished_at" json:"finished_at"`
}

// Service 定时任务服务。
type Service struct {
	st       *store.Store
	acc      *accounts.Service
	conns    *conns.Service
	set      *settings.Service
	agent    *agent.Service
	audit    *audit.Log
	log      *slog.Logger
	Notifier TaskNotifier
	// BaseURL 对外地址（通知里的链接），可为空
	BaseURL string
	// MaxConcurrent 同时运行的任务数
	MaxConcurrent int

	mu      sync.Mutex
	running map[string]context.CancelFunc
	wg      sync.WaitGroup
}

// New 创建服务。
func New(st *store.Store, acc *accounts.Service, cs *conns.Service, set *settings.Service, ag *agent.Service, au *audit.Log, log *slog.Logger) *Service {
	return &Service{st: st, acc: acc, conns: cs, set: set, agent: ag, audit: au, log: log, MaxConcurrent: 4, running: map[string]context.CancelFunc{}}
}

// Spec 解析计划。
func (t *Task) Spec() *sched.Spec {
	var sp sched.Spec
	_ = json.Unmarshal([]byte(t.Schedule), &sp)
	return &sp
}

// Levels 解析连接档位。
func (t *Task) Levels() tools.Grant {
	m := tools.Grant{}
	_ = json.Unmarshal([]byte(t.Connections), &m)
	return m
}

// ChannelIDs 解析通知渠道。
func (t *Task) ChannelIDs() []string {
	var out []string
	_ = json.Unmarshal([]byte(t.Channels), &out)
	return out
}

// View 给前端的任务描述。
func (t *Task) View() map[string]any {
	sp := t.Spec()
	return map[string]any{"id": t.ID, "workspace_id": t.WorkspaceID, "owner_id": t.OwnerID, "name": t.Name, "schedule": sp, "schedule_text": sp.Describe(),
		"prompt": t.Prompt, "model": t.Model, "effort": t.Effort, "connections": t.Levels(), "channels": t.ChannelIDs(), "notify_on": t.NotifyOn,
		"enabled": t.Enabled, "next_run_at": t.NextRunAt, "last_run_at": t.LastRunAt, "last_status": t.LastStatus, "created_by": t.CreatedBy,
		"created_at": t.CreatedAt, "updated_at": t.UpdatedAt}
}

// Input 是新建或修改任务的请求。
type Input struct {
	Name        string            `json:"name"`
	Schedule    sched.Spec        `json:"schedule"`
	Prompt      string            `json:"prompt"`
	Model       string            `json:"model"`
	Effort      string            `json:"effort"`
	Connections map[string]string `json:"connections"` // 连接 ID → 档位（空 = 只读）
	Channels    []string          `json:"channels"`
	NotifyOn    string            `json:"notify_on"` // always | failure | never
	Enabled     *bool             `json:"enabled"`
	OwnerID     string            `json:"owner_id"` // 以谁的身份运行（默认创建者）
}

func (s *Service) validate(ctx context.Context, wsID string, in *Input, owner *accounts.User) (string, string, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len([]rune(in.Name)) > 100 {
		return "", "", errors.New("任务名称不能为空，且不超过 100 个字符")
	}
	if strings.TrimSpace(in.Prompt) == "" || len([]rune(in.Prompt)) > 20000 {
		return "", "", errors.New("提示词不能为空，且不超过 20000 字")
	}
	if err := in.Schedule.Validate(); err != nil {
		return "", "", err
	}
	switch in.NotifyOn {
	case "":
		in.NotifyOn = "always"
	case "always", "failure", "never":
	default:
		return "", "", errors.New("notify_on 应为 always、failure 或 never")
	}
	if len(in.Channels) > 20 {
		return "", "", errors.New("通知渠道太多")
	}
	if _, err := s.acc.Require(ctx, wsID, owner, accounts.WSMember); err != nil {
		return "", "", errors.New("任务的运行身份必须是该工作空间的成员及以上")
	}
	allowFull := s.set.System(ctx).AllowAIFullAccess
	levels := tools.Grant{}
	for id, l := range in.Connections {
		if l == "" {
			l = conns.AIRead
		}
		if l == conns.AIHidden {
			continue
		}
		if !tools.ValidGrantLevel(l) {
			return "", "", errors.New("任务里的连接档位只能是 hidden、read、write_auto 或 full（无人值守，不能逐条确认）")
		}
		c, err := s.conns.Get(ctx, id)
		if err != nil || c.WorkspaceID != wsID {
			return "", "", fmt.Errorf("连接 %s 不存在", id)
		}
		eff := tools.EffectiveAI(c, s.conns.Access(ctx, owner, c), nil, false, allowFull)
		if max := tools.MaxGrantLevel(eff); conns.AIRank(l) > conns.AIRank(max) {
			return "", "", fmt.Errorf("连接「%s」在任务里最高只能设为 %s", c.Name, max)
		}
		levels[id] = l
	}
	if len(levels) == 0 {
		return "", "", errors.New("请至少给任务一个可用的连接")
	}
	lb, _ := json.Marshal(levels)
	ch := in.Channels
	if ch == nil {
		ch = []string{}
	}
	cb, _ := json.Marshal(ch)
	return string(lb), string(cb), nil
}

func nextRun(sp *sched.Spec, after time.Time) int64 {
	if sp.Kind == "manual" {
		return 0
	}
	t := sp.Next(after)
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// Create 新建任务。
func (s *Service) Create(ctx context.Context, wsID string, by *accounts.User, in Input) (*Task, error) {
	owner := by
	if in.OwnerID != "" && in.OwnerID != by.ID {
		u, err := s.acc.UserByID(ctx, in.OwnerID)
		if err != nil {
			return nil, errors.New("运行身份不存在")
		}
		owner = u
	}
	lv, ch, err := s.validate(ctx, wsID, &in, owner)
	if err != nil {
		return nil, err
	}
	sb, _ := json.Marshal(in.Schedule)
	now := store.Now()
	t := &Task{ID: ids.New(), WorkspaceID: wsID, OwnerID: owner.ID, Name: in.Name, Schedule: string(sb), Prompt: in.Prompt, Model: in.Model, Effort: in.Effort,
		Connections: lv, Channels: ch, NotifyOn: in.NotifyOn, Enabled: in.Enabled == nil || *in.Enabled, CreatedBy: by.ID, CreatedAt: now, UpdatedAt: now}
	if t.Enabled {
		t.NextRunAt = nextRun(&in.Schedule, time.Now())
	}
	if err := s.st.Add(ctx, "agent_tasks", t); err != nil {
		return nil, err
	}
	return t, nil
}

// Update 修改任务。
func (s *Service) Update(ctx context.Context, t *Task, by *accounts.User, in Input) error {
	ownerID := t.OwnerID
	if in.OwnerID != "" {
		ownerID = in.OwnerID
	}
	owner, err := s.acc.UserByID(ctx, ownerID)
	if err != nil {
		return errors.New("运行身份不存在")
	}
	lv, ch, err := s.validate(ctx, t.WorkspaceID, &in, owner)
	if err != nil {
		return err
	}
	sb, _ := json.Marshal(in.Schedule)
	t.OwnerID, t.Name, t.Schedule, t.Prompt, t.Model, t.Effort, t.Connections, t.Channels, t.NotifyOn = owner.ID, in.Name, string(sb), in.Prompt, in.Model, in.Effort, lv, ch, in.NotifyOn
	if in.Enabled != nil {
		t.Enabled = *in.Enabled
	}
	t.NextRunAt = 0
	if t.Enabled {
		t.NextRunAt = nextRun(&in.Schedule, time.Now())
	}
	t.UpdatedAt = store.Now()
	return s.st.Put(ctx, "agent_tasks", t)
}

// Get 取任务。
func (s *Service) Get(ctx context.Context, id string) (*Task, error) {
	var t Task
	if !ids.Valid(id) || s.st.GetW(ctx, &t, `SELECT * FROM agent_tasks WHERE id = ?`, id) != nil {
		return nil, errors.New("任务不存在")
	}
	return &t, nil
}

// List 空间里的任务。
func (s *Service) List(ctx context.Context, wsID string) ([]Task, error) {
	var out []Task
	err := s.st.Select(ctx, &out, `SELECT * FROM agent_tasks WHERE workspace_id = ? ORDER BY name`, wsID)
	return out, err
}

// Delete 删除任务（正在运行的先停止）。
func (s *Service) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	if c := s.running[id]; c != nil {
		c()
	}
	s.mu.Unlock()
	_, err := s.st.Exec(ctx, `DELETE FROM agent_tasks WHERE id = ?`, id)
	return err
}

// Runs 任务的运行记录。
func (s *Service) Runs(ctx context.Context, taskID string, limit int) ([]RunRecord, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var out []RunRecord
	err := s.st.Select(ctx, &out, `SELECT * FROM agent_task_runs WHERE task_id = ? ORDER BY started_at DESC LIMIT ?`, taskID, limit)
	return out, err
}

// Running 任务是否正在运行。
func (s *Service) Running(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running[id] != nil
}

// Start 立即运行一次（后台）。同一任务同时只跑一个。
func (s *Service) Start(ctx context.Context, t *Task, trigger string) (string, error) {
	s.mu.Lock()
	if s.running[t.ID] != nil {
		s.mu.Unlock()
		return "", errors.New("任务正在运行")
	}
	if len(s.running) >= s.MaxConcurrent {
		s.mu.Unlock()
		return "", errors.New("同时运行的任务已达上限，请稍后再试")
	}
	rctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.running[t.ID] = cancel
	s.mu.Unlock()
	rec := &RunRecord{ID: ids.New(), TaskID: t.ID, Trigger: trigger, Status: "running", StartedAt: store.Now()}
	if err := s.st.Add(ctx, "agent_task_runs", rec); err != nil {
		s.mu.Lock()
		delete(s.running, t.ID)
		s.mu.Unlock()
		cancel()
		return "", err
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			delete(s.running, t.ID)
			s.mu.Unlock()
			cancel()
		}()
		s.runOnce(rctx, t, rec)
	}()
	return rec.ID, nil
}

// Stop 停止正在运行的任务。
func (s *Service) Stop(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.running[id]; c != nil {
		c()
		return true
	}
	return false
}

func (s *Service) runOnce(ctx context.Context, t *Task, rec *RunRecord) {
	out, err := s.agent.RunTask(ctx, agent.TaskRunInput{UserID: t.OwnerID, WorkspaceID: t.WorkspaceID, TaskID: t.ID, Title: t.Name + " " + time.Now().Format("01-02 15:04"),
		Prompt: t.Prompt, Model: t.Model, Effort: t.Effort, Grant: t.Levels()})
	rec.FinishedAt = store.Now()
	if err != nil {
		rec.Status, rec.Error = "error", err.Error()
	} else {
		rec.ConversationID, rec.Status, rec.Error, rec.Summary = out.ConversationID, out.Status, out.Error, truncate(out.Summary, 8000)
		if len(out.Reports) > 0 {
			rec.ReportID = out.Reports[len(out.Reports)-1]
		}
	}
	bctx := context.Background()
	_ = s.st.Put(bctx, "agent_task_runs", rec)
	_, _ = s.st.Exec(bctx, `UPDATE agent_tasks SET last_run_at = ?, last_status = ? WHERE id = ?`, rec.StartedAt, rec.Status, t.ID)
	s.audit.Record(audit.Entry{UserID: t.OwnerID, WorkspaceID: t.WorkspaceID, Source: audit.SrcTask, Action: "task.run", Target: t.ID, OK: rec.Status == "done", Error: rec.Error,
		DurationMS: rec.FinishedAt - rec.StartedAt}, map[string]any{"run": rec.ID, "conversation": rec.ConversationID, "report": rec.ReportID})
	s.notify(bctx, t, rec, out)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func (s *Service) notify(ctx context.Context, t *Task, rec *RunRecord, out *agent.TaskRunOutput) {
	failed := rec.Status != "done"
	if t.NotifyOn == "never" || (t.NotifyOn == "failure" && !failed) {
		return
	}
	n := TaskNotification{WorkspaceID: t.WorkspaceID, ChannelIDs: t.ChannelIDs(), TaskID: t.ID, TaskName: t.Name, RunID: rec.ID, Status: rec.Status, Level: "info",
		Summary: truncate(rec.Summary, 2000), Error: rec.Error, StartedAt: rec.StartedAt, FinishedAt: rec.FinishedAt}
	if out != nil {
		n.ReportIDs = out.Reports
	}
	if failed {
		n.Level = "error"
		n.Title = "定时任务失败：" + t.Name
		n.Message = rec.Error
		if n.Message == "" {
			n.Message = "运行被停止"
		}
	} else {
		n.Title = "定时任务完成：" + t.Name
		n.Message = n.Summary
	}
	if s.BaseURL != "" {
		n.Link = strings.TrimRight(s.BaseURL, "/") + "/agent-tasks/" + t.ID + "?run=" + rec.ID
	}
	if s.Notifier == nil {
		s.log.Info("定时任务运行结束（未配置通知模块，只记录日志）", "task", t.Name, "status", rec.Status, "error", rec.Error)
		return
	}
	if len(n.ChannelIDs) == 0 {
		return
	}
	if err := s.Notifier.NotifyTask(ctx, n); err != nil {
		s.log.Warn("定时任务通知发送失败", "task", t.ID, "err", err)
	}
}

// Recover 启动时把上次残留的运行标为失败。
func (s *Service) Recover(ctx context.Context) {
	_, _ = s.st.Exec(ctx, `UPDATE agent_task_runs SET status = 'error', error = '服务重启，运行中断', finished_at = ? WHERE status = 'running'`, store.Now())
}

// Run 调度循环：每 30 秒检查到期的任务。
func (s *Service) Run(ctx context.Context) {
	s.Recover(ctx)
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		s.tick(ctx)
		select {
		case <-ctx.Done():
			s.mu.Lock()
			for _, c := range s.running {
				c()
			}
			s.mu.Unlock()
			s.wg.Wait()
			return
		case <-t.C:
		}
	}
}

func (s *Service) tick(ctx context.Context) {
	now := time.Now()
	var due []Task
	if err := s.st.Select(ctx, &due, `SELECT * FROM agent_tasks WHERE enabled = 1 AND next_run_at > 0 AND next_run_at <= ?`, now.UnixMilli()); err != nil {
		return
	}
	for i := range due {
		t := &due[i]
		// 先推进下一次时间，避免同一时刻重复触发
		_, _ = s.st.Exec(ctx, `UPDATE agent_tasks SET next_run_at = ? WHERE id = ?`, nextRun(t.Spec(), now), t.ID)
		if _, err := s.Start(ctx, t, "schedule"); err != nil {
			s.log.Warn("定时任务没有启动", "task", t.ID, "err", err)
		}
	}
}
