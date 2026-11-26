package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/aitest"
	"github.com/LemonITCN/lemi/lemi-service/internal/conns"
	"github.com/LemonITCN/lemi/lemi-service/internal/ids"
	"github.com/go-chi/chi/v5"
)

func TestRedirectRules(t *testing.T) {
	for u, ok := range map[string]bool{
		"https://app.example/cb": true, "http://localhost:3333/callback": true, "http://127.0.0.1/cb": true, "cursor://anysphere/oauth": true,
		"http://evil.example/cb": false, "javascript:alert(1)": false, "data:text/html,x": false, "https://a.example/cb#frag": false, "/relative": false,
	} {
		if got := validRedirect(u) == nil; got != ok {
			t.Errorf("%s: %v 应为 %v", u, got, ok)
		}
	}
	reg := []string{"http://localhost:1234/callback", "https://app.example/cb"}
	for u, ok := range map[string]bool{
		"http://localhost:5555/callback": true, "http://localhost:5555/other": false, "https://app.example/cb": true,
		"https://app.example/cb2": false, "http://127.0.0.1:1234/callback": false,
	} {
		if got := redirectMatches(reg, u); got != ok {
			t.Errorf("匹配 %s: %v 应为 %v", u, got, ok)
		}
	}
}

func newSvc(t *testing.T) (*Service, *aitest.Fixture) {
	f := aitest.New(t)
	return New(f.Store, f.Accounts, f.Conns, f.Settings, f.Log, func(*http.Request) string { return "http://lemi.test" }), f
}

