package reports

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
	"github.com/LemonITCN/lemi/lemi-service/internal/auth"
	"github.com/LemonITCN/lemi/lemi-service/internal/ids"
	"github.com/LemonITCN/lemi/lemi-service/internal/store"
	"github.com/LemonITCN/lemi/lemi-service/internal/web"
	"github.com/go-chi/chi/v5"
)

// Routes 挂载需要登录的报告接口。
func (s *Service) Routes(srv *web.Server, r chi.Router) {
	h := srv.H
	r.Get("/workspaces/{ws}/reports", h(func(w http.ResponseWriter, r *http.Request) error {
		ws := chi.URLParam(r, "ws")
		if _, err := srv.RequireWS(r, ws, accounts.WSViewer); err != nil {
			return err
		}
		list, err := s.List(r.Context(), ws, web.QueryInt(r, "limit", 200))
		if err != nil {
			return err
		}
		out := make([]map[string]any, 0, len(list))
		for i := range list {
			rp := &list[i]
			out = append(out, map[string]any{"id": rp.ID, "title": rp.Title, "created_by": rp.CreatedBy, "updated_by": rp.UpdatedBy,
				"conversation_id": rp.ConversationID, "task_id": rp.TaskID, "created_at": rp.CreatedAt, "updated_at": rp.UpdatedAt, "shared": rp.ShareToken != ""})
		}
		return web.OK(w, map[string]any{"reports": out})
	}))
	r.Post("/workspaces/{ws}/reports", h(func(w http.ResponseWriter, r *http.Request) error {
		ws := chi.URLParam(r, "ws")
		if _, err := srv.RequireWS(r, ws, accounts.WSMember); err != nil {
			return err
		}
		var in struct {
			Title string `json:"title"`
			Body  string `json:"body"`
		}
		if err := web.ReadJSON(r, &in); err != nil {
			return err
		}
		rp, warns, err := s.Create(r.Context(), ws, web.UserOf(r), in.Title, in.Body, "", "")
		if err != nil {
			return apperr.Wrap("report_invalid", err)
		}
		srv.AuditR(r, audit.Entry{WorkspaceID: ws, Action: "report.create", Target: rp.ID, OK: true}, map[string]any{"title": rp.Title})
		return web.OK(w, map[string]any{"report": s.View(rp), "warnings": warns})
	}))
	r.Route("/reports/{id}", func(r chi.Router) {
		r.Get("/", h(func(w http.ResponseWriter, r *http.Request) error {
			rp, _, err := s.access(srv, r, accounts.WSViewer)
			if err != nil {
				return err
			}
			return web.OK(w, map[string]any{"report": s.View(rp)})
		}))
		r.Put("/", h(func(w http.ResponseWriter, r *http.Request) error {
			rp, _, err := s.access(srv, r, accounts.WSMember)
			if err != nil {
				return err
			}
			var in struct {
				Title string  `json:"title"`
				Body  *string `json:"body"`
			}
			if err := web.ReadJSON(r, &in); err != nil {
				return err
			}
			body := rp.Body
			if in.Body != nil {
				body = *in.Body
			}
			warns, err := s.Update(r.Context(), rp, web.UserOf(r), in.Title, body)
			if err != nil {
				return apperr.Wrap("report_invalid", err)
			}
			return web.OK(w, map[string]any{"report": s.View(rp), "warnings": warns})
		}))
		r.Delete("/", h(func(w http.ResponseWriter, r *http.Request) error {
			rp, role, err := s.access(srv, r, accounts.WSMember)
			if err != nil {
				return err
			}
			if rp.CreatedBy != web.UserOf(r).ID && accounts.RoleRank(role) < accounts.RoleRank(accounts.WSAdmin) {
				return apperr.Forbidden("只有创建者或空间管理员可以删除报告")
			}
			if err := s.Delete(r.Context(), rp.ID); err != nil {
				return err
			}
			srv.AuditR(r, audit.Entry{WorkspaceID: rp.WorkspaceID, Action: "report.delete", Target: rp.ID, OK: true}, map[string]any{"title": rp.Title})
			return web.OK(w, map[string]any{"ok": true})
		}))
		r.Get("/export.md", func(w http.ResponseWriter, r *http.Request) {
			rp, _, err := s.access(srv, r, accounts.WSViewer)
			if err != nil {
				srv.WriteError(w, r, err)
				return
			}
			md := s.Markdown(rp)
			w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
			w.Header().Set("Content-Disposition", `attachment; filename="report-`+rp.ID+`.md"`)
			srv.AuditR(r, audit.Entry{WorkspaceID: rp.WorkspaceID, Action: "report.export", Target: rp.ID, OK: true}, map[string]any{"format": "md"})
			_, _ = w.Write([]byte(md))
		})
		r.Post("/share", h(func(w http.ResponseWriter, r *http.Request) error {
			rp, role, err := s.access(srv, r, accounts.WSMember)
			if err != nil {
				return err
			}
			if rp.CreatedBy != web.UserOf(r).ID && accounts.RoleRank(role) < accounts.RoleRank(accounts.WSAdmin) {
				return apperr.Forbidden("只有创建者或空间管理员可以分享报告")
			}
			if !s.set.System(r.Context()).AllowPublicShare {
				return apperr.Forbidden("实例管理员关闭了公开分享")
			}
			var in struct {
				Password    string `json:"password"`
				ExpiresDays int    `json:"expires_days"` // 0 = 不过期
			}
			if err := web.ReadJSON(r, &in); err != nil {
				return err
			}
			if in.ExpiresDays < 0 || in.ExpiresDays > 3650 {
				return apperr.Bad("有效期应在 0–3650 天")
			}
			pw := ""
			if in.Password != "" {
				if len([]rune(in.Password)) < 4 || len(in.Password) > 128 {
					return apperr.Bad("访问密码 4–128 个字符")
				}
				if pw, err = auth.HashPassword(in.Password); err != nil {
					return err
				}
			}
			tok := ids.RandomToken(32)
			now := store.Now()
			exp := int64(0)
			if in.ExpiresDays > 0 {
				exp = time.Now().Add(time.Duration(in.ExpiresDays) * 24 * time.Hour).UnixMilli()
			}
			if _, err := s.st.Exec(r.Context(), `UPDATE reports SET share_token = ?, share_password = ?, share_expires_at = ?, share_created_at = ? WHERE id = ?`,
				hashToken(tok), pw, exp, now, rp.ID); err != nil {
				return err
			}
			srv.AuditR(r, audit.Entry{WorkspaceID: rp.WorkspaceID, Action: "report.share", Target: rp.ID, OK: true}, map[string]any{"password": pw != "", "expires_at": exp})
			path := "/share/reports/" + tok
			url := path
			if s.BaseURL != nil {
				if b := s.BaseURL(); b != "" {
					url = b + path
				}
			}
			return web.OK(w, map[string]any{"token": tok, "path": path, "url": url, "api": "/api/public/reports/" + tok, "expires_at": exp, "has_password": pw != ""})
		}))
		r.Delete("/share", h(func(w http.ResponseWriter, r *http.Request) error {
			rp, role, err := s.access(srv, r, accounts.WSMember)
			if err != nil {
				return err
			}
			if rp.CreatedBy != web.UserOf(r).ID && accounts.RoleRank(role) < accounts.RoleRank(accounts.WSAdmin) {
				return apperr.Forbidden("只有创建者或空间管理员可以撤销分享")
			}
			if _, err := s.st.Exec(r.Context(), `UPDATE reports SET share_token = '', share_password = '', share_expires_at = 0, share_created_at = 0 WHERE id = ?`, rp.ID); err != nil {
				return err
			}
			srv.AuditR(r, audit.Entry{WorkspaceID: rp.WorkspaceID, Action: "report.unshare", Target: rp.ID, OK: true}, nil)
			return web.OK(w, map[string]any{"ok": true})
		}))
	})
}

