package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/LemonITCN/lemi/lemi-service/internal/crypt"
	"github.com/LemonITCN/lemi/lemi-service/internal/ids"
	"github.com/LemonITCN/lemi/lemi-service/internal/llm"
	"github.com/LemonITCN/lemi/lemi-service/internal/netsafe"
	"github.com/LemonITCN/lemi/lemi-service/internal/store"
)

// 供应商种类。
const (
	ProviderOpenCode = "opencode-go"
	ProviderCustom   = "custom"
)

// ProviderRow 是 ai_providers 的一行。
type ProviderRow struct {
	ID         string `db:"id" json:"id"`
	Kind       string `db:"kind" json:"kind"`
	Name       string `db:"name" json:"name"`
	BaseURL    string `db:"base_url" json:"base_url"`
	APIKey     string `db:"api_key" json:"-"`
	Protocol   string `db:"protocol" json:"protocol"`
	Models     string `db:"models" json:"-"`
	AutoModels bool   `db:"auto_models" json:"auto_models"`
	Enabled    bool   `db:"enabled" json:"enabled"`
	CreatedAt  int64  `db:"created_at" json:"created_at"`
	UpdatedAt  int64  `db:"updated_at" json:"updated_at"`
}

// ProviderInput 是新建或修改供应商的请求（api_key 为空表示保持原值）。
type ProviderInput struct {
	Kind       string      `json:"kind"`
	Name       string      `json:"name"`
	BaseURL    string      `json:"base_url"`
	APIKey     string      `json:"api_key"`
	Protocol   string      `json:"protocol"`
	Models     []llm.Model `json:"models"`
	AutoModels bool        `json:"auto_models"`
	Enabled    *bool       `json:"enabled"`
}

// ModelInfo 是给界面的模型（带供应商）。
type ModelInfo struct {
	llm.Model
	Ref          string `json:"ref"` // <供应商 ID>/<模型 ID>
	ProviderID   string `json:"provider_id"`
	ProviderName string `json:"provider_name"`
}

// ErrNoProvider 还没有配置模型供应商。
var ErrNoProvider = errors.New("实例管理员还没有配置模型供应商（系统设置 → AI 模型）")

func aadProvider(id string) string { return crypt.AAD("ai_providers", id, "api_key") }

// View 给界面的供应商描述（密钥只给是否已设置）。
func (p *ProviderRow) View() map[string]any {
	var ms []llm.Model
	_ = json.Unmarshal([]byte(p.Models), &ms)
	if ms == nil {
		ms = []llm.Model{}
	}
	return map[string]any{"id": p.ID, "kind": p.Kind, "name": p.Name, "base_url": p.BaseURL, "protocol": p.Protocol, "models": ms,
		"auto_models": p.AutoModels, "enabled": p.Enabled, "api_key": map[string]any{"set": p.APIKey != ""}, "created_at": p.CreatedAt, "updated_at": p.UpdatedAt}
}

// Providers 全部供应商。
func (s *Service) Providers(ctx context.Context) ([]ProviderRow, error) {
	var out []ProviderRow
	err := s.st.Select(ctx, &out, `SELECT * FROM ai_providers ORDER BY created_at`)
	return out, err
}

func (s *Service) providerRow(ctx context.Context, id string) (*ProviderRow, error) {
	var p ProviderRow
	if err := s.st.GetW(ctx, &p, `SELECT * FROM ai_providers WHERE id = ?`, id); err != nil {
		return nil, errors.New("供应商不存在")
	}
	return &p, nil
}

