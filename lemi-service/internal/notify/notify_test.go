package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
	"github.com/LemonITCN/lemi/lemi-service/internal/crypt"
	"github.com/LemonITCN/lemi/lemi-service/internal/settings"
	"github.com/LemonITCN/lemi/lemi-service/internal/store"
)

type fixture struct {
	s     *Service
	st    *store.Store
	set   *settings.Service
	acc   *accounts.Service
	box   *crypt.Box
	au    *audit.Log
	owner *accounts.User
	ws    string
	ws2   string
	dir   string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	old := RetryDelays
	RetryDelays = []time.Duration{5 * time.Millisecond, 5 * time.Millisecond, 5 * time.Millisecond}
	t.Cleanup(func() { RetryDelays = old })
	dir := t.TempDir()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(dir, "lemi.db"))
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	key[3] = 9
	box, _ := crypt.New(key)
	acc := accounts.New(st)
	set := settings.New(st)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	au := audit.New(st, log)
	owner := &accounts.User{Username: "owner", PasswordHash: "x", Email: "owner@example.com"}
	if err := acc.CreateUser(ctx, owner); err != nil {
		t.Fatal(err)
	}
	ws, err := acc.CreateWorkspace(ctx, "测试", "", owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	ws2, err := acc.CreateWorkspace(ctx, "另一个", "", owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	s := New(Deps{Store: st, Accounts: acc, Settings: set, Box: box, Audit: au, Log: log, PublicURL: "https://lemi.example.com/"})
	t.Cleanup(func() {
		s.Close()
		st.Close()
	})
	return &fixture{s: s, st: st, set: set, acc: acc, box: box, au: au, owner: owner, ws: ws.ID, ws2: ws2.ID, dir: dir}
}

// allowPrivate 打开 / 关闭系统设置「允许访问内网」（httptest 服务器在 127.0.0.1）。
func (f *fixture) allowPrivate(t *testing.T, on bool) {
	t.Helper()
	sys := f.set.System(context.Background())
	sys.AllowPrivateEgress = on
	if err := f.set.SetSystem(context.Background(), sys); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) create(t *testing.T, kind string, cfg map[string]any) *Channel {
	t.Helper()
	c, err := f.s.Create(context.Background(), f.ws, f.owner.ID, &Input{Name: kind + " 渠道", Kind: kind, Config: cfg})
	if err != nil {
		t.Fatalf("创建渠道失败：%v", err)
	}
	return c
}

func (f *fixture) logs(t *testing.T) []LogEntry {
	t.Helper()
	list, _, err := f.s.Logs(context.Background(), f.ws, "", "", 200)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// recorder 是记录请求的 httptest 服务器。
type recorder struct {
	mu   sync.Mutex
	reqs []recorded
	srv  *httptest.Server
	resp func(n int, r *http.Request, body []byte) (int, string)
}

type recorded struct {
	Method string
	URL    *url.URL
	Header http.Header
	Body   []byte
}

func newRecorder(t *testing.T, resp func(n int, r *http.Request, body []byte) (int, string)) *recorder {
	rc := &recorder{resp: resp}
	rc.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rc.mu.Lock()
		rc.reqs = append(rc.reqs, recorded{Method: r.Method, URL: r.URL, Header: r.Header.Clone(), Body: body})
		n := len(rc.reqs)
		rc.mu.Unlock()
		code, out := 200, "ok"
		if rc.resp != nil {
			code, out = rc.resp(n, r, body)
		}
		w.WriteHeader(code)
		io.WriteString(w, out)
	}))
	t.Cleanup(rc.srv.Close)
	return rc
}

func (rc *recorder) all() []recorded {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return append([]recorded(nil), rc.reqs...)
}

// 带引号、反斜杠、换行、HTML、模板变量字样的事件，用来验证转义与「只替换一遍」。
var trickyEvent = Event{Kind: EvBackupFailed, Level: LevelError,
	Title:   `备份 "app" 失败 \ {{message}}`,
	Message: "第一行\n第二行 \"quoted\" </script> {{title}} \\n",
	Link:    "/w/ws1/backups?x=1&y=2"}

