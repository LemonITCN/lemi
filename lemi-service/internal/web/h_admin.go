package web

import (
	"net/http"
	"net/netip"
	"runtime"
	"strings"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
	"github.com/LemonITCN/lemi/lemi-service/internal/auth"
	"github.com/LemonITCN/lemi/lemi-service/internal/ids"
	"github.com/LemonITCN/lemi/lemi-service/internal/settings"
	"github.com/LemonITCN/lemi/lemi-service/internal/version"
	"github.com/go-chi/chi/v5"
)

func (s *Server) routesAdmin(r chi.Router) {
	r.Get("/admin/users", s.H(s.handleAdminUsers))
	r.Post("/admin/users", s.H(s.handleAdminUserCreate))
	r.Patch("/admin/users/{id}", s.H(s.handleAdminUserUpdate))
	r.Delete("/admin/users/{id}", s.H(s.handleAdminUserDelete))
	r.Post("/admin/users/{id}/reset-password", s.H(s.handleAdminResetPassword))
	r.Post("/admin/users/{id}/reset-totp", s.H(s.handleAdminResetTOTP))
	r.Post("/admin/invites", s.H(s.handleAdminInvite))
	r.Get("/admin/settings", s.H(s.handleAdminSettings))
	r.Put("/admin/settings", s.H(s.handleAdminSettingsPut))
	r.Get("/admin/audit", s.H(s.handleAdminAudit))
	r.Get("/admin/system", s.H(s.handleAdminSystem))
}

func (s *Server) handleAdminUsers(w http.ResponseWriter, r *http.Request) error {
	list, err := s.Accounts.Users(r.Context())
	if err != nil {
		return err
	}
	out := make([]map[string]any, 0, len(list))
	for i := range list {
		u := &list[i]
		v := userView(u)
		v["status"] = u.Status
		v["last_login_at"] = u.LastLoginAt
		v["last_login_ip"] = u.LastLoginIP
		out = append(out, v)
	}
	return OK(w, map[string]any{"users": out})
}

// tempPassword 生成符合口令策略的临时口令。
func tempPassword() string { return ids.RandomToken(8) + "-" + ids.RandomToken(8) }

func (s *Server) handleAdminUserCreate(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Username      string `json:"username"`
		DisplayName   string `json:"display_name"`
		Email         string `json:"email"`
		Role          string `json:"role"`
		Password      string `json:"password"` // 空则生成临时口令，首次登录必须修改
		WorkspaceID   string `json:"workspace_id"`
		WorkspaceRole string `json:"workspace_role"`
	}
	if err := ReadJSON(r, &req); err != nil {
		return err
	}
	if req.Role != accounts.RoleAdmin {
		req.Role = accounts.RoleUser
	}
	pw, must := req.Password, false
	if pw == "" {
		pw, must = tempPassword(), true
	} else if err := auth.CheckPasswordPolicy(pw, req.Username); err != nil {
		return apperr.Bad("%s", err.Error())
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	u := &accounts.User{Username: req.Username, DisplayName: strings.TrimSpace(req.DisplayName), Email: strings.TrimSpace(req.Email), Role: req.Role, PasswordHash: hash, MustChangePassword: must}
	if err := s.Accounts.CreateUser(r.Context(), u); err != nil {
		return apperr.Bad("%s", err.Error())
	}
	if req.WorkspaceID != "" && accounts.ValidWSRole(req.WorkspaceRole) {
		if err := s.Accounts.SetMember(r.Context(), req.WorkspaceID, u.ID, req.WorkspaceRole); err != nil {
			return err
		}
	}
	s.AuditR(r, audit.Entry{Action: "user.create", Target: u.Username}, map[string]any{"role": u.Role})
	out := map[string]any{"user": userView(u)}
	if must {
		out["temp_password"] = pw
	}
	return OK(w, out)
}

func (s *Server) handleAdminUserUpdate(w http.ResponseWriter, r *http.Request) error {
	u, err := s.Accounts.UserByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		return apperr.NotFound("用户")
	}
	var req struct {
		DisplayName *string `json:"display_name"`
		Email       *string `json:"email"`
		Role        *string `json:"role"`
		Status      *string `json:"status"`
	}
	if err := ReadJSON(r, &req); err != nil {
		return err
	}
	me := UserOf(r)
	if req.DisplayName != nil {
		u.DisplayName = strings.TrimSpace(*req.DisplayName)
	}
	if req.Email != nil {
		u.Email = strings.TrimSpace(*req.Email)
	}
	if req.Role != nil && *req.Role != u.Role {
		if *req.Role != accounts.RoleAdmin && *req.Role != accounts.RoleUser {
			return apperr.Bad("角色不正确")
		}
		if u.ID == me.ID {
			return apperr.Bad("不能修改自己的实例角色")
		}
		if err := s.RequireReauth(r); err != nil {
			return err
		}
		u.Role = *req.Role
	}
	if req.Status != nil && *req.Status != u.Status {
		if *req.Status != "active" && *req.Status != "disabled" {
			return apperr.Bad("状态不正确")
		}
		if u.ID == me.ID {
			return apperr.Bad("不能停用自己")
		}
		u.Status = *req.Status
		if u.Status == "disabled" {
			s.Auth.RevokeOthers(r.Context(), u.ID, "")
		}
	}
	if err := s.Accounts.SaveUser(r.Context(), u); err != nil {
		return err
	}
	s.AuditR(r, audit.Entry{Action: "user.update", Target: u.Username}, map[string]any{"role": u.Role, "status": u.Status})
	return OK(w, userView(u))
}

