// Package mcpserver 把工具注册表以 MCP（Streamable HTTP）暴露给外部智能体（Claude Code、Cursor、Codex 等）。
// 同时支持 2026-07-28 起的无会话协议与更早的有会话协议（由 go-sdk 按客户端协商）。
// 鉴权：OAuth 2.1 访问令牌或个人访问令牌（Authorization: Bearer ...），每次工具调用都按当前请求的令牌重新确认身份与授权；
// 权限 = min(用户对连接的权限, 连接 AI 档位, 授权里该连接的档位)。校验 Host / Origin 防 DNS 重绑定，每个授权每分钟 120 次请求。
package mcpserver

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
	"github.com/LemonITCN/lemi/lemi-service/internal/oauth"
	"github.com/LemonITCN/lemi/lemi-service/internal/tools"
	"github.com/LemonITCN/lemi/lemi-service/internal/version"
	"github.com/LemonITCN/lemi/lemi-service/internal/web"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const instructions = `Lemi 是 Web 数据库管理与分析工具。这些工具让你访问用户在 Lemi 里授权给你的数据库连接（MySQL / MariaDB / SQLite）：
先用 list_connections 看可用连接与你在每个连接上的档位，用 list_databases / list_tables / describe_tables / search_schema 摸清结构，
用 run_query 做只读查询（完整结果保存在 Lemi，返回结果 ID 与前几十行，更多用 read_result 分页、过滤、排序），
能在 SQL 里聚合的就在 SQL 里聚合。写入用 execute_sql（档位允许时才能执行，执行前请向用户确认）。
create_chart / create_diagram / er_diagram 生成的图表与图保存在 Lemi 里，report_write 写的报告可以在 Lemi 网页里查看、导出与分享。`

// RateLimit 每个授权每分钟的请求数。
var RateLimit = 120

// Config 是 MCP 端点的依赖。
type Config struct {
	OAuth     *oauth.Service
	Tools     *tools.Services
	Log       *slog.Logger
	PublicURL string // 配置了对外地址时，Host 必须是它（或回环地址）
}

type limiter struct {
	mu sync.Mutex
	m  map[string][]time.Time
}

func (l *limiter) allow(key string, n int) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.m == nil || len(l.m) > 10000 {
		l.m = map[string][]time.Time{}
	}
	now := time.Now()
	var kept []time.Time
	for _, t := range l.m[key] {
		if now.Sub(t) < time.Minute {
			kept = append(kept, t)
		}
	}
	if len(kept) >= n {
		l.m[key] = kept
		return false, time.Minute - now.Sub(kept[0])
	}
	l.m[key] = append(kept, now)
	return true, 0
}

// Handler 返回 /mcp 的处理器。
func Handler(cfg Config) http.Handler {
	srv := newServer(cfg)
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{
		Logger: cfg.Log, SessionTimeout: 30 * time.Minute, DisableLocalhostProtection: true, MaxRequestBodyBytes: 4 << 20,
	})
	var lim limiter
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostOK(r, cfg.PublicURL) {
			web.WriteJSON(w, 403, map[string]any{"error": "forbidden", "error_description": "Host 不被允许（防 DNS 重绑定）"})
			return
		}
		if !originOK(r, cfg.PublicURL) {
			web.WriteJSON(w, 403, map[string]any{"error": "forbidden", "error_description": "Origin 不被允许"})
			return
		}
		raw := bearer(r)
		id, err := cfg.OAuth.Lookup(r.Context(), raw)
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+cfg.OAuth.Base(r)+`/.well-known/oauth-protected-resource"`)
			web.WriteJSON(w, 401, map[string]any{"error": "invalid_token", "error_description": "需要授权：按客户端提示在浏览器里登录 Lemi 并允许访问，或在 Lemi 设置 → MCP 里创建访问令牌"})
			return
		}
		if ok, wait := lim.allow(id.Grant.ID, RateLimit); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
			web.WriteJSON(w, 429, map[string]any{"error": "rate_limited", "error_description": "请求过于频繁：每个授权每分钟最多 " + strconv.Itoa(RateLimit) + " 次"})
			return
		}
		// 工具处理函数从请求头读身份；客户端 IP 由这里写入，不信任客户端自带的同名头
		r.Header.Del("X-Lemi-Client-Ip")
		r.Header.Set("X-Lemi-Client-Ip", web.ClientIP(r))
		h.ServeHTTP(w, r)
	})
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) < 7 || !strings.EqualFold(h[:7], "bearer ") {
		return ""
	}
	return strings.TrimSpace(h[7:])
}