// SaveProvider 新建（id 为空）或修改供应商。
func (s *Service) SaveProvider(ctx context.Context, id string, in ProviderInput) (*ProviderRow, error) {
	var p *ProviderRow
	if id != "" {
		old, err := s.providerRow(ctx, id)
		if err != nil {
			return nil, err
		}
		p = old
	} else {
		p = &ProviderRow{ID: ids.New(), Kind: in.Kind, Enabled: true, CreatedAt: store.Now()}
	}
	switch p.Kind {
	case ProviderOpenCode:
		if p.Name = strings.TrimSpace(in.Name); p.Name == "" {
			p.Name = "OpenCode Go"
		}
		p.BaseURL = strings.TrimSpace(in.BaseURL) // 模型列表地址，空 = 默认
		if p.BaseURL != "" {
			if u, err := url.Parse(p.BaseURL); err != nil || u.Scheme != "https" {
				return nil, errors.New("模型列表地址必须是 https 地址")
			}
		}
	case ProviderCustom:
		p.Name = strings.TrimSpace(in.Name)
		if p.Name == "" || len([]rune(p.Name)) > 60 {
			return nil, errors.New("供应商名称不能为空，且不超过 60 个字符")
		}
		base, err := s.checkCustomURL(ctx, in.BaseURL)
		if err != nil {
			return nil, err
		}
		u, _ := url.Parse(base)
		oldHost := ""
		if pu, err := url.Parse(p.BaseURL); err == nil {
			oldHost = pu.Host
		}
		if id != "" && oldHost != "" && !strings.EqualFold(oldHost, u.Host) && in.APIKey == "" && p.APIKey != "" {
			return nil, errors.New("地址的主机变了，请重新输入密钥")
		}
		p.BaseURL = base
		if !llm.ValidAPI(in.Protocol) {
			return nil, errors.New("协议应为 chat-completions、messages 或 responses")
		}
		p.Protocol = in.Protocol
		p.AutoModels = in.AutoModels
		var ms []llm.Model
		for _, m := range in.Models {
			m.ID = strings.TrimSpace(m.ID)
			if m.ID == "" {
				continue
			}
			ms = append(ms, llm.Model{ID: m.ID, Name: strings.TrimSpace(m.Name), Context: m.Context, MaxOutput: m.MaxOutput, Vision: m.Vision, Efforts: m.Efforts})
		}
		if len(ms) == 0 && !p.AutoModels {
			return nil, errors.New("请填写至少一个模型，或打开自动拉取模型列表")
		}
		b, _ := json.Marshal(ms)
		p.Models = string(b)
	default:
		return nil, errors.New("未知的供应商种类")
	}
	if in.APIKey != "" {
		p.APIKey = s.box.Seal(strings.TrimSpace(in.APIKey), aadProvider(p.ID))
	}
	if p.Kind == ProviderOpenCode && p.APIKey == "" {
		return nil, errors.New("请填写 OpenCode Go 密钥")
	}
	if in.Enabled != nil {
		p.Enabled = *in.Enabled
	}
	p.UpdatedAt = store.Now()
	if err := s.st.Put(ctx, "ai_providers", p); err != nil {
		return nil, err
	}
	s.provMu.Lock()
	delete(s.provCache, p.ID)
	s.provMu.Unlock()
	return p, nil
}

// DeleteProvider 删除供应商。
func (s *Service) DeleteProvider(ctx context.Context, id string) error {
	_, err := s.st.Exec(ctx, `DELETE FROM ai_providers WHERE id = ?`, id)
	s.provMu.Lock()
	delete(s.provCache, id)
	s.provMu.Unlock()
	return err
}

