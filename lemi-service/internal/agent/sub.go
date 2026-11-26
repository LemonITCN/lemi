package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/llm"
	"github.com/LemonITCN/lemi/lemi-service/internal/tools"
)

// SubMaxSteps 子智能体的最大步数。
var SubMaxSteps = 50

// buildSubSystem 子智能体的系统提示：子智能体说明 + 父会话的环境块。
func (s *Service) buildSubSystem(ctx context.Context, env *tools.Env, conv *Conversation, u *accounts.User, typ string) string {
	head := subExplorePrompt
	if typ == "analyst" {
		head = subAnalystPrompt
	}
	full := s.buildSystem(ctx, env, conv, u, s.currentFacts(env, conv, nil, false))
	// 取父提示里「# 环境」开始的部分（环境、空间约定、个人指令）
	if i := strings.Index(full, "\n# 环境\n"); i >= 0 {
		full = full[i:]
	}
	return head + "\n\n# 工作方法\n- 先摸清结构再查询；能在数据库里聚合的就在 SQL 里聚合；查询先加 LIMIT 试探。\n- 互不依赖的工具调用放在同一轮里一起发出。\n- 只通过工具获取数据，不臆造。\n" + full
}

// runSub 运行子智能体：独立上下文（单独存为 kind=sub 的会话，可用 task_id 续接），只读，只把结论交回。
func (s *Service) runSub(ctx context.Context, run *Run, parent *Conversation, user *accounts.User, penv *tools.Env, in tools.TaskInput) (string, error) {
	typ := in.SubagentType
	if typ != "analyst" {
		typ = "explore"
	}
	subEnv := *penv
	subEnv.Plan, subEnv.Sub, subEnv.Agentic, subEnv.Approve, subEnv.Client = true, true, nil, nil, nil
	subEnv.Approval = tools.ApprovalUnattended
	var sc *Conversation
	if id := strings.TrimSpace(in.TaskID); id != "" {
		c, err := s.getConv(ctx, id)
		if err != nil || c.Kind != KindSub || c.ParentID != parent.ID {
			return "", fmt.Errorf("task_id %s 不存在（只能续接本会话派出的子智能体）", id)
		}
		sc = c
	} else {
		title := strings.TrimSpace(in.Description)
		if title == "" {
			title = "子任务"
		}
		sc = &Conversation{WorkspaceID: parent.WorkspaceID, UserID: parent.UserID, Kind: KindSub, ParentID: parent.ID, Title: truncateRunes(title, 60),
			Cfg: ConvSettings{Model: parent.Cfg.Model, Effort: parent.Cfg.Effort, Mode: ModePlan}}
		sc.System = s.buildSubSystem(ctx, &subEnv, sc, user, typ)
		if err := s.createConv(context.WithoutCancel(ctx), sc); err != nil {
			return "", err
		}
	}
	subEnv.ConvID = sc.ID
	if _, err := s.appendMessage(context.WithoutCancel(ctx), sc.ID, "user", []StoredPart{{Part: llm.Part{Type: "text", Text: in.Prompt}}}, nil); err != nil {
		return "", err
	}
	run.emit("subagent_start", map[string]any{"task_id": sc.ID, "description": sc.Title, "type": typ, "resumed": in.TaskID != ""})
	prov, mdl, _, err := s.resolveModel(ctx, parent.WorkspaceID, parent.Cfg.Model)
	if err != nil {
		run.emit("subagent_done", map[string]any{"task_id": sc.ID, "ok": false, "error": err.Error()})
		return "", err
	}
	emit := func(typ string, data any) {
		switch typ {
		case "tool_call", "tool_result", "retry", "notice":
			run.emit("subagent_event", map[string]any{"task_id": sc.ID, "type": typ, "data": data})
		}
	}
	sp := &stepper{s: s, run: run, conv: sc, env: &subEnv, emit: emit, sub: true}
	sp.tools = tools.Default.List(&subEnv, tools.ProfileSub)
	defs := tools.Defs(sp.tools)
	limit := contextLimit(mdl.Context)
	final := ""
	okDone := false
	for step := 1; step <= SubMaxSteps; step++ {
		if ctx.Err() != nil {
			break
		}
		msgs, err := s.loadMessages(ctx, sc.ID)
		if err != nil {
			return "", err
		}
		req := llm.Request{Model: mdl.ID, System: sc.System, Messages: toLLM(sc, msgs), Tools: defs, Effort: sc.Cfg.Effort, SessionID: sc.ID}
		last := step == SubMaxSteps || estimateRequest(req) > limit
		if last {
			note := "已经达到子任务的步数上限或上下文上限。请停止调用工具，立即根据已有信息给出最终结论（结论、数字与口径、结果 ID、未完成的部分）。"
			_, _ = s.appendMessage(context.WithoutCancel(ctx), sc.ID, "user", []StoredPart{{Part: llm.Part{Type: "text", Text: note}, Hidden: true}}, map[string]any{"synthetic": "final_step"})
			msgs, _ = s.loadMessages(ctx, sc.ID)
			req.Messages = toLLM(sc, msgs)
			req.ToolChoice = "none"
		}
		res := sp.streamStep(ctx, prov, req)
		if res.hasUsage {
			run.addSubUsage(res.usage)
			sc.Usage.Input += res.usage.Input
			sc.Usage.Output += res.usage.Output
			sc.Usage.CacheRead += res.usage.CacheRead
			sc.Usage.CacheWrite += res.usage.CacheWrite
		}
		sc.Usage.Requests++
		if res.err != nil && !res.partial {
			_ = s.saveState(context.WithoutCancel(ctx), sc)
			if res.err.Kind == llm.ErrCanceled || ctx.Err() != nil {
				run.emit("subagent_done", map[string]any{"task_id": sc.ID, "ok": false, "error": "已停止"})
				return "", context.Canceled
			}
			run.emit("subagent_done", map[string]any{"task_id": sc.ID, "ok": false, "error": res.err.Message})
			return "", errors.New("子智能体出错：" + res.err.Kind.Label() + "：" + res.err.Message)
		}
		pctx := context.WithoutCancel(ctx)
		if len(res.assistant) > 0 {
			_, _ = s.appendMessage(pctx, sc.ID, "assistant", res.assistant, map[string]any{"step": step})
		}
		if len(res.results) > 0 {
			_, _ = s.appendMessage(pctx, sc.ID, "tool", res.results, nil)
		}
		if res.partial {
			if res.calls == 0 {
				_, _ = s.appendMessage(pctx, sc.ID, "user", []StoredPart{{Part: llm.Part{Type: "text", Text: resumePrompt}, Hidden: true}}, map[string]any{"synthetic": "resume"})
			}
			continue
		}
		if t := strings.TrimSpace(textOf(res.assistant)); t != "" {
			final = t
		}
		if res.calls == 0 || last {
			okDone = res.calls == 0
			break
		}
	}
	_ = s.saveState(context.WithoutCancel(ctx), sc)
	if ctx.Err() != nil {
		run.emit("subagent_done", map[string]any{"task_id": sc.ID, "ok": false, "error": "已停止"})
		return "", context.Canceled
	}
	if final == "" {
		final = "（子智能体没有给出结论）"
	}
	run.emit("subagent_done", map[string]any{"task_id": sc.ID, "ok": okDone})
	return fmt.Sprintf("<task_result task_id=%q>\n%s\n</task_result>", sc.ID, final), nil
}
