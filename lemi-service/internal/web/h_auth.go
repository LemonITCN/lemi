package web

import (
	"crypto/subtle"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
	"github.com/LemonITCN/lemi/lemi-service/internal/auth"
	"github.com/LemonITCN/lemi/lemi-service/internal/store"
	"github.com/LemonITCN/lemi/lemi-service/internal/version"
)

func (s *Server) setCookie(w http.ResponseWriter, r *http.Request, tok string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: tok, Path: "/", HttpOnly: true, Secure: IsHTTPS(r),
		SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
}

// insecureTransport 公网地址上用 HTTP 访问时返回 true（前端顶部显示警告）。
func insecureTransport(r *http.Request) bool {
	if IsHTTPS(r) {
		return false
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return false
	}
	if a, err := netip.ParseAddr(host); err == nil {
		a = a.Unmap()
		return !(a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast())
	}
	return true
}

func (s *Server) bootstrapPayload(r *http.Request, a *AuthInfo) map[string]any {
	ctx := r.Context()
	n, _ := s.Accounts.CountUsers(ctx)
	out := map[string]any{
		"version":     version.Version,
		"needs_setup": n == 0,
		"insecure":    insecureTransport(r),
		"user":        nil,
	}
	if a == nil {
		return out
	}
	out["user"] = userView(a.User)
	out["stage"] = a.Session.Stage
	out["csrf"] = a.Session.CSRF
	if a.Session.Stage == auth.StageDone {
		ws, _ := s.Accounts.MyWorkspaces(ctx, a.User)
		if ws == nil {
			ws = []accounts.Workspace{}
		}
		out["workspaces"] = ws
		sys := s.Settings.System(ctx)
		out["system"] = map[string]any{"allow_public_share": sys.AllowPublicShare, "allow_ai_full_access": sys.AllowAIFullAccess}
		for _, fn := range s.BootstrapExtra {
			fn(ctx, a.User, out)
		}
	}
	return out
}

func userView(u *accounts.User) map[string]any {
	return map[string]any{
		"id": u.ID, "username": u.Username, "display_name": u.DisplayName, "email": u.Email, "role": u.Role,
		"totp_enabled": u.TOTPEnabled, "must_change_password": u.MustChangePassword, "locale": u.Locale,
		"recovery_left": auth.RecoveryLeft(u), "created_at": u.CreatedAt,
	}
}

func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request) error {
	return OK(w, s.bootstrapPayload(r, AuthOf(r)))
}

// handleSetup 首次启动创建实例管理员：必须提供控制台打印的一次性初始化令牌。
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Token         string `json:"token"`
		Username      string `json:"username"`
		Password      string `json:"password"`
		DisplayName   string `json:"display_name"`
		Email         string `json:"email"`
		WorkspaceName string `json:"workspace_name"`
	}
	if err := ReadJSON(r, &req); err != nil {
		return err
	}
	ctx := r.Context()
	if n, _ := s.Accounts.CountUsers(ctx); n > 0 {
		return apperr.Conflict("已经初始化过了")
	}
	want := ""
	if s.SetupToken != nil {
		want = s.SetupToken()
	}
	if want == "" || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(req.Token)), []byte(want)) != 1 {
		s.AuditR(r, audit.Entry{Action: "setup.bad_token", OK: false, Source: audit.SrcSystem}, nil)
		time.Sleep(time.Second)
		return apperr.New(403, "bad_setup_token", "初始化令牌不正确（见服务启动时的控制台输出或数据目录里的 setup-token 文件）")
	}
	if err := auth.CheckPasswordPolicy(req.Password, req.Username); err != nil {
		return apperr.Bad("%s", err.Error())
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		return err
	}
	u := &accounts.User{Username: req.Username, DisplayName: strings.TrimSpace(req.DisplayName), Email: strings.TrimSpace(req.Email), Role: accounts.RoleAdmin, PasswordHash: hash}
	if err := s.Accounts.CreateUser(ctx, u); err != nil {
		return apperr.Bad("%s", err.Error())
	}
	name := strings.TrimSpace(req.WorkspaceName)
	if name == "" {
		name = "默认空间"
	}
	if _, err := s.Accounts.CreateWorkspace(ctx, name, "", u.ID); err != nil {
		return err
	}
	if s.OnSetupDone != nil {
		s.OnSetupDone()
	}
	tok, sess, err := s.Auth.CreateSession(ctx, u, auth.StageTOTPSetup, ClientIP(r), r.UserAgent())
	if err != nil {
		return err
	}
	s.Auth.SeenDevice(ctx, u, ClientIP(r), r.UserAgent())
	s.setCookie(w, r, tok, 0)
	s.AuditR(r, audit.Entry{Action: "setup.admin_created", Target: u.Username, Source: audit.SrcSystem, UserID: u.ID, Username: u.Username}, nil)
	return OK(w, s.bootstrapPayload(r, &AuthInfo{User: u, Session: sess}))
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := ReadJSON(r, &req); err != nil {
		return err
	}
	ctx := r.Context()
	ip, ua := ClientIP(r), r.UserAgent()
	u, err := s.Auth.CheckPassword(ctx, req.Username, req.Password, ip, ua)
	if err != nil {
		s.Audit.Record(audit.Entry{Action: "auth.login_failed", Target: accounts.NormalizeUsername(req.Username), IP: ip, OK: false, Error: err.Error(), Source: audit.SrcUI}, nil)
		return err
	}
	stage := auth.StageDone
	switch {
	case u.TOTPEnabled:
		stage = auth.StageMFA
	case s.Accounts.NeedsTOTP(ctx, u, s.Settings.System(ctx).RequireMemberTOTP):
		stage = auth.StageTOTPSetup
	}
	if a := AuthOf(r); a != nil {
		s.Auth.DeleteSession(ctx, a.Session)
	}
	tok, sess, err := s.Auth.CreateSession(ctx, u, stage, ip, ua)
	if err != nil {
		return err
	}
	s.setCookie(w, r, tok, 0)
	if stage == auth.StageDone {
		s.afterLogin(r, u)
	}
	return OK(w, s.bootstrapPayload(r, &AuthInfo{User: u, Session: sess}))
}

