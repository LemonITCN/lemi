package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/textproto"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
	"github.com/LemonITCN/lemi/lemi-service/internal/netsafe"
	"github.com/LemonITCN/lemi/lemi-service/internal/version"
)

// dispatch 按渠道类型发一次。cfg 是解密后的配置。
func (s *Service) dispatch(ctx context.Context, kind string, cfg map[string]any, ev Event) error {
	switch kind {
	case KindEmail:
		return s.sendEmailChannel(ctx, cfg, ev)
	case KindWebhook:
		return s.sendWebhook(ctx, cfg, ev)
	case KindDingTalk:
		return s.sendDingTalk(ctx, cfg, ev)
	case KindWeCom:
		return s.sendWeCom(ctx, cfg, ev)
	case KindFeishu:
		return s.sendFeishu(ctx, cfg, ev)
	}
	return permanent(fmt.Errorf("未知的渠道类型 %q", kind))
}

func cfgStr(cfg map[string]any, k string) string { v, _ := cfg[k].(string); return v }

// userAgent 出站请求的 User-Agent。
func userAgent() string { return "Lemi/" + version.Version + " (+https://github.com/LemonITCN/lemi)" }

// do 发 HTTP 请求，返回状态码与最多 64 KB 的响应体。错误里不带地址（地址可能含令牌）。
func (s *Service) do(req *http.Request) (int, []byte, error) {
	req.Header.Set("User-Agent", userAgent())
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return 0, nil, cleanHTTPErr(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, body, nil
}

// cleanHTTPErr 去掉 *url.Error 里的请求地址（可能含令牌），只留原因。
func cleanHTTPErr(err error) error {
	if errors.Is(err, netsafe.ErrBlocked) {
		return permanent(errors.New("目标地址是内网或保留地址，已拦截（实例管理员可在系统设置里允许访问内网）"))
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		if ue.Timeout() {
			return errors.New("请求超时")
		}
		return fmt.Errorf("请求失败：%v", ue.Err)
	}
	return fmt.Errorf("请求失败：%v", err)
}

// httpStatusErr 非 2xx 响应。4xx（408、429 除外）重试也不会成功，不再重试。
func httpStatusErr(code int, body []byte) error {
	snippet := strings.TrimSpace(truncBytes(string(body), 300))
	err := fmt.Errorf("对方返回 HTTP %d", code)
	if snippet != "" {
		err = fmt.Errorf("对方返回 HTTP %d：%s", code, snippet)
	}
	if code >= 400 && code < 500 && code != http.StatusRequestTimeout && code != http.StatusTooManyRequests {
		return permanent(err)
	}
	return err
}

func (s *Service) parseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return nil, permanent(errors.New("地址格式不正确"))
	}
	if err := netsafe.CheckURLWith(u, s.Egress.AllowPrivate != nil && s.Egress.AllowPrivate()); err != nil {
		if errors.Is(err, netsafe.ErrBlocked) {
			return nil, permanent(errors.New("目标地址是内网或保留地址，已拦截（实例管理员可在系统设置里允许访问内网）"))
		}
		return nil, permanent(err)
	}
	return u, nil
}

// ---------- Webhook ----------

const defaultWebhookBody = `{"event":"{{event}}","level":"{{level}}","title":"{{title}}","message":"{{message}}","link":"{{link}}"}`
const defaultFormBody = `event={{event}}&level={{level}}&title={{title}}&message={{message}}&link={{link}}`
const defaultTextBody = "[{{level}}] {{title}}\n\n{{message}}\n\n{{link}}"

var tplVar = regexp.MustCompile(`\{\{\s*(title|message|level|link|event)\s*\}\}`)

// renderTemplate 一遍替换模板变量（替换进去的值不会再被当作变量展开），按 Content-Type 转义：
// JSON 做字符串转义（模板里自己写引号），表单做 URL 编码，纯文本原样。
func renderTemplate(tpl, contentType string, ev Event) string {
	vars := map[string]string{"title": ev.Title, "message": ev.Message, "level": ev.Level, "link": ev.Link, "event": ev.Kind}
	esc := func(v string) string { return v }
	switch {
	case strings.HasPrefix(contentType, "application/json"):
		esc = jsonEscape
	case strings.HasPrefix(contentType, "application/x-www-form-urlencoded"):
		esc = url.QueryEscape
	}
	return tplVar.ReplaceAllStringFunc(tpl, func(m string) string {
		name := tplVar.FindStringSubmatch(m)[1]
		return esc(vars[name])
	})
}

// jsonEscape 返回 JSON 字符串字面量去掉两边引号的部分。
func jsonEscape(v string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	out := strings.TrimSuffix(b.String(), "\n")
	return out[1 : len(out)-1]
}

