package notify

import (
	"context"
	"net/http"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
	"github.com/LemonITCN/lemi/lemi-service/internal/web"
	"github.com/go-chi/chi/v5"
)

// Routes 挂载通知相关接口（/api/v1 下，已登录）。
func (s *Service) Routes(srv *web.Server, r chi.Router) {
	h := func(fn func(srv *web.Server, w http.ResponseWriter, r *http.Request) error) http.HandlerFunc {
		return srv.H(func(w http.ResponseWriter, r *http.Request) error { return fn(srv, w, r) })
	}
	r.Get("/workspaces/{ws}/notify/kinds", h(s.hKinds))
	r.Get("/workspaces/{ws}/notify/channels", h(s.hList))
	r.Post("/workspaces/{ws}/notify/channels", h(s.hCreate))
	r.Post("/workspaces/{ws}/notify/channels/test", h(s.hTestDraft))
	r.Get("/workspaces/{ws}/notify/channels/{id}", h(s.hGet))
	r.Put("/workspaces/{ws}/notify/channels/{id}", h(s.hUpdate))
	r.Delete("/workspaces/{ws}/notify/channels/{id}", h(s.hDelete))
	r.Post("/workspaces/{ws}/notify/channels/{id}/test", h(s.hTest))
	r.Get("/workspaces/{ws}/notify/logs", h(s.hLogs))
	r.Get("/admin/smtp", h(s.hSMTPGet))
	r.Put("/admin/smtp", h(s.hSMTPPut))
	r.Post("/admin/smtp/test", h(s.hSMTPTest))
}

func (s *Service) hKinds(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	if _, err := srv.RequireWS(r, chi.URLParam(r, "ws"), accounts.WSViewer); err != nil {
		return err
	}
	return web.OK(w, map[string]any{"kinds": Kinds(), "events": Events(), "system_smtp": s.SystemSMTPConfigured(r.Context())})
}

func (s *Service) hList(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	ws := chi.URLParam(r, "ws")
	role, err := srv.RequireWS(r, ws, accounts.WSViewer)
	if err != nil {
		return err
	}
	list, err := s.List(r.Context(), ws)
	if err != nil {
		return err
	}
	full := accounts.RoleRank(role) >= accounts.RoleRank(accounts.WSAdmin)
	out := make([]map[string]any, 0, len(list))
	for i := range list {
		out = append(out, s.View(&list[i], full))
	}
	return web.OK(w, map[string]any{"channels": out})
}

// channel 取 URL 里的渠道并确认属于 {ws}，同时要求空间角色至少 minRole。
func (s *Service) channel(srv *web.Server, r *http.Request, minRole string) (*Channel, string, error) {
	ws := chi.URLParam(r, "ws")
	role, err := srv.RequireWS(r, ws, minRole)
	if err != nil {
		return nil, role, err
	}
	c, err := s.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		return nil, role, err
	}
	if c.WorkspaceID != ws {
		return nil, role, apperr.NotFound("通知渠道")
	}
	return c, role, nil
}

func (s *Service) hGet(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, role, err := s.channel(srv, r, accounts.WSViewer)
	if err != nil {
		return err
	}
	return web.OK(w, s.View(c, accounts.RoleRank(role) >= accounts.RoleRank(accounts.WSAdmin)))
}

func (s *Service) hCreate(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	ws := chi.URLParam(r, "ws")
	if _, err := srv.RequireWS(r, ws, accounts.WSAdmin); err != nil {
		return err
	}
	var in Input
	if err := web.ReadJSON(r, &in); err != nil {
		return err
	}
	c, err := s.Create(r.Context(), ws, web.UserOf(r).ID, &in)
	if err != nil {
		return err
	}
	srv.AuditR(r, audit.Entry{Action: "notify.channel_create", WorkspaceID: ws, Target: c.Name}, map[string]any{"id": c.ID, "kind": c.Kind})
	return web.OK(w, s.View(c, true))
}

func (s *Service) hUpdate(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, _, err := s.channel(srv, r, accounts.WSAdmin)
	if err != nil {
		return err
	}
	var in Input
	if err := web.ReadJSON(r, &in); err != nil {
		return err
	}
	before := map[string]any{"name": c.Name, "kind": c.Kind, "enabled": c.Enabled}
	if err := s.Update(r.Context(), c, &in); err != nil {
		return err
	}
	srv.AuditR(r, audit.Entry{Action: "notify.channel_update", WorkspaceID: c.WorkspaceID, Target: c.Name},
		map[string]any{"id": c.ID, "before": before, "kind": c.Kind, "enabled": c.Enabled, "cleared": in.Clear})
	return web.OK(w, s.View(c, true))
}

func (s *Service) hDelete(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, _, err := s.channel(srv, r, accounts.WSAdmin)
	if err != nil {
		return err
	}
	if err := s.Delete(r.Context(), c); err != nil {
		return err
	}
	srv.AuditR(r, audit.Entry{Action: "notify.channel_delete", WorkspaceID: c.WorkspaceID, Target: c.Name}, map[string]any{"id": c.ID, "kind": c.Kind})
	return web.OK(w, map[string]any{"ok": true})
}

