// Package web 是 HTTP 层：中间件（安全响应头、真实 IP、会话、CSRF）、主干接口、静态前端与模块路由挂载。
// 各业务模块通过 AddMount 挂自己的路由，web 不反向依赖业务模块。
package web

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"runtime/debug"
	"strings"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
	"github.com/LemonITCN/lemi/lemi-service/internal/auth"
	"github.com/LemonITCN/lemi/lemi-service/internal/config"
	"github.com/LemonITCN/lemi/lemi-service/internal/crypt"
	"github.com/LemonITCN/lemi/lemi-service/internal/realtime"
	"github.com/LemonITCN/lemi/lemi-service/internal/settings"
	"github.com/LemonITCN/lemi/lemi-service/internal/store"
	"github.com/go-chi/chi/v5"
)

// Deps 是 HTTP 层依赖的服务。
type Deps struct {
	Log      *slog.Logger
	Cfg      *config.Config
	Store    *store.Store
	Auth     *auth.Service
	Accounts *accounts.Service
	Settings *settings.Service
	Audit    *audit.Log
	Hub      *realtime.Hub
	Box      *crypt.Box
	Started  time.Time
	// SetupToken 首次启动的一次性初始化令牌（已有用户时为空）
	SetupToken func() string
	// OnSetupDone 创建第一个管理员后调用（删除令牌文件）
	OnSetupDone func()
}

// Mount 让业务模块挂载自己的路由（已登录、已完成两步验证的路由组）。
type Mount func(s *Server, r chi.Router)

// Server 是 HTTP 服务。
type Server struct {
	Deps
	trusted []netip.Prefix
	mounts  []Mount
	public  []func(r chi.Router)
	// MCP 是 /mcp 端点（自带 Bearer 鉴权，不走会话与 CSRF）
	MCP http.Handler
	// Bootstrap 额外字段（例如模型是否已配置），由模块注入
	BootstrapExtra []func(ctx context.Context, u *accounts.User, out map[string]any)
}

// NewServer 创建 HTTP 服务。
func NewServer(d Deps) *Server {
	s := &Server{Deps: d}
	for _, p := range d.Cfg.TrustedProxies {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !strings.Contains(p, "/") {
			if strings.Contains(p, ":") {
				p += "/128"
			} else {
				p += "/32"
			}
		}
		if pr, err := netip.ParsePrefix(p); err == nil {
			s.trusted = append(s.trusted, pr.Masked())
		}
	}
	return s
}

// AddMount 注册需要登录的模块路由。
func (s *Server) AddMount(m Mount) { s.mounts = append(s.mounts, m) }

// AddPublic 注册不需要登录的顶层路由（OAuth 元数据、公开报告、一次性下载等）。
func (s *Server) AddPublic(fn func(r chi.Router)) { s.public = append(s.public, fn) }

type ctxKey int

const (
	keyAuth ctxKey = iota
	keyIP
	keyHTTPS
)

// AuthInfo 是当前请求的身份。
type AuthInfo struct {
	User    *accounts.User
	Session *auth.Session
}

// AuthOf 返回请求身份（未登录为 nil）。
func AuthOf(r *http.Request) *AuthInfo {
	a, _ := r.Context().Value(keyAuth).(*AuthInfo)
	return a
}

// UserOf 返回当前用户（未登录为 nil）。
func UserOf(r *http.Request) *accounts.User {
	if a := AuthOf(r); a != nil {
		return a.User
	}
	return nil
}

