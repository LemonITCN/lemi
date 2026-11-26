package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/conns"
	"github.com/LemonITCN/lemi/lemi-service/internal/llm"
	"github.com/LemonITCN/lemi/lemi-service/internal/store"
	"github.com/LemonITCN/lemi/lemi-service/internal/tools"
)

func TestBasicLoop(t *testing.T) {
	e := setup(t)
	c := e.f.SQLite(t, "shop", conns.AIRead, nil)
	e.p.push(
		toolReply(call{"list_connections", map[string]any{}}, call{"run_query", map[string]any{"connection_id": c.ID, "sql": "SELECT city, COUNT(*) n FROM users GROUP BY city"}}),
		textReply("北京、上海、深圳各 10 个用户。"),
	)
	run := e.send("各城市有多少用户？")
	e.wait(run)
	if run.State() != RunDone {
		t.Fatalf("运行应正常结束：%s %s", run.State(), run.errMsg)
	}
	reqs := e.p.mainReqs()
	if len(reqs) != 2 {
		t.Fatalf("应请求 2 次，实际 %d", len(reqs))
	}
	if !strings.Contains(reqs[0].System, c.ID) || !strings.Contains(reqs[0].System, "数据分析") {
		t.Fatal("系统提示应包含角色与可用连接")
	}
	if reqs[0].System != reqs[1].System {
		t.Fatal("系统提示在会话里应保持不变")
	}
	rt := reqText(reqs[1])
	if !strings.Contains(rt, "tool:tool_result:run_query") || !strings.Contains(rt, "结果 ID") {
		t.Fatalf("第二次请求应包含工具结果：%s", rt)
	}
	ms := e.messages()
	roles := ""
	for _, m := range ms {
		roles += m.Role + ","
	}
	if roles != "user,assistant,tool,assistant," {
		t.Fatalf("消息顺序不对：%s", roles)
	}
	if len(events(run, "tool_result")) != 2 || len(events(run, "run_done")) != 1 {
		t.Fatal("事件不全")
	}
	conv := e.reload()
	if conv.Usage.Requests != 2 || conv.Usage.Input != 2000 || conv.St.LastContext == 0 {
		t.Fatalf("用量统计不对：%+v %d", conv.Usage, conv.St.LastContext)
	}
	if conv.Title != "各城市有多少用户？" {
		t.Fatalf("应有临时标题：%s", conv.Title)
	}
}

func TestRetryBackoffAndClassification(t *testing.T) {
	e := setup(t)
	e.p.push(
		reply{err: &llm.APIError{Status: 429, Message: "Too many requests", RetryAfterMS: 1500}},
		reply{err: &llm.APIError{Status: 503, Message: "overloaded"}},
		textReply("好了"),
	)
	run := e.send("你好")
	e.wait(run)
	if run.State() != RunDone {
		t.Fatalf("可重试错误重试后应成功：%s", run.errMsg)
	}
	sl := e.clk.Sleeps()
	if len(sl) != 2 || sl[0] != 1500*time.Millisecond || sl[1] != 4*time.Second {
		t.Fatalf("退避不对（应尊重 retry-after-ms，之后 2 秒起指数退避）：%v", sl)
	}
	if len(events(run, "retry")) != 2 {
		t.Fatal("应推送重试事件")
	}
	// 鉴权错误不重试
	e.p.push(reply{err: &llm.APIError{Status: 401, Message: "invalid api key"}})
	run = e.send("再来")
	e.wait(run)
	if run.State() != RunError || !strings.Contains(run.errMsg, "密钥") || len(e.clk.Sleeps()) != 2 {
		t.Fatalf("鉴权错误应直接失败：%s %v", run.errMsg, e.clk.Sleeps())
	}
	// x-should-retry: false 时 5xx 也不重试
	f := false
	e.p.push(reply{err: &llm.APIError{Status: 500, Message: "boom", ShouldRetry: &f}})
	run = e.send("再来")
	e.wait(run)
	if run.State() != RunError || len(e.clk.Sleeps()) != 2 {
		t.Fatal("x-should-retry: false 应不重试")
	}
}

