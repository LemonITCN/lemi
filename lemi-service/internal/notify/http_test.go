package notify

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/auth"
	"github.com/LemonITCN/lemi/lemi-service/internal/config"
	"github.com/LemonITCN/lemi/lemi-service/internal/realtime"
	"github.com/LemonITCN/lemi/lemi-service/internal/web"
)

type apiClient struct {
	t    *testing.T
	base string
	tok  string
	csrf string
}

func (c *apiClient) do(method, path string, body any) (int, map[string]any) {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, c.base+"/api/v1"+path, r)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", c.base)
	req.Header.Set("X-Lemi-CSRF", c.csrf)
	req.AddCookie(&http.Cookie{Name: web.SessionCookie, Value: c.tok})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func newAPI(t *testing.T, f *fixture) (*httptest.Server, func(u *accounts.User) *apiClient) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	au := auth.New(f.st, f.acc, f.set, f.box)
	srv := web.NewServer(web.Deps{Log: log, Cfg: &config.Config{DataDir: f.dir}, Store: f.st, Auth: au, Accounts: f.acc, Settings: f.set,
		Audit: f.au, Hub: realtime.New(log), Box: f.box, Started: time.Now(), SetupToken: func() string { return "" }, OnSetupDone: func() {}})
	srv.AddMount(f.s.Routes)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	login := func(u *accounts.User) *apiClient {
		tok, sess, err := au.CreateSession(context.Background(), u, auth.StageDone, "127.0.0.1", "test")
		if err != nil {
			t.Fatal(err)
		}
		return &apiClient{t: t, base: ts.URL, tok: tok, csrf: sess.CSRF}
	}
	return ts, login
}

