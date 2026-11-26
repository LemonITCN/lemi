package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		err  error
		want ErrorKind
	}{
		{&APIError{Status: 429, Message: "Too many requests"}, ErrRateLimit},
		{&APIError{Status: 429, Code: "insufficient_quota", Message: "You exceeded your current quota"}, ErrQuota},
		{&APIError{Status: 401, Message: "invalid api key"}, ErrAuth},
		{&APIError{Status: 402, Message: "payment required"}, ErrQuota},
		{&APIError{Status: 400, Message: "prompt is too long: 250000 tokens > 200000 maximum"}, ErrContextOverflow},
		{&APIError{Status: 400, Code: "context_length_exceeded", Message: "too long"}, ErrContextOverflow},
		{&APIError{Status: 400, Message: "This model's maximum context length is 128000 tokens"}, ErrContextOverflow},
		{&APIError{Status: 413, Message: "request entity too large"}, ErrRequestTooLarge},
		{&APIError{Status: 400, Code: "content_policy_violation", Message: "blocked"}, ErrContentPolicy},
		{&APIError{Status: 500, Message: "internal"}, ErrServer},
		{&APIError{Status: 529, Message: "overloaded"}, ErrServer},
		{&APIError{Status: 400, Message: "MissingSessionID"}, ErrInvalidRequest},
		{&APIError{Status: 404, Message: "model not found"}, ErrInvalidRequest},
		{streamError("overloaded_error", "Overloaded"), ErrServer},
		{&net.DNSError{Err: "no such host", Name: "x.example"}, ErrNetworkDown},
		{errors.New("dial tcp 1.2.3.4:443: connect: connection refused"), ErrNetwork},
		{fmt.Errorf("read: %w", io.ErrUnexpectedEOF), ErrStreamIncomplete},
		{context.Canceled, ErrCanceled},
		{errors.New("something odd"), ErrUnknown},
	}
	for i, c := range cases {
		if got := Classify(c.err).Kind; got != c.want {
			t.Errorf("用例 %d（%v）：得到 %s，应为 %s", i, c.err, got, c.want)
		}
	}
	for _, k := range []ErrorKind{ErrRateLimit, ErrServer, ErrNetwork, ErrNetworkDown, ErrStreamIncomplete, ErrUnknown} {
		if !k.Retryable() {
			t.Errorf("%s 应可重试", k)
		}
	}
	for _, k := range []ErrorKind{ErrContextOverflow, ErrRequestTooLarge, ErrContentPolicy, ErrQuota, ErrAuth, ErrInvalidRequest, ErrCanceled} {
		if k.Retryable() {
			t.Errorf("%s 不应重试", k)
		}
	}
}

func TestRetryHeaders(t *testing.T) {
	now := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
	h := http.Header{}
	h.Set("retry-after-ms", "1500")
	if ms, _ := retryHints(h, now); ms != 1500 {
		t.Fatalf("retry-after-ms：%d", ms)
	}
	h = http.Header{}
	h.Set("retry-after", "3")
	h.Set("x-should-retry", "false")
	ms, should := retryHints(h, now)
	if ms != 3000 || should == nil || *should {
		t.Fatalf("retry-after 秒数与 x-should-retry：%d %v", ms, should)
	}
	h = http.Header{}
	h.Set("retry-after", now.Add(20*time.Second).Format(http.TimeFormat))
	if ms, _ := retryHints(h, now); ms != 20000 {
		t.Fatalf("retry-after HTTP 日期：%d", ms)
	}
	// 从响应构造的错误带上提示并归类
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("retry-after-ms", "800")
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":{"message":"slow down","type":"rate_limit_error"}}`))
	}))
	defer srv.Close()
	_, err := (&chatAdapter{baseURL: srv.URL}).stream(context.Background(), Request{Model: "m"})
	ae := Classify(err)
	if ae.Kind != ErrRateLimit || ae.RetryAfterMS != 800 || ae.Message != "slow down" {
		t.Fatalf("错误响应解析不对：%+v", ae)
	}
}

func sse(w http.ResponseWriter, chunks ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, c := range chunks {
		fmt.Fprintf(w, "data: %s\n\n", c)
	}
}

