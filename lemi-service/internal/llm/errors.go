package llm

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// 上下文溢出的常见文案（各家供应商，参考 OpenCode provider/error.ts）。
var overflowPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)prompt is too long`),
	regexp.MustCompile(`(?i)input is too long for requested model`),
	regexp.MustCompile(`(?i)exceeds? the context window`),
	regexp.MustCompile(`(?i)input token count.*exceeds the maximum`),
	regexp.MustCompile(`(?i)maximum prompt length is \d+`),
	regexp.MustCompile(`(?i)reduce the length of the messages`),
	regexp.MustCompile(`(?i)maximum context length is \d+ tokens`),
	regexp.MustCompile(`(?i)exceeds the limit of \d+`),
	regexp.MustCompile(`(?i)exceeds the available context size`),
	regexp.MustCompile(`(?i)greater than the context length`),
	regexp.MustCompile(`(?i)context window exceeds limit`),
	regexp.MustCompile(`(?i)exceeded model token limit`),
	regexp.MustCompile(`(?i)context[_ ]length[_ ]exceeded`),
	regexp.MustCompile(`(?i)too many (input )?tokens`),
	regexp.MustCompile(`(?i)input length .*exceed`),
	regexp.MustCompile(`上下文.{0,8}(超出|超过|过长)`),
	regexp.MustCompile(`(超出|超过).{0,8}(最大)?(上下文|长度)`),
}

var (
	contentPolicyRe = regexp.MustCompile(`(?i)content[_ ]?(policy|filter|management)|safety (system|filter)|data_inspection_failed|sensitive content|inappropriate content|违反|敏感`)
	quotaRe         = regexp.MustCompile(`(?i)insufficient[_ ]quota|quota exceeded|exceeded your (current )?quota|billing|out of credits|insufficient (balance|credits?|funds)|余额不足|额度`)
	authRe          = regexp.MustCompile(`(?i)invalid[_ ]api[_ ]key|incorrect api key|unauthorized|authentication|permission denied`)
	rateRe          = regexp.MustCompile(`(?i)rate[_ ]?limit|too many requests|请求过于频繁`)
	overloadRe      = regexp.MustCompile(`(?i)overloaded|server[_ ]error|internal error|service unavailable|bad gateway|upstream`)
)

// anthropicTypes Anthropic 的错误类型名 → 类别。
var anthropicTypes = map[string]ErrorKind{
	"invalid_request_error": ErrInvalidRequest, "authentication_error": ErrAuth, "permission_error": ErrAuth, "not_found_error": ErrInvalidRequest,
	"request_too_large": ErrRequestTooLarge, "rate_limit_error": ErrRateLimit, "api_error": ErrServer, "overloaded_error": ErrServer,
	"billing_error": ErrQuota,
}

// IsOverflowMessage 判断错误文案是否表示上下文溢出。
func IsOverflowMessage(msg string) bool {
	for _, re := range overflowPatterns {
		if re.MatchString(msg) {
			return true
		}
	}
	return false
}

// Classify 把任意错误归类。返回的 *APIError 一定带 Kind。
func Classify(err error) *APIError {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return &APIError{Kind: ErrCanceled, Message: "已停止"}
	}
	var ae *APIError
	if errors.As(err, &ae) {
		if ae.Kind == "" {
			c := *ae
			c.Kind = classifyAPI(ae.Status, ae.Code, ae.Message)
			return &c
		}
		return ae
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &APIError{Kind: ErrNetwork, Message: "请求超时"}
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return &APIError{Kind: ErrNetworkDown, Message: "域名解析失败：" + dnsErr.Error()}
	}
	if errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH) {
		return &APIError{Kind: ErrNetworkDown, Message: "网络不可达"}
	}
	msg := err.Error()
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "no such host") || strings.Contains(low, "network is unreachable") || strings.Contains(low, "no route to host") || strings.Contains(low, "temporary failure in name resolution"):
		return &APIError{Kind: ErrNetworkDown, Message: msg}
	case errors.Is(err, io.ErrUnexpectedEOF) || strings.Contains(low, "unexpected eof"):
		return &APIError{Kind: ErrStreamIncomplete, Message: "响应流中途断开"}
	case strings.Contains(low, "connection refused") || strings.Contains(low, "connection reset") || strings.Contains(low, "broken pipe") ||
		strings.Contains(low, "tls handshake") || strings.Contains(low, "timeout") || strings.Contains(low, "eof") || strings.Contains(low, "stream error") ||
		strings.Contains(low, "http2:") || strings.Contains(low, "server closed"):
		return &APIError{Kind: ErrNetwork, Message: msg}
	}
	if IsOverflowMessage(msg) {
		return &APIError{Kind: ErrContextOverflow, Message: msg}
	}
	return &APIError{Kind: ErrUnknown, Message: msg}
}

// classifyAPI 按状态码、错误码与文案归类供应商错误。
func classifyAPI(status int, code, msg string) ErrorKind {
	text := code + " " + msg
	if IsOverflowMessage(text) || code == "context_length_exceeded" || code == "string_above_max_length" {
		return ErrContextOverflow
	}
	// Anthropic 风格的错误类型（代理会把它包在自己的文案里，例如 "Upstream request failed: [invalid_request_error] ..."）
	for t, k := range anthropicTypes {
		if code == t || strings.Contains(msg, "["+t+"]") || strings.HasPrefix(msg, t) {
			if k == ErrInvalidRequest && (quotaRe.MatchString(text) || contentPolicyRe.MatchString(text)) {
				break
			}
			return k
		}
	}
	switch {
	case status == 413:
		return ErrRequestTooLarge
	case status == 401:
		return ErrAuth
	case status == 402:
		return ErrQuota
	case quotaRe.MatchString(text):
		return ErrQuota
	case contentPolicyRe.MatchString(text):
		return ErrContentPolicy
	case status == 403:
		if authRe.MatchString(text) {
			return ErrAuth
		}
		return ErrAuth
	case status == 429 || rateRe.MatchString(text):
		return ErrRateLimit
	case status >= 500 || status == 408 || status == 409 || status == 425:
		return ErrServer
	case status == 400 || status == 404 || status == 405 || status == 422:
		if overloadRe.MatchString(text) {
			return ErrServer
		}
		return ErrInvalidRequest
	case status == 0 && overloadRe.MatchString(text):
		return ErrServer
	case authRe.MatchString(text) && status == 0:
		return ErrAuth
	}
	return ErrUnknown
}

// Retryable 该类错误是否可以重试（不含上下文溢出：它触发压缩）。
func (k ErrorKind) Retryable() bool {
	switch k {
	case ErrRateLimit, ErrServer, ErrNetwork, ErrNetworkDown, ErrStreamIncomplete, ErrUnknown:
		return true
	}
	return false
}

// Label 给用户看的中文类别名。
func (k ErrorKind) Label() string {
	switch k {
	case ErrContextOverflow:
		return "上下文超出模型窗口"
	case ErrRequestTooLarge:
		return "请求内容过大"
	case ErrContentPolicy:
		return "触发内容安全策略"
	case ErrQuota:
		return "模型额度不足"
	case ErrAuth:
		return "模型密钥无效或没有权限"
	case ErrInvalidRequest:
		return "请求无效"
	case ErrRateLimit:
		return "模型服务限流"
	case ErrServer:
		return "模型服务端错误"
	case ErrNetwork:
		return "网络错误"
	case ErrNetworkDown:
		return "网络不可达"
	case ErrStreamIncomplete:
		return "响应流中途断开"
	case ErrCanceled:
		return "已停止"
	}
	return "未知错误"
}

// retryHints 读取响应头里的重试提示：retry-after-ms、retry-after（秒或 HTTP 日期）、x-should-retry。
func retryHints(h http.Header, now time.Time) (int64, *bool) {
	var ms int64
	if v := strings.TrimSpace(h.Get("retry-after-ms")); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			ms = int64(f)
		}
	}
	if ms == 0 {
		if v := strings.TrimSpace(h.Get("retry-after")); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
				ms = int64(f * 1000)
			} else if t, err := http.ParseTime(v); err == nil {
				if d := t.Sub(now); d > 0 {
					ms = d.Milliseconds()
				}
			}
		}
	}
	var should *bool
	switch strings.ToLower(strings.TrimSpace(h.Get("x-should-retry"))) {
	case "true":
		t := true
		should = &t
	case "false":
		f := false
		should = &f
	}
	return ms, should
}
