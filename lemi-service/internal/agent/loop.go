package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
	"github.com/LemonITCN/lemi/lemi-service/internal/llm"
	"github.com/LemonITCN/lemi/lemi-service/internal/realtime"
	"github.com/LemonITCN/lemi/lemi-service/internal/tools"
)

// overflowFactors 模型报上下文溢出时，按首个被拒请求大小逐级缩小的比例。
var overflowFactors = []float64{0.85, 0.7, 0.5, 0.35}

// baseEnv 构造工具环境（不含运行相关的审批与浏览器桥）。
func (s *Service) baseEnv(ctx context.Context, conv *Conversation, u *accounts.User, grant tools.Grant, unattended bool) *tools.Env {
	env := &tools.Env{Ctx: ctx, Svc: s.Tools, User: u, WorkspaceID: conv.WorkspaceID, Source: audit.SrcAgent, Plan: conv.Cfg.Mode == ModePlan,
		Approval: tools.ApprovalInteractive, ConvID: conv.ID}
	if unattended {
		env.Source, env.Approval, env.TaskID = audit.SrcTask, tools.ApprovalUnattended, conv.TaskID
	}
	if grant != nil {
		env.Grant = grant
	}
	return env
}

// toolEnv 一次运行的工具环境：审批、浏览器端工具、计划清单 / 子智能体 / 提问。
func (s *Service) toolEnv(run *Run, conv *Conversation, u *accounts.User) *tools.Env {
	env := s.baseEnv(run.ctx, conv, u, run.grant, run.Unattended)
	env.Approve = s.approver(run, conv)
	if !run.Unattended && s.hub != nil {
		env.Client = &clientBridge{hub: s.hub, userID: u.ID, connID: run.ConnID}
	}
	env.Agentic = &hooks{s: s, run: run, conv: conv, user: u, env: env}
	return env
}

func (s *Service) profile(run *Run) tools.Profile {
	if run.Unattended {
		return tools.ProfileTask
	}
	return tools.ProfileAgent
}

type clientBridge struct {
	hub    *realtime.Hub
	userID string
	connID string
}

func (b *clientBridge) Online() bool { return b.hub.Online(b.userID, "") }
func (b *clientBridge) Call(ctx context.Context, tool string, args any) (json.RawMessage, error) {
	return b.hub.CallClient(ctx, b.userID, b.connID, tool, args)
}

// approver 内置智能体的审批：已「总是允许」的直接放行，否则弹审批卡片等用户回应。
func (s *Service) approver(run *Run, conv *Conversation) func(ctx context.Context, req *tools.ApprovalRequest) error {
	return func(ctx context.Context, req *tools.ApprovalRequest) error {
		if run.allowed(conv, req.Key) {
			return nil
		}
		var data map[string]any
		_ = json.Unmarshal(mustJSON(req), &data)
		rep, err := run.wait(ctx, "approval", req.Key, data)
		if err != nil {
			return err
		}
		switch rep.Reply {
		case "once", "always":
			return nil
		}
		if m := strings.TrimSpace(rep.Message); m != "" {
			return &tools.RejectedError{Message: m}
		}
		return tools.ErrRejected
	}
}

type hooks struct {
	s    *Service
	run  *Run
	conv *Conversation
	user *accounts.User
	env  *tools.Env
}

func (h *hooks) Todo(ctx context.Context, items []tools.Todo) error {
	cp := append([]tools.Todo(nil), items...)
	h.run.mu.Lock()
	h.run.todos = &cp
	h.run.mu.Unlock()
	h.run.emit("todos", map[string]any{"todos": cp})
	return nil
}

func (h *hooks) Task(ctx context.Context, in tools.TaskInput) (string, error) {
	return h.s.runSub(ctx, h.run, h.conv, h.user, h.env, in)
}

func (h *hooks) Ask(ctx context.Context, qs []tools.Question) (string, error) {
	if h.run.Unattended {
		return "", fmt.Errorf("定时任务无人值守，不能向用户提问，请自行判断")
	}
	rep, err := h.run.wait(ctx, "question", "", map[string]any{"questions": qs})
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for i, q := range qs {
		ans := "（没有回答）"
		if i < len(rep.Answers) {
			var one string
			var many []string
			switch {
			case json.Unmarshal(rep.Answers[i], &one) == nil:
				ans = one
			case json.Unmarshal(rep.Answers[i], &many) == nil:
				ans = strings.Join(many, "；")
			}
		}
		fmt.Fprintf(&b, "问：%s\n答：%s\n", q.Question, ans)
	}
	if strings.TrimSpace(rep.Message) != "" {
		b.WriteString("补充说明：" + rep.Message + "\n")
	}
	return b.String(), nil
}