func TestRetryPolicy(t *testing.T) {
	p := DefaultRetry
	p.Jitter = func() float64 { return 0 }
	now := time.Now()
	st := &retryState{}
	var got []time.Duration
	for {
		d, ok := p.next(&llm.APIError{Kind: llm.ErrServer}, st, now)
		if !ok {
			break
		}
		got = append(got, d)
	}
	if len(got) != 10 || got[0] != 1600*time.Millisecond || got[2] != 6400*time.Millisecond || got[9] != 8*time.Second {
		t.Fatalf("最多 10 次、2 秒起翻倍、封顶 10 秒、带抖动：%v", got)
	}
	d, _ := p.next(&llm.APIError{Kind: llm.ErrRateLimit, RetryAfterMS: int64(time.Hour / time.Millisecond)}, &retryState{}, now)
	if d != 15*time.Minute {
		t.Fatalf("retry-after 应封顶 15 分钟：%v", d)
	}
	for _, k := range []llm.ErrorKind{llm.ErrAuth, llm.ErrQuota, llm.ErrContentPolicy, llm.ErrInvalidRequest, llm.ErrRequestTooLarge, llm.ErrContextOverflow, llm.ErrCanceled} {
		if _, ok := p.next(&llm.APIError{Kind: k}, &retryState{}, now); ok {
			t.Errorf("%s 不应重试", k)
		}
	}
	tr := true
	if _, ok := p.next(&llm.APIError{Kind: llm.ErrInvalidRequest, ShouldRetry: &tr}, &retryState{}, now); !ok {
		t.Error("x-should-retry: true 应重试")
	}
	// 网络不可达：单独计预算，最长 1 小时
	st = &retryState{}
	n := 0
	tt := now
	for {
		d, ok := p.next(&llm.APIError{Kind: llm.ErrNetworkDown}, st, tt)
		if !ok {
			break
		}
		n++
		tt = tt.Add(d)
	}
	if n < 300 || tt.Sub(now) < 59*time.Minute || tt.Sub(now) > 61*time.Minute {
		t.Fatalf("网络不可达应重试约 1 小时：%d 次 %v", n, tt.Sub(now))
	}
	if llm.Classify(&net.DNSError{Err: "no such host", Name: "x"}).Kind != llm.ErrNetworkDown {
		t.Fatal("DNS 失败应归为网络不可达")
	}
}

func TestStreamIncompleteAndPartial(t *testing.T) {
	e := setup(t)
	// 没有输出就断流（长参数工具调用中途断开）：整步重试
	e.p.push(
		reply{events: []llm.Event{{Type: llm.EvToolCallStart, CallID: "x", Name: "run_query"}, {Type: llm.EvToolCallDelta, CallID: "x", Text: `{"sql":"SEL`}},
			errAfter: &llm.APIError{Kind: llm.ErrStreamIncomplete, Message: "断了"}},
		textReply("完整回复"),
	)
	run := e.send("一")
	e.wait(run)
	if run.State() != RunDone || len(e.p.mainReqs()) != 2 || len(e.clk.Sleeps()) != 1 {
		t.Fatalf("没有输出的断流应整步重试：%s %d", run.State(), len(e.p.mainReqs()))
	}
	// 已经输出一部分后断流：保存已有部分，开新的一步继续
	e.p.push(
		reply{events: []llm.Event{{Type: llm.EvText, Text: "前半段分析……"}}, errAfter: errors.New("connection reset by peer")},
		textReply("后半段分析。"),
	)
	run = e.send("二")
	e.wait(run)
	reqs := e.p.mainReqs()
	last := reqText(reqs[len(reqs)-1])
	if !strings.Contains(last, "assistant:text::前半段分析") || !strings.Contains(last, "网络中断") {
		t.Fatalf("断流后应保存已有输出并提示继续：%s", last)
	}
	ms := e.messages()
	var partial bool
	for _, m := range ms {
		if m.Role == "assistant" && strings.Contains(string(m.Meta), `"partial":true`) {
			partial = true
		}
	}
	if !partial || run.State() != RunDone {
		t.Fatal("应有标为 partial 的助手消息")
	}
}