// checkCustomURL 校验自定义供应商地址：http/https；内网地址要实例管理员允许（AllowPrivateEgress）。
func (s *Service) checkCustomURL(ctx context.Context, raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", errors.New("地址格式不正确，例如 https://api.example.com/v1")
	}
	allow := s.set.System(ctx).AllowPrivateEgress
	netsafe.AllowPrivate = allow
	if err := netsafe.CheckURL(u); err != nil {
		return "", fmt.Errorf("地址不允许：%v（内网地址需要实例管理员在系统设置里允许访问内网）", err)
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// egressClient 自定义供应商的出站客户端：建连时按实际 IP 检查（防 DNS 重绑定）。
func (s *Service) egressClient(ctx context.Context) *http.Client {
	netsafe.AllowPrivate = s.set.System(ctx).AllowPrivateEgress
	return netsafe.StreamingClient()
}

type cachedProvider struct {
	version int64
	p       llm.Provider
}

// provider 构造供应商实例（缓存，配置变了重建）。
func (s *Service) provider(ctx context.Context, p *ProviderRow) (llm.Provider, error) {
	s.provMu.Lock()
	if c := s.provCache[p.ID]; c != nil && c.version == p.UpdatedAt {
		s.provMu.Unlock()
		return c.p, nil
	}
	s.provMu.Unlock()
	key, err := s.box.Open(p.APIKey, aadProvider(p.ID))
	if err != nil {
		return nil, fmt.Errorf("供应商「%s」的密钥无法解密", p.Name)
	}
	var prov llm.Provider
	switch p.Kind {
	case ProviderOpenCode:
		prov = &llm.OpenCodeGo{APIKey: key, ModelsURL: p.BaseURL}
	case ProviderCustom:
		var ms []llm.Model
		_ = json.Unmarshal([]byte(p.Models), &ms)
		prov = &llm.Custom{BaseURL: p.BaseURL, APIKey: key, Protocol: p.Protocol, Static: ms, AutoModels: p.AutoModels, Client: s.egressClient(ctx)}
	default:
		return nil, errors.New("未知的供应商种类")
	}
	s.provMu.Lock()
	s.provCache[p.ID] = &cachedProvider{version: p.UpdatedAt, p: prov}
	s.provMu.Unlock()
	return prov, nil
}

// AllModels 全部启用的供应商的模型（拉取失败的供应商跳过并返回错误说明）。
func (s *Service) AllModels(ctx context.Context) ([]ModelInfo, []string) {
	if s.TestProvider != nil {
		ms, _ := s.TestProvider.Models(ctx)
		var out []ModelInfo
		for _, m := range ms {
			out = append(out, ModelInfo{Model: m, Ref: "test/" + m.ID, ProviderID: "test", ProviderName: "测试"})
		}
		return out, nil
	}
	rows, err := s.Providers(ctx)
	if err != nil {
		return nil, []string{err.Error()}
	}
	var out []ModelInfo
	var errs []string
	for i := range rows {
		p := &rows[i]
		if !p.Enabled {
			continue
		}
		prov, err := s.provider(ctx, p)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		ms, err := prov.Models(ctx)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s：%v", p.Name, err))
			continue
		}
		for _, m := range ms {
			if m.Type == "embedding" {
				continue
			}
			out = append(out, ModelInfo{Model: m, Ref: p.ID + "/" + m.ID, ProviderID: p.ID, ProviderName: p.Name})
		}
	}
	return out, errs
}

// aiSettings 实例级 AI 设置。
type aiSettings struct {
	DefaultModel string `json:"default_model"`
}

// wsAISettings 工作空间的 AI 设置。
type wsAISettings struct {
	Models       []string `json:"models"` // 启用的模型（空 = 全部）
	DefaultModel string   `json:"default_model"`
	Instructions string   `json:"instructions"`
}

func (s *Service) instanceAI(ctx context.Context) aiSettings {
	var v aiSettings
	_, _ = s.set.GetJSON(ctx, "ai", &v)
	return v
}

func (s *Service) wsAI(ctx context.Context, wsID string) wsAISettings {
	var v wsAISettings
	_, _ = s.set.GetJSON(ctx, "ws:"+wsID+":ai", &v)
	return v
}