func (s *Service) appendSynthetic(ctx context.Context, run *Run, conv *Conversation, kind, text string) {
	msg, err := s.appendMessage(context.WithoutCancel(ctx), conv.ID, "user", []StoredPart{{Part: llm.Part{Type: "text", Text: text}, Hidden: true}}, map[string]any{"synthetic": kind})
	if err == nil {
		run.emit("message_saved", map[string]any{"id": msg.ID, "seq": msg.Seq, "role": "user", "synthetic": kind})
	}
}

// userParts 用户消息的片段：正文 + 环境变化（隐藏）。
func (s *Service) userParts(env *tools.Env, conv *Conversation, in SendInput) []StoredPart {
	parts := []StoredPart{{Part: llm.Part{Type: "text", Text: strings.TrimSpace(in.Text)}}}
	facts := s.currentFacts(env, conv, in.Context, true)
	if upd := systemUpdate(conv.St.Env, facts); upd != "" {
		parts = append([]StoredPart{{Part: llm.Part{Type: "text", Text: upd}, Hidden: true}}, parts...)
	}
	conv.St.Env = facts
	return parts
}

// flushSteer 在步骤边界插入运行中用户发来的消息。
func (s *Service) flushSteer(ctx context.Context, run *Run, conv *Conversation, env *tools.Env) {
	for _, in := range run.takeSteer() {
		msg, err := s.appendMessage(context.WithoutCancel(ctx), conv.ID, "user", s.userParts(env, conv, in), map[string]any{"steer": true})
		if err == nil {
			run.emit("user_message", msg)
		}
	}
}

// maybeUpdate 环境变化（换日、切换计划模式、连接变化）时插入 <system-update>。
func (s *Service) maybeUpdate(ctx context.Context, run *Run, conv *Conversation, env *tools.Env) {
	facts := s.currentFacts(env, conv, nil, true)
	if upd := systemUpdate(conv.St.Env, facts); upd != "" {
		conv.St.Env = facts
		s.appendSynthetic(ctx, run, conv, "system_update", upd)
		run.emit("system_update", map[string]any{"text": upd})
	}
}

