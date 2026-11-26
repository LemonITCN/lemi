package mcpserver_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LemonITCN/lemi/lemi-service/internal/aitest"
	"github.com/LemonITCN/lemi/lemi-service/internal/conns"
	"github.com/LemonITCN/lemi/lemi-service/internal/mcpserver"
	"github.com/LemonITCN/lemi/lemi-service/internal/oauth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fx struct {
	f      *aitest.Fixture
	oa     *oauth.Service
	srv    *httptest.Server
	token  string
	grant  *oauth.Grant
	vis    *conns.Connection
	hidden *conns.Connection
}

func setup(t *testing.T) *fx {
	f := aitest.New(t)
	oa := oauth.New(f.Store, f.Accounts, f.Conns, f.Settings, f.Log, func(*http.Request) string { return "http://lemi.test" })
	vis := f.SQLite(t, "vis", conns.AIWriteAuto, nil)
	other := f.SQLite(t, "other", conns.AIRead, nil) // 没有授权给 MCP
	hid := f.SQLite(t, "hid", conns.AIHidden, nil)
	_ = other
	tok, g, err := oa.CreatePAT(context.Background(), f.Owner, "测试", f.WS, map[string]string{vis.ID: conns.AIRead}, 0)
	if err != nil {
		t.Fatal(err)
	}
	h := mcpserver.Handler(mcpserver.Config{OAuth: oa, Tools: f.Tools, Log: f.Log})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &fx{f: f, oa: oa, srv: srv, token: tok, grant: g, vis: vis, hidden: hid}
}

// rpc 发一条 JSON-RPC（旧的有会话协议），返回响应体里的 result 与响应头。
func (x *fx) rpc(t *testing.T, session string, body string, hdr map[string]string) (int, http.Header, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("POST", x.srv.URL, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+x.token)
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
		req.Header.Set("Mcp-Protocol-Version", "2025-06-18")
	}
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 8<<20)
		for sc.Scan() {
			if line := sc.Text(); strings.HasPrefix(line, "data:") {
				_ = json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &out)
				break
			}
		}
	} else {
		b, _ := io.ReadAll(resp.Body)
		_ = json.Unmarshal(b, &out)
	}
	return resp.StatusCode, resp.Header, out
}