// testEvent 测试消息。
func testEvent(name string) Event {
	return Event{Kind: EvTest, Level: LevelInfo, Title: "Lemi 测试消息",
		Message: "这是一条来自 Lemi 的测试通知，收到说明渠道「" + name + "」配置正确。\nThis is a test notification from Lemi.", Link: "/"}
}

// TestChannel 立即给渠道发一条测试消息（同步、不重试），结果写进发送记录。
func (s *Service) TestChannel(ctx context.Context, c *Channel) (time.Duration, error) {
	ev := s.normalizeEvent(testEvent(c.Name))
	logID := s.logStart(c.WorkspaceID, c.ID, c.Name, c.Kind, ev)
	start := time.Now()
	err := s.sendOnce(ctx, c, ev)
	s.logFinish(logID, 1, err)
	return time.Since(start), err
}

func testResult(w http.ResponseWriter, d time.Duration, err error) error {
	if err != nil {
		if ae, ok := apperr.As(err); ok {
			return ae
		}
		return apperr.New(400, "notify_failed", "发送失败："+err.Error())
	}
	return web.OK(w, map[string]any{"ok": true, "latency_ms": d.Milliseconds()})
}

func (s *Service) hTest(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	c, _, err := s.channel(srv, r, accounts.WSAdmin)
	if err != nil {
		return err
	}
	d, err := s.TestChannel(r.Context(), c)
	return testResult(w, d, err)
}

// hTestDraft 用表单内容测试（保存前）。带 id 时沿用该渠道已保存的密钥（规则与保存相同：地址变了必须重新输入）。
func (s *Service) hTestDraft(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	ws := chi.URLParam(r, "ws")
	if _, err := srv.RequireWS(r, ws, accounts.WSAdmin); err != nil {
		return err
	}
	var req struct {
		ID string `json:"id"`
		Input
	}
	if err := web.ReadJSON(r, &req); err != nil {
		return err
	}
	var old *Channel
	id := "draft-" + web.UserOf(r).ID
	if req.ID != "" {
		c, err := s.Get(r.Context(), req.ID)
		if err != nil {
			return err
		}
		if c.WorkspaceID != ws {
			return apperr.NotFound("通知渠道")
		}
		old, id = c, c.ID
	}
	cfg, err := s.prepare(r.Context(), id, &req.Input, old)
	if err != nil {
		return err
	}
	tmp := &Channel{ID: id, WorkspaceID: ws, Name: req.Name, Kind: req.Kind, Config: cfg, Enabled: true}
	ev := s.normalizeEvent(testEvent(req.Name))
	logID := s.logStart(ws, "", req.Name+"（未保存）", req.Kind, ev)
	start := time.Now()
	err = s.sendOnce(r.Context(), tmp, ev)
	s.logFinish(logID, 1, err)
	return testResult(w, time.Since(start), err)
}

func (s *Service) hLogs(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	ws := chi.URLParam(r, "ws")
	if _, err := srv.RequireWS(r, ws, accounts.WSViewer); err != nil {
		return err
	}
	q := r.URL.Query()
	list, next, err := s.Logs(r.Context(), ws, q.Get("channel"), q.Get("cursor"), web.QueryInt(r, "limit", 50))
	if err != nil {
		return err
	}
	return web.OK(w, map[string]any{"logs": list, "next_cursor": next})
}

// ---------- 系统邮件服务器（实例管理员） ----------

func requireAdmin(r *http.Request) error {
	if !web.UserOf(r).IsAdmin() {
		return apperr.Forbidden("需要实例管理员权限")
	}
	return nil
}

func (s *Service) hSMTPGet(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	if err := requireAdmin(r); err != nil {
		return err
	}
	v, err := s.SystemSMTPView(r.Context())
	if err != nil {
		return err
	}
	return web.OK(w, v)
}

func (s *Service) hSMTPPut(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	if err := requireAdmin(r); err != nil {
		return err
	}
	if err := srv.RequireReauth(r); err != nil {
		return err
	}
	var in SMTPInput
	if err := web.ReadJSON(r, &in); err != nil {
		return err
	}
	saved, err := s.prepareSystemSMTP(r.Context(), in)
	if err != nil {
		return err
	}
	if err := s.set.SetJSON(r.Context(), smtpSettingsKey, saved); err != nil {
		return err
	}
	srv.AuditR(r, audit.Entry{Action: "notify.smtp_update", Target: saved.Host}, saved.auditDetail())
	v, err := s.SystemSMTPView(r.Context())
	if err != nil {
		return err
	}
	return web.OK(w, v)
}

func (s *Service) hSMTPTest(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	if err := requireAdmin(r); err != nil {
		return err
	}
	var req struct {
		To     string     `json:"to"`
		Config *SMTPInput `json:"config"`
	}
	if err := web.ReadJSON(r, &req); err != nil {
		return err
	}
	if req.To == "" {
		req.To = web.UserOf(r).Email
	}
	if req.To == "" {
		return apperr.Bad("请填写收件地址")
	}
	start := time.Now()
	err := s.TestSystemSMTP(r.Context(), req.Config, req.To)
	return testResult(w, time.Since(start), err)
}