func drain(t *testing.T, s Stream) ([]Event, error) {
	t.Helper()
	var evs []Event
	for {
		e, err := s.Recv()
		if err == io.EOF {
			return evs, nil
		}
		if err != nil {
			return evs, err
		}
		evs = append(evs, e)
	}
}

func TestChatStream(t *testing.T) {
	var body map[string]any
	complete := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		chunks := []string{
			`{"choices":[{"delta":{"content":"好的</tool_call>"}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"run_query","arguments":"{\"sql\":"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"select 1\"}"}}]}}]}`,
		}
		if complete {
			chunks = append(chunks, `{"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":100,"completion_tokens":10,"prompt_tokens_details":{"cached_tokens":40}}}`, "[DONE]")
		}
		sse(w, chunks...)
	}))
	defer srv.Close()
	a := &chatAdapter{baseURL: srv.URL}
	s, err := a.stream(context.Background(), Request{Model: "m", Tools: []ToolDef{{Name: "run_query", Schema: json.RawMessage(`{"type":"object"}`)}}, ToolChoice: "none"})
	if err != nil {
		t.Fatal(err)
	}
	evs, err := drain(t, s)
	if err != nil {
		t.Fatal(err)
	}
	if body["tool_choice"] != "none" || body["tools"] == nil {
		t.Fatalf("tool_choice=none 时工具定义仍应发送：%v", body)
	}
	var text, args string
	var usage Usage
	var stop string
	for _, e := range evs {
		switch e.Type {
		case EvText:
			text += e.Text
		case EvToolCallEnd:
			args = e.Args
		case EvUsage:
			usage = e.Usage
		case EvDone:
			stop = e.StopReason
		}
	}
	if text != "好的" || args != `{"sql":"select 1"}` || stop != "tool_use" || usage.CacheRead != 40 || usage.Input != 60 {
		t.Fatalf("流解析不对：%q %q %s %+v", text, args, stop, usage)
	}
	// 没有 finish_reason 也没有 [DONE] 就断开：流不完整
	complete = false
	s, _ = a.stream(context.Background(), Request{Model: "m"})
	if _, err := drain(t, s); Classify(err).Kind != ErrStreamIncomplete {
		t.Fatalf("应识别为流不完整：%v", err)
	}
}

func TestAnthropicStream(t *testing.T) {
	var body map[string]any
	complete := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		chunks := []string{
			`{"type":"message_start","message":{"usage":{"input_tokens":50,"cache_read_input_tokens":30}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t1","name":"list_tables"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"db\":\"main\"}"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":12}}`,
		}
		if complete {
			chunks = append(chunks, `{"type":"message_stop"}`)
		}
		sse(w, chunks...)
	}))
	defer srv.Close()
	a := &anthropicAdapter{baseURL: srv.URL}
	req := Request{Model: "m", System: "sys", Tools: []ToolDef{{Name: "list_tables", Schema: json.RawMessage(`{"type":"object"}`)}}, ToolChoice: "none",
		Messages: []Message{{Role: RoleUser, Parts: []Part{{Type: "text", Text: "hi"}}}}}
	s, err := a.stream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	evs, err := drain(t, s)
	if err != nil {
		t.Fatal(err)
	}
	tc, _ := body["tool_choice"].(map[string]any)
	if tc["type"] != "none" {
		t.Fatalf("Anthropic 的 tool_choice 应为 none：%v", body["tool_choice"])
	}
	b, _ := json.Marshal(body)
	if strings.Count(string(b), "cache_control") < 3 {
		t.Fatalf("应有缓存断点（系统提示、工具、消息）：%s", b)
	}
	found := false
	for _, e := range evs {
		if e.Type == EvToolCallEnd && e.Args == `{"db":"main"}` {
			found = true
		}
	}
	if !found {
		t.Fatal("工具调用解析不对")
	}
	complete = false
	s, _ = a.stream(context.Background(), req)
	if _, err := drain(t, s); Classify(err).Kind != ErrStreamIncomplete {
		t.Fatalf("没有 message_stop 应识别为流不完整：%v", err)
	}
}

