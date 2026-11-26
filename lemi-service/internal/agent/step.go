package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/ids"
	"github.com/LemonITCN/lemi/lemi-service/internal/llm"
	"github.com/LemonITCN/lemi/lemi-service/internal/tools"
)

// stepper 执行一步：流式请求模型，工具调用流出即开始执行（只读工具并发最多 4 个，其余按顺序），带重试。
// 主会话与子智能体共用。
type stepper struct {
	s     *Service
	run   *Run
	conv  *Conversation
	env   *tools.Env
	tools []*tools.Tool
	emit  func(typ string, data any)
	sub   bool
	// history 工具调用指纹（死循环检测：同一工具同样参数连续 3 次）
	history []string
}

func (sp *stepper) recordCall(name string, args json.RawMessage) bool {
	key := name + "\x00" + canonicalJSON(args)
	sp.history = append(sp.history, key)
	if len(sp.history) > 50 {
		sp.history = sp.history[len(sp.history)-50:]
	}
	n := len(sp.history)
	return n >= 3 && sp.history[n-2] == key && sp.history[n-3] == key
}

type stepResult struct {
	assistant []StoredPart
	results   []StoredPart
	usage     llm.Usage
	hasUsage  bool
	stop      string
	err       *llm.APIError
	partial   bool // 已经有输出后断流：保存已有部分，开新的一步
	halt      bool // 用户拒绝且没有说明：结束本步
	calls     int
}

// streamStep 请求模型；没有输出之前的可重试错误按策略退避重试。
func (sp *stepper) streamStep(ctx context.Context, prov llm.Provider, req llm.Request) *stepResult {
	var st retryState
	for {
		res := sp.streamOnce(ctx, prov, req)
		if res.err == nil || res.partial {
			return res
		}
		if ctx.Err() != nil {
			res.err = &llm.APIError{Kind: llm.ErrCanceled, Message: "已停止"}
			return res
		}
		d, ok := sp.s.Retry.next(res.err, &st, sp.s.Clock.Now())
		if !ok {
			return res
		}
		sp.emit("step_reset", map[string]any{"reason": string(res.err.Kind)})
		sp.emit("retry", map[string]any{"attempt": st.attempts + st.netAttempt, "wait_ms": d.Milliseconds(), "kind": string(res.err.Kind),
			"label": res.err.Kind.Label(), "error": truncateRunes(res.err.Message, 300)})
		if err := sp.s.Clock.Sleep(ctx, d); err != nil {
			return &stepResult{err: &llm.APIError{Kind: llm.ErrCanceled, Message: "已停止"}}
		}
	}
}

func (sp *stepper) streamOnce(ctx context.Context, prov llm.Provider, req llm.Request) *stepResult {
	stream, err := prov.Stream(ctx, req)
	if err != nil {
		return &stepResult{err: llm.Classify(err)}
	}
	defer stream.Close()
	ex := &executor{sp: sp, ctx: ctx, sem: make(chan struct{}, 4)}
	var text, reasoning strings.Builder
	sig := ""
	var calls []StoredPart
	res := &stepResult{}
	msgID := ids.New()
	sp.emit("assistant_start", map[string]any{"id": msgID})
	assemble := func() []StoredPart {
		var parts []StoredPart
		// 思考内容可能被供应商隐去（只有签名）：签名也要原样回传
		if reasoning.Len() > 0 || sig != "" {
			parts = append(parts, StoredPart{Part: llm.Part{Type: "reasoning", Text: reasoning.String(), Signature: sig}})
		}
		if strings.TrimSpace(text.String()) != "" {
			parts = append(parts, StoredPart{Part: llm.Part{Type: "text", Text: text.String()}})
		}
		return append(parts, calls...)
	}
	for {
		ev, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			ae := llm.Classify(err)
			if ctx.Err() != nil {
				ae = &llm.APIError{Kind: llm.ErrCanceled, Message: "已停止"}
			}
			if strings.TrimSpace(text.String()) != "" || len(calls) > 0 {
				res.assistant = assemble()
				res.results, res.halt = ex.finish()
				res.partial, res.err, res.calls = true, ae, len(calls)
				sp.emit("assistant_done", map[string]any{"id": msgID, "partial": true})
				return res
			}
			res.err = ae
			return res
		}
		switch ev.Type {
		case llm.EvText:
			text.WriteString(ev.Text)
			sp.emit("text", map[string]any{"id": msgID, "text": ev.Text})
		case llm.EvReasoning:
			reasoning.WriteString(ev.Text)
			sp.emit("reasoning", map[string]any{"id": msgID, "text": ev.Text})
		case llm.EvReasoningSig:
			sig += ev.Text
		case llm.EvToolCallStart:
			sp.emit("tool_input_start", map[string]any{"call_id": ev.CallID, "name": ev.Name, "title": sp.title(ev.Name)})
		case llm.EvToolCallDelta:
			sp.emit("tool_input_delta", map[string]any{"call_id": ev.CallID, "text": ev.Text})
		case llm.EvToolCallEnd:
			part := sp.normalizeCall(ev)
			calls = append(calls, part)
			sp.emit("tool_call", map[string]any{"call_id": part.CallID, "name": part.Name, "title": part.Title, "args": part.Args})
			ex.add(part)
		case llm.EvUsage:
			res.usage, res.hasUsage = ev.Usage, true
		case llm.EvDone:
			res.stop = ev.StopReason
		}
	}
	res.assistant = assemble()
	res.results, res.halt = ex.finish()
	res.calls = len(calls)
	sp.emit("assistant_done", map[string]any{"id": msgID})
	return res
}

