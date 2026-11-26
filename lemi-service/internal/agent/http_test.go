package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/auth"
	"github.com/LemonITCN/lemi/lemi-service/internal/config"
	"github.com/LemonITCN/lemi/lemi-service/internal/conns"
	"github.com/LemonITCN/lemi/lemi-service/internal/realtime"
	"github.com/LemonITCN/lemi/lemi-service/internal/web"
)

type client struct {
	t     *testing.T
	base  string
	token string
	csrf  string
}

func (c *client) do(method, path string, body any) (int, map[string]any) {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, c.base+path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", c.base)
	req.Header.Set("X-Lemi-CSRF", c.csrf)
	req.AddCookie(&http.Cookie{Name: web.SessionCookie, Value: c.token})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestHTTPFlow(t *testing.T) {
	e := setup(t)
	c := e.f.SQLite(t, "db", conns.AIRead, nil)
	au := auth.New(e.f.Store, e.f.Accounts, e.f.Settings, e.f.Box)
	srv := web.NewServer(web.Deps{Log: e.f.Log, Cfg: &config.Config{}, Store: e.f.Store, Auth: au, Accounts: e.f.Accounts, Settings: e.f.Settings,
		Audit: e.f.Audit, Hub: realtime.New(e.f.Log), Box: e.f.Box, Started: time.Now()})
	srv.AddMount(e.s.Routes)
	srv.BootstrapExtra = append(srv.BootstrapExtra, e.s.Bootstrap)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	login := func(u *accounts.User) *client {
		tok, sess, err := au.CreateSession(context.Background(), u, auth.StageDone, "127.0.0.1", "test")
		if err != nil {
			t.Fatal(err)
		}
		return &client{t: t, base: ts.URL, token: tok, csrf: sess.CSRF}
	}
	owner := login(e.f.Owner)
	code, boot := owner.do("GET", "/api/v1/bootstrap", nil)
	if code != 200 || boot["ai"].(map[string]any)["configured"] != true {
		t.Fatalf("bootstrap 应告诉前端 AI 已配置：%v", boot)
	}
	code, out := owner.do("GET", "/api/v1/workspaces/"+e.f.WS+"/ai/models", nil)
	if code != 200 || out["default"] != "test/fake-1" {
		t.Fatalf("模型列表不对：%v", out)
	}
	code, out = owner.do("POST", "/api/v1/workspaces/"+e.f.WS+"/agent/conversations", map[string]any{"settings": map[string]any{"mode": "plan"}})
	if code != 200 {
		t.Fatalf("新建会话失败：%d %v", code, out)
	}
	convID := out["conversation"].(map[string]any)["id"].(string)
	e.p.push(toolReply(call{"list_tables", map[string]any{"connection_id": c.ID, "db": "main"}}), textReply("有 orders 与 users 两张表。"))
	code, out = owner.do("POST", "/api/v1/agent/conversations/"+convID+"/messages", map[string]any{"text": "有哪些表？",
		"context": map[string]any{"open_tabs": []any{map[string]any{"id": "t1", "kind": "table", "connection_id": c.ID, "db": "main", "table": "orders"}}, "active_tab": "t1"}})
	if code != 200 {
		t.Fatalf("发送失败：%d %v", code, out)
	}
	runID := out["run_id"].(string)
	// SSE 续传：从头读到 run_done
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/agent/runs/"+runID+"/events?after=0", nil)
	req.AddCookie(&http.Cookie{Name: web.SessionCookie, Value: owner.token})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev Event
		_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev)
		types = append(types, ev.Type)
		if ev.Type == "run_done" {
			break
		}
	}
	resp.Body.Close()
	joined := strings.Join(types, ",")
	if !strings.HasPrefix(joined, "run_start,user_message") || !strings.Contains(joined, "tool_result") || !strings.HasSuffix(joined, "run_done") {
		t.Fatalf("事件流不对：%s", joined)
	}
	code, out = owner.do("GET", "/api/v1/agent/conversations/"+convID, nil)
	msgs := out["messages"].([]any)
	if code != 200 || len(msgs) != 4 {
		t.Fatalf("会话详情不对：%d %d", code, len(msgs))
	}
	if !strings.Contains(e.p.mainReqs()[0].System, "当前打开的标签") || !strings.Contains(e.p.mainReqs()[0].System, "计划模式") {
		t.Fatal("系统提示应包含打开的标签与计划模式")
	}
	code, _ = owner.do("POST", "/api/v1/agent/runs/"+runID+"/respond", map[string]any{"id": "x", "reply": "once"})
	if code != 400 {
		t.Fatalf("已结束的运行回应应报错：%d", code)
	}
	// 别人的会话看不到；查看者不能使用智能体
	member := login(e.f.Member)
	if code, _ := member.do("GET", "/api/v1/agent/conversations/"+convID, nil); code != 404 {
		t.Fatalf("别人的会话应 404：%d", code)
	}
	viewer := login(e.f.Viewer)
	if code, _ := viewer.do("POST", "/api/v1/workspaces/"+e.f.WS+"/agent/conversations", nil); code != 403 {
		t.Fatalf("查看者不能使用智能体：%d", code)
	}
	// 非实例管理员不能管理供应商
	if code, _ := owner.do("GET", "/api/v1/admin/ai", nil); code != 403 {
		t.Fatalf("非实例管理员不能管理供应商：%d", code)
	}
}
