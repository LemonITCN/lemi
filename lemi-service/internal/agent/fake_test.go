package agent

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/aitest"
	"github.com/LemonITCN/lemi/lemi-service/internal/llm"
	"github.com/LemonITCN/lemi/lemi-service/internal/tools"
)

// ---------- 假供应商：按请求种类回放预先写好的事件流 ----------

type reply struct {
	events   []llm.Event
	err      error         // Stream 直接返回的错误（还没有任何输出）
	errAfter error         // 事件发完后 Recv 返回的错误（断流）
	wait     chan struct{} // 发事件前等待（模拟慢请求）
}

type fakeProv struct {
	mu     sync.Mutex
	model  llm.Model
	main   []reply
	sub    []reply
	comp   []reply
	reqs   []llm.Request // 主会话的请求
	subReq []llm.Request
	cmpReq []llm.Request
	t      *testing.T
}

func newFake(t *testing.T) *fakeProv {
	return &fakeProv{t: t, model: llm.Model{ID: "fake-1", Name: "假模型", Context: 200000, MaxOutput: 8000, API: "chat-completions", Type: "chat"}}
}

func (p *fakeProv) Models(ctx context.Context) ([]llm.Model, error) { return []llm.Model{p.model}, nil }

func (p *fakeProv) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	p.mu.Lock()
	var r reply
	switch {
	case req.System == compactSystem:
		p.cmpReq = append(p.cmpReq, req)
		if len(p.comp) == 0 {
			p.mu.Unlock()
			return nil, &llm.APIError{Kind: llm.ErrInvalidRequest, Message: "没有准备压缩回复"}
		}
		r, p.comp = p.comp[0], p.comp[1:]
	case req.System == titlePrompt:
		p.mu.Unlock()
		return &fakeStream{ctx: ctx, events: textReply("测试标题").events}, nil
	case strings.Contains(req.System, "派出的子智能体"):
		p.subReq = append(p.subReq, req)
		if len(p.sub) == 0 {
			p.mu.Unlock()
			return nil, &llm.APIError{Kind: llm.ErrInvalidRequest, Message: "没有准备子智能体回复"}
		}
		r, p.sub = p.sub[0], p.sub[1:]
	default:
		p.reqs = append(p.reqs, req)
		if len(p.main) == 0 {
			p.mu.Unlock()
			return nil, &llm.APIError{Kind: llm.ErrInvalidRequest, Message: "没有准备主会话回复"}
		}
		r, p.main = p.main[0], p.main[1:]
	}
	p.mu.Unlock()
	if r.wait != nil {
		select {
		case <-r.wait:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if r.err != nil {
		return nil, r.err
	}
	return &fakeStream{ctx: ctx, events: r.events, errAfter: r.errAfter}, nil
}

func (p *fakeProv) push(rs ...reply) {
	p.mu.Lock()
	p.main = append(p.main, rs...)
	p.mu.Unlock()
}

func (p *fakeProv) mainReqs() []llm.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]llm.Request(nil), p.reqs...)
}

type fakeStream struct {
	ctx      context.Context
	events   []llm.Event
	errAfter error
}

func (s *fakeStream) Recv() (llm.Event, error) {
	if err := s.ctx.Err(); err != nil {
		return llm.Event{}, err
	}
	if len(s.events) == 0 {
		if s.errAfter != nil {
			return llm.Event{}, s.errAfter
		}
		return llm.Event{}, io.EOF
	}
	e := s.events[0]
	s.events = s.events[1:]
	return e, nil
}

func (s *fakeStream) Close() error { return nil }

func textReply(text string) reply {
	return reply{events: []llm.Event{{Type: llm.EvText, Text: text}, {Type: llm.EvUsage, Usage: llm.Usage{Input: 1000, Output: 20}}, {Type: llm.EvDone, StopReason: "end_turn"}}}
}

type call struct {
	name string
	args any
}

func toolReply(calls ...call) reply {
	var evs []llm.Event
	for i, c := range calls {
		id := "call_" + c.name + "_" + string(rune('a'+i))
		var args string
		switch a := c.args.(type) {
		case string:
			args = a
		default:
			b, _ := json.Marshal(a)
			args = string(b)
		}
		evs = append(evs, llm.Event{Type: llm.EvToolCallStart, CallID: id, Name: c.name, Index: i},
			llm.Event{Type: llm.EvToolCallEnd, CallID: id, Name: c.name, Args: args, Index: i})
	}
	evs = append(evs, llm.Event{Type: llm.EvUsage, Usage: llm.Usage{Input: 1000, Output: 30}}, llm.Event{Type: llm.EvDone, StopReason: "tool_use"})
	return reply{events: evs}
}