// checkTemplate 保存时校验模板：JSON 类型用带引号、换行、反斜杠的样例值渲染后必须是合法 JSON。
func checkTemplate(tpl, contentType string) error {
	if strings.HasPrefix(contentType, "application/json") {
		sample := Event{Kind: "test", Level: "info", Title: `样例 "标题" \ 1`, Message: "第一行\n第二行 \"引号\" \\ </script>", Link: "https://example.com/a?b=1&c=2"}
		if !json.Valid([]byte(renderTemplate(tpl, contentType, sample))) {
			return apperr.Bad("正文模板替换变量后不是合法的 JSON（字符串变量要写在引号里，例如 \"title\":\"{{title}}\"）")
		}
	}
	return nil
}

// parseHeaders 解析「名称: 值」一行一个的自定义请求头。
func parseHeaders(raw string) (http.Header, error) {
	h := http.Header{}
	n := 0
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, val, ok := strings.Cut(line, ":")
		name, val = strings.TrimSpace(name), strings.TrimSpace(val)
		if !ok || !validHeaderName(name) {
			return nil, apperr.Bad("自定义请求头格式不正确，每行写「名称: 值」")
		}
		if strings.ContainsAny(val, "\r\n\x00") {
			return nil, apperr.Bad("自定义请求头的值不能包含换行")
		}
		switch strings.ToLower(name) {
		case "host", "content-length", "transfer-encoding", "connection", "te", "upgrade", "trailer", "expect",
			"proxy-connection", "keep-alive", "x-lemi-signature", "x-lemi-timestamp", "x-lemi-event":
			return nil, apperr.Bad("不能自定义请求头 %s", name)
		}
		h.Add(textproto.CanonicalMIMEHeaderKey(name), val)
		n++
		if n > 20 {
			return nil, apperr.Bad("自定义请求头最多 20 个")
		}
	}
	return h, nil
}

