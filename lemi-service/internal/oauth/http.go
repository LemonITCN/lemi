package oauth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
	"github.com/LemonITCN/lemi/lemi-service/internal/ids"
	"github.com/LemonITCN/lemi/lemi-service/internal/store"
	"github.com/LemonITCN/lemi/lemi-service/internal/web"
	"github.com/go-chi/chi/v5"
)

// oauthErr 按 RFC 6749 的格式回错误。
func oauthErr(w http.ResponseWriter, status int, code, desc string) {
	w.Header().Set("Cache-Control", "no-store")
	web.WriteJSON(w, status, map[string]any{"error": code, "error_description": desc})
}

func cors(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 元数据、注册、换令牌、撤销不依赖 Cookie，允许任意来源（浏览器里的 MCP 客户端也能用）
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, MCP-Protocol-Version")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(204)
			return
		}
		next(w, r)
	}
}

// ConsentPath 前端授权确认页的路由。/oauth/authorize 会带着原样的查询参数跳到这里。
const ConsentPath = "/connect/authorize"

// Public 挂载不需要登录的端点（顶层路由，不走会话 CSRF）。
func (s *Service) Public(r chi.Router) {
	for _, p := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"} {
		r.Get(p, cors(s.resourceMeta))
		r.Options(p, cors(s.resourceMeta))
	}
	for _, p := range []string{"/.well-known/oauth-authorization-server", "/.well-known/oauth-authorization-server/mcp", "/.well-known/openid-configuration"} {
		r.Get(p, cors(s.serverMeta))
		r.Options(p, cors(s.serverMeta))
	}
	r.Post("/oauth/register", cors(s.register))
	r.Options("/oauth/register", cors(s.register))
	r.Post("/oauth/token", cors(s.tokenEndpoint))
	r.Options("/oauth/token", cors(s.tokenEndpoint))
	r.Post("/oauth/revoke", cors(s.revoke))
	r.Options("/oauth/revoke", cors(s.revoke))
	// 授权要登录并由用户确认：跳到前端确认页，查询参数原样带过去
	r.Get("/oauth/authorize", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "no-referrer")
		http.Redirect(w, r, ConsentPath+"?"+r.URL.RawQuery, http.StatusFound)
	})
}

func (s *Service) resourceMeta(w http.ResponseWriter, r *http.Request) {
	base := s.Base(r)
	web.WriteJSON(w, 200, map[string]any{
		"resource": base + "/mcp", "authorization_servers": []string{base}, "bearer_methods_supported": []string{"header"},
		"scopes_supported": []string{ScopeMCP}, "resource_name": "Lemi",
	})
}

func (s *Service) serverMeta(w http.ResponseWriter, r *http.Request) {
	base := s.Base(r)
	web.WriteJSON(w, 200, map[string]any{
		"issuer": base, "authorization_endpoint": base + "/oauth/authorize", "token_endpoint": base + "/oauth/token",
		"registration_endpoint": base + "/oauth/register", "revocation_endpoint": base + "/oauth/revoke",
		"response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported": []string{"S256"}, "token_endpoint_auth_methods_supported": []string{"none"},
		"revocation_endpoint_auth_methods_supported": []string{"none"}, "scopes_supported": []string{ScopeMCP},
	})
}