func (s *Service) access(srv *web.Server, r *http.Request, min string) (*Report, string, error) {
	rp, err := s.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		return nil, "", apperr.NotFound("报告")
	}
	role, err := srv.RequireWS(r, rp.WorkspaceID, min)
	if err != nil {
		return nil, role, err
	}
	return rp, role, nil
}

func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

// limiter 是公开访问的简单限速（每 IP 每分钟 30 次，密码错误另计每分钟 5 次）。
type limiter struct {
	mu sync.Mutex
	m  map[string][]time.Time
}

func (l *limiter) allow(key string, n int, per time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.m == nil {
		l.m = map[string][]time.Time{}
	}
	now := time.Now()
	var kept []time.Time
	for _, t := range l.m[key] {
		if now.Sub(t) < per {
			kept = append(kept, t)
		}
	}
	if len(kept) >= n {
		l.m[key] = kept
		return false
	}
	l.m[key] = append(kept, now)
	if len(l.m) > 10000 {
		l.m = map[string][]time.Time{}
	}
	return true
}

var publicLimit limiter

// Public 挂载公开报告的只读 JSON：GET /api/public/reports/{token}，有访问密码时放在 X-Lemi-Report-Password 头里。
func (s *Service) Public(srv *web.Server) func(r chi.Router) {
	return func(r chi.Router) {
		r.Get("/api/public/reports/{token}", func(w http.ResponseWriter, r *http.Request) {
			ip := web.ClientIP(r)
			if !publicLimit.allow("v:"+ip, 30, time.Minute) {
				web.WriteJSON(w, 429, map[string]any{"error": apperr.New(429, "too_many_requests", "访问过于频繁，请稍后再试")})
				return
			}
			notFound := func() {
				web.WriteJSON(w, 404, map[string]any{"error": apperr.New(404, "not_found", "链接不存在、已过期或已撤销")})
			}
			if !s.set.System(r.Context()).AllowPublicShare {
				notFound()
				return
			}
			tok := chi.URLParam(r, "token")
			if len(tok) < 20 || len(tok) > 64 {
				notFound()
				return
			}
			var rp Report
			if err := s.st.Get(r.Context(), &rp, `SELECT * FROM reports WHERE share_token = ?`, hashToken(tok)); err != nil ||
				subtle.ConstantTimeCompare([]byte(rp.ShareToken), []byte(hashToken(tok))) != 1 {
				notFound()
				return
			}
			if rp.ShareExpiresAt > 0 && rp.ShareExpiresAt < store.Now() {
				notFound()
				return
			}
			if rp.SharePassword != "" {
				pw := r.Header.Get("X-Lemi-Report-Password")
				if pw == "" {
					web.WriteJSON(w, 401, map[string]any{"error": apperr.New(401, "password_required", "需要访问密码"), "title": rp.Title})
					return
				}
				if !publicLimit.allow("p:"+ip+":"+rp.ID, 5, time.Minute) {
					web.WriteJSON(w, 429, map[string]any{"error": apperr.New(429, "too_many_requests", "密码错误次数过多，请一分钟后再试")})
					return
				}
				if !auth.VerifyPassword(pw, rp.SharePassword) {
					web.WriteJSON(w, 401, map[string]any{"error": apperr.New(401, "bad_password", "访问密码不正确")})
					return
				}
			}
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Robots-Tag", "noindex")
			web.WriteJSON(w, 200, map[string]any{"report": map[string]any{"title": rp.Title, "body": rp.Body, "snapshot": rp.Decode(), "updated_at": rp.UpdatedAt,
				"expires_at": rp.ShareExpiresAt}})
		})
	}
}

// ValidShareToken 前端分享页的路径是否像一个令牌（给静态路由判断用）。
func ValidShareToken(t string) bool { return len(t) >= 20 && len(t) <= 64 && !strings.ContainsAny(t, "/.") }