func TestHTTPChannels(t *testing.T) {
	f := setup(t)
	f.allowPrivate(t, true)
	ctx := context.Background()
	viewerU := &accounts.User{Username: "viewer", PasswordHash: "x"}
	adminU := &accounts.User{Username: "root", PasswordHash: "x", Role: accounts.RoleAdmin, Email: "root@example.com"}
	for _, u := range []*accounts.User{viewerU, adminU} {
		if err := f.acc.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.acc.SetMember(ctx, f.ws, viewerU.ID, accounts.WSViewer); err != nil {
		t.Fatal(err)
	}
	_, login := newAPI(t, f)
	owner, viewer, admin := login(f.owner), login(viewerU), login(adminU)
	rc := newRecorder(t, nil)
	base := "/workspaces/" + f.ws + "/notify"

	code, out := viewer.do("GET", base+"/kinds", nil)
	if code != 200 || len(out["kinds"].([]any)) != 5 || len(out["events"].([]any)) == 0 {
		t.Fatalf("kinds：%d %v", code, out)
	}
	body := map[string]any{"name": "运维群", "kind": "webhook", "config": map[string]any{"url": rc.srv.URL + "/h?token=HTTPSECRET", "sign_secret": "S"}}
	if code, _ := viewer.do("POST", base+"/channels", body); code != 403 {
		t.Fatalf("查看者不能创建渠道：%d", code)
	}
	code, out = owner.do("POST", base+"/channels", body)
	if code != 200 {
		t.Fatalf("创建渠道：%d %v", code, out)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "HTTPSECRET") || strings.Contains(string(raw), "enc:v1") {
		t.Fatalf("接口不应返回密钥：%s", raw)
	}
	id := out["id"].(string)
	code, out = viewer.do("GET", base+"/channels", nil)
	if code != 200 || len(out["channels"].([]any)) != 1 {
		t.Fatalf("查看者应能列出渠道：%d %v", code, out)
	}
	if _, has := out["channels"].([]any)[0].(map[string]any)["config"]; has {
		t.Fatal("查看者不应看到渠道配置")
	}
	if code, out = owner.do("GET", base+"/channels/"+id, nil); code != 200 || out["config"] == nil {
		t.Fatalf("管理员应看到配置：%d %v", code, out)
	}
	if code, _ := viewer.do("POST", base+"/channels/"+id+"/test", nil); code != 403 {
		t.Fatal("查看者不能测试发送")
	}
	if code, out = owner.do("POST", base+"/channels/"+id+"/test", nil); code != 200 || out["ok"] != true {
		t.Fatalf("测试发送：%d %v", code, out)
	}
	// 保存前测试：带 id 沿用已保存的地址与密钥
	draft := map[string]any{"id": id, "name": "草稿", "kind": "webhook", "config": map[string]any{"method": "POST"}}
	if code, out = owner.do("POST", base+"/channels/test", draft); code != 200 {
		t.Fatalf("草稿测试：%d %v", code, out)
	}
	reqs := rc.all()
	if len(reqs) != 2 || reqs[1].URL.Query().Get("token") != "HTTPSECRET" || reqs[1].Header.Get("X-Lemi-Signature") == "" {
		t.Fatalf("草稿测试应沿用已保存的地址与签名密钥：%d", len(reqs))
	}
	// 草稿测试失败时返回可读错误
	bad := map[string]any{"name": "坏", "kind": "webhook", "config": map[string]any{"url": "http://127.0.0.1:1/x"}}
	if code, out = owner.do("POST", base+"/channels/test", bad); code != 400 || out["error"].(map[string]any)["code"] != "notify_failed" {
		t.Fatalf("发送失败应返回 notify_failed：%d %v", code, out)
	}
	if code, out = owner.do("PUT", base+"/channels/"+id, map[string]any{"name": "改名", "kind": "webhook", "enabled": false, "config": map[string]any{}}); code != 200 || out["enabled"] != false {
		t.Fatalf("修改渠道：%d %v", code, out)
	}
	code, out = viewer.do("GET", base+"/logs?limit=2", nil)
	if code != 200 || len(out["logs"].([]any)) != 2 || out["next_cursor"] == "" {
		t.Fatalf("发送记录分页：%d %v", code, out)
	}
	code, out = viewer.do("GET", base+"/logs?limit=2&cursor="+out["next_cursor"].(string), nil)
	if code != 200 || len(out["logs"].([]any)) != 1 {
		t.Fatalf("发送记录第二页：%d %v", code, out)
	}
	// 别的空间的路径访问不到
	if code, _ := owner.do("GET", "/workspaces/"+f.ws2+"/notify/channels/"+id, nil); code != 404 {
		t.Fatalf("跨空间访问应 404：%d", code)
	}
	if code, _ := owner.do("DELETE", base+"/channels/"+id, nil); code != 200 {
		t.Fatal("删除渠道失败")
	}

	// 系统邮件服务器：只有实例管理员
	if code, _ := owner.do("GET", "/admin/smtp", nil); code != 403 {
		t.Fatalf("非实例管理员应 403：%d", code)
	}
	if code, out = admin.do("GET", "/admin/smtp", nil); code != 200 || out["configured"] != false {
		t.Fatalf("读取系统邮件服务器：%d %v", code, out)
	}
	m := startSMTP(t, "starttls")
	smtpBody := map[string]any{"host": "127.0.0.1", "port": m.port, "tls": "starttls", "username": "u", "password": "SMTPPASS", "from": "noreply@example.com"}
	if code, out = admin.do("POST", "/admin/smtp/test", map[string]any{"config": smtpBody}); code != 200 {
		t.Fatalf("保存前测试系统邮件服务器：%d %v", code, out)
	}
	if msgs := m.messages(); len(msgs) != 1 || msgs[0].To[0] != "root@example.com" {
		t.Fatalf("测试邮件应发给管理员本人：%+v", msgs)
	}
	if code, out = admin.do("PUT", "/admin/smtp", smtpBody); code != 200 || out["configured"] != true {
		t.Fatalf("保存系统邮件服务器：%d %v", code, out)
	}
	raw, _ = json.Marshal(out)
	if strings.Contains(string(raw), "SMTPPASS") {
		t.Fatal("不应返回口令")
	}
	f.au.Flush()
	var n int
	_ = f.st.Get(ctx, &n, `SELECT COUNT(*) FROM audit_log WHERE action IN ('notify.channel_create','notify.channel_update','notify.channel_delete','notify.smtp_update')`)
	if n != 4 {
		t.Fatalf("配置变更应记审计，实际 %d 条", n)
	}
	var detail string
	_ = f.st.Get(ctx, &detail, `SELECT detail FROM audit_log WHERE action = 'notify.smtp_update'`)
	if strings.Contains(detail, "SMTPPASS") {
		t.Fatal("审计里不应有口令")
	}
}