func TestWebhookPOSTTemplateHeadersSignature(t *testing.T) {
	f := setup(t)
	f.allowPrivate(t, true)
	rc := newRecorder(t, nil)
	c := f.create(t, KindWebhook, map[string]any{
		"url":          rc.srv.URL + "/hook?token=abc",
		"method":       "POST",
		"content_type": "application/json",
		"body":         `{"t":"{{ title }}","m":"{{message}}","l":"{{level}}","k":"{{link}}","e":"{{event}}","fixed":1}`,
		"headers":      "Authorization: Bearer tok-123\nX-Custom: hello",
		"sign_secret":  "whsec-xyz",
	})
	before := time.Now().Unix()
	f.s.Send(context.Background(), f.ws, []string{c.ID, c.ID, "unknown-id"}, trickyEvent)
	f.s.wait()
	reqs := rc.all()
	if len(reqs) != 1 {
		t.Fatalf("应发出 1 个请求（重复 ID 去重、未知 ID 忽略），实际 %d", len(reqs))
	}
	r := reqs[0]
	if r.Method != "POST" || r.URL.Path != "/hook" || r.URL.Query().Get("token") != "abc" {
		t.Fatalf("请求不对：%s %s", r.Method, r.URL)
	}
	var got map[string]any
	if err := json.Unmarshal(r.Body, &got); err != nil {
		t.Fatalf("正文不是合法 JSON：%v\n%s", err, r.Body)
	}
	if got["t"] != trickyEvent.Title || got["m"] != trickyEvent.Message || got["l"] != "error" || got["e"] != EvBackupFailed {
		t.Fatalf("模板变量替换或转义不对：%#v", got)
	}
	if got["k"] != "https://lemi.example.com/w/ws1/backups?x=1&y=2" {
		t.Fatalf("相对链接应按 PublicURL 补全：%v", got["k"])
	}
	if r.Header.Get("Authorization") != "Bearer tok-123" || r.Header.Get("X-Custom") != "hello" {
		t.Fatalf("自定义请求头没带上：%v", r.Header)
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") || r.Header.Get("X-Lemi-Event") != EvBackupFailed {
		t.Fatalf("请求头不对：%v", r.Header)
	}
	// 独立重算签名：hex(HMAC-SHA256(secret, ts + "." + body))
	ts := r.Header.Get("X-Lemi-Timestamp")
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || n < before-1 || n > time.Now().Unix()+1 {
		t.Fatalf("时间戳不对：%q", ts)
	}
	mac := hmac.New(sha256.New, []byte("whsec-xyz"))
	mac.Write([]byte(ts + "." + string(r.Body)))
	if want := "sha256=" + hex.EncodeToString(mac.Sum(nil)); r.Header.Get("X-Lemi-Signature") != want {
		t.Fatalf("签名不对：%s，应为 %s", r.Header.Get("X-Lemi-Signature"), want)
	}
	logs := f.logs(t)
	if len(logs) != 1 || logs[0].Status != "ok" || logs[0].Attempts != 1 || logs[0].Event != EvBackupFailed || logs[0].ChannelID != c.ID {
		t.Fatalf("发送记录不对：%+v", logs)
	}
}

func TestWebhookGETAndFormAndDefaults(t *testing.T) {
	f := setup(t)
	f.allowPrivate(t, true)
	rc := newRecorder(t, nil)
	get := f.create(t, KindWebhook, map[string]any{"url": rc.srv.URL + "/g?k=1", "method": "GET", "sign_secret": "s1"})
	form := f.create(t, KindWebhook, map[string]any{"url": rc.srv.URL + "/f", "content_type": "application/x-www-form-urlencoded"})
	def := f.create(t, KindWebhook, map[string]any{"url": rc.srv.URL + "/d"})
	f.s.Send(context.Background(), f.ws, []string{get.ID}, trickyEvent)
	f.s.wait()
	f.s.Send(context.Background(), f.ws, []string{form.ID}, trickyEvent)
	f.s.wait()
	f.s.Send(context.Background(), f.ws, []string{def.ID}, trickyEvent)
	f.s.wait()
	reqs := rc.all()
	if len(reqs) != 3 {
		t.Fatalf("应有 3 个请求，实际 %d", len(reqs))
	}
	g := reqs[0]
	q := g.URL.Query()
	if g.Method != "GET" || len(g.Body) != 0 || q.Get("k") != "1" || q.Get("title") != trickyEvent.Title || q.Get("message") != trickyEvent.Message || q.Get("event") != EvBackupFailed {
		t.Fatalf("GET 请求不对：%s %v", g.Method, q)
	}
	mac := hmac.New(sha256.New, []byte("s1"))
	mac.Write([]byte(g.Header.Get("X-Lemi-Timestamp") + "." + g.URL.RawQuery))
	if g.Header.Get("X-Lemi-Signature") != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
		t.Fatal("GET 请求应对完整查询串签名")
	}
	fr := reqs[1]
	vals, err := url.ParseQuery(string(fr.Body))
	if err != nil || vals.Get("title") != trickyEvent.Title || vals.Get("message") != trickyEvent.Message || vals.Get("level") != "error" {
		t.Fatalf("表单正文转义不对：%v %s", err, fr.Body)
	}
	if fr.Header.Get("X-Lemi-Signature") != "" {
		t.Fatal("没有签名密钥时不应带签名头")
	}
	var d map[string]any
	if err := json.Unmarshal(reqs[2].Body, &d); err != nil || d["title"] != trickyEvent.Title || d["event"] != EvBackupFailed {
		t.Fatalf("默认 JSON 模板不对：%v %s", err, reqs[2].Body)
	}
}

