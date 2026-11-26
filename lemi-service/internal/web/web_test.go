package web_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
	"github.com/LemonITCN/lemi/lemi-service/internal/auth"
	"github.com/LemonITCN/lemi/lemi-service/internal/config"
	"github.com/LemonITCN/lemi/lemi-service/internal/crypt"
	"github.com/LemonITCN/lemi/lemi-service/internal/realtime"
	"github.com/LemonITCN/lemi/lemi-service/internal/settings"
	"github.com/LemonITCN/lemi/lemi-service/internal/store"
	"github.com/LemonITCN/lemi/lemi-service/internal/web"
)

type env struct {
	srv   *httptest.Server
	token string
	acc   *accounts.Service
	st    *store.Store
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "lemi.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	key := make([]byte, 32)
	key[0] = 7
	box, _ := crypt.New(key)
	acc := accounts.New(st)
	set := settings.New(st)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := &env{token: "setup-token-for-tests-0123456789abcdef", acc: acc, st: st}
	s := web.NewServer(web.Deps{Log: log, Cfg: &config.Config{DataDir: dir}, Store: st, Auth: auth.New(st, acc, set, box), Accounts: acc, Settings: set,
		Audit: audit.New(st, log), Hub: realtime.New(log), Box: box, Started: time.Now(),
		SetupToken: func() string { return e.token }, OnSetupDone: func() { e.token = "" }})
	e.srv = httptest.NewServer(s.Handler())
	t.Cleanup(e.srv.Close)
	return e
}

type client struct {
	t    *testing.T
	c    *http.Client
	base string
	csrf string
}

func (e *env) client(t *testing.T) *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: t, c: &http.Client{Jar: jar}, base: e.srv.URL}
}

func (c *client) do(method, path string, body any, hdr map[string]string) (int, map[string]any) {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, c.base+path, r)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", c.base)
	if c.csrf != "" {
		req.Header.Set("X-Lemi-CSRF", c.csrf)
	}
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := c.c.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if s, ok := out["csrf"].(string); ok && s != "" {
		c.csrf = s
	}
	return resp.StatusCode, out
}

func code(m map[string]any) string {
	if e, ok := m["error"].(map[string]any); ok {
		s, _ := e["code"].(string)
		return s
	}
	return ""
}

func TestSetupTokenRequired(t *testing.T) {
	e := newEnv(t)
	c := e.client(t)
	st, out := c.do("POST", "/api/v1/setup", map[string]any{"token": "wrong", "username": "admin", "password": "Lemi-Strong-2026"}, nil)
	if st != 403 || code(out) != "bad_setup_token" {
		t.Fatalf("错误令牌应被拒绝：%d %v", st, out)
	}
	st, out = c.do("POST", "/api/v1/setup", map[string]any{"token": e.token, "username": "admin", "password": "short"}, nil)
	if st != 400 {
		t.Fatalf("弱口令应被拒绝：%d %v", st, out)
	}
	st, out = c.do("POST", "/api/v1/setup", map[string]any{"token": e.token, "username": "admin", "password": "Lemi-Strong-2026"}, nil)
	if st != 200 || out["stage"] != "totp_setup" {
		t.Fatalf("初始化应成功且进入绑定两步验证阶段：%d %v", st, out)
	}
	// 绑定前只能访问绑定接口
	st, out = c.do("GET", "/api/v1/workspaces", nil, nil)
	if st != 403 || code(out) != "totp_setup_required" {
		t.Fatalf("未绑定两步验证不能访问其他接口：%d %v", st, out)
	}
	// 再次初始化被拒绝
	c2 := e.client(t)
	st, _ = c2.do("POST", "/api/v1/setup", map[string]any{"token": "x", "username": "evil", "password": "Lemi-Strong-2026"}, nil)
	if st != 409 {
		t.Fatalf("已初始化后不能再初始化：%d", st)
	}
}

// login 走完 初始化 → 绑定两步验证，返回已登录客户端与 TOTP 密钥。
func login(t *testing.T, e *env) (*client, string) {
	c := e.client(t)
	if st, out := c.do("POST", "/api/v1/setup", map[string]any{"token": e.token, "username": "admin", "password": "Lemi-Strong-2026"}, nil); st != 200 {
		t.Fatalf("初始化失败：%v", out)
	}
	_, out := c.do("POST", "/api/v1/auth/totp/begin", nil, nil)
	secret, _ := out["secret"].(string)
	st, out := c.do("POST", "/api/v1/auth/totp/confirm", map[string]any{"code": auth.CodeAt(secret, time.Now())}, nil)
	if st != 200 || out["stage"] != "done" {
		t.Fatalf("绑定两步验证失败：%d %v", st, out)
	}
	return c, secret
}