func hostOnly(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		return strings.ToLower(strings.Trim(host, "[]"))
	}
	return strings.ToLower(strings.Trim(h, "[]"))
}

func isLoopbackHost(h string) bool {
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// hostOK 防 DNS 重绑定：配置了对外地址时 Host 必须是它或回环地址；没配置时，经回环地址直连的请求 Host 也必须是回环地址或 IP。
func hostOK(r *http.Request, publicURL string) bool {
	host := hostOnly(r.Host)
	if host == "" {
		return false
	}
	if publicURL != "" {
		if u, err := url.Parse(publicURL); err == nil && strings.EqualFold(u.Hostname(), host) {
			return true
		}
		return isLoopbackHost(host) || net.ParseIP(host) != nil
	}
	remote, _, _ := net.SplitHostPort(r.RemoteAddr)
	viaProxy := web.ClientIP(r) != remote // 经受信反向代理转发：由代理负责 Host
	if ip := net.ParseIP(remote); ip != nil && ip.IsLoopback() && !viaProxy {
		return isLoopbackHost(host) || net.ParseIP(host) != nil
	}
	return true
}

// originOK 浏览器发起的请求（带 Origin）必须与本站同源。
func originOK(r *http.Request, publicURL string) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	if err != nil {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	if publicURL != "" {
		if pu, err := url.Parse(publicURL); err == nil && strings.EqualFold(pu.Host, u.Host) {
			return true
		}
	}
	return false
}

func newServer(cfg Config) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "lemi", Title: "Lemi", Version: version.Version}, &mcp.ServerOptions{Instructions: instructions, Logger: cfg.Log})
	probe := &tools.Env{Svc: cfg.Tools}
	for _, t := range tools.Default.List(probe, tools.ProfileMCP) {
		t := t
		ro := t.ReadOnly() && t.Kind != tools.KindLemi
		destructive := t.Destructive
		openWorld := false
		srv.AddTool(&mcp.Tool{
			Name:        t.Name,
			Title:       t.Title,
			Description: t.Description,
			InputSchema: t.Schema,
			Annotations: &mcp.ToolAnnotations{Title: t.Title, ReadOnlyHint: ro, DestructiveHint: &destructive, OpenWorldHint: &openWorld, IdempotentHint: ro},
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return call(ctx, cfg, t, req), nil
		})
	}
	return srv
}

func errResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: msg}}}
}

// call 每次调用都按当前请求的令牌重新确认身份（令牌吊销、账号停用、授权收紧立刻生效）。
func call(ctx context.Context, cfg Config, t *tools.Tool, req *mcp.CallToolRequest) *mcp.CallToolResult {
	var hdr http.Header
	if req.Extra != nil {
		hdr = req.Extra.Header
	}
	if hdr == nil {
		return errResult("无法确认身份")
	}
	raw := ""
	if h := hdr.Get("Authorization"); len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		raw = strings.TrimSpace(h[7:])
	}
	id, err := cfg.OAuth.Lookup(ctx, raw)
	if err != nil {
		return errResult("授权已失效，请重新授权")
	}
	env := &tools.Env{Ctx: ctx, Svc: cfg.Tools, User: id.User, WorkspaceID: id.Grant.WorkspaceID, Source: audit.SrcMCP,
		Grant: id.Grant.Levels(), Approval: tools.ApprovalDelegated, IP: hdr.Get("X-Lemi-Client-Ip")}
	var args json.RawMessage
	if req.Params != nil {
		args = req.Params.Arguments
	}
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	res := tools.Default.Call(env, t, args)
	return &mcp.CallToolResult{IsError: res.IsError, Content: []mcp.Content{&mcp.TextContent{Text: res.Text}}}
}