// afterLogin 记录登录、新设备提醒。
func (s *Server) afterLogin(r *http.Request, u *accounts.User) {
	ctx := r.Context()
	ip, ua := ClientIP(r), r.UserAgent()
	_, _ = s.Store.Exec(ctx, `UPDATE users SET last_login_at = ?, last_login_ip = ? WHERE id = ?`, store.Now(), ip, u.ID)
	if s.Auth.SeenDevice(ctx, u, ip, ua) && s.Auth.OnEvent != nil {
		s.Auth.OnEvent(ctx, "new_device_login", u, ip, ua)
	}
	s.Audit.Record(audit.Entry{UserID: u.ID, Username: u.Username, Action: "auth.login", IP: ip, Source: audit.SrcUI}, map[string]any{"ua": ua})
}

func (s *Server) handleMFA(w http.ResponseWriter, r *http.Request) error {
	a := AuthOf(r)
	if a == nil || a.Session.Stage != auth.StageMFA {
		return apperr.New(401, "unauthorized", "请先输入用户名和口令")
	}
	var req struct {
		Code string `json:"code"`
	}
	if err := ReadJSON(r, &req); err != nil {
		return err
	}
	if err := s.Auth.CheckCode(r.Context(), a.User, req.Code, ClientIP(r)); err != nil {
		s.Audit.Record(audit.Entry{UserID: a.User.ID, Username: a.User.Username, Action: "auth.mfa_failed", IP: ClientIP(r), OK: false, Source: audit.SrcUI}, nil)
		return err
	}
	if err := s.Auth.SetStage(r.Context(), a.Session, auth.StageDone); err != nil {
		return err
	}
	s.afterLogin(r, a.User)
	return OK(w, s.bootstrapPayload(r, a))
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) error {
	if a := AuthOf(r); a != nil {
		s.Auth.DeleteSession(r.Context(), a.Session)
		s.AuditR(r, audit.Entry{Action: "auth.logout"}, nil)
	}
	s.setCookie(w, r, "", -1)
	return OK(w, map[string]any{"ok": true})
}

// handleTOTPBegin 生成两步验证密钥（绑定阶段或已登录用户重新绑定）。重新绑定需要二次验证。
func (s *Server) handleTOTPBegin(w http.ResponseWriter, r *http.Request) error {
	a := AuthOf(r)
	if a.Session.Stage == auth.StageDone && a.User.TOTPEnabled {
		if err := s.RequireReauth(r); err != nil {
			return err
		}
	}
	secret, uri, err := s.Auth.BeginTOTP(r.Context(), a.User)
	if err != nil {
		return err
	}
	return OK(w, map[string]any{"secret": secret, "otpauth_uri": uri})
}

func (s *Server) handleTOTPConfirm(w http.ResponseWriter, r *http.Request) error {
	a := AuthOf(r)
	var req struct {
		Code string `json:"code"`
	}
	if err := ReadJSON(r, &req); err != nil {
		return err
	}
	codes, err := s.Auth.ConfirmTOTP(r.Context(), a.User, req.Code, ClientIP(r))
	if err != nil {
		return err
	}
	wasSetup := a.Session.Stage == auth.StageTOTPSetup
	if err := s.Auth.SetStage(r.Context(), a.Session, auth.StageDone); err != nil {
		return err
	}
	s.Auth.RevokeOthers(r.Context(), a.User.ID, a.Session.ID)
	s.AuditR(r, audit.Entry{Action: "auth.totp_bound"}, nil)
	if wasSetup {
		s.afterLogin(r, a.User)
	}
	out := s.bootstrapPayload(r, a)
	out["recovery_codes"] = codes
	return OK(w, out)
}