// ---------- 假时钟：Sleep 只记录不等待 ----------

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	c.mu.Lock()
	c.sleeps = append(c.sleeps, d)
	c.now = c.now.Add(d)
	c.mu.Unlock()
	return ctx.Err()
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	go func() {
		time.Sleep(30 * time.Millisecond)
		ch <- c.Now()
	}()
	return ch
}

func (c *fakeClock) Sleeps() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.sleeps...)
}

// ---------- 环境 ----------

type env struct {
	f    *aitest.Fixture
	s    *Service
	p    *fakeProv
	clk  *fakeClock
	conv *Conversation
	t    *testing.T
}

func setup(t *testing.T) *env {
	t.Helper()
	f := aitest.New(t)
	p := newFake(t)
	s := New(f.Store, f.Settings, f.Accounts, f.Box, nil, f.Log, f.Tools)
	clk := &fakeClock{now: time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)}
	s.TestProvider, s.TitleGen, s.Clock, s.TZ = p, false, clk, time.UTC
	s.Retry.Jitter = func() float64 { return 1 }
	conv, err := s.CreateConversation(context.Background(), f.WS, f.Owner, ConvSettings{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &env{f: f, s: s, p: p, clk: clk, conv: conv, t: t}
}

func (e *env) send(text string) *Run {
	e.t.Helper()
	res, err := e.s.Send(context.Background(), e.f.Owner, e.conv.ID, "", SendInput{Text: text})
	if err != nil {
		e.t.Fatal(err)
	}
	run, ok := e.s.GetRun(e.f.Owner.ID, res.RunID)
	if !ok {
		e.t.Fatal("运行不存在")
	}
	return run
}

func (e *env) wait(run *Run) {
	e.t.Helper()
	select {
	case <-run.Done():
	case <-time.After(20 * time.Second):
		e.t.Fatal("运行没有在 20 秒内结束")
	}
}

func (e *env) pending(run *Run, kind string) map[string]any {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, pend := run.Resume()
		for _, p := range pend {
			if p["kind"] == kind {
				return p
			}
		}
		select {
		case <-run.Done():
			for _, m := range e.messages() {
				e.t.Logf("%s %s", m.Role, truncateRunes(string(mustJSON(m.Parts)), 300))
			}
			e.t.Fatalf("运行已结束（%s %s），没有等到 %s", run.State(), run.errMsg, kind)
		case <-time.After(10 * time.Millisecond):
		}
	}
	e.t.Fatalf("没有等到 %s", kind)
	return nil
}

func (e *env) respond(run *Run, id any, rep Reply) {
	e.t.Helper()
	if err := e.s.Respond(e.f.Owner.ID, run.ID, id.(string), rep); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) messages() []Message {
	e.t.Helper()
	ms, err := e.s.loadMessages(context.Background(), e.conv.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return ms
}

func (e *env) reload() *Conversation {
	c, err := e.s.getConv(context.Background(), e.conv.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

func events(run *Run, typ string) []Event {
	evs, _ := run.Since(0)
	var out []Event
	for _, ev := range evs {
		if ev.Type == typ {
			out = append(out, ev)
		}
	}
	return out
}

func reqText(req llm.Request) string {
	var b strings.Builder
	for _, m := range req.Messages {
		for _, p := range m.Parts {
			b.WriteString(string(m.Role) + ":" + p.Type + ":" + p.Name + ":" + p.Text + "\n")
		}
	}
	return b.String()
}

// 测试用的工具：阻塞直到被取消 / 无视取消 / 等测试放行。
var gate = make(chan struct{})

func init() {
	schema := map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
	tools.Default.Register(&tools.Tool{Name: "test_block", Title: "阻塞", Kind: tools.KindRead, Schema: schema,
		Handler: func(env *tools.Env, args json.RawMessage) (*tools.Result, error) {
			<-env.Ctx.Done()
			return nil, env.Ctx.Err()
		}})
	tools.Default.Register(&tools.Tool{Name: "test_stuck", Title: "卡住", Kind: tools.KindRead, Schema: schema,
		Handler: func(env *tools.Env, args json.RawMessage) (*tools.Result, error) {
			time.Sleep(3 * time.Second) // 不理会取消
			return &tools.Result{Text: "终于完成"}, nil
		}})
	tools.Default.Register(&tools.Tool{Name: "test_gate", Title: "等放行", Kind: tools.KindRead, Schema: schema,
		Handler: func(env *tools.Env, args json.RawMessage) (*tools.Result, error) {
			select {
			case <-gate:
			case <-env.Ctx.Done():
				return nil, env.Ctx.Err()
			}
			return &tools.Result{Text: "放行了"}, nil
		}})
}

var _ = accounts.WSMember