func TestTemplateValidation(t *testing.T) {
	f := setup(t)
	f.allowPrivate(t, true)
	_, err := f.s.Create(context.Background(), f.ws, f.owner.ID, &Input{Name: "坏模板", Kind: KindWebhook,
		Config: map[string]any{"url": "http://127.0.0.1:1/x", "body": `{"t":{{title}}}`}})
	if err == nil || !strings.Contains(err.Error(), "JSON") {
		t.Fatalf("变量没写在引号里的 JSON 模板应拒绝：%v", err)
	}
	for _, h := range []string{"Host: evil", "X-Lemi-Signature: x", "Bad Header: 1", "NoColon"} {
		_, err := f.s.Create(context.Background(), f.ws, f.owner.ID, &Input{Name: "坏请求头", Kind: KindWebhook,
			Config: map[string]any{"url": "http://127.0.0.1:1/x", "headers": h}})
		if err == nil {
			t.Fatalf("请求头 %q 应拒绝", h)
		}
	}
}

func TestDingTalkSign(t *testing.T) {
	f := setup(t)
	f.allowPrivate(t, true)
	fail := atomic.Bool{}
	rc := newRecorder(t, func(n int, r *http.Request, body []byte) (int, string) {
		if fail.Load() {
			return 200, `{"errcode":310000,"errmsg":"sign not match"}`
		}
		return 200, `{"errcode":0,"errmsg":"ok"}`
	})
	secret := "SECabc+/=测试"
	c := f.create(t, KindDingTalk, map[string]any{"url": rc.srv.URL + "/robot/send?access_token=tok", "secret": secret})
	f.s.Send(context.Background(), f.ws, []string{c.ID}, trickyEvent)
	f.s.wait()
	reqs := rc.all()
	if len(reqs) != 1 {
		t.Fatalf("请求数 %d", len(reqs))
	}
	r := reqs[0]
	q := r.URL.Query()
	ts := q.Get("timestamp")
	if q.Get("access_token") != "tok" || len(ts) != 13 {
		t.Fatalf("地址参数不对：%s", r.URL.RawQuery)
	}
	// 独立重算：base64(HmacSHA256(key=secret, timestamp+"\n"+secret))，并且在查询串里是 urlEncode 过的
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "\n" + secret))
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if q.Get("sign") != want {
		t.Fatalf("钉钉签名不对：%s，应为 %s", q.Get("sign"), want)
	}
	if strings.Contains(r.URL.RawQuery, "sign="+want) && strings.ContainsAny(want, "+/=") {
		t.Fatal("sign 参数应 urlEncode")
	}
	var body struct {
		MsgType  string `json:"msgtype"`
		Markdown struct {
			Title string `json:"title"`
			Text  string `json:"text"`
		} `json:"markdown"`
	}
	if err := json.Unmarshal(r.Body, &body); err != nil || body.MsgType != "markdown" || !strings.Contains(body.Markdown.Title, "Lemi") ||
		!strings.Contains(body.Markdown.Text, "第二行") || !strings.Contains(body.Markdown.Text, "https://lemi.example.com/w/ws1/backups") {
		t.Fatalf("钉钉消息格式不对：%v %s", err, r.Body)
	}
	// errcode 非 0 判失败，并重试 3 次
	fail.Store(true)
	f.s.Send(context.Background(), f.ws, []string{c.ID}, Event{Kind: EvTest, Title: "x"})
	f.s.wait()
	logs := f.logs(t)
	if logs[0].Status != "failed" || logs[0].Attempts != 4 || !strings.Contains(logs[0].Error, "310000") {
		t.Fatalf("errcode 非 0 应判失败并重试：%+v", logs[0])
	}
}