func (s *Server) handleAdminUserDelete(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if id == UserOf(r).ID {
		return apperr.Bad("不能删除自己")
	}
	if err := s.RequireReauth(r); err != nil {
		return err
	}
	u, err := s.Accounts.UserByID(r.Context(), id)
	if err != nil {
		return apperr.NotFound("用户")
	}
	if err := s.Accounts.DeleteUser(r.Context(), id); err != nil {
		return apperr.Bad("%s", err.Error())
	}
	s.AuditR(r, audit.Entry{Action: "user.delete", Target: u.Username}, nil)
	return OK(w, map[string]any{"ok": true})
}

func (s *Server) handleAdminResetPassword(w http.ResponseWriter, r *http.Request) error {
	if err := s.RequireReauth(r); err != nil {
		return err
	}
	u, err := s.Accounts.UserByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		return apperr.NotFound("用户")
	}
	pw := tempPassword()
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	u.PasswordHash, u.MustChangePassword, u.PasswordChangedAt = hash, true, time.Now().UnixMilli()
	if err := s.Accounts.SaveUser(r.Context(), u); err != nil {
		return err
	}
	s.Auth.RevokeOthers(r.Context(), u.ID, "")
	s.AuditR(r, audit.Entry{Action: "user.reset_password", Target: u.Username}, nil)
	return OK(w, map[string]any{"temp_password": pw})
}

func (s *Server) handleAdminResetTOTP(w http.ResponseWriter, r *http.Request) error {
	if err := s.RequireReauth(r); err != nil {
		return err
	}
	u, err := s.Accounts.UserByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		return apperr.NotFound("用户")
	}
	if err := s.Auth.ResetTOTP(r.Context(), u.ID); err != nil {
		return err
	}
	s.Auth.RevokeOthers(r.Context(), u.ID, "")
	s.AuditR(r, audit.Entry{Action: "user.reset_totp", Target: u.Username}, nil)
	return OK(w, map[string]any{"ok": true})
}

func (s *Server) handleAdminInvite(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Role          string `json:"role"`
		WorkspaceID   string `json:"workspace_id"`
		WorkspaceRole string `json:"workspace_role"`
		Note          string `json:"note"`
	}
	if err := ReadJSON(r, &req); err != nil {
		return err
	}
	if req.Role != accounts.RoleAdmin {
		req.Role = accounts.RoleUser
	}
	if req.WorkspaceID != "" {
		if _, err := s.Accounts.Workspace(r.Context(), req.WorkspaceID); err != nil {
			return apperr.NotFound("工作空间")
		}
		if !accounts.ValidWSRole(req.WorkspaceRole) || req.WorkspaceRole == accounts.WSOwner {
			return apperr.Bad("空间角色不正确")
		}
	}
	tok, err := s.createInvite(r, req.Role, req.WorkspaceID, req.WorkspaceRole, req.Note)
	if err != nil {
		return err
	}
	s.AuditR(r, audit.Entry{Action: "user.invite_created", WorkspaceID: req.WorkspaceID}, map[string]any{"role": req.Role, "workspace_role": req.WorkspaceRole})
	return OK(w, map[string]any{"token": tok, "path": "/invite/" + tok, "expires_in": 86400})
}

func (s *Server) handleAdminSettings(w http.ResponseWriter, r *http.Request) error {
	return OK(w, s.Settings.System(r.Context()))
}

func (s *Server) handleAdminSettingsPut(w http.ResponseWriter, r *http.Request) error {
	if err := s.RequireReauth(r); err != nil {
		return err
	}
	var sys settings.System
	if err := ReadJSON(r, &sys); err != nil {
		return err
	}
	for _, list := range [][]string{sys.ConnAllow, sys.ConnDeny} {
		for _, c := range list {
			if _, err := netip.ParsePrefix(strings.TrimSpace(c)); err != nil {
				return apperr.Bad("网段格式不正确：%s（例如 10.0.0.0/8）", c)
			}
		}
	}
	old := s.Settings.System(r.Context())
	if err := s.Settings.SetSystem(r.Context(), sys); err != nil {
		return err
	}
	s.AuditR(r, audit.Entry{Action: "system.settings"}, map[string]any{"from": old, "to": sys})
	return OK(w, s.Settings.System(r.Context()))
}

func auditQuery(r *http.Request) audit.Query {
	q := r.URL.Query()
	return audit.Query{UserID: q.Get("user_id"), ConnectionID: q.Get("connection_id"), Source: q.Get("source"), Action: q.Get("action"),
		Text: q.Get("q"), Before: QueryInt64(r, "before"), Since: QueryInt64(r, "since"), Until: QueryInt64(r, "until"), Limit: QueryInt(r, "limit", 100)}
}

func (s *Server) handleAdminAudit(w http.ResponseWriter, r *http.Request) error {
	q := auditQuery(r)
	q.WorkspaceID = r.URL.Query().Get("workspace_id")
	list, err := s.Audit.List(r.Context(), q)
	if err != nil {
		return err
	}
	return OK(w, map[string]any{"entries": list})
}

func (s *Server) handleAdminSystem(w http.ResponseWriter, r *http.Request) error {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return OK(w, map[string]any{
		"version": version.Version, "commit": version.Commit, "build_date": version.Date,
		"go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH,
		"started_at": s.Started.UnixMilli(), "data_dir": s.Cfg.DataDir, "listen": s.Cfg.Listen, "tls": s.Cfg.TLS.Mode,
		"in_docker": s.Cfg.InDocker, "goroutines": runtime.NumGoroutine(), "heap_mb": ms.HeapAlloc >> 20,
	})
}