func TestCSRFAndOrigin(t *testing.T) {
	e := newEnv(t)
	c, _ := login(t, e)
	// 正常
	if st, _ := c.do("POST", "/api/v1/workspaces", map[string]any{"name": "a"}, nil); st != 200 {
		t.Fatalf("带 CSRF 令牌应成功：%d", st)
	}
	// 缺 CSRF 令牌
	if st, out := c.do("POST", "/api/v1/workspaces", map[string]any{"name": "b"}, map[string]string{"X-Lemi-CSRF": ""}); st != 403 || code(out) != "csrf" {
		t.Fatalf("缺 CSRF 令牌应拒绝：%d %v", st, out)
	}
	// 错误的令牌
	if st, _ := c.do("POST", "/api/v1/workspaces", map[string]any{"name": "b"}, map[string]string{"X-Lemi-CSRF": "nope"}); st != 403 {
		t.Fatalf("错误 CSRF 令牌应拒绝：%d", st)
	}
	// 跨站来源
	if st, out := c.do("POST", "/api/v1/workspaces", map[string]any{"name": "b"}, map[string]string{"Origin": "https://evil.example"}); st != 403 || code(out) != "csrf" {
		t.Fatalf("跨站来源应拒绝：%d %v", st, out)
	}
	// 未登录的登录请求也校验来源（防登录 CSRF）
	c2 := e.client(t)
	if st, _ := c2.do("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": "x"}, map[string]string{"Origin": "https://evil.example"}); st != 403 {
		t.Fatalf("跨站登录请求应拒绝：%d", st)
	}
}

func TestLoginLockoutAndMFA(t *testing.T) {
	e := newEnv(t)
	_, secret := login(t, e)
	c := e.client(t)
	for i := 0; i < 5; i++ {
		st, out := c.do("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": "wrong-password-x"}, nil)
		if st != 401 || code(out) != "bad_credentials" {
			t.Fatalf("第 %d 次错误口令：%d %v", i+1, st, out)
		}
	}
	st, out := c.do("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": "Lemi-Strong-2026"}, nil)
	if st != 429 || code(out) != "too_many_attempts" {
		t.Fatalf("连续失败 5 次后应锁定（即使口令正确）：%d %v", st, out)
	}
	// 解锁后：口令正确 → 需要两步验证
	ctx := context.Background()
	_, _ = e.st.Exec(ctx, `DELETE FROM login_attempts`)
	st, out = c.do("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": "Lemi-Strong-2026"}, nil)
	if st != 200 || out["stage"] != "mfa" {
		t.Fatalf("应进入两步验证阶段：%d %v", st, out)
	}
	if st, _ := c.do("GET", "/api/v1/workspaces", nil, nil); st != 401 {
		t.Fatalf("两步验证前不能访问接口：%d", st)
	}
	// 用户名不存在与口令错误的回应一致
	c3 := e.client(t)
	_, o1 := c3.do("POST", "/api/v1/auth/login", map[string]any{"username": "nobody", "password": "x"}, nil)
	if code(o1) != "bad_credentials" {
		t.Fatalf("不存在的用户名也应回「用户名或口令不正确」：%v", o1)
	}
	// 验证码：同一个码不能用两次（防重放）
	now := time.Now()
	codeNow := auth.CodeAt(secret, now.Add(30*time.Second))
	st, out = c.do("POST", "/api/v1/auth/mfa", map[string]any{"code": codeNow}, nil)
	if st != 200 || out["stage"] != "done" {
		t.Fatalf("验证码应通过：%d %v", st, out)
	}
	c4 := e.client(t)
	c4.do("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": "Lemi-Strong-2026"}, nil)
	if st, out := c4.do("POST", "/api/v1/auth/mfa", map[string]any{"code": codeNow}, nil); st == 200 {
		t.Fatalf("同一个验证码第二次使用应被拒绝：%v", out)
	}
}