func TestResponsesStream(t *testing.T) {
	complete := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunks := []string{
			`{"type":"response.output_text.delta","delta":"你好"}`,
			`{"type":"response.output_item.added","item":{"type":"function_call","id":"i1","call_id":"c1","name":"run_query"}}`,
			`{"type":"response.function_call_arguments.delta","item_id":"i1","delta":"{\"sql\":\"x\"}"}`,
		}
		if complete {
			chunks = append(chunks, `{"type":"response.completed","response":{"usage":{"input_tokens":10,"output_tokens":5}}}`)
		}
		sse(w, chunks...)
	}))
	defer srv.Close()
	a := &responsesAdapter{baseURL: srv.URL}
	s, _ := a.stream(context.Background(), Request{Model: "m"})
	evs, err := drain(t, s)
	if err != nil {
		t.Fatal(err)
	}
	if evs[len(evs)-1].StopReason != "tool_use" {
		t.Fatal("Responses 协议的工具调用应以 tool_use 结束")
	}
	complete = false
	s, _ = a.stream(context.Background(), Request{Model: "m"})
	if _, err := drain(t, s); Classify(err).Kind != ErrStreamIncomplete {
		t.Fatalf("没有 response.completed 应识别为流不完整：%v", err)
	}
}

func TestOpenCodeHeaders(t *testing.T) {
	var got http.Header
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			fmt.Fprintf(w, `{"data":[{"id":"m1","name":"M1","limit":{"context":100000,"output":8000},"capabilities":{"tools":{"supported":true}},"api":{"chat-completions":{"base_url":%q}},"recommended_api":"chat-completions"}]}`, srvURL)
			return
		}
		got = r.Header.Clone()
		sse(w, `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`, "[DONE]")
	}))
	defer srv.Close()
	srvURL = srv.URL
	p := &OpenCodeGo{APIKey: "k", ModelsURL: srv.URL + "/models"}
	s, err := p.Stream(context.Background(), Request{Model: "m1", SessionID: "会话01abc"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drain(t, s); err != nil {
		t.Fatal(err)
	}
	if got.Get("x-opencode-session") != "01abc" || !strings.HasPrefix(got.Get("User-Agent"), "lemi/") || got.Get("Authorization") != "Bearer k" {
		t.Fatalf("OpenCode Go 必带会话头与自定义 UA：%v", got)
	}
}

func TestAnthropicThinkingModes(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		th, _ := b["thinking"].(map[string]any)
		if th["type"] == "enabled" {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"Upstream request failed: [invalid_request_error] \"thinking.type.enabled\" is not supported for this model. Use \"thinking.type.adaptive\" and \"output_config.effort\""}}`))
			return
		}
		sse(w, `{"type":"message_start","message":{"usage":{"input_tokens":5}}}`, `{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}`, `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`, `{"type":"message_stop"}`)
	}))
	defer srv.Close()
	a := &anthropicAdapter{baseURL: srv.URL, thinking: true}
	// 新 Claude 模型直接用 adaptive
	s, err := a.stream(context.Background(), Request{Model: "claude-haiku-5-5", Effort: "low"})
	if err != nil {
		t.Fatal(err)
	}
	evs, _ := drain(t, s)
	th := bodies[0]["thinking"].(map[string]any)
	oc := bodies[0]["output_config"].(map[string]any)
	if th["type"] != "adaptive" || oc["effort"] != "low" {
		t.Fatalf("新模型应使用 adaptive 思考：%v", bodies[0])
	}
	sig := false
	for _, e := range evs {
		if e.Type == EvReasoningSig && e.Text == "sig" {
			sig = true
		}
	}
	if !sig {
		t.Fatal("思考签名应传出")
	}
	// 猜成 budget 的模型报错要求 adaptive：换参数重试一次并记住
	bodies = nil
	if _, err := a.stream(context.Background(), Request{Model: "compat-model", Effort: "high"}); err != nil {
		t.Fatalf("应自动换用 adaptive：%v", err)
	}
	if len(bodies) != 2 || thinkingMode("compat-model") != "adaptive" {
		t.Fatalf("应重试一次并记住：%d %s", len(bodies), thinkingMode("compat-model"))
	}
	if thinkingMode("claude-sonnet-4-5") != "budget" || thinkingMode("qwen3.8-max") != "budget" {
		t.Fatal("旧模型与兼容实现默认用 budget_tokens")
	}
	if k := Classify(&APIError{Status: 500, Message: `Upstream request failed: [invalid_request_error] bad`}).Kind; k != ErrInvalidRequest {
		t.Fatalf("代理包装的 invalid_request_error 不应重试：%s", k)
	}
}