func textOf(parts []StoredPart) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// execute 是一轮执行的主循环。
func (s *Service) execute(run *Run, user *accounts.User) {
	ctx := run.ctx
	status, errMsg := RunDone, ""
	defer func() {
		if v := recover(); v != nil {
			s.log.Error("智能体执行崩溃", "panic", v)
			status, errMsg = RunError, "内部错误"
		}
		if ctx.Err() != nil && status == RunDone {
			status = RunStopped
		}
		s.finishRun(run, user, status, errMsg)
	}()
	conv, err := s.getConv(ctx, run.ConvID)
	if err != nil {
		status, errMsg = RunError, err.Error()
		return
	}
	prov, mdl, ref, err := s.resolveModel(ctx, conv.WorkspaceID, conv.Cfg.Model)
	if err != nil {
		status, errMsg = RunError, err.Error()
		return
	}
	if conv.Cfg.Model != ref {
		conv.Cfg.Model = ref
		_ = s.saveMeta(context.WithoutCancel(ctx), conv)
	}
	env := s.toolEnv(run, conv, user)
	sp := &stepper{s: s, run: run, conv: conv, env: env, emit: run.emit}
	maxSteps := s.set.System(ctx).AgentMaxSteps
	if s.MaxSteps > 0 {
		maxSteps = s.MaxSteps
	}
	window := mdl.Context
	if window <= 0 {
		window = 128000
	}
	limit := contextLimit(window)
	contCount := 0
	for step := 1; ; step++ {
		if ctx.Err() != nil {
			return
		}
		// 步骤边界：用户在运行中改了模式 / 标题时同步；插话与环境变化插入
		if fresh, err := s.getConv(ctx, conv.ID); err == nil {
			conv.Cfg.Mode, conv.Title = fresh.Cfg.Mode, fresh.Title
			env.Plan = conv.Cfg.Mode == ModePlan
		}
		s.flushSteer(ctx, run, conv, env)
		s.maybeUpdate(ctx, run, conv, env)
		sp.tools = tools.Default.List(env, s.profile(run))
		defs := tools.Defs(sp.tools)
		msgs, err := s.loadMessages(ctx, conv.ID)
		if err != nil {
			status, errMsg = RunError, err.Error()
			return
		}
		fixed := estimateTokens(conv.System) + estimateRequest(llm.Request{Tools: defs})
		if est := estimateContext(conv, msgs, fixed); est > limit {
			if err := s.compact(ctx, run, conv, prov, mdl, msgs, limit-fixed, "auto"); err != nil {
				if ctx.Err() != nil {
					return
				}
				run.emit("notice", map[string]any{"level": "warn", "text": "自动压缩上下文失败：" + err.Error()})
			}
			if msgs, err = s.loadMessages(ctx, conv.ID); err != nil {
				status, errMsg = RunError, err.Error()
				return
			}
		}
		last := step >= maxSteps
		if last {
			s.appendSynthetic(ctx, run, conv, "final_step", finalStepPrompt)
			msgs, _ = s.loadMessages(ctx, conv.ID)
		}
		req := llm.Request{Model: mdl.ID, System: conv.System, Messages: toLLM(conv, msgs), Tools: defs, Effort: conv.Cfg.Effort, SessionID: conv.ID}
		if last {
			req.ToolChoice = "none"
		}
		run.emit("step_start", map[string]any{"step": step, "last": last})
		res := sp.streamStep(ctx, prov, req)
		s.setSteps(run, step)
		if res.hasUsage {
			conv.Usage.Input += res.usage.Input
			conv.Usage.Output += res.usage.Output
			conv.Usage.CacheRead += res.usage.CacheRead
			conv.Usage.CacheWrite += res.usage.CacheWrite
			conv.Usage.Reasoning += res.usage.Reasoning
		}
		conv.Usage.Requests++
		if res.err != nil && !res.partial {
			switch res.err.Kind {
			case llm.ErrCanceled:
				return
			case llm.ErrContextOverflow:
				lvl := conv.St.OverflowLevel
				if lvl >= len(overflowFactors) {
					status, errMsg = RunError, "上下文超出模型窗口，压缩后仍然放不下。请新开一个对话，或换上下文更大的模型"
					return
				}
				target := int(float64(estimateRequest(req))*overflowFactors[lvl]) - fixed
				conv.St.OverflowLevel++
				run.emit("notice", map[string]any{"level": "warn", "text": fmt.Sprintf("模型报上下文溢出，压缩到原来的 %.0f%% 后重试", overflowFactors[lvl]*100)})
				if err := s.compact(ctx, run, conv, prov, mdl, msgs, target, "overflow"); err != nil {
					if ctx.Err() != nil {
						return
					}
					status, errMsg = RunError, "上下文超出模型窗口，压缩失败："+err.Error()
					return
				}
				continue
			default:
				status, errMsg = RunError, res.err.Kind.Label()+"："+res.err.Message
				return
			}
		}
		pctx := context.WithoutCancel(ctx)
		var am *Message
		if len(res.assistant) > 0 {
			meta := map[string]any{"model": ref, "usage": res.usage, "stop": res.stop, "step": step}
			if res.partial {
				meta["partial"] = true
			}
			if am, err = s.appendMessage(pctx, conv.ID, "assistant", res.assistant, meta); err == nil {
				run.emit("message_saved", map[string]any{"id": am.ID, "seq": am.Seq, "role": "assistant"})
			}
		}
		if len(res.results) > 0 {
			if tm, err := s.appendMessage(pctx, conv.ID, "tool", res.results, nil); err == nil {
				run.emit("message_saved", map[string]any{"id": tm.ID, "seq": tm.Seq, "role": "tool"})
			}
		}
		if am != nil {
			if res.hasUsage && res.usage.Context() > 0 {
				conv.St.LastContext = res.usage.Context() + res.usage.Output
			} else {
				conv.St.LastContext = estimateRequest(req) + estimateStored([]Message{*am})
			}
			conv.St.LastContextSeq = am.Seq
		}
		if !res.partial {
			conv.St.OverflowLevel = 0
		}
		run.mergeInto(conv)
		run.emit("usage", map[string]any{"context": conv.St.LastContext, "limit": limit, "window": window, "total": conv.Usage})
		_ = s.saveState(pctx, conv)
		if t := textOf(res.assistant); strings.TrimSpace(t) != "" {
			run.mu.Lock()
			run.final = t
			run.mu.Unlock()
		}
		if res.partial {
			if ctx.Err() != nil {
				return
			}
			if res.calls == 0 {
				s.appendSynthetic(ctx, run, conv, "resume", resumePrompt)
			}
			run.emit("notice", map[string]any{"level": "info", "text": "响应流中途断开，已保存输出到中断处，继续执行"})
			continue
		}
		if res.halt {
			run.emit("notice", map[string]any{"level": "info", "text": "用户拒绝了操作，本轮已停止"})
			return
		}
		if ctx.Err() != nil {
			return
		}
		if res.calls > 0 {
			contCount = 0
			if last {
				return
			}
			continue
		}
		if res.stop == "max_tokens" {
			if contCount < 2 && !last {
				contCount++
				s.appendSynthetic(ctx, run, conv, "continue", continuePrompt)
				continue
			}
			run.emit("notice", map[string]any{"level": "warn", "text": "回复达到长度上限被截断"})
		}
		if last {
			run.emit("notice", map[string]any{"level": "info", "text": fmt.Sprintf("已达到单轮最多 %d 步的上限，回复「继续」可以接着做", maxSteps)})
			return
		}
		if run.hasSteer() {
			continue
		}
		return
	}
}

func (s *Service) setSteps(run *Run, n int) {
	_, _ = s.st.Exec(context.Background(), `UPDATE agent_runs SET steps = ? WHERE id = ?`, n, run.ID)
}