func (sp *stepper) title(name string) string {
	if t, ok := tools.Resolve(name, sp.tools); ok {
		return t.Title
	}
	return name
}

// normalizeCall 修正工具名（大小写、别名）；参数不是合法 JSON 时原样保留文本（执行时修复或报错），回放用 {}。
func (sp *stepper) normalizeCall(ev llm.Event) StoredPart {
	name := ev.Name
	title := name
	if t, ok := tools.Resolve(ev.Name, sp.tools); ok {
		name, title = t.Name, t.Title
	}
	args := json.RawMessage(ev.Args)
	if !json.Valid(args) {
		args = mustJSON(map[string]any{"_raw": ev.Args})
	}
	if ev.CallID == "" {
		ev.CallID = "call_" + ids.New()
	}
	return StoredPart{Part: llm.Part{Type: "tool_call", CallID: ev.CallID, Name: name, Args: args}, Title: title}
}

// ---------- 工具执行 ----------

type slot struct {
	call       StoredPart
	tool       *tools.Tool
	concurrent bool
	doom       bool
	mu         sync.Mutex
	res        *tools.Result
	status     string
	dur        int64
	done       chan struct{}
	started    bool
	final      bool
}

type executor struct {
	sp    *stepper
	ctx   context.Context
	sem   chan struct{}
	slots []*slot
	wg    sync.WaitGroup
}

func concurrentOK(t *tools.Tool) bool {
	if t == nil {
		return true
	}
	switch t.Kind {
	case tools.KindRead:
		return true
	case tools.KindAgent:
		return t.Name == "task" || t.Name == "todo_write"
	}
	return false
}

func (ex *executor) add(call StoredPart) {
	sl := &slot{call: call, done: make(chan struct{})}
	if t, ok := tools.Resolve(call.Name, ex.sp.tools); ok {
		sl.tool = t
	}
	sl.doom = ex.sp.recordCall(call.Name, call.Args)
	sl.concurrent = concurrentOK(sl.tool) && !sl.doom
	ex.slots = append(ex.slots, sl)
	if sl.concurrent {
		ex.start(sl)
	}
}

func (ex *executor) start(sl *slot) {
	sl.started = true
	ex.wg.Add(1)
	go func() {
		defer ex.wg.Done()
		if sl.concurrent {
			select {
			case ex.sem <- struct{}{}:
				defer func() { <-ex.sem }()
			case <-ex.ctx.Done():
			}
		}
		ex.exec(sl)
	}()
}

// exec 执行一个工具调用并记录结果。
func (ex *executor) exec(sl *slot) {
	defer close(sl.done)
	sp := ex.sp
	start := time.Now()
	var res *tools.Result
	status := StatusDone
	switch {
	case ex.ctx.Err() != nil:
		res, status = &tools.Result{Text: "已中断（用户停止），没有执行", IsError: true}, StatusInterrupted
	case sl.tool == nil:
		names := make([]string, 0, len(sp.tools))
		for _, t := range sp.tools {
			names = append(names, t.Name)
		}
		res, status = &tools.Result{Text: fmt.Sprintf("错误：没有名为 %q 的工具。可用的工具：%s。请用正确的工具名重新调用。", sl.call.Name, strings.Join(names, ", ")), IsError: true}, StatusError
	default:
		if sl.doom {
			if r := ex.doomCheck(sl); r != nil {
				res = r
				status = StatusDenied
				if !r.Rejected {
					status = StatusError
				}
				break
			}
		}
		sp.emit("tool_running", map[string]any{"call_id": sl.call.CallID, "name": sl.call.Name})
		args := sl.call.Args
		var raw map[string]any
		if json.Unmarshal(args, &raw) == nil && len(raw) == 1 {
			if s, ok := raw["_raw"].(string); ok {
				args = json.RawMessage(s) // 模型给的参数不是合法 JSON：交给参数修复处理原文
			}
		}
		res = tools.Default.Call(sp.env, sl.tool, args)
		switch {
		case res.Rejected:
			status = StatusDenied
		case ex.ctx.Err() != nil && res.IsError:
			status = StatusInterrupted
		case res.IsError:
			status = StatusError
		}
	}
	sl.mu.Lock()
	defer sl.mu.Unlock()
	if sl.final {
		return // 已被强制中断，结果丢弃
	}
	sl.final = true
	sl.res, sl.status, sl.dur = res, status, time.Since(start).Milliseconds()
	ex.report(sl)
}