func TestFeishuSign(t *testing.T) {
	f := setup(t)
	f.allowPrivate(t, true)
	fail := atomic.Bool{}
	rc := newRecorder(t, func(n int, r *http.Request, body []byte) (int, string) {
		if fail.Load() {
			return 200, `{"code":19021,"msg":"sign match fail or timestamp is not within one hour from current time"}`
		}
		return 200, `{"code":0,"msg":"success","data":{}}`
	})
	c := f.create(t, KindFeishu, map[string]any{"url": rc.srv.URL + "/open-apis/bot/v2/hook/xyz", "secret": "fs-secret"})
	f.s.Send(context.Background(), f.ws, []string{c.ID}, trickyEvent)
	f.s.wait()
	reqs := rc.all()
	if len(reqs) != 1 {
		t.Fatalf("请求数 %d", len(reqs))
	}
	var body struct {
		Timestamp string `json:"timestamp"`
		Sign      string `json:"sign"`
		MsgType   string `json:"msg_type"`
		Card      struct {
			Schema string `json:"schema"`
			Header struct {
				Title struct {
					Content string `json:"content"`
				} `json:"title"`
				Template string `json:"template"`
			} `json:"header"`
			Body struct {
				Elements []map[string]any `json:"elements"`
			} `json:"body"`
		} `json:"card"`
	}
	if err := json.Unmarshal(reqs[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	// 独立重算：base64(HmacSHA256(key=timestamp+"\n"+secret, data=空))，时间戳为秒
	mac := hmac.New(sha256.New, []byte(body.Timestamp+"\n"+"fs-secret"))
	if want := base64.StdEncoding.EncodeToString(mac.Sum(nil)); body.Sign != want || len(body.Timestamp) != 10 {
		t.Fatalf("飞书签名不对：%s / %s，应为 %s", body.Timestamp, body.Sign, want)
	}
	if body.MsgType != "interactive" || body.Card.Schema != "2.0" || body.Card.Header.Template != "red" ||
		!strings.Contains(body.Card.Header.Title.Content, trickyEvent.Title) || len(body.Card.Body.Elements) != 2 {
		t.Fatalf("飞书卡片不对：%s", reqs[0].Body)
	}
	fail.Store(true)
	f.s.Send(context.Background(), f.ws, []string{c.ID}, Event{Title: "x"})
	f.s.wait()
	if l := f.logs(t)[0]; l.Status != "failed" || !strings.Contains(l.Error, "19021") {
		t.Fatalf("code 非 0 应判失败：%+v", l)
	}
}

func TestWeCom(t *testing.T) {
	f := setup(t)
	f.allowPrivate(t, true)
	fail := atomic.Bool{}
	rc := newRecorder(t, func(n int, r *http.Request, body []byte) (int, string) {
		if fail.Load() {
			return 200, `{"errcode":93000,"errmsg":"invalid webhook url"}`
		}
		return 200, `{"errcode":0,"errmsg":"ok"}`
	})
	c := f.create(t, KindWeCom, map[string]any{"url": rc.srv.URL + "/cgi-bin/webhook/send?key=k1"})
	ev := trickyEvent
	ev.Message = strings.Repeat("很长的消息", 2000)
	f.s.Send(context.Background(), f.ws, []string{c.ID}, ev)
	f.s.wait()
	reqs := rc.all()
	var body struct {
		MsgType  string `json:"msgtype"`
		Markdown struct {
			Content string `json:"content"`
		} `json:"markdown"`
	}
	if err := json.Unmarshal(reqs[0].Body, &body); err != nil || body.MsgType != "markdown" {
		t.Fatalf("企业微信消息格式不对：%v %s", err, reqs[0].Body)
	}
	if len(body.Markdown.Content) > 4096 || !strings.Contains(body.Markdown.Content, `<font color="warning">`) || reqs[0].URL.Query().Get("key") != "k1" {
		t.Fatalf("企业微信 markdown 应不超过 4096 字节并带颜色：%d 字节", len(body.Markdown.Content))
	}
	fail.Store(true)
	f.s.Send(context.Background(), f.ws, []string{c.ID}, Event{Title: "x"})
	f.s.wait()
	if l := f.logs(t)[0]; l.Status != "failed" || l.Attempts != 4 || !strings.Contains(l.Error, "93000") {
		t.Fatalf("errcode 非 0 应判失败：%+v", l)
	}
}

func TestRetryAndRecord(t *testing.T) {
	f := setup(t)
	f.allowPrivate(t, true)
	mode := atomic.Int32{}
	rc := newRecorder(t, func(n int, r *http.Request, body []byte) (int, string) {
		switch mode.Load() {
		case 0: // 前两次 500，第三次成功
			if n <= 2 {
				return 500, "boom"
			}
			return 200, "ok"
		case 1:
			return 503, "down"
		default:
			return 401, "bad token"
		}
	})
	c := f.create(t, KindWebhook, map[string]any{"url": rc.srv.URL})
	f.s.Send(context.Background(), f.ws, []string{c.ID}, Event{Kind: EvConnDown, Title: "第一条"})
	f.s.wait()
	if l := f.logs(t)[0]; l.Status != "ok" || l.Attempts != 3 {
		t.Fatalf("前两次失败第三次成功：%+v", l)
	}
	mode.Store(1)
	f.s.Send(context.Background(), f.ws, []string{c.ID}, Event{Kind: EvConnDown, Title: "第二条"})
	f.s.wait()
	if l := f.logs(t)[0]; l.Status != "failed" || l.Attempts != 4 || !strings.Contains(l.Error, "503") || l.FinishedAt == 0 {
		t.Fatalf("一直失败应重试 3 次（共 4 次）：%+v", l)
	}
	mode.Store(2)
	f.s.Send(context.Background(), f.ws, []string{c.ID}, Event{Kind: EvConnDown, Title: "第三条"})
	f.s.wait()
	if l := f.logs(t)[0]; l.Status != "failed" || l.Attempts != 1 {
		t.Fatalf("4xx 不应重试：%+v", l)
	}
	if n := len(rc.all()); n != 3+4+1 {
		t.Fatalf("总请求数应为 8，实际 %d", n)
	}
	// 停用的渠道不发
	en := false
	if err := f.s.Update(context.Background(), c, &Input{Name: c.Name, Kind: c.Kind, Enabled: &en, Config: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	f.s.Send(context.Background(), f.ws, []string{c.ID}, Event{Title: "停用"})
	f.s.wait()
	if n := len(rc.all()); n != 8 {
		t.Fatal("停用的渠道不应发送")
	}
	// 发送记录清理
	_, _ = f.st.Exec(context.Background(), `UPDATE notify_logs SET created_at = ? WHERE title = '第一条'`, time.Now().Add(-31*24*time.Hour).UnixMilli())
	f.s.Prune(context.Background())
	for _, l := range f.logs(t) {
		if l.Title == "第一条" {
			t.Fatal("超过 30 天的记录应被清理")
		}
	}
	if len(f.logs(t)) != 2 {
		t.Fatal("没过期的记录不应清理")
	}
}

func TestSendOnlyToOwnWorkspace(t *testing.T) {
	f := setup(t)
	f.allowPrivate(t, true)
	rc := newRecorder(t, nil)
	c := f.create(t, KindWebhook, map[string]any{"url": rc.srv.URL})
	f.s.Send(context.Background(), f.ws2, []string{c.ID}, Event{Title: "别的空间"})
	f.s.wait()
	if len(rc.all()) != 0 {
		t.Fatal("不属于该空间的渠道不应发送")
	}
	ctx := context.Background()
	if err := f.s.CheckChannels(ctx, f.ws, []string{c.ID}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.CheckChannels(ctx, f.ws2, []string{c.ID}); err == nil {
		t.Fatal("别的空间的渠道应校验失败")
	}
	if err := f.s.CheckChannels(ctx, f.ws, []string{c.ID, "nope"}); err == nil {
		t.Fatal("不存在的渠道应校验失败")
	}
	if got := f.s.FilterChannels(ctx, f.ws, []string{"nope", c.ID}); len(got) != 1 || got[0] != c.ID {
		t.Fatalf("FilterChannels 不对：%v", got)
	}
}

func TestSecretsWriteOnly(t *testing.T) {
	f := setup(t)
	f.allowPrivate(t, true)
	rc := newRecorder(t, nil)
	ctx := context.Background()
	hook := rc.srv.URL + "/hook?token=SUPERSECRETTOKEN"
	c := f.create(t, KindWebhook, map[string]any{"url": hook, "headers": "Authorization: Bearer HEADERSECRET", "sign_secret": "SIGNSECRET"})
	// 数据库里是密文
	var raw string
	_ = f.st.Get(ctx, &raw, `SELECT config FROM notify_channels WHERE id = ?`, c.ID)
	for _, p := range []string{"SUPERSECRETTOKEN", "HEADERSECRET", "SIGNSECRET"} {
		if strings.Contains(raw, p) {
			t.Fatalf("数据库里不应有明文 %s", p)
		}
	}
	// View 只给 secrets_set 与主机名
	v := f.s.View(c, true)
	b, _ := json.Marshal(v)
	for _, p := range []string{"SUPERSECRETTOKEN", "HEADERSECRET", "SIGNSECRET", "enc:v1:"} {
		if strings.Contains(string(b), p) {
			t.Fatalf("View 里不应出现 %s：%s", p, b)
		}
	}
	if set, _ := v["secrets_set"].([]string); len(set) != 3 || v["url_host"] != "127.0.0.1" {
		t.Fatalf("secrets_set / url_host 不对：%s", b)
	}
	if _, has := f.s.View(c, false)["config"]; has {
		t.Fatal("非管理员视图不应带配置")
	}
	// 更新时不传密钥 = 保持原值
	if err := f.s.Update(ctx, c, &Input{Name: "改名", Kind: KindWebhook, Config: map[string]any{"method": "POST"}}); err != nil {
		t.Fatal(err)
	}
	cfg, err := f.s.decryptConfig(c)
	if err != nil || cfg["url"] != hook || cfg["sign_secret"] != "SIGNSECRET" || !strings.Contains(cfgStr(cfg, "headers"), "HEADERSECRET") {
		t.Fatalf("不传密钥应保持原值：%v %v", err, cfg)
	}
	// 重新输入一样的地址：视为没换，其他密钥沿用
	if err := f.s.Update(ctx, c, &Input{Name: "改名", Kind: KindWebhook, Config: map[string]any{"url": hook}}); err != nil {
		t.Fatalf("地址没变时应沿用其他密钥：%v", err)
	}
	// 换地址：自定义头与签名密钥必须重新输入或清除
	other := rc.srv.URL + "/other"
	err = f.s.Update(ctx, c, &Input{Name: "改名", Kind: KindWebhook, Config: map[string]any{"url": other}})
	if err == nil || !strings.Contains(err.Error(), "重新输入") {
		t.Fatalf("换地址不重输密钥应拒绝：%v", err)
	}
	if err := f.s.Update(ctx, c, &Input{Name: "改名", Kind: KindWebhook, Config: map[string]any{"url": other, "sign_secret": "NEW"}, Clear: []string{"headers"}}); err != nil {
		t.Fatalf("换地址并重输 / 清除后应通过：%v", err)
	}
	cfg, _ = f.s.decryptConfig(c)
	if cfg["url"] != other || cfg["sign_secret"] != "NEW" || cfg["headers"] != nil {
		t.Fatalf("更新后的配置不对：%v", cfg)
	}
	// 不接受客户端传来的密文
	var sealed map[string]any
	_ = json.Unmarshal([]byte(c.Config), &sealed)
	if err := f.s.Update(ctx, c, &Input{Name: "改名", Kind: KindWebhook, Config: map[string]any{"sign_secret": sealed["sign_secret"]}}); err == nil {
		t.Fatal("应拒绝客户端传来的密文")
	}
	// 密文挪到别的渠道解不开（AAD 绑定渠道 ID）
	c2 := f.create(t, KindWebhook, map[string]any{"url": rc.srv.URL + "/x"})
	c2.Config = c.Config
	if _, err := f.s.decryptConfig(c2); err == nil {
		t.Fatal("密文挪到别的渠道不应能解开")
	}
	// 邮件：主机或端口改了必须重新输入口令
	e := f.create(t, KindEmail, map[string]any{"recipients": "a@example.com, b@example.com", "use_system": false, "host": "127.0.0.1", "port": 2525,
		"tls": "starttls", "username": "u", "password": "MAILPASS", "from": "Lemi <noreply@example.com>"})
	if err := f.s.Update(ctx, e, &Input{Name: "邮件", Kind: KindEmail, Config: map[string]any{"recipients": []any{"a@example.com"}, "use_system": false,
		"host": "127.0.0.1", "port": 2525, "tls": "starttls", "username": "u", "from": "noreply@example.com"}}); err != nil {
		t.Fatalf("主机端口没变时应保留口令：%v", err)
	}
	err = f.s.Update(ctx, e, &Input{Name: "邮件", Kind: KindEmail, Config: map[string]any{"recipients": []any{"a@example.com"}, "use_system": false,
		"host": "127.0.0.1", "port": 2526, "tls": "starttls", "username": "u", "from": "noreply@example.com"}})
	if err == nil || !strings.Contains(err.Error(), "口令") {
		t.Fatalf("端口改了不重输口令应拒绝：%v", err)
	}
	cfg, _ = f.s.decryptConfig(e)
	if cfg["password"] != "MAILPASS" {
		t.Fatal("被拒绝的更新不应改动已保存的口令")
	}
}

func TestEgressBlocked(t *testing.T) {
	f := setup(t)
	rc := newRecorder(t, nil)
	ctx := context.Background()
	f.allowPrivate(t, false)
	_, err := f.s.Create(ctx, f.ws, f.owner.ID, &Input{Name: "内网", Kind: KindWebhook, Config: map[string]any{"url": rc.srv.URL}})
	if err == nil || !strings.Contains(err.Error(), "内网") {
		t.Fatalf("不允许内网时保存应拒绝：%v", err)
	}
	f.allowPrivate(t, true)
	c := f.create(t, KindWebhook, map[string]any{"url": rc.srv.URL})
	f.allowPrivate(t, false)
	f.s.Send(ctx, f.ws, []string{c.ID}, Event{Title: "被拦截"})
	f.s.wait()
	if l := f.logs(t)[0]; l.Status != "failed" || l.Attempts != 1 || !strings.Contains(l.Error, "内网") {
		t.Fatalf("不允许内网时发送应被拦截且不重试：%+v", l)
	}
	if len(rc.all()) != 0 {
		t.Fatal("被拦截时不应发出请求")
	}
	// 测试发送也一样
	if _, err := f.s.TestChannel(ctx, c); err == nil {
		t.Fatal("测试发送也应被拦截")
	}
	f.allowPrivate(t, true)
	if _, err := f.s.TestChannel(ctx, c); err != nil {
		t.Fatalf("允许内网后应能发送：%v", err)
	}
	if len(rc.all()) != 1 {
		t.Fatal("允许后应发出请求")
	}
	// 云元数据地址任何时候都拒绝
	if _, err := f.s.Create(ctx, f.ws, f.owner.ID, &Input{Name: "元数据", Kind: KindWebhook, Config: map[string]any{"url": "http://169.254.169.254/latest"}}); err == nil {
		t.Fatal("云元数据地址应拒绝")
	}
}

func TestErrorsDoNotLeakURL(t *testing.T) {
	f := setup(t)
	f.allowPrivate(t, true)
	// 一个立即关闭的端口：连接被拒绝的错误里不应带出含令牌的地址
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := srv.URL
	srv.Close()
	c := f.create(t, KindWebhook, map[string]any{"url": addr + "/hook?token=LEAKME"})
	_, err := f.s.TestChannel(context.Background(), c)
	if err == nil {
		t.Fatal("应失败")
	}
	if strings.Contains(err.Error(), "LEAKME") {
		t.Fatalf("错误里不应带地址：%v", err)
	}
	if l := f.logs(t)[0]; strings.Contains(l.Error, "LEAKME") {
		t.Fatalf("发送记录里不应带地址：%v", l.Error)
	}
}

func TestResolveLink(t *testing.T) {
	s := &Service{publicURL: "https://a.example.com"}
	cases := map[string]string{
		"/w/x":                "https://a.example.com/w/x",
		"https://b.example/p": "https://b.example/p",
		"javascript:alert(1)": "",
		"//evil.example/x":    "",
		"":                    "",
		"/a\r\nX-Injected: 1": "",
	}
	for in, want := range cases {
		if got := s.resolveLink(in); got != want {
			t.Errorf("resolveLink(%q) = %q，应为 %q", in, got, want)
		}
	}
	s.publicURL = ""
	if s.resolveLink("/w/x") != "" {
		t.Error("PublicURL 为空时相对链接应去掉")
	}
}