func validHeaderName(s string) bool {
	if s == "" || len(s) > 100 {
		return false
	}
	for _, r := range s {
		if !(r == '-' || r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

// WebhookSignature 计算 Webhook 签名：hex(HMAC-SHA256(secret, timestamp + "." + payload))。
// timestamp 是 Unix 秒（十进制字符串，即请求头 X-Lemi-Timestamp 的值）；payload 是 POST 的原始正文，
// GET 请求是完整查询串（URL 中 ? 之后的部分，按发送时的原样）。请求头 X-Lemi-Signature 的值为 "sha256=" + 返回值。
func WebhookSignature(secret, timestamp string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Service) sendWebhook(ctx context.Context, cfg map[string]any, ev Event) error {
	u, err := s.parseURL(cfgStr(cfg, "url"))
	if err != nil {
		return err
	}
	method := cfgStr(cfg, "method")
	if method != http.MethodGet {
		method = http.MethodPost
	}
	var hdr http.Header
	if raw := cfgStr(cfg, "headers"); raw != "" {
		if hdr, err = parseHeaders(raw); err != nil {
			return permanent(err)
		}
	}
	var body []byte
	ct := cfgStr(cfg, "content_type")
	if method == http.MethodGet {
		q := u.Query()
		for k, v := range map[string]string{"event": ev.Kind, "level": ev.Level, "title": ev.Title, "message": ev.Message, "link": ev.Link} {
			q.Set(k, v)
		}
		u.RawQuery = q.Encode()
	} else {
		if ct == "" {
			ct = "application/json"
		}
		tpl := cfgStr(cfg, "body")
		if tpl == "" {
			switch {
			case strings.HasPrefix(ct, "application/json"):
				tpl = defaultWebhookBody
			case strings.HasPrefix(ct, "application/x-www-form-urlencoded"):
				tpl = defaultFormBody
			default:
				tpl = defaultTextBody
			}
		}
		body = []byte(renderTemplate(tpl, ct, ev))
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return permanent(errors.New("地址格式不正确"))
	}
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", ct+"; charset=utf-8")
	} else {
		req.Body, req.ContentLength = nil, 0
	}
	req.Header.Set("X-Lemi-Event", ev.Kind)
	if secret := cfgStr(cfg, "sign_secret"); secret != "" {
		ts := strconv.FormatInt(s.now().Unix(), 10)
		payload := body
		if method == http.MethodGet {
			payload = []byte(u.RawQuery)
		}
		req.Header.Set("X-Lemi-Timestamp", ts)
		req.Header.Set("X-Lemi-Signature", "sha256="+WebhookSignature(secret, ts, payload))
	}
	code, resp, err := s.do(req)
	if err != nil {
		return err
	}
	if code < 200 || code > 299 {
		return httpStatusErr(code, resp)
	}
	return nil
}

// ---------- 机器人消息排版 ----------

// breakLinks 打断文本里的 Markdown 链接语法，防止消息内容（例如数据库返回的错误）伪造链接。
func breakLinks(s string) string { return strings.ReplaceAll(s, "](", "] (") }

// botMarkdown 机器人消息的 Markdown 正文。maxBytes 为整条正文的字节上限：超出时只截断消息内容，
// 标题、级别、时间与链接保持完整。
func botMarkdown(ev Event, now time.Time, levelText string, maxBytes int) string {
	head := "### [Lemi] " + breakLinks(ev.Title) + "\n\n"
	tail := "> 级别：" + levelText + "　事件：" + eventName(ev.Kind) + "\n\n" +
		"> 时间：" + now.Format("2006-01-02 15:04:05 -0700") + "\n"
	if ev.Link != "" {
		tail += "\n[查看详情](" + ev.Link + ")\n"
	}
	body := breakLinks(strings.ReplaceAll(ev.Message, "\n", "\n\n"))
	msg := ""
	if body != "" {
		msg = body + "\n\n"
	}
	if room := maxBytes - len(head) - len(tail); len(msg) > room {
		msg = ""
		if room > 8 {
			msg = truncBytes(body, room-2) + "\n\n"
		}
	}
	return truncBytes(head+msg+tail, maxBytes)
}

// postJSON 以 JSON 发 POST，返回响应体。
func (s *Service) postJSON(ctx context.Context, u *url.URL, payload any) ([]byte, error) {
	b, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(b))
	if err != nil {
		return nil, permanent(errors.New("地址格式不正确"))
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	code, body, err := s.do(req)
	if err != nil {
		return nil, err
	}
	if code < 200 || code > 299 {
		return nil, httpStatusErr(code, body)
	}
	return body, nil
}

// ---------- 钉钉 ----------
//
// 依据钉钉开放平台「自定义机器人安全设置」（https://open.dingtalk.com/document/robots/customize-robot-security-settings ，
// 镜像 https://help.dingtalk.io/zh/open/dingstart/customize-robot-security-settings.md ）：
// 加签时 timestamp 为当前毫秒时间戳（与服务器时间相差不能超过 1 小时），把 timestamp + "\n" + secret 作为签名字符串，
// 以 secret 为密钥做 HmacSHA256，结果 Base64 后再 urlEncode（UTF-8），作为 timestamp 与 sign 参数拼到 Webhook 地址后。
// 消息格式与返回值依据「自定义机器人发送群消息」：{"msgtype":"markdown","markdown":{"title":...,"text":...}}，
// 返回 {"errcode":0,"errmsg":"ok"}，errcode 非 0 即失败。

// DingTalkSign 计算钉钉加签：base64(HmacSHA256(key=secret, data=timestamp+"\n"+secret))（未 urlEncode）。
func DingTalkSign(secret, timestampMS string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestampMS + "\n" + secret))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Service) sendDingTalk(ctx context.Context, cfg map[string]any, ev Event) error {
	u, err := s.parseURL(cfgStr(cfg, "url"))
	if err != nil {
		return err
	}
	if secret := cfgStr(cfg, "secret"); secret != "" {
		ts := strconv.FormatInt(s.now().UnixMilli(), 10)
		q := u.Query()
		q.Set("timestamp", ts)
		q.Set("sign", DingTalkSign(secret, ts)) // Encode 时做 urlEncode（+ / = 编码为 %2B %2F %3D，与官方 quote_plus 一致）
		u.RawQuery = q.Encode()
	}
	payload := map[string]any{"msgtype": "markdown", "markdown": map[string]any{
		"title": truncRunes("[Lemi] "+ev.Title, 60),
		"text":  botMarkdown(ev, s.now(), levelName(ev.Level), 18000),
	}}
	body, err := s.postJSON(ctx, u, payload)
	if err != nil {
		return err
	}
	var r struct {
		ErrCode *int   `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if json.Unmarshal(body, &r) != nil || r.ErrCode == nil {
		return fmt.Errorf("钉钉返回的内容不是预期格式：%s", truncBytes(string(body), 200))
	}
	if *r.ErrCode != 0 {
		return fmt.Errorf("钉钉返回错误 %d：%s", *r.ErrCode, truncRunes(r.ErrMsg, 200))
	}
	return nil
}

// ---------- 企业微信 ----------
//
// 依据企业微信「消息推送配置说明」（https://developer.work.weixin.qq.com/document/path/91770 ）：
// 群机器人没有签名机制，鉴权靠地址里的 key；markdown 消息 {"msgtype":"markdown","markdown":{"content":...}}，
// content 最长 4096 字节（UTF-8），支持 <font color="info|comment|warning">；每个机器人每分钟最多 20 条。
// 返回 {"errcode":0,"errmsg":"ok"}，errcode 非 0 即失败。

func (s *Service) sendWeCom(ctx context.Context, cfg map[string]any, ev Event) error {
	u, err := s.parseURL(cfgStr(cfg, "url"))
	if err != nil {
		return err
	}
	color := "info"
	switch ev.Level {
	case LevelError:
		color = "warning"
	case LevelWarning:
		color = "comment"
	}
	level := `<font color="` + color + `">` + levelName(ev.Level) + `</font>`
	payload := map[string]any{"msgtype": "markdown", "markdown": map[string]any{
		"content": botMarkdown(ev, s.now(), level, 4000),
	}}
	body, err := s.postJSON(ctx, u, payload)
	if err != nil {
		return err
	}
	var r struct {
		ErrCode *int   `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if json.Unmarshal(body, &r) != nil || r.ErrCode == nil {
		return fmt.Errorf("企业微信返回的内容不是预期格式：%s", truncBytes(string(body), 200))
	}
	if *r.ErrCode != 0 {
		return fmt.Errorf("企业微信返回错误 %d：%s", *r.ErrCode, truncRunes(r.ErrMsg, 200))
	}
	return nil
}

// ---------- 飞书 ----------
//
// 依据飞书开放平台「自定义机器人使用指南」（https://open.feishu.cn/document/client-docs/bot-v3/add-custom-bot ）：
// 签名校验时 timestamp 为当前秒级时间戳（与服务器时间相差不能超过 1 小时），把 timestamp + "\n" + secret 作为
// HmacSHA256 的「密钥」，对空字符串计算签名，结果 Base64；timestamp（字符串）与 sign 放在请求体顶层。
// 消息用 interactive 卡片（card JSON 2.0：schema、header、body.elements 里放 markdown 与 open_url 按钮）。
// 返回 {"code":0,"msg":"success"}，code 非 0 即失败（如 19021 签名不匹配）；请求体不能超过 20 KB。

// FeishuSign 计算飞书签名：base64(HmacSHA256(key=timestamp+"\n"+secret, data=空))。
func FeishuSign(secret, timestampSec string) string {
	mac := hmac.New(sha256.New, []byte(timestampSec+"\n"+secret))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Service) sendFeishu(ctx context.Context, cfg map[string]any, ev Event) error {
	u, err := s.parseURL(cfgStr(cfg, "url"))
	if err != nil {
		return err
	}
	template := "blue"
	switch ev.Level {
	case LevelError:
		template = "red"
	case LevelWarning:
		template = "orange"
	}
	var md strings.Builder
	if ev.Message != "" {
		md.WriteString(breakLinks(truncBytes(ev.Message, 12000)) + "\n\n")
	}
	md.WriteString("**级别**：" + levelName(ev.Level) + "　**事件**：" + eventName(ev.Kind) + "\n")
	md.WriteString("**时间**：" + s.now().Format("2006-01-02 15:04:05 -0700"))
	elements := []any{map[string]any{"tag": "markdown", "content": md.String()}}
	if ev.Link != "" {
		elements = append(elements, map[string]any{
			"tag": "button", "type": "primary",
			"text":      map[string]any{"tag": "plain_text", "content": "查看详情"},
			"behaviors": []any{map[string]any{"type": "open_url", "default_url": ev.Link}},
		})
	}
	payload := map[string]any{
		"msg_type": "interactive",
		"card": map[string]any{
			"schema": "2.0",
			"header": map[string]any{"title": map[string]any{"tag": "plain_text", "content": "[Lemi] " + ev.Title}, "template": template},
			"body":   map[string]any{"elements": elements},
		},
	}
	if secret := cfgStr(cfg, "secret"); secret != "" {
		ts := strconv.FormatInt(s.now().Unix(), 10)
		payload["timestamp"] = ts
		payload["sign"] = FeishuSign(secret, ts)
	}
	body, err := s.postJSON(ctx, u, payload)
	if err != nil {
		return err
	}
	var r struct {
		Code       *int   `json:"code"`
		Msg        string `json:"msg"`
		StatusCode *int   `json:"StatusCode"`
	}
	if json.Unmarshal(body, &r) != nil || (r.Code == nil && r.StatusCode == nil) {
		return fmt.Errorf("飞书返回的内容不是预期格式：%s", truncBytes(string(body), 200))
	}
	code := r.StatusCode
	if r.Code != nil {
		code = r.Code
	}
	if *code != 0 {
		return fmt.Errorf("飞书返回错误 %d：%s", *code, truncRunes(r.Msg, 200))
	}
	return nil
}