// doomCheck 同一工具同样参数连续第 3 次：交互会话向用户确认，其他情况直接拒绝。返回 nil 表示放行。
func (ex *executor) doomCheck(sl *slot) *tools.Result {
	sp := ex.sp
	key := "doom|" + sl.call.Name
	if sp.run.allowed(sp.conv, key) {
		return nil
	}
	msg := fmt.Sprintf("检测到 %s 以完全相同的参数连续调用了 3 次，", sl.call.Name)
	if sp.sub || sp.run.Unattended || sp.env.Approval != tools.ApprovalInteractive {
		return &tools.Result{Text: "错误：" + msg + "已阻止。结果不会变化，请换个思路或调整参数。", IsError: true}
	}
	rep, err := sp.run.wait(ex.ctx, "doom_loop", key, map[string]any{"call_id": sl.call.CallID, "tool": sl.call.Name, "args": sl.call.Args,
		"message": msg + "可能陷入了循环。是否继续执行？"})
	if err != nil {
		return &tools.Result{Text: "已中断（用户停止）", IsError: true}
	}
	switch rep.Reply {
	case "once", "always":
		return nil
	}
	if strings.TrimSpace(rep.Message) != "" {
		return &tools.Result{Text: "用户拒绝了这次重复调用，用户的说明：" + rep.Message, IsError: true, Rejected: true}
	}
	return &tools.Result{Text: "用户拒绝了这次重复调用。", IsError: true, Rejected: true, Halt: true}
}

func (ex *executor) report(sl *slot) {
	sp := ex.sp
	var data json.RawMessage
	if sl.res.Data != nil {
		data, _ = json.Marshal(sl.res.Data)
	}
	if sl.call.Name == "report_write" && !sl.res.IsError {
		if m, ok := sl.res.Data.(map[string]any); ok {
			if id, ok := m["report_id"].(string); ok {
				sp.run.mu.Lock()
				dup := false
				for _, x := range sp.run.reports {
					if x == id {
						dup = true
					}
				}
				if !dup {
					sp.run.reports = append(sp.run.reports, id)
				}
				sp.run.mu.Unlock()
			}
		}
	}
	sp.emit("tool_result", map[string]any{"call_id": sl.call.CallID, "name": sl.call.Name, "status": sl.status, "text": truncateRunes(sl.res.Text, 4000),
		"data": data, "duration": sl.dur, "truncated_result_id": sl.res.Truncated})
}

// wait 等一个调用结束；运行被停止后最多再等 ForceStopAfter，超时强制记为「已中断」。
func (ex *executor) wait(sl *slot) {
	select {
	case <-sl.done:
		return
	case <-ex.ctx.Done():
	}
	select {
	case <-sl.done:
	case <-ex.sp.s.Clock.After(ForceStopAfter):
		sl.mu.Lock()
		if !sl.final {
			sl.final = true
			sl.res = &tools.Result{Text: "已中断（用户停止，工具没有在 5 秒内结束，已强制中断）", IsError: true}
			sl.status = StatusInterrupted
			ex.report(sl)
		}
		sl.mu.Unlock()
	}
}

// finish 按顺序执行剩下的（写入、提问、浏览器端）调用，等全部结束，返回按调用顺序排列的结果。
func (ex *executor) finish() ([]StoredPart, bool) {
	halted := false
	for _, sl := range ex.slots {
		if sl.concurrent {
			continue
		}
		if halted {
			sl.final = true
			sl.res, sl.status = &tools.Result{Text: "没有执行：用户拒绝了前面的操作。", IsError: true}, StatusSkipped
			close(sl.done)
			ex.report(sl)
			continue
		}
		ex.start(sl)
		ex.wait(sl)
		sl.mu.Lock()
		if sl.res != nil && sl.res.Halt {
			halted = true
		}
		sl.mu.Unlock()
	}
	for _, sl := range ex.slots {
		if sl.concurrent {
			ex.wait(sl)
		}
	}
	var out []StoredPart
	for _, sl := range ex.slots {
		sl.mu.Lock()
		res, status, dur := sl.res, sl.status, sl.dur
		sl.mu.Unlock()
		if res == nil {
			res, status = &tools.Result{Text: "已中断", IsError: true}, StatusInterrupted
		}
		if res.Halt {
			halted = true
		}
		var data json.RawMessage
		if res.Data != nil {
			data, _ = json.Marshal(res.Data)
		}
		text := res.Text
		if strings.TrimSpace(text) == "" {
			text = "（无输出）"
		}
		out = append(out, StoredPart{Part: llm.Part{Type: "tool_result", CallID: sl.call.CallID, Name: sl.call.Name, Text: text, IsError: res.IsError},
			Title: sl.call.Title, Data: data, Status: status, Duration: dur})
	}
	return out, halted
}
