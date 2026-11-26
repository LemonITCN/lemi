// Package realtime 经 WebSocket 把事件推给浏览器（健康状态、备份进度、智能体事件），
// 也让服务端调用浏览器里的「客户端工具」（智能体操作主工作区）并等待结果。
package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/ids"
	"github.com/coder/websocket"
)

// Message 是推给浏览器或浏览器发来的消息。
type Message struct {
	T string          `json:"t"`
	D json.RawMessage `json:"d,omitempty"`
}

// Conn 是一个浏览器连接。
type Conn struct {
	ID       string
	UserID   string
	Opened   time.Time
	hub      *Hub
	ws       *websocket.Conn
	send     chan []byte
	closed   chan struct{}
	once     sync.Once
	mu       sync.Mutex
	visible  bool   // 页面是否在前台（客户端上报）
	activeWS string // 当前所在工作空间（客户端上报）
}

// Hub 管理全部连接。
type Hub struct {
	log      *slog.Logger
	mu       sync.RWMutex
	conns    map[string]*Conn
	pending  sync.Map // 调用 ID → chan clientResult
	handlers map[string]func(c *Conn, data json.RawMessage)
}

type clientResult struct {
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
}

// New 创建 Hub。
func New(log *slog.Logger) *Hub {
	h := &Hub{log: log, conns: map[string]*Conn{}, handlers: map[string]func(*Conn, json.RawMessage){}}
	h.handlers["client_result"] = h.onClientResult
	h.handlers["presence"] = h.onPresence
	return h
}

// Handle 注册浏览器消息处理器。
func (h *Hub) Handle(t string, fn func(c *Conn, data json.RawMessage)) {
	h.mu.Lock()
	h.handlers[t] = fn
	h.mu.Unlock()
}

// Serve 升级 HTTP 连接为 WebSocket。调用方已完成会话与 Origin 校验。
func (h *Hub) Serve(w http.ResponseWriter, r *http.Request, userID string) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{}) // 默认校验 Origin 与 Host 一致
	if err != nil {
		return
	}
	ws.SetReadLimit(4 << 20)
	c := &Conn{ID: ids.New(), UserID: userID, Opened: time.Now(), hub: h, ws: ws, send: make(chan []byte, 256), closed: make(chan struct{}), visible: true}
	h.mu.Lock()
	h.conns[c.ID] = c
	h.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
		h.mu.Lock()
		delete(h.conns, c.ID)
		h.mu.Unlock()
		c.close()
	}()
	hello, _ := json.Marshal(map[string]any{"conn_id": c.ID})
	c.sendRaw(mustMsg("hello", hello))
	go c.writeLoop(ctx)
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var m Message
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		if m.T == "ping" {
			c.sendRaw(mustMsg("pong", nil))
			continue
		}
		h.mu.RLock()
		fn := h.handlers[m.T]
		h.mu.RUnlock()
		if fn != nil {
			fn(c, m.D)
		}
	}
}

func mustMsg(t string, d json.RawMessage) []byte {
	b, _ := json.Marshal(Message{T: t, D: d})
	return b
}

func (c *Conn) close() {
	c.once.Do(func() {
		close(c.closed)
		_ = c.ws.Close(websocket.StatusNormalClosure, "")
	})
}

func (c *Conn) writeLoop(ctx context.Context) {
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case b := <-c.send:
			wctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			err := c.ws.Write(wctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				c.close()
				return
			}
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			err := c.ws.Ping(pctx)
			cancel()
			if err != nil {
				c.close()
				return
			}
		}
	}
}

func (c *Conn) sendRaw(b []byte) {
	select {
	case c.send <- b:
	case <-c.closed:
	default:
		// 发送队列满：浏览器太慢，断开让它重连
		c.close()
	}
}

// Send 给这个连接发一条消息。
func (c *Conn) Send(t string, data any) {
	b, err := json.Marshal(data)
	if err != nil {
		return
	}
	c.sendRaw(mustMsg(t, b))
}

// SendToUser 发给某用户的全部连接。
func (h *Hub) SendToUser(userID, t string, data any) {
	b, err := json.Marshal(data)
	if err != nil {
		return
	}
	msg := mustMsg(t, b)
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, c := range h.conns {
		if c.UserID == userID {
			c.sendRaw(msg)
		}
	}
}

