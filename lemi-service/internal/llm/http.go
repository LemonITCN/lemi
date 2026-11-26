package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// publicClient 访问公网供应商（OpenCode Go）用：走环境代理，不设整体超时（流式响应可能持续数分钟）。
var publicClient = &http.Client{Timeout: 0, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, MaxIdleConns: 20, IdleConnTimeout: 90 * time.Second,
	TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 180 * time.Second}}

// postStream 发起流式请求；非 2xx 时读出错误信息并归类。
func postStream(ctx context.Context, client *http.Client, url string, headers map[string]string, body any) (*http.Response, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if client == nil {
		client = publicClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return nil, apiErrorFrom(resp.StatusCode, resp.Header, data)
	}
	return resp, nil
}

// apiErrorFrom 从错误响应构造并归类错误。
func apiErrorFrom(status int, h http.Header, data []byte) *APIError {
	code, msg := errorBody(data)
	e := &APIError{Status: status, Code: code, Message: msg}
	if h != nil {
		e.RetryAfterMS, e.ShouldRetry = retryHints(h, time.Now())
	}
	e.Kind = classifyAPI(status, code, msg)
	return e
}

// errorBody 解析常见的错误响应体：{"error":{"message","type","code"}}、{"error":"..."}、{"message":"..."}。
func errorBody(data []byte) (code, msg string) {
	var v struct {
		Error any    `json:"error"`
		Msg   string `json:"message"`
		Code  any    `json:"code"`
		Type  string `json:"type"`
	}
	if json.Unmarshal(data, &v) == nil {
		switch e := v.Error.(type) {
		case string:
			msg = e
		case map[string]any:
			if m, ok := e["message"].(string); ok {
				msg = m
			}
			if c, ok := e["code"].(string); ok {
				code = c
			} else if t, ok := e["type"].(string); ok {
				code = t
			}
			if code == "" {
				if t, ok := e["type"].(string); ok {
					code = t
				}
			}
		}
		if msg == "" {
			msg = v.Msg
		}
		if code == "" {
			if c, ok := v.Code.(string); ok {
				code = c
			}
		}
		if msg != "" {
			return code, msg
		}
	}
	s := strings.TrimSpace(string(data))
	if len(s) > 300 {
		s = s[:300]
	}
	if s == "" {
		s = "（没有错误说明）"
	}
	return code, s
}

// streamError 把流里出现的错误事件归类。
func streamError(code, msg string) *APIError {
	e := &APIError{Code: code, Message: msg}
	e.Kind = classifyAPI(0, code, msg)
	return e
}

// incomplete 是流没有正常结束标记就断开的错误。
func incomplete() *APIError {
	return &APIError{Kind: ErrStreamIncomplete, Message: "响应流没有正常结束就断开了"}
}