func TestGrantLevelCaps(t *testing.T) {
	s, f := newSvc(t)
	ctx := context.Background()
	wc := f.SQLite(t, "wc", conns.AIWriteConfirm, nil)
	wa := f.SQLite(t, "wa", conns.AIWriteAuto, nil)
	hid := f.SQLite(t, "hid", conns.AIHidden, nil)
	ch, err := s.Choices(ctx, f.Owner, f.WS)
	if err != nil {
		t.Fatal(err)
	}
	max := map[string]string{}
	for _, c := range ch {
		max[c.ID] = c.MaxLevel
	}
	if max[wc.ID] != conns.AIRead || max[wa.ID] != conns.AIWriteAuto {
		t.Fatalf("逐条确认档的连接在 MCP 里最高只读：%v", max)
	}
	if _, ok := max[hid.ID]; ok {
		t.Fatal("不可见的连接不应出现在授权页")
	}
	if _, err := s.CheckLevels(ctx, f.Owner, f.WS, map[string]string{wa.ID: conns.AIFull}); err == nil {
		t.Fatal("授权档位不能高于连接档位")
	}
	if _, err := s.CheckLevels(ctx, f.Owner, f.WS, map[string]string{wc.ID: conns.AIWriteConfirm}); err == nil {
		t.Fatal("MCP 授权不能选逐条确认")
	}
	if _, err := s.CheckLevels(ctx, f.Owner, f.WS, map[string]string{hid.ID: conns.AIRead}); err == nil {
		t.Fatal("不可见的连接不能授权")
	}
	g, err := s.CheckLevels(ctx, f.Owner, f.WS, map[string]string{wa.ID: conns.AIWriteAuto, wc.ID: conns.AIRead})
	if err != nil || g[wa.ID] != conns.AIWriteAuto {
		t.Fatalf("合法的授权应通过：%v %v", g, err)
	}
	// 查看者：连接档位再高也最多只读
	ch, _ = s.Choices(ctx, f.Viewer, f.WS)
	for _, c := range ch {
		if c.MaxLevel != conns.AIRead {
			t.Fatalf("查看者在 MCP 授权里最多只读：%+v", c)
		}
	}
	// 个人访问令牌：只存哈希，吊销立刻失效
	raw, gr, err := s.CreatePAT(ctx, f.Owner, "测试令牌", f.WS, map[string]string{wa.ID: conns.AIRead}, 30)
	if err != nil || !strings.HasPrefix(raw, PATPrefix) || gr.TokenHash == raw {
		t.Fatalf("创建令牌失败：%v", err)
	}
	id, err := s.Lookup(ctx, raw)
	if err != nil || id.User.ID != f.Owner.ID || id.Grant.Levels()[wa.ID] != conns.AIRead {
		t.Fatalf("令牌校验不对：%v", err)
	}
	if err := s.RevokeGrant(ctx, f.Owner.ID, gr.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Lookup(ctx, raw); err == nil {
		t.Fatal("吊销后令牌应失效")
	}
	// 过期
	raw, gr, _ = s.CreatePAT(ctx, f.Owner, "过期令牌", f.WS, map[string]string{wa.ID: conns.AIRead}, 1)
	_, _ = f.Store.Exec(ctx, `UPDATE mcp_grants SET expires_at = ? WHERE id = ?`, time.Now().Add(-time.Hour).UnixMilli(), gr.ID)
	if _, err := s.Lookup(ctx, raw); err == nil {
		t.Fatal("过期的令牌应失效")
	}
}

func TestTokenFlow(t *testing.T) {
	s, f := newSvc(t)
	ctx := context.Background()
	c := f.SQLite(t, "db", conns.AIWriteAuto, nil)
	r := chi.NewRouter()
	s.Public(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/oauth/register", "application/json", strings.NewReader(`{"client_name":"测试客户端","redirect_uris":["http://localhost:9999/callback"]}`))
	if err != nil || resp.StatusCode != 201 {
		t.Fatalf("注册失败 %v %v", err, resp.Status)
	}
	var reg map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&reg)
	cid := reg["client_id"].(string)
	if resp, _ := http.Post(srv.URL+"/oauth/register", "application/json", strings.NewReader(`{"redirect_uris":["http://evil.example/cb"]}`)); resp.StatusCode != 400 {
		t.Fatal("非本机的 http 回调应拒绝")
	}
	// /oauth/authorize 跳到前端确认页，参数原样带过去
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, _ = cl.Get(srv.URL + "/oauth/authorize?client_id=" + cid + "&state=s1")
	if resp.StatusCode != 302 || !strings.HasPrefix(resp.Header.Get("Location"), ConsentPath+"?client_id=") {
		t.Fatalf("授权端点应跳到确认页：%d %s", resp.StatusCode, resp.Header.Get("Location"))
	}

	grant := func() string {
		g := &Grant{ID: ids.New(), UserID: f.Owner.ID, Kind: "oauth", ClientID: cid, Name: "测试客户端", WorkspaceID: f.WS}
		if err := s.createGrant(ctx, g, map[string]string{c.ID: conns.AIRead}); err != nil {
			t.Fatal(err)
		}
		return g.ID
	}
	verifier := strings.Repeat("v", 50)
	sum := sha256.Sum256([]byte(verifier))
	ch := base64.RawURLEncoding.EncodeToString(sum[:])
	newCode := func(gid string) string {
		raw := randToken("")
		d := codeData{RedirectURI: "http://localhost:4444/callback", Challenge: ch, GrantID: gid}
		if err := s.st.Add(ctx, "oauth_codes", &code{ID: hash(raw), ClientID: cid, UserID: f.Owner.ID, Data: toJSON(d), ExpiresAt: time.Now().Add(time.Minute).UnixMilli()}); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	post := func(form url.Values) (int, map[string]any) {
		resp, err := http.PostForm(srv.URL+"/oauth/token", form)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&m)
		return resp.StatusCode, m
	}
	gid := grant()
	raw := newCode(gid)
	if code, _ := post(url.Values{"grant_type": {"authorization_code"}, "code": {raw}, "client_id": {cid}, "redirect_uri": {"http://localhost:4444/callback"}, "code_verifier": {strings.Repeat("x", 50)}}); code != 400 {
		t.Fatal("verifier 不对应拒绝")
	}
	raw = newCode(gid)
	code, tok := post(url.Values{"grant_type": {"authorization_code"}, "code": {raw}, "client_id": {cid}, "redirect_uri": {"http://localhost:4444/callback"}, "code_verifier": {verifier}})
	if code != 200 || tok["access_token"] == nil {
		t.Fatalf("换令牌失败 %d %v", code, tok)
	}
	id, err := s.Lookup(ctx, tok["access_token"].(string))
	if err != nil || id.User.ID != f.Owner.ID || id.Grant.WorkspaceID != f.WS || id.Grant.Levels()[c.ID] != conns.AIRead {
		t.Fatalf("访问令牌校验不对：%v", err)
	}
	// 授权码重放：拒绝，并作废已发出的令牌
	if code, _ := post(url.Values{"grant_type": {"authorization_code"}, "code": {raw}, "client_id": {cid}, "redirect_uri": {"http://localhost:4444/callback"}, "code_verifier": {verifier}}); code != 400 {
		t.Fatal("授权码不能用两次")
	}
	if _, err := s.Lookup(ctx, tok["access_token"].(string)); err == nil {
		t.Fatal("授权码重放后应作废已发出的令牌")
	}
	// 刷新轮换与重用检测
	raw = newCode(gid)
	_, tok = post(url.Values{"grant_type": {"authorization_code"}, "code": {raw}, "client_id": {cid}, "redirect_uri": {"http://localhost:4444/callback"}, "code_verifier": {verifier}})
	r1 := tok["refresh_token"].(string)
	code, tok2 := post(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {r1}, "client_id": {cid}})
	if code != 200 || tok2["refresh_token"] == r1 {
		t.Fatal("刷新应换新的刷新令牌")
	}
	if _, err := s.Lookup(ctx, tok2["access_token"].(string)); err != nil {
		t.Fatal("新访问令牌应可用")
	}
	if code, _ := post(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {r1}, "client_id": {cid}}); code != 400 {
		t.Fatal("旧刷新令牌不能再用")
	}
	if _, err := s.Lookup(ctx, tok2["access_token"].(string)); err == nil {
		t.Fatal("旧刷新令牌被重用后整条链应作废")
	}
	// 吊销授权：令牌全部失效，刷新也不行
	raw = newCode(gid)
	_, tok = post(url.Values{"grant_type": {"authorization_code"}, "code": {raw}, "client_id": {cid}, "redirect_uri": {"http://localhost:4444/callback"}, "code_verifier": {verifier}})
	if err := s.RevokeGrant(ctx, f.Owner.ID, gid); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Lookup(ctx, tok["access_token"].(string)); err == nil {
		t.Fatal("吊销授权后访问令牌应失效")
	}
	if code, _ := post(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tok["refresh_token"].(string)}, "client_id": {cid}}); code != 400 {
		t.Fatal("吊销授权后不能刷新")
	}
	// 元数据
	for _, p := range []string{"/.well-known/oauth-authorization-server", "/.well-known/oauth-protected-resource"} {
		resp, _ = http.Get(srv.URL + p)
		var meta map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&meta)
		if meta["token_endpoint"] != "http://lemi.test/oauth/token" && meta["resource"] != "http://lemi.test/mcp" {
			t.Fatalf("元数据不对：%v", meta)
		}
	}
}