func TestMCPLegacySessionProtocol(t *testing.T) {
	x := setup(t)
	code, hdr, _ := x.rpc(t, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`, map[string]string{"Authorization": ""})
	if code != 401 || !strings.Contains(hdr.Get("WWW-Authenticate"), "resource_metadata") {
		t.Fatalf("没有令牌应 401 并给出资源元数据地址：%d %v", code, hdr)
	}
	code, _, _ = x.rpc(t, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`, map[string]string{"Authorization": "Bearer lemi_pat_wrongwrongwrongwrongwrong"})
	if code != 401 {
		t.Fatalf("错误的令牌应 401：%d", code)
	}
	code, _, _ = x.rpc(t, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`, map[string]string{"Origin": "https://evil.example"})
	if code != 403 {
		t.Fatalf("跨站 Origin 应拒绝：%d", code)
	}
	code, hdr, res := x.rpc(t, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`, nil)
	sid := hdr.Get("Mcp-Session-Id")
	if code != 200 || sid == "" || res["result"] == nil {
		t.Fatalf("initialize 失败：%d %v", code, res)
	}
	if r := res["result"].(map[string]any); r["protocolVersion"] != "2025-06-18" || !strings.Contains(r["instructions"].(string), "Lemi") {
		t.Fatalf("协议版本或说明不对：%v", r)
	}
	if code, _, _ := x.rpc(t, sid, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, nil); code != 202 {
		t.Fatalf("initialized 通知应 202：%d", code)
	}
	_, _, res = x.rpc(t, sid, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, nil)
	b, _ := json.Marshal(res)
	list := string(b)
	for _, want := range []string{`"run_query"`, `"execute_sql"`, `"create_chart"`, `"report_write"`, `"readOnlyHint":true`} {
		if !strings.Contains(list, want) {
			t.Errorf("tools/list 缺少 %s", want)
		}
	}
	for _, no := range []string{"workspace_open_table", "todo_write", `"task"`, "ask_user"} {
		if strings.Contains(list, no) {
			t.Errorf("MCP 不应有 %s", no)
		}
	}
	_, _, res = x.rpc(t, sid, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_connections","arguments":{}}}`, nil)
	b, _ = json.Marshal(res)
	if !strings.Contains(string(b), x.vis.ID) || strings.Contains(string(b), x.hidden.ID) || strings.Contains(string(b), "other") {
		t.Fatalf("MCP 只能看到授权里的连接：%s", b)
	}
	// 授权档位是只读：写入被拒
	_, _, res = x.rpc(t, sid, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"execute_sql","arguments":{"connection_id":"`+x.vis.ID+`","sql":"DELETE FROM orders","reason":"x"}}}`, nil)
	b, _ = json.Marshal(res)
	if !strings.Contains(string(b), `"isError":true`) || !strings.Contains(string(b), "只读") {
		t.Fatalf("授权只读时写入应被拒：%s", b)
	}
	// 吊销后同一个会话也立刻失效
	if err := x.oa.RevokeGrant(context.Background(), x.f.Owner.ID, x.grant.ID); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := x.rpc(t, sid, `{"jsonrpc":"2.0","id":5,"method":"tools/list"}`, nil); code != 401 {
		t.Fatalf("吊销后应 401：%d", code)
	}
}

type authTransport struct{ token string }

func (a authTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+a.token)
	return http.DefaultTransport.RoundTrip(r)
}

func TestMCPWithSDKClient(t *testing.T) {
	x := setup(t)
	ctx := context.Background()
	cl := mcp.NewClient(&mcp.Implementation{Name: "lemi-test", Version: "1"}, nil)
	sess, err := cl.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: x.srv.URL, HTTPClient: &http.Client{Transport: authTransport{x.token}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if ir := sess.InitializeResult(); ir != nil {
		t.Logf("协商的协议版本：%s", ir.ProtocolVersion)
	}
	tl, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Tools) < 10 {
		t.Fatalf("工具太少：%d", len(tl.Tools))
	}
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "run_query", Arguments: map[string]any{"connection_id": x.vis.ID, "sql": "SELECT COUNT(*) AS n FROM orders"}})
	if err != nil {
		t.Fatal(err)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if res.IsError || !strings.Contains(text, "结果 ID") || !strings.Contains(text, "120") {
		t.Fatalf("run_query 结果不对：%s", text)
	}
	res, _ = sess.CallTool(ctx, &mcp.CallToolParams{Name: "run_query", Arguments: map[string]any{"connection_id": x.hidden.ID, "sql": "SELECT 1"}})
	if !res.IsError {
		t.Fatal("不可见的连接应报错")
	}
}

func TestMCPSDKLegacyVersion(t *testing.T) {
	x := setup(t)
	ctx := context.Background()
	cl := mcp.NewClient(&mcp.Implementation{Name: "lemi-test", Version: "1"}, nil)
	sess, err := cl.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: x.srv.URL, HTTPClient: &http.Client{Transport: authTransport{x.token}}},
		&mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if v := sess.InitializeResult().ProtocolVersion; v != "2025-11-25" {
		t.Fatalf("应按客户端要求使用有会话的旧协议：%s", v)
	}
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "list_tables", Arguments: map[string]any{"connection_id": x.vis.ID, "db": "main"}})
	if err != nil || res.IsError {
		t.Fatalf("旧协议下调用工具失败：%v %+v", err, res)
	}
}

func TestMCPRateLimit(t *testing.T) {
	old := mcpserver.RateLimit
	mcpserver.RateLimit = 3
	defer func() { mcpserver.RateLimit = old }()
	x := setup(t)
	var codes []int
	for i := 0; i < 4; i++ {
		code, _, _ := x.rpc(t, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`, nil)
		codes = append(codes, code)
	}
	if codes[3] != 429 || codes[0] != 200 {
		t.Fatalf("每个授权每分钟限速：%v", codes)
	}
}
