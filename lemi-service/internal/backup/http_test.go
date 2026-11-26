package backup

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	if out == nil {
		out = map[string]any{"_raw": string(raw)}
	}
	return resp.StatusCode, out
}

func newAPI(t *testing.T, f *fixture) func(u *accounts.User) *apiClient {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	au := auth.New(f.st, f.acc, f.set, f.box)
	srv := web.NewServer(web.Deps{Log: log, Cfg: &config.Config{DataDir: f.dir}, Store: f.st, Auth: au, Accounts: f.acc, Settings: f.set,
		Audit: f.au, Hub: realtime.New(log), Box: f.box, Started: time.Now(), SetupToken: func() string { return "" }, OnSetupDone: func() {}})
	srv.AddMount(f.dl.Routes)
	srv.AddMount(f.s.Routes)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return func(u *accounts.User) *apiClient {
		tok, sess, err := au.CreateSession(context.Background(), u, auth.StageDone, "127.0.0.1", "test")
		if err != nil {
			t.Fatal(err)
		}
		return &apiClient{t: t, base: ts.URL, tok: tok, csrf: sess.CSRF}
	}
}

func TestHTTPPermissions(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	login := newAPI(t, f)
	member := &accounts.User{Username: "member", PasswordHash: "x"}
	admin := &accounts.User{Username: "root", PasswordHash: "x", Role: accounts.RoleAdmin}
	outsider := &accounts.User{Username: "outsider", PasswordHash: "x"}
	for _, u := range []*accounts.User{member, admin, outsider} {
		if err := f.acc.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	f.acc.SetMember(ctx, f.ws, member.ID, accounts.WSMember)
	owner, viewer, mem, root, out := login(f.owner), login(f.viewer), login(member), login(admin), login(outsider)
	ws := "/workspaces/" + f.ws + "/backups"
	src := f.sqliteDB(t, "a.db", true)

	// 本地目录只能由实例管理员配置
	nas := filepath.Join(f.dir, "nas")
	os.MkdirAll(nas, 0o700)
	local := map[string]any{"name": "nas", "kind": "local", "config": map[string]any{"path": nas}}
	if code, _ := owner.do("POST", ws+"/targets", local); code != 403 {
		t.Fatalf("空间所有者配置本地目录应 403，实际 %d", code)
	}
	code, tg := root.do("POST", ws+"/targets", local)
	if code != 200 {
		t.Fatalf("实例管理员配置本地目录：%d %v", code, tg)
	}
	tid := tg["id"].(string)
	if code, _ := viewer.do("POST", ws+"/targets", map[string]any{"name": "s3", "kind": "s3", "config": map[string]any{}}); code != 403 {
		t.Fatalf("viewer 不能建目标：%d", code)
	}
	if code, _ := out.do("GET", ws+"/targets", nil); code != 404 {
		t.Fatalf("非成员看空间应 404：%d", code)
	}
	// 测试连接
	if code, res := owner.do("POST", ws+"/targets/"+tid+"/test", nil); code != 200 {
		t.Fatalf("测试连接：%d %v", code, res)
	}
	// viewer 看目标列表：没有配置详情
	_, list := viewer.do("GET", ws+"/targets", nil)
	if b, _ := json.Marshal(list); strings.Contains(string(b), nas) {
		t.Fatal("viewer 不应看到目标配置")
	}
	// 计划：admin 管理，member 只能立即备份
	plan := map[string]any{"connection_id": src.ID, "name": "p", "method": "sqlite_file", "targets": []string{tid}, "schedule": map[string]any{"kind": "manual"},
		"encrypt": true, "passphrase": "一个足够长的备份密码-123"}
	if code, _ := mem.do("POST", ws+"/plans", plan); code != 403 {
		t.Fatalf("member 不能建计划：%d", code)
	}
	code, p := owner.do("POST", ws+"/plans", plan)
	if code != 200 {
		t.Fatalf("建计划：%d %v", code, p)
	}
	if b, _ := json.Marshal(p); strings.Contains(string(b), "备份密码") {
		t.Fatal("计划视图泄露了备份密码")
	}
	pid := p["id"].(string)
	if code, _ := viewer.do("POST", ws+"/plans/"+pid+"/run", nil); code != 403 {
		t.Fatalf("viewer 不能发起备份：%d", code)
	}
	code, run := mem.do("POST", ws+"/plans/"+pid+"/run", nil)
	if code != 200 {
		t.Fatalf("member 立即备份：%d %v", code, run)
	}
	f.wait(t)
	rid := run["id"].(string)
	_, got := viewer.do("GET", ws+"/runs/"+rid, nil)
	if got["status"] != StatusSuccess {
		t.Fatalf("备份结果：%v", got)
	}
	// viewer 可以下载
	code, dl := viewer.do("POST", ws+"/runs/"+rid+"/download", map[string]any{"format": "sql"})
	if code != 200 {
		t.Fatalf("viewer 下载：%d %v", code, dl)
	}
	req, _ := http.NewRequest("GET", viewer.base+dl["url"].(string), nil)
	req.AddCookie(&http.Cookie{Name: web.SessionCookie, Value: viewer.tok})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(string(body), "SQLite format 3") {
		t.Fatalf("下载内容：%d %d 字节", resp.StatusCode, len(body))
	}
	// 一次性：第二次失效
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 404 {
		t.Fatalf("下载链接应只能用一次：%d", resp.StatusCode)
	}
	resp.Body.Close()
	// viewer 不能还原；member 可以预检
	if code, _ := viewer.do("POST", ws+"/runs/"+rid+"/restore/precheck", map[string]any{"connection_id": src.ID}); code != 403 {
		t.Fatalf("viewer 不能还原：%d", code)
	}
	code, pc := mem.do("POST", ws+"/runs/"+rid+"/restore/precheck", map[string]any{"connection_id": src.ID})
	if code != 200 || pc["method"] != RestoreSQLiteFile {
		t.Fatalf("预检：%d %v", code, pc)
	}
	// 非空目标没确认：409
	if code, res := mem.do("POST", ws+"/runs/"+rid+"/restore", map[string]any{"connection_id": src.ID}); code != 409 || res["error"].(map[string]any)["code"] != "restore_confirm_required" {
		t.Fatalf("没确认应 409：%d %v", code, res)
	}
	// 连接被收紧为 none 的成员看不到这条记录
	f.cs.SetLimit(ctx, src.ID, member.ID, "none")
	if code, _ := mem.do("GET", ws+"/runs/"+rid, nil); code != 404 {
		t.Fatalf("收紧为 none 后应 404：%d", code)
	}
	_, runs := mem.do("GET", ws+"/runs", nil)
	if n := len(runs["runs"].([]any)); n != 0 {
		t.Fatalf("收紧为 none 后列表应为空：%d", n)
	}
	f.cs.SetLimit(ctx, src.ID, member.ID, "read")
	if code, _ := mem.do("POST", ws+"/plans/"+pid+"/run", nil); code != 403 {
		t.Fatalf("只读成员不能发起备份：%d", code)
	}
	// 被使用的目标不能删
	if code, _ := root.do("DELETE", ws+"/targets/"+tid, nil); code != 409 {
		t.Fatalf("被计划使用的目标删除应 409：%d", code)
	}
	// mysqldump 工具管理只限实例管理员
	if code, _ := owner.do("GET", "/admin/dumptools", nil); code != 403 {
		t.Fatalf("非实例管理员看工具：%d", code)
	}
	// 跨空间：别的空间的 ID 一律 404
	ws2, _ := f.acc.CreateWorkspace(ctx, "另一个", "", f.owner.ID)
	if code, _ := owner.do("GET", "/workspaces/"+ws2.ID+"/backups/runs/"+rid, nil); code != 404 {
		t.Fatalf("跨空间取记录应 404：%d", code)
	}
	if code, _ := owner.do("GET", "/workspaces/"+ws2.ID+"/backups/plans/"+pid, nil); code != 404 {
		t.Fatalf("跨空间取计划应 404：%d", code)
	}
}