// register 动态注册公开客户端（RFC 7591）。
func (s *Service) register(w http.ResponseWriter, r *http.Request) {
	if !ipLimit.allow("reg:"+web.ClientIP(r), 20, time.Hour) {
		oauthErr(w, 429, "slow_down", "注册过于频繁")
		return
	}
	var in struct {
		ClientName   string   `json:"client_name"`
		RedirectURIs []string `json:"redirect_uris"`
		ClientURI    string   `json:"client_uri"`
		Software     string   `json:"software_id"`
		AuthMethod   string   `json:"token_endpoint_auth_method"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in); err != nil {
		oauthErr(w, 400, "invalid_client_metadata", "请求体不是合法的 JSON")
		return
	}
	if len(in.RedirectURIs) == 0 || len(in.RedirectURIs) > 10 {
		oauthErr(w, 400, "invalid_redirect_uri", "需要 1 到 10 个 redirect_uris")
		return
	}
	for _, u := range in.RedirectURIs {
		if err := validRedirect(u); err != nil {
			oauthErr(w, 400, "invalid_redirect_uri", err.Error()+"："+u)
			return
		}
	}
	if in.AuthMethod != "" && in.AuthMethod != "none" {
		oauthErr(w, 400, "invalid_client_metadata", "只支持公开客户端（token_endpoint_auth_method=none）")
		return
	}
	name := strings.TrimSpace(in.ClientName)
	if name == "" {
		name = "MCP 客户端"
	}
	if rs := []rune(name); len(rs) > 80 {
		name = string(rs[:80])
	}
	now := time.Now()
	c := client{ID: "lemic_" + ids.New(), Name: name, RedirectURIs: toJSON(in.RedirectURIs),
		Data: toJSON(map[string]any{"client_uri": in.ClientURI, "software_id": in.Software, "ip": web.ClientIP(r)}), CreatedAt: now.UnixMilli()}
	if err := s.st.Add(r.Context(), "oauth_clients", &c); err != nil {
		oauthErr(w, 500, "server_error", "保存失败")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	web.WriteJSON(w, 201, map[string]any{
		"client_id": c.ID, "client_id_issued_at": now.Unix(), "client_name": name, "redirect_uris": in.RedirectURIs,
		"token_endpoint_auth_method": "none", "grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"},
	})
}

// AuthzRequest 是授权请求参数（前端确认页原样转交）。
type AuthzRequest struct {
	ResponseType  string `json:"response_type"`
	ClientID      string `json:"client_id"`
	RedirectURI   string `json:"redirect_uri"`
	State         string `json:"state"`
	Scope         string `json:"scope"`
	Challenge     string `json:"code_challenge"`
	ChallengeMeth string `json:"code_challenge_method"`
	Resource      string `json:"resource"`
}

// check 校验授权请求。redirectOK 表示回调地址已确认可信，出错时可以带 error 跳回去。
func (s *Service) check(r *http.Request, q AuthzRequest) (c *client, redirectOK bool, errCode, errDesc string) {
	c, err := s.client(r.Context(), q.ClientID)
	if err != nil {
		return nil, false, "invalid_client", err.Error()
	}
	if q.RedirectURI == "" || !redirectMatches(c.uris(), q.RedirectURI) {
		return c, false, "invalid_request", "回调地址与注册的不一致"
	}
	if q.ResponseType != "code" {
		return c, true, "unsupported_response_type", "只支持 response_type=code"
	}
	if q.ChallengeMeth != "S256" || len(q.Challenge) < 43 || len(q.Challenge) > 128 {
		return c, true, "invalid_request", "必须使用 PKCE（code_challenge_method=S256）"
	}
	if q.Resource != "" && !strings.HasPrefix(strings.TrimRight(q.Resource, "/"), s.Base(r)) {
		return c, true, "invalid_target", "resource 不是本服务"
	}
	return c, true, "", ""
}

func withQuery(raw string, kv map[string]string) string {
	u, _ := url.Parse(raw)
	q := u.Query()
	for k, v := range kv {
		if v != "" {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// Routes 挂载需要登录的接口：授权确认、已授权应用、个人访问令牌、客户端配置片段。
func (s *Service) Routes(srv *web.Server, r chi.Router) {
	h := srv.H
	r.Post("/oauth/authorize-info", h(func(w http.ResponseWriter, r *http.Request) error {
		var q AuthzRequest
		if err := web.ReadJSON(r, &q); err != nil {
			return err
		}
		u := web.UserOf(r)
		c, ok, ecode, desc := s.check(r, q)
		out := map[string]any{"ok": ecode == "", "error": ecode, "error_description": desc}
		if c != nil {
			out["client_name"] = c.Name
			out["client_id"] = c.ID
		}
		if ok {
			ru, _ := url.Parse(q.RedirectURI)
			out["redirect_host"] = ru.Scheme + "://" + ru.Host
		}
		if ecode == "" {
			wss, _ := s.acc.MyWorkspaces(r.Context(), u)
			var list []map[string]any
			for _, ws := range wss {
				ch, err := s.Choices(r.Context(), u, ws.ID)
				if err != nil {
					continue
				}
				list = append(list, map[string]any{"id": ws.ID, "name": ws.Name, "role": ws.MyRole, "connections": ch})
			}
			if list == nil {
				list = []map[string]any{}
			}
			out["workspaces"] = list
			out["levels"] = mcpLevels
		}
		return web.OK(w, out)
	}))
	r.Post("/oauth/consent", h(func(w http.ResponseWriter, r *http.Request) error {
		a := web.AuthOf(r)
		var in struct {
			AuthzRequest
			Approve     bool              `json:"approve"`
			WorkspaceID string            `json:"workspace_id"`
			Levels      map[string]string `json:"levels"`
			ExpiresDays int               `json:"expires_days"` // 0 = 不过期
		}
		if err := web.ReadJSON(r, &in); err != nil {
			return err
		}
		c, ok, ecode, desc := s.check(r, in.AuthzRequest)
		if !ok {
			return apperr.Bad("%s", desc)
		}
		if ecode != "" {
			return web.OK(w, map[string]any{"redirect": withQuery(in.RedirectURI, map[string]string{"error": ecode, "error_description": desc, "state": in.State})})
		}
		if !in.Approve {
			return web.OK(w, map[string]any{"redirect": withQuery(in.RedirectURI, map[string]string{"error": "access_denied", "state": in.State})})
		}
		if in.ExpiresDays < 0 || in.ExpiresDays > 3650 {
			return apperr.Bad("有效期应在 0–3650 天")
		}
		levels, err := s.CheckLevels(r.Context(), a.User, in.WorkspaceID, in.Levels)
		if err != nil {
			return apperr.Bad("%s", err.Error())
		}
		g := &Grant{ID: ids.New(), UserID: a.User.ID, Kind: "oauth", ClientID: c.ID, Name: c.Name, WorkspaceID: in.WorkspaceID}
		if in.ExpiresDays > 0 {
			g.ExpiresAt = time.Now().Add(time.Duration(in.ExpiresDays) * 24 * time.Hour).UnixMilli()
		}
		if err := s.createGrant(r.Context(), g, levels); err != nil {
			return err
		}
		raw := randToken("")
		d := codeData{RedirectURI: in.RedirectURI, Challenge: in.Challenge, GrantID: g.ID, Resource: in.Resource}
		if err := s.st.Add(r.Context(), "oauth_codes", &code{ID: hash(raw), ClientID: c.ID, UserID: a.User.ID, Data: toJSON(d),
			ExpiresAt: time.Now().Add(CodeTTL).UnixMilli()}); err != nil {
			return err
		}
		srv.AuditR(r, audit.Entry{WorkspaceID: in.WorkspaceID, Action: "mcp.oauth_authorized", Target: c.ID, OK: true, Source: audit.SrcUI},
			map[string]any{"client": c.Name, "grant": g.ID, "levels": levels, "expires_at": g.ExpiresAt})
		return web.OK(w, map[string]any{"redirect": withQuery(in.RedirectURI, map[string]string{"code": raw, "state": in.State, "iss": s.Base(r)})})
	}))
	r.Get("/me/mcp/grants", h(func(w http.ResponseWriter, r *http.Request) error {
		return s.listGrants(w, r, "oauth")
	}))
	r.Delete("/me/mcp/grants/{id}", h(func(w http.ResponseWriter, r *http.Request) error {
		if err := s.RevokeGrant(r.Context(), web.UserOf(r).ID, chi.URLParam(r, "id")); err != nil {
			return apperr.NotFound("授权")
		}
		srv.AuditR(r, audit.Entry{Action: "mcp.grant_revoked", Target: chi.URLParam(r, "id"), OK: true}, nil)
		return web.OK(w, map[string]any{"ok": true})
	}))
	r.Get("/me/mcp/tokens", h(func(w http.ResponseWriter, r *http.Request) error {
		return s.listGrants(w, r, "pat")
	}))
	r.Post("/me/mcp/tokens", h(func(w http.ResponseWriter, r *http.Request) error {
		if err := srv.RequireReauth(r); err != nil {
			return err
		}
		u := web.UserOf(r)
		var in struct {
			Name        string            `json:"name"`
			WorkspaceID string            `json:"workspace_id"`
			Levels      map[string]string `json:"levels"`
			ExpiresDays int               `json:"expires_days"`
		}
		if err := web.ReadJSON(r, &in); err != nil {
			return err
		}
		raw, g, err := s.CreatePAT(r.Context(), u, in.Name, in.WorkspaceID, in.Levels, in.ExpiresDays)
		if err != nil {
			return apperr.Bad("%s", err.Error())
		}
		srv.AuditR(r, audit.Entry{WorkspaceID: in.WorkspaceID, Action: "mcp.token_created", Target: g.ID, OK: true}, map[string]any{"name": g.Name, "levels": g.Levels(), "expires_at": g.ExpiresAt})
		return web.OK(w, map[string]any{"token": raw, "grant": s.grantView(r.Context(), g)})
	}))
	r.Delete("/me/mcp/tokens/{id}", h(func(w http.ResponseWriter, r *http.Request) error {
		if err := s.RevokeGrant(r.Context(), web.UserOf(r).ID, chi.URLParam(r, "id")); err != nil {
			return apperr.NotFound("令牌")
		}
		srv.AuditR(r, audit.Entry{Action: "mcp.token_revoked", Target: chi.URLParam(r, "id"), OK: true}, nil)
		return web.OK(w, map[string]any{"ok": true})
	}))
	r.Get("/me/mcp/choices", h(func(w http.ResponseWriter, r *http.Request) error {
		ws := r.URL.Query().Get("workspace")
		if _, err := srv.RequireWS(r, ws, "viewer"); err != nil {
			return err
		}
		ch, err := s.Choices(r.Context(), web.UserOf(r), ws)
		if err != nil {
			return err
		}
		return web.OK(w, map[string]any{"connections": ch, "levels": mcpLevels})
	}))
	r.Get("/me/mcp/config", h(func(w http.ResponseWriter, r *http.Request) error {
		return web.OK(w, ConfigSnippets(s.Base(r)+"/mcp"))
	}))
}

func (s *Service) grantView(ctx context.Context, g *Grant) map[string]any {
	wsName := ""
	if ws, err := s.acc.Workspace(ctx, g.WorkspaceID); err == nil {
		wsName = ws.Name
	}
	return map[string]any{"id": g.ID, "kind": g.Kind, "name": g.Name, "client_id": g.ClientID, "workspace_id": g.WorkspaceID, "workspace_name": wsName,
		"levels": g.Levels(), "token_hint": g.TokenHint, "expires_at": g.ExpiresAt, "created_at": g.CreatedAt, "last_used_at": g.LastUsedAt}
}

func (s *Service) listGrants(w http.ResponseWriter, r *http.Request, kind string) error {
	list, err := s.Grants(r.Context(), web.UserOf(r).ID, kind)
	if err != nil {
		return err
	}
	out := make([]map[string]any, 0, len(list))
	now := store.Now()
	for i := range list {
		g := &list[i]
		v := s.grantView(r.Context(), g)
		v["expired"] = g.ExpiresAt > 0 && g.ExpiresAt < now
		out = append(out, v)
	}
	key := "grants"
	if kind == "pat" {
		key = "tokens"
	}
	return web.OK(w, map[string]any{key: out})
}

// ConfigSnippets 各客户端的 MCP 配置片段（设置页一键复制）。
func ConfigSnippets(mcpURL string) map[string]any {
	return map[string]any{
		"url": mcpURL,
		"clients": []map[string]any{
			{"id": "claude-code", "name": "Claude Code", "format": "shell",
				"oauth": "claude mcp add --transport http lemi " + mcpURL,
				"token": "claude mcp add --transport http lemi " + mcpURL + " --header \"Authorization: Bearer <访问令牌>\""},
			{"id": "cursor", "name": "Cursor", "format": "json", "file": "~/.cursor/mcp.json",
				"oauth": `{"mcpServers":{"lemi":{"url":"` + mcpURL + `"}}}`,
				"token": `{"mcpServers":{"lemi":{"url":"` + mcpURL + `","headers":{"Authorization":"Bearer <访问令牌>"}}}}`},
			{"id": "codex", "name": "Codex", "format": "toml", "file": "~/.codex/config.toml",
				"oauth": "[mcp_servers.lemi]\nurl = \"" + mcpURL + "\"",
				"token": "[mcp_servers.lemi]\nurl = \"" + mcpURL + "\"\nbearer_token_env_var = \"LEMI_TOKEN\""},
			{"id": "claude-desktop", "name": "Claude Desktop", "format": "text",
				"oauth": "设置 → 连接器 → 添加自定义连接器，地址填 " + mcpURL + "，按提示在浏览器里登录 Lemi 并授权",
				"token": `{"mcpServers":{"lemi":{"command":"npx","args":["-y","mcp-remote","` + mcpURL + `","--header","Authorization: Bearer <访问令牌>"]}}}`},
		},
	}
}

// ---------- 换令牌 ----------

func (s *Service) tokenEndpoint(w http.ResponseWriter, r *http.Request) {
	if !ipLimit.allow("tok:"+web.ClientIP(r), 60, time.Minute) {
		oauthErr(w, 429, "slow_down", "请求过于频繁")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		oauthErr(w, 400, "invalid_request", "请求格式不对")
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		s.exchangeCode(w, r)
	case "refresh_token":
		s.refresh(w, r)
	default:
		oauthErr(w, 400, "unsupported_grant_type", "只支持 authorization_code 与 refresh_token")
	}
}

func pkceOK(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	got := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(got), []byte(challenge)) == 1
}

func (s *Service) exchangeCode(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f := r.PostForm
	raw, clientID := f.Get("code"), f.Get("client_id")
	var c code
	if err := s.st.GetW(ctx, &c, `SELECT * FROM oauth_codes WHERE id = ?`, hash(raw)); err != nil {
		// 授权码可能已经用过：按 RFC 6749 §4.1.2，把用它换出的令牌全部作废
		s.revokeByCode(ctx, hash(raw))
		oauthErr(w, 400, "invalid_grant", "授权码无效或已使用")
		return
	}
	// 一次性：先删再用
	res, err := s.st.Exec(ctx, `DELETE FROM oauth_codes WHERE id = ?`, c.ID)
	if err != nil {
		oauthErr(w, 400, "invalid_grant", "授权码无效或已使用")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		oauthErr(w, 400, "invalid_grant", "授权码无效或已使用")
		return
	}
	var d codeData
	_ = json.Unmarshal([]byte(c.Data), &d)
	if c.ExpiresAt < store.Now() || c.ClientID != clientID || d.RedirectURI != f.Get("redirect_uri") || !pkceOK(f.Get("code_verifier"), d.Challenge) {
		oauthErr(w, 400, "invalid_grant", "授权码、回调地址或 code_verifier 不匹配")
		return
	}
	s.issue(w, r, c.ClientID, c.UserID, d.GrantID, ids.New(), c.ID)
}

func (s *Service) revokeByCode(ctx context.Context, codeHash string) {
	var fam []string
	_ = s.st.Select(ctx, &fam, `SELECT DISTINCT family FROM oauth_tokens WHERE code_id = ?`, codeHash)
	for _, f := range fam {
		s.revokeFamily(ctx, f)
	}
}

func (s *Service) revokeFamily(ctx context.Context, family string) {
	_, _ = s.st.Exec(ctx, `UPDATE oauth_tokens SET revoked_at = ? WHERE family = ? AND revoked_at = 0`, store.Now(), family)
}

func (s *Service) refresh(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	raw := r.PostForm.Get("refresh_token")
	var t token
	if !strings.HasPrefix(raw, RefreshPrefix) || s.st.GetW(ctx, &t, `SELECT * FROM oauth_tokens WHERE id = ? AND kind = 'refresh'`, hash(raw)) != nil {
		oauthErr(w, 400, "invalid_grant", "刷新令牌无效")
		return
	}
	if t.RevokedAt != 0 {
		s.revokeFamily(ctx, t.Family) // 用过的刷新令牌又被拿来用：可能泄露，整条链作废
		oauthErr(w, 400, "invalid_grant", "刷新令牌已失效")
		return
	}
	if t.ExpiresAt < store.Now() || t.ClientID != r.PostForm.Get("client_id") {
		oauthErr(w, 400, "invalid_grant", "刷新令牌无效")
		return
	}
	var g Grant
	if s.st.GetW(ctx, &g, `SELECT * FROM mcp_grants WHERE id = ?`, t.GrantID) != nil || g.RevokedAt != 0 || (g.ExpiresAt > 0 && g.ExpiresAt < store.Now()) {
		oauthErr(w, 400, "invalid_grant", "授权已吊销或已过期")
		return
	}
	u, err := s.acc.UserByID(ctx, t.UserID)
	if err != nil || u.Status != "active" {
		oauthErr(w, 400, "invalid_grant", "账号已停用")
		return
	}
	res, err := s.st.Exec(ctx, `UPDATE oauth_tokens SET revoked_at = ? WHERE id = ? AND revoked_at = 0`, store.Now(), t.ID)
	if err != nil {
		oauthErr(w, 400, "invalid_grant", "刷新令牌已失效")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		oauthErr(w, 400, "invalid_grant", "刷新令牌已失效")
		return
	}
	s.issue(w, r, t.ClientID, t.UserID, t.GrantID, t.Family, t.CodeID)
}

func (s *Service) issue(w http.ResponseWriter, r *http.Request, clientID, userID, grantID, family, codeID string) {
	ctx := r.Context()
	now := time.Now()
	var g Grant
	if s.st.GetW(ctx, &g, `SELECT * FROM mcp_grants WHERE id = ?`, grantID) != nil || g.RevokedAt != 0 {
		oauthErr(w, 400, "invalid_grant", "授权已吊销")
		return
	}
	accessExp, refreshExp := now.Add(AccessTTL).UnixMilli(), now.Add(RefreshTTL).UnixMilli()
	if g.ExpiresAt > 0 {
		accessExp, refreshExp = min(accessExp, g.ExpiresAt), min(refreshExp, g.ExpiresAt)
	}
	access, refresh := randToken(AccessPrefix), randToken(RefreshPrefix)
	for _, t := range []token{
		{ID: hash(access), Kind: "access", ClientID: clientID, UserID: userID, GrantID: grantID, Family: family, CodeID: codeID, ExpiresAt: accessExp, CreatedAt: now.UnixMilli()},
		{ID: hash(refresh), Kind: "refresh", ClientID: clientID, UserID: userID, GrantID: grantID, Family: family, CodeID: codeID, ExpiresAt: refreshExp, CreatedAt: now.UnixMilli()},
	} {
		if err := s.st.Add(ctx, "oauth_tokens", &t); err != nil {
			oauthErr(w, 500, "server_error", "签发令牌失败")
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	web.WriteJSON(w, 200, map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": int((time.Duration(accessExp-now.UnixMilli()) * time.Millisecond).Seconds()),
		"refresh_token": refresh, "scope": ScopeMCP})
}

// revoke 撤销令牌（RFC 7009）：不管令牌存不存在都回 200。
func (s *Service) revoke(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	_ = r.ParseForm()
	raw := r.PostForm.Get("token")
	var t token
	if s.st.GetW(r.Context(), &t, `SELECT * FROM oauth_tokens WHERE id = ?`, hash(raw)) == nil {
		if t.Kind == "refresh" {
			s.revokeFamily(r.Context(), t.Family)
		} else {
			_, _ = s.st.Exec(r.Context(), `UPDATE oauth_tokens SET revoked_at = ? WHERE id = ?`, store.Now(), t.ID)
		}
	}
	w.WriteHeader(200)
}

// Cleanup 清理过期的授权码与令牌。
func (s *Service) Cleanup(ctx context.Context) {
	now := store.Now()
	_, _ = s.st.Exec(ctx, `DELETE FROM oauth_codes WHERE expires_at < ?`, now)
	_, _ = s.st.Exec(ctx, `DELETE FROM oauth_tokens WHERE expires_at < ?`, now-24*3600*1000)
}
