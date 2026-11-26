package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/ids"
	"github.com/LemonITCN/lemi/lemi-service/internal/llm"
	"github.com/LemonITCN/lemi/lemi-service/internal/store"
	"github.com/LemonITCN/lemi/lemi-service/internal/tools"
)

// 运行状态。
const (
	RunRunning = "running"
	RunWaiting = "waiting" // 等待用户审批或回答
	RunDone    = "done"
	RunError   = "error"
	RunStopped = "stopped"
)

// ForceStopAfter 停止时先让工具协作取消，超过这个时间强制中断。
var ForceStopAfter = 5 * time.Second

// Run 是一轮执行（用户发一条消息到智能体停下来）。
type Run struct {
	ID          string
	ConvID      string
	UserID      string
	WorkspaceID string
	ConnID      string // 发起的浏览器连接（浏览器端工具优先发给它）
	Unattended  bool   // 定时任务

	s      *Service
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	mu        sync.Mutex
	events    []Event
	subs      map[chan struct{}]struct{}
	state     string
	errMsg    string
	pending   map[string]*interaction
	steer     []SendInput
	queued    []SendInput
	stepStart int
	allow     []string // 本轮新增的「总是允许」
	grant     map[string]string
	todos     *[]tools.Todo
	subUsage  llm.Usage
	subReqs   int
	reports   []string
	final     string
	started   time.Time
	finished  time.Time
}

type interaction struct {
	kind string
	key  string
	data map[string]any
	ch   chan Reply
}

func (r *Run) emit(typ string, data any) {
	r.mu.Lock()
	ev := Event{Seq: len(r.events) + 1, Type: typ, Data: data, At: store.Now()}
	r.events = append(r.events, ev)
	if typ == "assistant_start" {
		r.stepStart = ev.Seq
	}
	for ch := range r.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	r.mu.Unlock()
	if r.s != nil && r.s.hub != nil {
		r.s.hub.SendToUser(r.UserID, "run_event", map[string]any{"run_id": r.ID, "conversation_id": r.ConvID, "seq": ev.Seq, "type": typ, "data": data, "at": ev.At})
	}
}

// Since 返回序号之后的事件与当前状态。
func (r *Run) Since(seq int) ([]Event, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if seq < 0 {
		seq = 0
	}
	if seq > len(r.events) {
		seq = len(r.events)
	}
	return append([]Event(nil), r.events[seq:]...), r.state
}

// Subscribe 订阅新事件通知。
func (r *Run) Subscribe() (chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	r.mu.Lock()
	r.subs[ch] = struct{}{}
	r.mu.Unlock()
	return ch, func() {
		r.mu.Lock()
		delete(r.subs, ch)
		r.mu.Unlock()
	}
}

// State 返回状态。
func (r *Run) State() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state
}

func (r *Run) setState(st, msg string) {
	r.mu.Lock()
	r.state, r.errMsg = st, msg
	r.mu.Unlock()
}

// Done 运行结束时关闭。
func (r *Run) Done() <-chan struct{} { return r.done }

// Resume 断线续传：从哪个序号之后重放（当前这一步的开头）、待处理的交互。
func (r *Run) Resume() (int, []map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	from := len(r.events)
	if r.stepStart > 0 {
		from = r.stepStart - 1
	}
	var pend []map[string]any
	for _, it := range r.pending {
		pend = append(pend, it.data)
	}
	return from, pend
}

// wait 发出审批 / 提问并等待用户回应。
func (r *Run) wait(ctx context.Context, kind, key string, data map[string]any) (Reply, error) {
	id := ids.New()
	it := &interaction{kind: kind, key: key, data: data, ch: make(chan Reply, 1)}
	data["id"] = id
	data["kind"] = kind
	r.mu.Lock()
	r.pending[id] = it
	r.state = RunWaiting
	r.mu.Unlock()
	r.emit(kind, data)
	defer func() {
		r.mu.Lock()
		delete(r.pending, id)
		if r.state == RunWaiting && len(r.pending) == 0 {
			r.state = RunRunning
		}
		r.mu.Unlock()
	}()
	select {
	case rep := <-it.ch:
		r.emit("interaction_done", map[string]any{"id": id, "reply": rep.Reply})
		return rep, nil
	case <-ctx.Done():
		return Reply{}, ctx.Err()
	}
}

// respond 回应一个交互；「总是允许」时自动放行其他已满足同一规则的挂起审批。
func (r *Run) respond(id string, rep Reply) error {
	r.mu.Lock()
	it := r.pending[id]
	if it == nil {
		r.mu.Unlock()
		return errors.New("这个请求已经处理过了")
	}
	delete(r.pending, id)
	var also []*interaction
	if rep.Reply == "always" && it.key != "" {
		r.allow = append(r.allow, it.key)
		for oid, o := range r.pending {
			if o.key == it.key {
				also = append(also, o)
				delete(r.pending, oid)
			}
		}
	}
	r.mu.Unlock()
	it.ch <- rep
	for _, o := range also {
		o.ch <- Reply{Reply: "once"}
	}
	return nil
}

func (r *Run) allowed(conv *Conversation, key string) bool {
	if key == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, k := range r.allow {
		if k == key {
			return true
		}
	}
	for _, k := range conv.St.AllowRules {
		if k == key {
			return true
		}
	}
	return false
}

// takeAllow 把本轮新增的允许规则并入会话状态（只在主循环里调用）。
func (r *Run) mergeInto(conv *Conversation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, k := range r.allow {
		dup := false
		for _, x := range conv.St.AllowRules {
			if x == k {
				dup = true
			}
		}
		if !dup {
			conv.St.AllowRules = append(conv.St.AllowRules, k)
		}
	}
	if r.todos != nil {
		conv.St.Todos = *r.todos
		r.todos = nil
	}
	if r.subReqs > 0 {
		conv.Usage.Input += r.subUsage.Input
		conv.Usage.Output += r.subUsage.Output
		conv.Usage.CacheRead += r.subUsage.CacheRead
		conv.Usage.CacheWrite += r.subUsage.CacheWrite
		conv.Usage.Reasoning += r.subUsage.Reasoning
		conv.Usage.Requests += r.subReqs
		r.subUsage, r.subReqs = llm.Usage{}, 0
	}
}

func (r *Run) addSubUsage(u llm.Usage) {
	r.mu.Lock()
	r.subUsage.Input += u.Input
	r.subUsage.Output += u.Output
	r.subUsage.CacheRead += u.CacheRead
	r.subUsage.CacheWrite += u.CacheWrite
	r.subUsage.Reasoning += u.Reasoning
	r.subReqs++
	r.mu.Unlock()
}

func canonicalJSON(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	b, _ := json.Marshal(v) // map 键排序
	return string(b)
}

func (r *Run) takeSteer() []SendInput {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.steer
	r.steer = nil
	return s
}

func (r *Run) hasSteer() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.steer) > 0
}