// SendToConn 发给指定连接。
func (h *Hub) SendToConn(connID, t string, data any) bool {
	h.mu.RLock()
	c := h.conns[connID]
	h.mu.RUnlock()
	if c == nil {
		return false
	}
	c.Send(t, data)
	return true
}

// Broadcast 发给满足条件的用户（filter 按用户 ID 判断，例如是否为某工作空间成员）。
func (h *Hub) Broadcast(t string, data any, filter func(userID string) bool) {
	b, err := json.Marshal(data)
	if err != nil {
		return
	}
	msg := mustMsg(t, b)
	h.mu.RLock()
	conns := make([]*Conn, 0, len(h.conns))
	for _, c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.RUnlock()
	allowed := map[string]bool{}
	for _, c := range conns {
		ok, seen := allowed[c.UserID]
		if !seen {
			ok = filter == nil || filter(c.UserID)
			allowed[c.UserID] = ok
		}
		if ok {
			c.sendRaw(msg)
		}
	}
}

// Online 用户是否有在线的浏览器连接；connID 非空时检查该连接。
func (h *Hub) Online(userID, connID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if connID != "" {
		c := h.conns[connID]
		return c != nil && c.UserID == userID
	}
	for _, c := range h.conns {
		if c.UserID == userID {
			return true
		}
	}
	return false
}

// PickConn 选用户最合适的连接：优先指定的，其次在前台且最近打开的。
func (h *Hub) PickConn(userID, prefer string) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if c := h.conns[prefer]; c != nil && c.UserID == userID {
		return c.ID
	}
	var best *Conn
	for _, c := range h.conns {
		if c.UserID != userID {
			continue
		}
		c.mu.Lock()
		vis := c.visible
		c.mu.Unlock()
		if best == nil || (vis && !best.visible) || (vis == best.visible && c.Opened.After(best.Opened)) {
			best = c
		}
	}
	if best == nil {
		return ""
	}
	return best.ID
}

func (h *Hub) onPresence(c *Conn, data json.RawMessage) {
	var p struct {
		Visible   bool   `json:"visible"`
		Workspace string `json:"workspace"`
	}
	if json.Unmarshal(data, &p) == nil {
		c.mu.Lock()
		c.visible, c.activeWS = p.Visible, p.Workspace
		c.mu.Unlock()
	}
}

// ErrClientGone 浏览器不在线或中途断开。
var ErrClientGone = errors.New("浏览器不在线")

// CallClient 让浏览器执行一个客户端工具并等待结果（智能体的 workspace_* 工具用）。
func (h *Hub) CallClient(ctx context.Context, userID, connID, tool string, args any) (json.RawMessage, error) {
	connID = h.PickConn(userID, connID)
	if connID == "" {
		return nil, ErrClientGone
	}
	h.mu.RLock()
	c := h.conns[connID]
	h.mu.RUnlock()
	if c == nil {
		return nil, ErrClientGone
	}
	callID := ids.New()
	ch := make(chan clientResult, 1)
	h.pending.Store(callID, pendingCall{ch: ch, connID: connID})
	defer h.pending.Delete(callID)
	c.Send("client_call", map[string]any{"id": callID, "tool": tool, "args": args})
	select {
	case r := <-ch:
		if r.Error != "" {
			return nil, errors.New(r.Error)
		}
		return r.Result, nil
	case <-c.closed:
		return nil, ErrClientGone
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(2 * time.Minute):
		return nil, errors.New("浏览器没有在 2 分钟内响应")
	}
}

type pendingCall struct {
	ch     chan clientResult
	connID string
}

func (h *Hub) onClientResult(c *Conn, data json.RawMessage) {
	var r struct {
		ID     string          `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error"`
	}
	if json.Unmarshal(data, &r) != nil {
		return
	}
	v, ok := h.pending.Load(r.ID)
	if !ok {
		return
	}
	p := v.(pendingCall)
	if p.connID != c.ID { // 只接受发给这个连接的调用的回执
		return
	}
	select {
	case p.ch <- clientResult{Result: r.Result, Error: r.Error}:
	default:
	}
}