// WorkspaceModels 工作空间可用的模型与默认模型。
func (s *Service) WorkspaceModels(ctx context.Context, wsID string) ([]ModelInfo, string, []string) {
	all, errs := s.AllModels(ctx)
	ws := s.wsAI(ctx, wsID)
	var out []ModelInfo
	if len(ws.Models) == 0 {
		out = all
	} else {
		allowed := map[string]bool{}
		for _, m := range ws.Models {
			allowed[m] = true
		}
		for _, m := range all {
			if allowed[m.Ref] {
				out = append(out, m)
			}
		}
	}
	def := ""
	for _, want := range []string{ws.DefaultModel, s.instanceAI(ctx).DefaultModel} {
		for _, m := range out {
			if want != "" && (m.Ref == want || m.ID == want) {
				def = m.Ref
				break
			}
		}
		if def != "" {
			break
		}
	}
	if def == "" {
		best, bestQ := "", -1
		for _, m := range out {
			q := m.Quota
			if m.Featured {
				q += 1 << 20
			}
			if q > bestQ {
				best, bestQ = m.Ref, q
			}
		}
		def = best
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ProviderName < out[j].ProviderName })
	return out, def, errs
}

// resolveModel 找到会话要用的供应商与模型。
func (s *Service) resolveModel(ctx context.Context, wsID, ref string) (llm.Provider, llm.Model, string, error) {
	if s.TestProvider != nil {
		ms, _ := s.TestProvider.Models(ctx)
		id := strings.TrimPrefix(ref, "test/")
		for _, m := range ms {
			if id == "" || m.ID == id {
				return s.TestProvider, m, "test/" + m.ID, nil
			}
		}
		return nil, llm.Model{}, "", errors.New("测试模型不存在")
	}
	if ref == "" {
		_, def, _ := s.WorkspaceModels(ctx, wsID)
		if def == "" {
			return nil, llm.Model{}, "", ErrNoProvider
		}
		ref = def
	}
	pid, mid, ok := strings.Cut(ref, "/")
	if !ok {
		return nil, llm.Model{}, "", fmt.Errorf("模型 %s 格式不正确", ref)
	}
	p, err := s.providerRow(ctx, pid)
	if err != nil || !p.Enabled {
		return nil, llm.Model{}, "", fmt.Errorf("模型 %s 的供应商不可用，请换一个模型", ref)
	}
	if ws := s.wsAI(ctx, wsID); len(ws.Models) > 0 {
		found := false
		for _, m := range ws.Models {
			if m == ref {
				found = true
			}
		}
		if !found {
			return nil, llm.Model{}, "", fmt.Errorf("模型 %s 没有在这个工作空间启用", ref)
		}
	}
	prov, err := s.provider(ctx, p)
	if err != nil {
		return nil, llm.Model{}, "", err
	}
	ms, err := prov.Models(ctx)
	if err != nil {
		return nil, llm.Model{}, "", fmt.Errorf("获取模型列表失败：%v", err)
	}
	for _, m := range ms {
		if m.ID == mid {
			return prov, m, ref, nil
		}
	}
	return nil, llm.Model{}, "", fmt.Errorf("模型 %s 当前不可用，请换一个模型", ref)
}

// Configured AI 是否已配置（有启用且有密钥的供应商）。
func (s *Service) Configured(ctx context.Context) bool {
	if s.TestProvider != nil {
		return true
	}
	var n int
	_ = s.st.GetW(ctx, &n, `SELECT COUNT(*) FROM ai_providers WHERE enabled = 1 AND (api_key != '' OR kind = 'custom')`)
	return n > 0
}

// SeedOpenCode 首次启动时按 LEMI_OPENCODE_API_KEY 写入 OpenCode Go 供应商（已有任何供应商时不动）。
func (s *Service) SeedOpenCode(ctx context.Context, key, defaultModel string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	var n int
	if err := s.st.GetW(ctx, &n, `SELECT COUNT(*) FROM ai_providers`); err != nil || n > 0 {
		return err
	}
	p, err := s.SaveProvider(ctx, "", ProviderInput{Kind: ProviderOpenCode, Name: "OpenCode Go", APIKey: key})
	if err != nil {
		return err
	}
	if defaultModel != "" {
		ref := defaultModel
		if !strings.Contains(ref, "/") {
			ref = p.ID + "/" + defaultModel
		}
		if cur := s.instanceAI(ctx); cur.DefaultModel == "" {
			return s.set.SetJSON(ctx, "ai", aiSettings{DefaultModel: ref})
		}
	}
	return nil
}