func TestSecurityHeadersAndStatic(t *testing.T) {
	e := newEnv(t)
	resp, err := http.Get(e.srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	h := resp.Header
	for k, want := range map[string]string{"X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY", "Referrer-Policy": "no-referrer"} {
		if h.Get(k) != want {
			t.Errorf("%s = %q，应为 %q", k, h.Get(k), want)
		}
	}
	csp := h.Get("Content-Security-Policy")
	for _, part := range []string{"default-src 'self'", "frame-ancestors 'none'", "object-src 'none'"} {
		if !strings.Contains(csp, part) {
			t.Errorf("CSP 缺少 %s：%s", part, csp)
		}
	}
	if strings.Contains(csp, "unsafe-eval") {
		t.Error("CSP 不应允许 unsafe-eval")
	}
	// 接口不存在时返回 JSON 404，不回落到前端页面
	resp, _ = http.Get(e.srv.URL + "/api/v1/nope")
	if resp.StatusCode != 404 && resp.StatusCode != 401 {
		t.Errorf("不存在的接口应 404/401，实际 %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestRoleEnforcement(t *testing.T) {
	e := newEnv(t)
	c, _ := login(t, e)
	_, out := c.do("GET", "/api/v1/workspaces", nil, nil)
	wsList := out["workspaces"].([]any)
	ws := wsList[0].(map[string]any)["id"].(string)
	// 管理员创建一个普通用户（临时口令）
	st, out := c.do("POST", "/api/v1/admin/users", map[string]any{"username": "bob"}, nil)
	if st != 200 {
		t.Fatalf("创建用户失败：%v", out)
	}
	tmp := out["temp_password"].(string)
	b := e.client(t)
	st, out = b.do("POST", "/api/v1/auth/login", map[string]any{"username": "bob", "password": tmp}, nil)
	if st != 200 {
		t.Fatalf("临时口令登录失败：%v", out)
	}
	// 默认策略要求所有人绑定两步验证
	if out["stage"] != "totp_setup" {
		t.Fatalf("普通成员默认也必须绑定两步验证：%v", out)
	}
	_, o := b.do("POST", "/api/v1/auth/totp/begin", nil, nil)
	b.do("POST", "/api/v1/auth/totp/confirm", map[string]any{"code": auth.CodeAt(o["secret"].(string), time.Now())}, nil)
	// 必须先改口令
	if st, out := b.do("GET", "/api/v1/workspaces", nil, nil); st != 403 || code(out) != "must_change_password" {
		t.Fatalf("临时口令登录后必须先改口令：%d %v", st, out)
	}
	if st, out := b.do("POST", "/api/v1/me/password", map[string]any{"current": tmp, "new": "Bob-Strong-Pass-1"}, nil); st != 200 {
		t.Fatalf("改口令失败：%v", out)
	}
	// 普通用户不是空间成员：看不到空间
	if st, _ := b.do("GET", "/api/v1/workspaces/"+ws, nil, nil); st != 404 {
		t.Fatalf("非成员访问空间应 404：%d", st)
	}
	// 不能访问管理接口
	if st, _ := b.do("GET", "/api/v1/admin/users", nil, nil); st != 403 {
		t.Fatalf("普通用户不能访问管理接口：%d", st)
	}
	// 加为查看者：能看不能改
	var bob accounts.User
	_ = e.st.Get(context.Background(), &bob, `SELECT * FROM users WHERE username = 'bob'`)
	c.do("PUT", "/api/v1/workspaces/"+ws+"/members/"+bob.ID, map[string]any{"role": "viewer"}, nil)
	if st, _ := b.do("GET", "/api/v1/workspaces/"+ws, nil, nil); st != 200 {
		t.Fatalf("查看者应能看到空间：%d", st)
	}
	if st, _ := b.do("PATCH", "/api/v1/workspaces/"+ws, map[string]any{"name": "x"}, nil); st != 403 {
		t.Fatalf("查看者不能改空间：%d", st)
	}
	// 敏感操作需要二次验证（管理员删除用户：登录 10 分钟内算已验证，这里伪造过期）
	_, _ = e.st.Exec(context.Background(), `UPDATE sessions SET reauth_at = 0`)
	if st, out := c.do("DELETE", "/api/v1/admin/users/"+bob.ID, nil, nil); st != 403 || code(out) != "reauth_required" {
		t.Fatalf("删除用户应要求二次验证：%d %v", st, out)
	}
	if st, _ := c.do("POST", "/api/v1/me/reauth", map[string]any{"password": "Lemi-Strong-2026"}, nil); st != 200 {
		t.Fatal("二次验证失败")
	}
	if st, out := c.do("DELETE", "/api/v1/admin/users/"+bob.ID, nil, nil); st != 200 {
		t.Fatalf("二次验证后应能删除：%d %v", st, out)
	}
}
