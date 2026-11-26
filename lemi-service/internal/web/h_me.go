package web

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
	"github.com/LemonITCN/lemi/lemi-service/internal/auth"
	"github.com/go-chi/chi/v5"
)

func (s *Server) routesMe(r chi.Router) {
	r.Get("/me", s.H(s.handleMe))
	r.Patch("/me", s.H(s.handleMeUpdate))
	r.Post("/me/password", s.H(s.handleMePassword))
	r.Post("/me/reauth", s.H(s.handleReauth))
	r.Get("/me/sessions", s.H(s.handleMySessions))
	r.Delete("/me/sessions/{key}", s.H(s.handleRevokeSession))
	r.Post("/me/sessions/revoke-others", s.H(s.handleRevokeOthers))
	r.Post("/me/totp/disable", s.H(s.handleTOTPDisable))
	r.Post("/me/totp/recovery", s.H(s.handleRecoveryRegen))
	r.Get("/me/prefs", s.H(s.handlePrefsGet))
	r.Put("/me/prefs", s.H(s.handlePrefsPut))
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) error {
	return OK(w, s.bootstrapPayload(r, AuthOf(r)))
}

func (s *Server) handleMeUpdate(w http.ResponseWriter, r *http.Request) error {
	u := UserOf(r)
	var req struct {
		DisplayName *string `json:"display_name"`
		Email       *string `json:"email"`
		Locale      *string `json:"locale"`
	}
	if err := ReadJSON(r, &req); err != nil {
		return err
	}
	if req.DisplayName != nil {
		v := strings.TrimSpace(*req.DisplayName)
		if len([]rune(v)) > 64 {
			return apperr.Bad("显示名不超过 64 个字符")
		}
		u.DisplayName = v
	}
	if req.Email != nil {
		v := strings.TrimSpace(*req.Email)
		if v != "" && (!strings.Contains(v, "@") || len(v) > 200) {
			return apperr.Bad("邮箱格式不正确")
		}
		u.Email = v
	}
	if req.Locale != nil {
		switch *req.Locale {
		case "", "zh-CN", "en":
			u.Locale = *req.Locale
		default:
			return apperr.Bad("不支持的语言")
		}
	}
	if err := s.Accounts.SaveUser(r.Context(), u); err != nil {
		return err
	}
	return OK(w, userView(u))
}

func (s *Server) handleMePassword(w http.ResponseWriter, r *http.Request) error {
	a := AuthOf(r)
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := ReadJSON(r, &req); err != nil {
		return err
	}
	if !auth.VerifyPassword(req.Current, a.User.PasswordHash) {
		return apperr.New(400, "bad_current_password", "当前口令不正确")
	}
	if req.Current == req.New {
		return apperr.Bad("新口令不能与当前口令相同")
	}
	if err := s.Auth.SetPassword(r.Context(), a.User, req.New, a.Session.ID); err != nil {
		if _, ok := apperr.As(err); ok {
			return err
		}
		return apperr.Bad("%s", err.Error())
	}
	s.AuditR(r, audit.Entry{Action: "auth.password_changed"}, nil)
	if s.Auth.OnEvent != nil {
		s.Auth.OnEvent(r.Context(), "password_changed", a.User, ClientIP(r), r.UserAgent())
	}
	return OK(w, map[string]any{"ok": true})
}

func (s *Server) handleReauth(w http.ResponseWriter, r *http.Request) error {
	a := AuthOf(r)
	var req struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := ReadJSON(r, &req); err != nil {
		return err
	}
	if err := s.Auth.Reauth(r.Context(), a.Session, a.User, req.Password, req.Code, ClientIP(r)); err != nil {
		return err
	}
	return OK(w, map[string]any{"ok": true})
}

func (s *Server) handleMySessions(w http.ResponseWriter, r *http.Request) error {
	a := AuthOf(r)
	list, err := s.Auth.Sessions(r.Context(), a.User.ID)
	if err != nil {
		return err
	}
	out := make([]map[string]any, 0, len(list))
	for i := range list {
		x := &list[i]
		if x.Stage != auth.StageDone {
			continue
		}
		out = append(out, map[string]any{"key": auth.SessionKey(x), "ip": x.IP, "ua": x.UA, "created_at": x.CreatedAt, "last_seen_at": x.LastSeenAt, "current": x.ID == a.Session.ID})
	}
	return OK(w, map[string]any{"sessions": out})
}

func (s *Server) handleRevokeSession(w http.ResponseWriter, r *http.Request) error {
	if err := s.Auth.RevokeSession(r.Context(), UserOf(r).ID, chi.URLParam(r, "key")); err != nil {
		return err
	}
	s.AuditR(r, audit.Entry{Action: "auth.session_revoked"}, nil)
	return OK(w, map[string]any{"ok": true})
}

func (s *Server) handleRevokeOthers(w http.ResponseWriter, r *http.Request) error {
	a := AuthOf(r)
	s.Auth.RevokeOthers(r.Context(), a.User.ID, a.Session.ID)
	s.AuditR(r, audit.Entry{Action: "auth.sessions_revoked"}, nil)
	return OK(w, map[string]any{"ok": true})
}

func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request) error {
	a := AuthOf(r)
	if err := s.RequireReauth(r); err != nil {
		return err
	}
	if s.Accounts.NeedsTOTP(r.Context(), a.User, s.Settings.System(r.Context()).RequireMemberTOTP) {
		return apperr.Forbidden("你的账号必须启用两步验证，不能关闭（可以重新绑定）")
	}
	if err := s.Auth.ResetTOTP(r.Context(), a.User.ID); err != nil {
		return err
	}
	s.AuditR(r, audit.Entry{Action: "auth.totp_disabled"}, nil)
	if s.Auth.OnEvent != nil {
		s.Auth.OnEvent(r.Context(), "totp_disabled", a.User, ClientIP(r), r.UserAgent())
	}
	return OK(w, map[string]any{"ok": true})
}

func (s *Server) handleRecoveryRegen(w http.ResponseWriter, r *http.Request) error {
	a := AuthOf(r)
	if err := s.RequireReauth(r); err != nil {
		return err
	}
	if !a.User.TOTPEnabled {
		return apperr.Bad("请先启用两步验证")
	}
	codes, err := s.Auth.RegenerateRecovery(r.Context(), a.User)
	if err != nil {
		return err
	}
	s.AuditR(r, audit.Entry{Action: "auth.recovery_regenerated"}, nil)
	return OK(w, map[string]any{"recovery_codes": codes})
}

// 个人偏好（界面布局、编辑器设置等），前端自由存取的 JSON，上限 64 KB。
func (s *Server) handlePrefsGet(w http.ResponseWriter, r *http.Request) error {
	u := UserOf(r)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, err := w.Write([]byte(u.Prefs))
	return err
}

func (s *Server) handlePrefsPut(w http.ResponseWriter, r *http.Request) error {
	u := UserOf(r)
	var v map[string]any
	if err := ReadJSON(r, &v); err != nil {
		return err
	}
	b, _ := json.Marshal(v)
	if len(b) > 64<<10 {
		return apperr.Bad("偏好设置太大")
	}
	u.Prefs = string(b)
	if err := s.Accounts.SaveUser(r.Context(), u); err != nil {
		return err
	}
	return OK(w, map[string]any{"ok": true})
}