// ClientIP 返回客户端真实 IP。
func ClientIP(r *http.Request) string {
	if v, _ := r.Context().Value(keyIP).(string); v != "" {
		return v
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}

// ConnID 返回前端带来的 WebSocket 连接 ID（智能体客户端工具定位浏览器用）。
func ConnID(r *http.Request) string {
	v := r.Header.Get("X-Lemi-Conn")
	if len(v) > 40 {
		return ""
	}
	return v
}

func (s *Server) realIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		ip := host
		https := r.TLS != nil
		if addr, err := netip.ParseAddr(host); err == nil && s.isTrusted(addr) {
			if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
				parts := strings.Split(xff, ",")
				for i := len(parts) - 1; i >= 0; i-- {
					cand := strings.TrimSpace(parts[i])
					a, err := netip.ParseAddr(cand)
					if err != nil {
						break
					}
					ip = a.Unmap().String()
					if !s.isTrusted(a) {
						break
					}
				}
			} else if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
				if a, err := netip.ParseAddr(xr); err == nil {
					ip = a.Unmap().String()
				}
			}
			if strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
				https = true
			}
		}
		ctx := context.WithValue(r.Context(), keyIP, ip)
		ctx = context.WithValue(ctx, keyHTTPS, https)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) isTrusted(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range s.trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// IsHTTPS 请求是否经 HTTPS 到达（直连 TLS 或受信代理声明）。
func IsHTTPS(r *http.Request) bool {
	v, _ := r.Context().Value(keyHTTPS).(bool)
	return v || r.TLS != nil
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.Log.Error("请求处理崩溃", "path", r.URL.Path, "panic", v, "stack", string(debug.Stack()))
				WriteJSON(w, 500, map[string]any{"error": apperr.New(500, "internal", "服务器内部错误")})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// ContentSecurityPolicy 前端页面的内容安全策略。
const ContentSecurityPolicy = "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; connect-src 'self'; font-src 'self' data:; worker-src 'self' blob:; " +
	"object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		h.Set("Content-Security-Policy", ContentSecurityPolicy)
		if IsHTTPS(r) {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/mcp") || strings.HasPrefix(r.URL.Path, "/oauth") {
			lvl := slog.LevelDebug
			if sw.status >= 500 {
				lvl = slog.LevelError
			}
			s.Log.Log(r.Context(), lvl, "http", "method", r.Method, "path", r.URL.Path, "status", sw.status,
				"ms", time.Since(start).Milliseconds(), "ip", ClientIP(r))
		}
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// SessionCookie 会话 Cookie 名。
const SessionCookie = "lemi_session"

// authenticate 解析会话 Cookie；不拒绝请求，只填充身份。
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(SessionCookie); err == nil {
			if sess, u, err := s.Auth.LookupSession(r.Context(), c.Value); err == nil {
				r = r.WithContext(context.WithValue(r.Context(), keyAuth, &AuthInfo{User: u, Session: sess}))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// csrf 改动类请求必须带 X-Lemi-CSRF（等于会话的 CSRF 令牌），且 Origin / Referer 与本站一致。
// 未登录的改动类请求（登录、初始化）也校验来源，防止登录 CSRF。
func (s *Server) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if !s.sameOrigin(r) {
			s.WriteError(w, r, apperr.New(403, "csrf", "不允许跨站请求"))
			return
		}
		if a := AuthOf(r); a != nil {
			if got := r.Header.Get("X-Lemi-CSRF"); got == "" || got != a.Session.CSRF {
				s.WriteError(w, r, apperr.New(403, "csrf", "请求校验失败，请刷新页面后重试"))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin 校验 Origin（没有时看 Referer）的主机与请求主机一致；两者都没有时只接受带自定义头的请求
// （浏览器跨站表单无法设置自定义头）。
func (s *Server) sameOrigin(r *http.Request) bool {
	src := r.Header.Get("Origin")
	if src == "" || src == "null" {
		src = r.Header.Get("Referer")
	}
	if src == "" {
		return r.Header.Get("X-Lemi-CSRF") != "" || r.Header.Get("X-Requested-With") == "lemi"
	}
	u, err := url.Parse(src)
	if err != nil {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	if s.Cfg.PublicURL != "" {
		if pu, err := url.Parse(s.Cfg.PublicURL); err == nil && strings.EqualFold(pu.Host, u.Host) {
			return true
		}
	}
	return false
}

// requireUser 要求已登录且完成两步验证（或处于绑定两步验证阶段时只允许绑定相关接口）。
func (s *Server) requireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := AuthOf(r)
		if a == nil {
			s.WriteError(w, r, apperr.New(401, "unauthorized", "请先登录"))
			return
		}
		switch a.Session.Stage {
		case auth.StageDone:
		case auth.StageTOTPSetup:
			if !strings.HasPrefix(r.URL.Path, "/api/v1/auth/totp/") {
				s.WriteError(w, r, apperr.New(403, "totp_setup_required", "请先绑定两步验证"))
				return
			}
		default:
			s.WriteError(w, r, apperr.New(401, "mfa_required", "需要完成两步验证"))
			return
		}
		if a.User.MustChangePassword && a.Session.Stage == auth.StageDone && !strings.HasPrefix(r.URL.Path, "/api/v1/me/password") && !strings.HasPrefix(r.URL.Path, "/api/v1/auth/") {
			s.WriteError(w, r, apperr.New(403, "must_change_password", "请先修改口令"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u := UserOf(r); u == nil || !u.IsAdmin() {
			s.WriteError(w, r, apperr.Forbidden("需要实例管理员权限"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// limitBody 限制请求体大小（导入、上传等接口在自己的路由里放宽）。
func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.Header.Get("X-Lemi-Upload") == "" {
			r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
		}
		next.ServeHTTP(w, r)
	})
}

// Handler 组装全部路由。
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(s.recoverer, s.realIP, s.securityHeaders, s.accessLog)
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) { WriteJSON(w, 200, map[string]any{"ok": true}) })
	r.Group(func(r chi.Router) {
		r.Use(s.authenticate, s.csrf)
		r.Route("/api/v1", func(r chi.Router) {
			r.Use(limitBody)
			r.Get("/bootstrap", s.H(s.handleBootstrap))
			r.Post("/setup", s.H(s.handleSetup))
			r.Post("/auth/login", s.H(s.handleLogin))
			r.Post("/auth/mfa", s.H(s.handleMFA))
			r.Post("/auth/logout", s.H(s.handleLogout))
			r.Post("/auth/invite/check", s.H(s.handleInviteCheck))
			r.Post("/auth/invite/accept", s.H(s.handleInviteAccept))
			r.Group(func(r chi.Router) {
				r.Use(s.requireUser)
				r.Post("/auth/totp/begin", s.H(s.handleTOTPBegin))
				r.Post("/auth/totp/confirm", s.H(s.handleTOTPConfirm))
				r.Get("/ws", s.handleWS)
				s.routesMe(r)
				s.routesWorkspaces(r)
				r.Group(func(r chi.Router) {
					r.Use(s.requireAdmin)
					s.routesAdmin(r)
				})
				for _, m := range s.mounts {
					m(s, r)
				}
			})
		})
	})
	// 公开路由（OAuth 端点、公开报告、一次性下载）：自己负责鉴权与来源校验，不走会话 CSRF。
	r.Group(func(r chi.Router) {
		r.Use(s.authenticate, limitBody)
		for _, fn := range s.public {
			fn(r)
		}
	})
	if s.MCP != nil {
		r.Handle("/mcp", s.MCP)
		r.Handle("/mcp/*", s.MCP)
	}
	r.NotFound(serveStatic)
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, 405, map[string]any{"error": apperr.New(405, "method_not_allowed", "不支持的请求方法")})
	})
	return r
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	a := AuthOf(r)
	if a == nil || a.Session.Stage != auth.StageDone {
		s.WriteError(w, r, apperr.New(401, "unauthorized", "请先登录"))
		return
	}
	if !s.sameOrigin(r) {
		s.WriteError(w, r, apperr.New(403, "csrf", "不允许跨站连接"))
		return
	}
	s.Hub.Serve(w, r, a.User.ID)
}

// RequireWS 要求当前用户在空间里至少有 minRole，返回有效角色。
func (s *Server) RequireWS(r *http.Request, wsID, minRole string) (string, error) {
	u := UserOf(r)
	if u == nil {
		return "", apperr.New(401, "unauthorized", "请先登录")
	}
	role, err := s.Accounts.Require(r.Context(), wsID, u, minRole)
	switch err {
	case nil:
		return role, nil
	case accounts.ErrNotMember:
		return "", apperr.NotFound("工作空间")
	default:
		return role, apperr.Forbidden("需要「" + roleName(minRole) + "」及以上的空间角色")
	}
}

func roleName(r string) string {
	switch r {
	case accounts.WSViewer:
		return "查看者"
	case accounts.WSMember:
		return "成员"
	case accounts.WSAdmin:
		return "管理员"
	case accounts.WSOwner:
		return "所有者"
	}
	return r
}

// RequireReauth 敏感操作要求 10 分钟内输过口令或验证码。
func (s *Server) RequireReauth(r *http.Request) error {
	a := AuthOf(r)
	if a == nil || !s.Auth.Reauthed(a.Session) {
		return apperr.New(403, "reauth_required", "请先验证身份")
	}
	return nil
}

// AuditR 记一条带请求身份的审计。
func (s *Server) AuditR(r *http.Request, e audit.Entry, detail any) {
	if u := UserOf(r); u != nil {
		e.UserID, e.Username = u.ID, u.Username
	}
	e.IP = ClientIP(r)
	if e.Source == "" {
		e.Source = audit.SrcUI
	}
	s.Audit.Record(e, detail)
}
