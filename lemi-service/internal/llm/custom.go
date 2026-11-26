package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/version"
)

// 自定义供应商支持的协议。
const (
	APIChat      = "chat-completions"
	APIMessages  = "messages"
	APIResponses = "responses"
)

// ValidAPI 协议名是否合法。
func ValidAPI(a string) bool { return a == APIChat || a == APIMessages || a == APIResponses }

// Custom 是自定义供应商：一个地址、一个密钥、一种协议；模型列表手填或从 /models 自动拉取。
type Custom struct {
	BaseURL    string
	APIKey     string
	Protocol   string
	Static     []Model      // 手填的模型（含上下文长度、是否看图）
	AutoModels bool         // 从 <BaseURL>/models 拉取，手填的信息覆盖拉到的同名模型
	Client     *http.Client // 出站客户端（经 netsafe 检查地址）；nil 用默认

	mu      sync.Mutex
	models  []Model
	fetched time.Time
}

func (p *Custom) headers() map[string]string {
	h := map[string]string{"User-Agent": version.UserAgent()}
	if p.APIKey == "" {
		return h
	}
	if p.Protocol == APIMessages {
		h["x-api-key"] = p.APIKey
	} else {
		h["Authorization"] = "Bearer " + p.APIKey
	}
	return h
}

// FetchModels 从 <BaseURL>/models 拉取模型 ID 列表（OpenAI 与 Anthropic 都是 {data:[{id}]}）。
func (p *Custom) FetchModels(ctx context.Context) ([]Model, error) {
	u := strings.TrimRight(p.BaseURL, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range p.headers() {
		req.Header.Set(k, v)
	}
	if p.Protocol == APIMessages {
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	c := p.Client
	if c == nil {
		c = publicClient
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second, Transport: c.Transport}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("获取模型列表失败：%v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != 200 {
		return nil, apiErrorFrom(resp.StatusCode, resp.Header, data)
	}
	var raw struct {
		Data []struct {
			ID            string `json:"id"`
			DisplayName   string `json:"display_name"`
			Name          string `json:"name"`
			ContextLength int    `json:"context_length"`
			ContextWindow int    `json:"context_window"`
			MaxInput      int    `json:"max_input_tokens"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("模型列表格式无法识别：%v", err)
	}
	var out []Model
	for _, m := range raw.Data {
		if m.ID == "" {
			continue
		}
		name := m.DisplayName
		if name == "" {
			name = m.Name
		}
		if name == "" {
			name = m.ID
		}
		ctxLen := m.ContextLength
		if ctxLen == 0 {
			ctxLen = m.ContextWindow
		}
		if ctxLen == 0 {
			ctxLen = m.MaxInput
		}
		out = append(out, Model{ID: m.ID, Name: name, Context: ctxLen, API: p.Protocol, Type: "chat"})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Models 返回可用模型（自动拉取的结果缓存一小时）。
func (p *Custom) Models(ctx context.Context) ([]Model, error) {
	fill := func(m Model) Model {
		if m.Name == "" {
			m.Name = m.ID
		}
		if m.Context <= 0 {
			m.Context = 128000
		}
		if m.MaxOutput <= 0 {
			m.MaxOutput = 16000
		}
		m.API, m.BaseURL, m.Type = p.Protocol, p.BaseURL, "chat"
		return m
	}
	if !p.AutoModels {
		out := make([]Model, 0, len(p.Static))
		for _, m := range p.Static {
			out = append(out, fill(m))
		}
		return out, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.models != nil && time.Since(p.fetched) < time.Hour {
		return p.models, nil
	}
	fetched, err := p.FetchModels(ctx)
	if err != nil {
		if p.models != nil {
			return p.models, nil
		}
		if len(p.Static) == 0 {
			return nil, err
		}
	}
	byID := map[string]Model{}
	var order []string
	for _, m := range fetched {
		byID[m.ID] = m
		order = append(order, m.ID)
	}
	for _, m := range p.Static {
		if _, ok := byID[m.ID]; !ok {
			order = append(order, m.ID)
		}
		byID[m.ID] = m
	}
	out := make([]Model, 0, len(order))
	for _, id := range order {
		out = append(out, fill(byID[id]))
	}
	p.models, p.fetched = out, time.Now()
	return out, nil
}

// Stream 按配置的协议发起流式请求。
func (p *Custom) Stream(ctx context.Context, req Request) (Stream, error) {
	var m Model
	ms, _ := p.Models(ctx)
	for _, x := range ms {
		if x.ID == req.Model {
			m = x
		}
	}
	if req.MaxTokens <= 0 {
		req.MaxTokens = 16000
		if m.MaxOutput > 0 && m.MaxOutput < req.MaxTokens {
			req.MaxTokens = m.MaxOutput
		}
	}
	effort := pickEffort(m, req.Effort)
	if len(m.Efforts) == 0 {
		effort = req.Effort
		if effort == "default" {
			effort = ""
		}
	}
	req.Effort = effort
	c := p.Client
	if c == nil {
		c = publicClient
	}
	switch p.Protocol {
	case APIMessages:
		return (&anthropicAdapter{client: c, baseURL: p.BaseURL, headers: p.headers(), thinking: effort != "" && effort != "none", temperature: true}).stream(ctx, req)
	case APIResponses:
		return (&responsesAdapter{client: c, baseURL: p.BaseURL, headers: p.headers(), supportsEffort: effort != ""}).stream(ctx, req)
	default:
		return (&chatAdapter{client: c, baseURL: p.BaseURL, headers: p.headers(), reasoningField: m.ReasoningField, supportsEffort: effort != "", temperature: true}).stream(ctx, req)
	}
}