func seedHistory(t *testing.T, e *env, n int, size int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		if _, err := e.s.appendMessage(ctx, e.conv.ID, "user", []StoredPart{{Part: llm.Part{Type: "text", Text: strings.Repeat("历史问题", size)}}}, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := e.s.appendMessage(ctx, e.conv.ID, "assistant", []StoredPart{{Part: llm.Part{Type: "text", Text: strings.Repeat("历史回答", size)}}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	// 会话已经开始过：固定系统提示
	c := e.reload()
	c.System = "测试系统提示"
	c.St.Env = EnvFacts{Date: dateText(e.s.now()), Mode: ModeNormal, Conns: "（没有可用的连接）"}
	if err := e.s.saveState(ctx, c); err != nil {
		t.Fatal(err)
	}
}

const goodSummary = "## 目标\n分析\n## 要求\n无\n## 决定\n无\n## 进展\n### 已完成\n查了表\n### 进行中\n无\n### 受阻\n无\n## 下一步\n继续\n## 相关对象\n结果 01abc\n## 重要上下文\n无"

func TestOverflowCompaction(t *testing.T) {
	e := setup(t)
	seedHistory(t, e, 8, 1500)
	e.p.push(reply{err: &llm.APIError{Status: 400, Message: "prompt is too long: 250000 tokens > 200000 maximum"}}, textReply("压缩后继续回答"))
	e.p.comp = []reply{textReply("随便写的摘要"), textReply(goodSummary)} // 第一次不合模板，补发提示后合格
	run := e.send("继续分析")
	e.wait(run)
	if run.State() != RunDone {
		t.Fatalf("溢出后应压缩并继续：%s", run.errMsg)
	}
	conv := e.reload()
	if conv.St.Summary != goodSummary || conv.St.SummaryUpTo == 0 || conv.St.Compacting {
		t.Fatalf("摘要不对：%+v", conv.St)
	}
	if len(e.p.cmpReq) != 2 || !strings.Contains(reqText(e.p.cmpReq[1]), "没有严格按模板") {
		t.Fatal("模板不合格应补发一次提示")
	}
	if len(e.clk.Sleeps()) != 0 {
		t.Fatal("上下文溢出不应重试等待")
	}
	reqs := e.p.mainReqs()
	second := reqText(reqs[1])
	if !strings.Contains(second, "<conversation-checkpoint>") || strings.Count(second, "历史问题") >= strings.Count(reqText(reqs[0]), "历史问题") {
		t.Fatal("压缩后请求应以摘要开头并变短")
	}
	if !strings.Contains(second, "继续分析") {
		t.Fatal("最近的消息应保留原文")
	}
	if len(events(run, "compaction_done")) != 1 {
		t.Fatal("应推送压缩事件")
	}
	// 摘要两次都不合格：压缩失败，运行报错
	e2 := setup(t)
	seedHistory(t, e2, 8, 1500)
	e2.p.push(reply{err: &llm.APIError{Status: 400, Code: "context_length_exceeded", Message: "too long"}})
	e2.p.comp = []reply{textReply("不合格"), textReply("还是不合格")}
	run = e2.send("继续")
	e2.wait(run)
	if run.State() != RunError || !strings.Contains(run.errMsg, "压缩失败") {
		t.Fatalf("摘要不合格应判失败：%s", run.errMsg)
	}
}

func TestAutoCompaction(t *testing.T) {
	e := setup(t)
	e.p.model.Context = 32000 // 上限 = 32000 − 16000 = 16000
	seedHistory(t, e, 10, 1000)
	e.p.comp = []reply{textReply(goodSummary)}
	e.p.push(textReply("好"))
	run := e.send("新问题")
	e.wait(run)
	if len(e.p.cmpReq) != 1 || e.reload().St.Summary == "" {
		t.Fatal("超过上限应自动压缩")
	}
	if est := estimateRequest(e.p.mainReqs()[0]); est > contextLimit(32000) {
		t.Fatalf("压缩后的请求仍然超限：%d", est)
	}
	// 再压缩时合并旧摘要
	seedHistory(t, e, 10, 1000)
	e.p.comp = []reply{textReply(goodSummary)}
	e.p.push(textReply("好"))
	run = e.send("再问")
	e.wait(run)
	if len(e.p.cmpReq) != 2 || !strings.Contains(reqText(e.p.cmpReq[1]), "<conversation-checkpoint>") {
		t.Fatal("再次压缩应带上旧摘要合并")
	}
}

func TestArgRepairAndUnknownTool(t *testing.T) {
	e := setup(t)
	c := e.f.SQLite(t, "db", conns.AIRead, nil)
	e.p.push(
		toolReply(
			call{"Run_Query", map[string]any{"connection_id": c.ID, "sql": "SELECT COUNT(*) FROM orders", "preview_rows": "3", "verbose": true}},
			call{"drop_database", map[string]any{}},
			call{"run_query", `{"connection_id": "` + c.ID + `", "sql": "SELECT 1",}`},
			call{"run_query", `{"connection_id": `},
		),
		textReply("完成"),
	)
	run := e.send("统计")
	e.wait(run)
	ms := e.messages()
	var results []StoredPart
	var calls []StoredPart
	for _, m := range ms {
		for _, p := range m.Parts {
			if p.Type == "tool_result" {
				results = append(results, p)
			}
			if p.Type == "tool_call" {
				calls = append(calls, p)
			}
		}
	}
	if len(results) != 4 || calls[0].Name != "run_query" {
		t.Fatalf("工具名应修正：%+v", calls)
	}
	if results[0].IsError || !strings.Contains(results[0].Text, "120") {
		t.Fatalf("字符串数字与多余字段应自动修复：%s", results[0].Text)
	}
	if !results[1].IsError || !strings.Contains(results[1].Text, "可用的工具") {
		t.Fatalf("未知工具应回灌可用工具列表：%s", results[1].Text)
	}
	if results[2].IsError {
		t.Fatalf("结尾多余逗号应修复：%s", results[2].Text)
	}
	if !results[3].IsError || !strings.Contains(results[3].Text, "不是合法的 JSON") {
		t.Fatalf("坏 JSON 应回灌错误：%s", results[3].Text)
	}
	if run.State() != RunDone || len(e.p.mainReqs()) != 2 {
		t.Fatal("坏的工具调用不应中断回合")
	}
}

func TestDoomLoop(t *testing.T) {
	e := setup(t)
	c := e.f.SQLite(t, "db", conns.AIRead, nil)
	q := call{"run_query", map[string]any{"connection_id": c.ID, "sql": "SELECT 1"}}
	e.p.push(toolReply(q), toolReply(q), toolReply(q), textReply("换个思路"))
	run := e.send("查")
	p := e.pending(run, "doom_loop")
	if p["tool"] != "run_query" {
		t.Fatalf("死循环确认内容不对：%v", p)
	}
	e.respond(run, p["id"], Reply{Reply: "reject", Message: "别重复了"})
	e.wait(run)
	ms := e.messages()
	last := ms[len(ms)-2]
	if last.Role != "tool" || last.Parts[0].Status != StatusDenied || !strings.Contains(last.Parts[0].Text, "别重复了") {
		t.Fatalf("拒绝重复调用应回灌说明：%+v", last.Parts)
	}
	if run.State() != RunDone {
		t.Fatal("带说明拒绝后循环应继续")
	}
}

func TestApprovalReplies(t *testing.T) {
	e := setup(t)
	c := e.f.SQLite(t, "w", conns.AIWriteConfirm, nil)
	upd := func(id int) call {
		return call{"execute_sql", map[string]any{"connection_id": c.ID, "sql": "UPDATE orders SET status = 's' WHERE id = " + string(rune('0'+id)), "reason": "修正"}}
	}
	e.p.push(toolReply(upd(1)), toolReply(upd(2)), toolReply(upd(3)),
		toolReply(call{"execute_sql", map[string]any{"connection_id": c.ID, "sql": "CREATE TABLE t (a INTEGER)", "reason": "建表"}}),
		toolReply(call{"execute_sql", map[string]any{"connection_id": c.ID, "sql": "DROP TABLE orders", "reason": "删表"}}),
		textReply("不应该走到这里"))
	run := e.send("改数据")
	p := e.pending(run, "approval")
	if p["connection_id"] != c.ID || p["class"] != "dml" || !strings.Contains(p["sql"].(string), "id = 1") {
		t.Fatalf("审批卡片内容不对：%v", p)
	}
	e.respond(run, p["id"], Reply{Reply: "once"})
	p = e.pending(run, "approval")
	if !strings.Contains(p["sql"].(string), "id = 2") {
		t.Fatalf("允许一次只放行一次：%v", p["sql"])
	}
	e.respond(run, p["id"], Reply{Reply: "always"})
	// 第 3 条同类写入自动放行，下一个审批是 DDL
	p = e.pending(run, "approval")
	if p["class"] != "ddl" {
		t.Fatalf("本会话总是允许应自动放行同一连接同类语句：%v", p)
	}
	e.respond(run, p["id"], Reply{Reply: "reject", Message: "不要建表"})
	p = e.pending(run, "approval")
	if !strings.Contains(p["sql"].(string), "DROP TABLE") {
		t.Fatalf("拒绝过的类别下次仍要审批：%v", p["sql"])
	}
	e.respond(run, p["id"], Reply{Reply: "reject"})
	e.wait(run)
	if len(e.p.mainReqs()) != 5 {
		t.Fatalf("不带说明的拒绝应结束本步：请求 %d 次", len(e.p.mainReqs()))
	}
	var statuses []string
	for _, m := range e.messages() {
		for _, pt := range m.Parts {
			if pt.Type == "tool_result" {
				statuses = append(statuses, pt.Status)
			}
		}
	}
	if strings.Join(statuses, ",") != "done,done,done,denied,denied" {
		t.Fatalf("工具结果状态不对：%v", statuses)
	}
	if conv := e.reload(); len(conv.St.AllowRules) != 1 || !strings.HasPrefix(conv.St.AllowRules[0], "execute_sql|"+c.ID+"|dml") {
		t.Fatalf("总是允许应按工具+连接+语句类别记住：%v", conv.St.AllowRules)
	}
}

func TestAlwaysResolvesOtherPending(t *testing.T) {
	r := &Run{pending: map[string]*interaction{}, subs: map[chan struct{}]struct{}{}}
	a := &interaction{key: "k", data: map[string]any{}, ch: make(chan Reply, 1)}
	b := &interaction{key: "k", data: map[string]any{}, ch: make(chan Reply, 1)}
	c := &interaction{key: "other", data: map[string]any{}, ch: make(chan Reply, 1)}
	r.pending["a"], r.pending["b"], r.pending["c"] = a, b, c
	if err := r.respond("a", Reply{Reply: "always"}); err != nil {
		t.Fatal(err)
	}
	if (<-b.ch).Reply != "once" || len(c.ch) != 0 || len(r.pending) != 1 {
		t.Fatal("总是允许应自动放行其他已满足的挂起请求")
	}
}

func TestStop(t *testing.T) {
	old := ForceStopAfter
	ForceStopAfter = 50 * time.Millisecond
	defer func() { ForceStopAfter = old }()
	e := setup(t)
	e.p.push(toolReply(call{"test_block", map[string]any{}}, call{"test_stuck", map[string]any{}}), textReply("不应该走到这里"))
	run := e.send("跑")
	deadline := time.Now().Add(5 * time.Second)
	for len(events(run, "tool_running")) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	start := time.Now()
	if err := e.s.Stop(e.f.Owner.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	e.wait(run)
	if time.Since(start) > 2*time.Second {
		t.Fatal("卡住的工具应在强制中断时限后放弃")
	}
	if run.State() != RunStopped || len(e.p.mainReqs()) != 1 {
		t.Fatalf("停止后不应继续请求：%s", run.State())
	}
	var res []StoredPart
	for _, m := range e.messages() {
		for _, p := range m.Parts {
			if p.Type == "tool_result" {
				res = append(res, p)
			}
		}
	}
	if len(res) != 2 || res[0].Status != StatusInterrupted || res[1].Status != StatusInterrupted || !strings.Contains(res[1].Text, "强制中断") {
		t.Fatalf("未完成的工具应记为已中断：%+v", res)
	}
}

func TestSteerAndQueue(t *testing.T) {
	gate = make(chan struct{})
	e := setup(t)
	e.p.push(toolReply(call{"test_gate", map[string]any{}}), textReply("按北京口径回答"), textReply("排队的消息也处理了"))
	run := e.send("分析用户")
	deadline := time.Now().Add(5 * time.Second)
	for len(events(run, "tool_running")) < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	res, err := e.s.Send(context.Background(), e.f.Owner, e.conv.ID, "", SendInput{Text: "补充：只看北京"})
	if err != nil || !res.Steered || res.RunID != run.ID {
		t.Fatalf("运行中发消息应插话：%+v %v", res, err)
	}
	res, err = e.s.Send(context.Background(), e.f.Owner, e.conv.ID, "", SendInput{Text: "然后导出", Delivery: "queue"})
	if err != nil || !res.Queued {
		t.Fatalf("delivery=queue 应排队：%+v %v", res, err)
	}
	close(gate)
	e.wait(run)
	reqs := e.p.mainReqs()
	if len(reqs) < 2 {
		t.Fatal("应有第二次请求")
	}
	second := reqText(reqs[1])
	if !strings.Contains(second, "tool:tool_result:test_gate") || !strings.Contains(second, "补充：只看北京") ||
		strings.Index(second, "补充：只看北京") < strings.Index(second, "tool_result:test_gate") {
		t.Fatalf("插话应在步骤边界、工具结果之后插入：%s", second)
	}
	if strings.Contains(second, "然后导出") {
		t.Fatal("排队的消息不应插进本轮")
	}
	next := events(run, "next_run")
	if len(next) != 1 {
		t.Fatal("本轮结束后应开始排队的消息")
	}
	nr, _ := e.s.GetRun(e.f.Owner.ID, next[0].Data.(map[string]any)["run_id"].(string))
	e.wait(nr)
	reqs = e.p.mainReqs()
	if !strings.Contains(reqText(reqs[len(reqs)-1]), "然后导出") {
		t.Fatal("排队的消息应在新一轮里处理")
	}
}

func TestMaxStepsAndFinishLength(t *testing.T) {
	e := setup(t)
	e.s.MaxSteps = 3
	c := e.f.SQLite(t, "db", conns.AIRead, nil)
	e.p.push(toolReply(call{"list_tables", map[string]any{"connection_id": c.ID, "db": "main"}}),
		toolReply(call{"describe_tables", map[string]any{"connection_id": c.ID, "db": "main", "tables": []string{"orders"}}}),
		textReply("总结：已完成……未完成……下一步……"))
	run := e.send("做个大分析")
	e.wait(run)
	reqs := e.p.mainReqs()
	if len(reqs) != 3 || reqs[2].ToolChoice != "none" || len(reqs[2].Tools) == 0 || !strings.Contains(reqText(reqs[2]), "步数上限") {
		t.Fatalf("最后一步应注入总结提示并禁止工具（保留工具定义）：%d %q", len(reqs), reqs[len(reqs)-1].ToolChoice)
	}
	if reqs[1].ToolChoice != "" {
		t.Fatal("非最后一步不应禁止工具")
	}
	// finish=length：最多续写两次
	e.s.MaxSteps = 0
	trunc := reply{events: []llm.Event{{Type: llm.EvText, Text: "很长的回答……"}, {Type: llm.EvDone, StopReason: "max_tokens"}}}
	e.p.push(trunc, trunc, trunc)
	run = e.send("写长文")
	e.wait(run)
	reqs = e.p.mainReqs()[3:]
	if len(reqs) != 3 || !strings.Contains(reqText(reqs[1]), "不要道歉") || len(events(run, "notice")) == 0 {
		t.Fatalf("达到长度上限应续写两次后停止：%d", len(reqs))
	}
}

func TestSubagent(t *testing.T) {
	e := setup(t)
	c := e.f.SQLite(t, "db", conns.AIWriteAuto, nil)
	e.p.push(toolReply(call{"task", map[string]any{"description": "摸底", "prompt": "看看订单表的数据分布", "subagent_type": "explore"}}), textReply("根据子任务：订单 120 条。"))
	e.p.sub = []reply{toolReply(call{"run_query", map[string]any{"connection_id": c.ID, "sql": "SELECT COUNT(*) FROM orders"}}), textReply("orders 表 120 行，状态 paid/refunded。")}
	run := e.send("先摸底")
	e.wait(run)
	if run.State() != RunDone {
		t.Fatalf("运行失败：%s", run.errMsg)
	}
	second := reqText(e.p.mainReqs()[1])
	if !strings.Contains(second, "<task_result task_id=") || !strings.Contains(second, "orders 表 120 行") {
		t.Fatalf("子智能体结论应以 task_result 返回：%s", second)
	}
	for _, td := range e.p.subReq[0].Tools {
		if td.Name == "execute_sql" || td.Name == "task" || td.Name == "ask_user" || td.Name == "report_write" {
			t.Fatalf("子智能体不应有 %s", td.Name)
		}
	}
	if !strings.Contains(e.p.subReq[0].System, "explore") || !strings.Contains(e.p.subReq[0].System, c.ID) {
		t.Fatal("子智能体的系统提示应包含类型与环境")
	}
	// 用 task_id 续接
	i := strings.Index(second, `task_id="`) + len(`task_id="`)
	taskID := second[i : i+26]
	e.p.push(toolReply(call{"task", map[string]any{"description": "继续", "prompt": "再看看用户表", "subagent_type": "analyst", "task_id": taskID}}), textReply("好"))
	e.p.sub = []reply{textReply("users 表 30 行。")}
	run = e.send("继续")
	e.wait(run)
	if len(e.p.subReq) != 3 || !strings.Contains(reqText(e.p.subReq[2]), "看看订单表的数据分布") {
		t.Fatal("续接的子智能体应带着之前的上下文")
	}
	if len(events(run, "subagent_done")) != 1 {
		t.Fatal("应推送子智能体事件")
	}
}

func TestRecover(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	_, _ = e.s.appendMessage(ctx, e.conv.ID, "user", []StoredPart{{Part: llm.Part{Type: "text", Text: "查"}}}, nil)
	_, _ = e.s.appendMessage(ctx, e.conv.ID, "assistant", []StoredPart{{Part: llm.Part{Type: "reasoning", Text: "想一想"}}, {Part: llm.Part{Type: "text", Text: "我来查"}},
		{Part: llm.Part{Type: "tool_call", CallID: "c1", Name: "run_query", Args: json.RawMessage(`{}`)}}}, nil)
	_, _ = e.s.st.Exec(ctx, `INSERT INTO agent_runs (id, conversation_id, user_id, status, started_at) VALUES ('r1', ?, ?, 'running', ?)`, e.conv.ID, e.f.Owner.ID, store.Now())
	conv := e.reload()
	conv.St.Compacting = true
	_ = e.s.saveState(ctx, conv)
	e.s.Recover(ctx)
	var st string
	_ = e.s.st.GetW(ctx, &st, `SELECT status FROM agent_runs WHERE id = 'r1'`)
	if st != RunError || e.reload().St.Compacting {
		t.Fatal("残留的运行与压缩应标为失败")
	}
	msgs := e.messages()
	lost := false
	for _, m := range msgs {
		for _, p := range m.Parts {
			if p.Type == "tool_result" && p.Status == StatusLost {
				lost = true
			}
		}
	}
	if !lost {
		t.Fatal("没执行完的工具应补上丢失结果")
	}
	out := toLLM(e.reload(), msgs)
	for _, m := range out {
		for _, p := range m.Parts {
			if p.Type == "tool_call" || p.Type == "tool_result" {
				t.Fatal("回放时没执行的工具调用应丢掉")
			}
		}
	}
	// 尾部没有输出的 reasoning 丢掉
	msgs2 := []Message{{Seq: 1, Role: "user", Parts: []StoredPart{{Part: llm.Part{Type: "text", Text: "q"}}}},
		{Seq: 2, Role: "assistant", Parts: []StoredPart{{Part: llm.Part{Type: "text", Text: "a"}}, {Part: llm.Part{Type: "reasoning", Text: "r"}}}},
		{Seq: 3, Role: "assistant", Parts: []StoredPart{{Part: llm.Part{Type: "reasoning", Text: "only"}}}}}
	out = toLLM(&Conversation{}, msgs2)
	if len(out) != 2 || len(out[1].Parts) != 1 || out[1].Parts[0].Type != "text" {
		t.Fatalf("尾部 reasoning 应丢掉：%+v", out)
	}
}

func TestRunTaskUnattended(t *testing.T) {
	e := setup(t)
	c := e.f.SQLite(t, "db", conns.AIWriteAuto, nil)
	e.p.push(
		toolReply(call{"execute_sql", map[string]any{"connection_id": c.ID, "sql": "CREATE TABLE x (a INTEGER)", "reason": "建表"}},
			call{"run_query", map[string]any{"connection_id": c.ID, "sql": "SELECT status, COUNT(*) n FROM orders GROUP BY status"}}),
		toolReply(call{"report_write", map[string]any{"title": "日报", "body": "## 结论\n退款 17 单。"}}),
		textReply("今天退款 17 单，已写日报。"),
	)
	out, err := e.s.RunTask(context.Background(), TaskRunInput{UserID: e.f.Owner.ID, WorkspaceID: e.f.WS, TaskID: "task1", Title: "日报", Prompt: "生成日报",
		Grant: tools.Grant{c.ID: conns.AIWriteAuto}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != RunDone || out.Summary != "今天退款 17 单，已写日报。" || len(out.Reports) != 1 {
		t.Fatalf("任务运行结果不对：%+v", out)
	}
	reqs := e.p.mainReqs()
	for _, td := range reqs[0].Tools {
		if td.Name == "ask_user" || strings.HasPrefix(td.Name, "workspace_") {
			t.Fatalf("定时任务不应有 %s", td.Name)
		}
	}
	if !strings.Contains(reqs[0].System, "无人值守") {
		t.Fatal("系统提示应说明无人值守")
	}
	if !strings.Contains(reqText(reqs[1]), "无人值守，需要确认的写入一律拒绝") {
		t.Fatalf("需要确认的 DDL 应被拒绝：%s", reqText(reqs[1]))
	}
	conv, _ := e.s.getConv(context.Background(), out.ConversationID)
	if conv.Kind != KindTask || conv.TaskID != "task1" {
		t.Fatal("每次运行是一段独立的任务对话")
	}
}

func TestSystemUpdate(t *testing.T) {
	e := setup(t)
	e.p.push(textReply("一"), textReply("二"))
	run := e.send("第一句")
	e.wait(run)
	conv := e.reload()
	conv.Cfg.Mode = ModePlan
	_ = e.s.saveMeta(context.Background(), conv)
	c := e.f.SQLite(t, "new", conns.AIRead, nil)
	run = e.send("第二句")
	e.wait(run)
	reqs := e.p.mainReqs()
	if reqs[0].System != reqs[1].System {
		t.Fatal("系统提示不应变化（保住缓存）")
	}
	second := reqText(reqs[1])
	if !strings.Contains(second, "<system-update>") || !strings.Contains(second, "进入计划模式") || !strings.Contains(second, c.ID) {
		t.Fatalf("模式与连接变化应以 system-update 插入：%s", second)
	}
	// 界面上看不到隐藏的系统更新
	for _, m := range viewMessages(e.messages()) {
		for _, p := range m.Parts {
			if strings.Contains(p.Text, "<system-update>") {
				t.Fatal("系统更新不应显示给用户")
			}
		}
	}
}

func TestStopCancelsDatabaseQuery(t *testing.T) {
	e := setup(t)
	e.s.Clock = realClock{}
	c := e.f.SQLite(t, "slow", conns.AIRead, nil)
	e.p.push(toolReply(call{"run_query", map[string]any{"connection_id": c.ID,
		"sql": "WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM n) SELECT COUNT(*) FROM n"}}), textReply("不应该走到这里"))
	run := e.send("跑个慢查询")
	deadline := time.Now().Add(5 * time.Second)
	for len(events(run, "tool_running")) < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	_ = e.s.Stop(e.f.Owner.ID, run.ID)
	e.wait(run)
	if time.Since(start) > 3*time.Second {
		t.Fatalf("停止应取消数据库查询而不是等到强制中断：%v", time.Since(start))
	}
	ms := e.messages()
	res := ms[len(ms)-1].Parts[0]
	if res.Status != StatusInterrupted || strings.Contains(res.Text, "强制中断") {
		t.Fatalf("查询应被协作取消：%s %s", res.Status, res.Text)
	}
}
