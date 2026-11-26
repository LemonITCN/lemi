package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"strconv"
	"strings"

	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
	"github.com/LemonITCN/lemi/lemi-service/internal/crypt"
	"github.com/LemonITCN/lemi/lemi-service/internal/ids"
	"github.com/LemonITCN/lemi/lemi-service/internal/netsafe"
	"github.com/LemonITCN/lemi/lemi-service/internal/store"
	"github.com/jmoiron/sqlx"
)

const channelsTable = "notify_channels"

// Channel 是保存的通知渠道。Config 是 JSON，密钥字段为密文（AAD = 表名 + 渠道 ID + config + 字段名）。
type Channel struct {
	ID          string `db:"id" json:"id"`
	WorkspaceID string `db:"workspace_id" json:"workspace_id"`
	Name        string `db:"name" json:"name"`
	Kind        string `db:"kind" json:"kind"`
	Config      string `db:"config" json:"-"`
	Enabled     bool   `db:"enabled" json:"enabled"`
	CreatedBy   string `db:"created_by" json:"created_by"`
	CreatedAt   int64  `db:"created_at" json:"created_at"`
	UpdatedAt   int64  `db:"updated_at" json:"updated_at"`
}

func channelAAD(id string) string { return crypt.AAD(channelsTable, id, "config") }

// Input 是创建、修改或测试渠道的请求。
type Input struct {
	Name    string         `json:"name"`
	Kind    string         `json:"kind"`
	Enabled *bool          `json:"enabled"`
	Config  map[string]any `json:"config"`
	// Clear 要清除的可选密钥字段（密钥只写不读，「不传」表示保持原值，所以清除要显式说明）。
	Clear []string `json:"clear"`
}

// Get 取渠道。
func (s *Service) Get(ctx context.Context, id string) (*Channel, error) {
	var c Channel
	if err := s.st.Get(ctx, &c, `SELECT * FROM notify_channels WHERE id = ?`, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, apperr.NotFound("通知渠道")
		}
		return nil, err
	}
	return &c, nil
}

// List 空间的全部渠道。
func (s *Service) List(ctx context.Context, wsID string) ([]Channel, error) {
	out := []Channel{}
	err := s.st.Select(ctx, &out, `SELECT * FROM notify_channels WHERE workspace_id = ? ORDER BY name, id`, wsID)
	return out, err
}

func (s *Service) channelsByIDs(ctx context.Context, wsID string, idList []string) ([]Channel, error) {
	if len(idList) == 0 {
		return nil, nil
	}
	q, args, err := sqlx.In(`SELECT * FROM notify_channels WHERE workspace_id = ? AND id IN (?)`, wsID, idList)
	if err != nil {
		return nil, err
	}
	var out []Channel
	err = s.st.Select(ctx, &out, q, args...)
	return out, err
}

// Create 新建渠道。
func (s *Service) Create(ctx context.Context, wsID, userID string, in *Input) (*Channel, error) {
	id := ids.New()
	cfg, err := s.prepare(ctx, id, in, nil)
	if err != nil {
		return nil, err
	}
	now := store.Now()
	c := &Channel{ID: id, WorkspaceID: wsID, Name: in.Name, Kind: in.Kind, Config: cfg, Enabled: in.Enabled == nil || *in.Enabled,
		CreatedBy: userID, CreatedAt: now, UpdatedAt: now}
	if err := s.st.Add(ctx, channelsTable, c); err != nil {
		return nil, err
	}
	return c, nil
}

// Update 修改渠道。
func (s *Service) Update(ctx context.Context, c *Channel, in *Input) error {
	cfg, err := s.prepare(ctx, c.ID, in, c)
	if err != nil {
		return err
	}
	c.Name, c.Kind, c.Config = in.Name, in.Kind, cfg
	if in.Enabled != nil {
		c.Enabled = *in.Enabled
	}
	c.UpdatedAt = store.Now()
	return s.st.Put(ctx, channelsTable, c)
}

// Delete 删除渠道（发送记录保留）。
func (s *Service) Delete(ctx context.Context, c *Channel) error {
	_, err := s.st.Exec(ctx, `DELETE FROM notify_channels WHERE id = ?`, c.ID)
	return err
}

// View 给前端的渠道描述：密钥只给「是否已设置」（secrets_set），完整配置只给空间管理员。
func (s *Service) View(c *Channel, full bool) map[string]any {
	out := map[string]any{"id": c.ID, "workspace_id": c.WorkspaceID, "name": c.Name, "kind": c.Kind, "enabled": c.Enabled,
		"created_by": c.CreatedBy, "created_at": c.CreatedAt, "updated_at": c.UpdatedAt}
	if !full {
		return out
	}
	k, _ := kindInfo(c.Kind)
	var m map[string]any
	_ = json.Unmarshal([]byte(c.Config), &m)
	cfg := map[string]any{}
	set := []string{}
	for _, f := range k.Fields {
		v, ok := m[f.Key]
		if !ok {
			continue
		}
		if f.Secret {
			if sv, _ := v.(string); sv != "" {
				set = append(set, f.Key)
			}
			continue
		}
		cfg[f.Key] = v
	}
	out["config"] = cfg
	out["secrets_set"] = set
	// 地址按密钥保存，只给主机名便于辨认是哪个机器人 / 服务
	if raw, _ := m["url"].(string); raw != "" {
		if plain, err := s.box.Open(raw, channelAAD(c.ID)+"\x00url"); err == nil {
			if u, err := url.Parse(plain); err == nil {
				out["url_host"] = u.Hostname()
			}
		}
	}
	return out
}

// decryptConfig 解密渠道配置。
func (s *Service) decryptConfig(c *Channel) (map[string]any, error) {
	k, ok := kindInfo(c.Kind)
	if !ok {
		return nil, fmt.Errorf("未知的渠道类型 %q", c.Kind)
	}
	m := map[string]any{}
	if err := json.Unmarshal([]byte(c.Config), &m); err != nil {
		return nil, err
	}
	if err := s.box.OpenFields(m, k.secretKeys(), channelAAD(c.ID)); err != nil {
		return nil, err
	}
	return m, nil
}

// prepare 校验并规范化输入，返回加密后的配置 JSON。
//
// old 不为空时（修改）：没有传的密钥字段保持原值；但「发往哪里」变了（类型变了、邮件的主机或端口变了、
// Webhook 类的地址变了）时，已保存的其他密钥不能沿用，必须重新输入或用 clear 显式清除，
// 防止把旧口令、旧令牌带去别的地址（与连接的规则一致，docs/spec.md §5.3）。
func (s *Service) prepare(ctx context.Context, id string, in *Input, old *Channel) (string, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len([]rune(in.Name)) > 100 {
		return "", apperr.Bad("渠道名称不能为空，且不超过 100 个字符")
	}
	k, ok := kindInfo(in.Kind)
	if !ok {
		return "", apperr.Bad("不支持的渠道类型")
	}
	clear := map[string]bool{}
	for _, c := range in.Clear {
		clear[c] = true
	}
	// 只保留表单里声明过的字段，并按字段类型规范化
	p := map[string]any{}
	for _, f := range k.Fields {
		v, has := in.Config[f.Key]
		if !has || v == nil {
			continue
		}
		nv, err := normalizeField(f, v)
		if err != nil {
			return "", err
		}
		if nv != nil {
			p[f.Key] = nv
		}
	}
	var oldCfg map[string]any // 旧配置（密钥为密文）
	if old != nil && old.Kind == in.Kind {
		_ = json.Unmarshal([]byte(old.Config), &oldCfg)
	}
	// 地址是否变了
	addrChanged := old != nil && old.Kind != in.Kind
	if oldCfg != nil {
		for _, f := range k.Fields {
			if !f.address {
				continue
			}
			if fmt.Sprint(oldCfg[f.Key]) != fmt.Sprint(p[f.Key]) {
				addrChanged = true
			}
		}
		if nu, ok := p["url"].(string); ok && nu != "" {
			ou, _ := oldCfg["url"].(string)
			plain, err := s.box.Open(ou, channelAAD(id)+"\x00url")
			if err != nil || plain != nu {
				addrChanged = true
			}
		}
	}
	for _, f := range k.Fields {
		if !f.Secret {
			continue
		}
		v, has := p[f.Key].(string)
		if has && v != "" {
			if crypt.IsSealed(v) {
				return "", apperr.Bad("「%s」格式不正确", f.Label)
			}
			continue
		}
		delete(p, f.Key)
		if clear[f.Key] || oldCfg == nil {
			continue
		}
		ov, _ := oldCfg[f.Key].(string)
		if ov == "" {
			continue
		}
		if addrChanged {
			return "", apperr.Bad("发送地址改了，请重新输入「%s」（或清除它）", f.Label)
		}
		p[f.Key] = ov // 保留原密文
	}
	if err := validateConfig(ctx, s, k, p, id); err != nil {
		return "", err
	}
	s.box.SealFields(p, k.secretKeys(), channelAAD(id))
	b, _ := json.Marshal(p)
	return string(b), nil
}

// normalizeField 把前端传来的值转成字段类型；空值返回 nil（等于没填）。
func normalizeField(f Field, v any) (any, error) {
	switch f.Type {
	case "bool":
		switch x := v.(type) {
		case bool:
			return x, nil
		case string:
			return x == "true" || x == "1", nil
		}
		return nil, apperr.Bad("「%s」应为开关值", f.Label)
	case "number":
		var n int64
		switch x := v.(type) {
		case json.Number:
			i, err := x.Int64()
			if err != nil {
				return nil, apperr.Bad("「%s」应为整数", f.Label)
			}
			n = i
		case float64:
			n = int64(x)
		case int:
			n = int64(x)
		case string:
			if strings.TrimSpace(x) == "" {
				return nil, nil
			}
			i, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
			if err != nil {
				return nil, apperr.Bad("「%s」应为整数", f.Label)
			}
			n = i
		default:
			return nil, apperr.Bad("「%s」应为整数", f.Label)
		}
		if n == 0 {
			return nil, nil
		}
		return n, nil
	case "list":
		var items []string
		switch x := v.(type) {
		case []any:
			for _, it := range x {
				if sv, ok := it.(string); ok {
					items = append(items, sv)
				}
			}
		case []string:
			items = x
		case string:
			items = strings.FieldsFunc(x, func(r rune) bool { return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '，' || r == '；' })
		default:
			return nil, apperr.Bad("「%s」格式不正确", f.Label)
		}
		out := []any{}
		for _, it := range items {
			if it = strings.TrimSpace(it); it != "" {
				out = append(out, it)
			}
		}
		if len(out) == 0 {
			return nil, nil
		}
		return out, nil
	default:
		sv, ok := v.(string)
		if !ok {
			return nil, apperr.Bad("「%s」应为文本", f.Label)
		}
		if !f.Secret || f.Key == "url" {
			sv = strings.TrimSpace(sv)
		}
		if sv == "" {
			return nil, nil
		}
		if len(sv) > 16<<10 {
			return nil, apperr.Bad("「%s」太长", f.Label)
		}
		if f.Type == "select" {
			valid := false
			for _, o := range f.Options {
				if strings.EqualFold(o.Value, sv) {
					sv, valid = o.Value, true
				}
			}
			if !valid {
				return nil, apperr.Bad("「%s」取值不正确", f.Label)
			}
		}
		return sv, nil
	}
}

// validateConfig 类型相关的校验（密钥只校验新填的明文；保留的密文之前保存时已校验过）。
func validateConfig(ctx context.Context, s *Service, k KindInfo, p map[string]any, id string) error {
	for _, f := range k.Fields {
		if f.Required {
			if v, ok := p[f.Key]; !ok || v == "" {
				return apperr.Bad("请填写「%s」", f.Label)
			}
		}
	}
	str := func(key string) string { v, _ := p[key].(string); return v }
	plainURL := func() string {
		if v := str("url"); v != "" && !crypt.IsSealed(v) {
			return v
		}
		return ""
	}
	if raw := plainURL(); raw != "" {
		if _, err := s.checkURL(raw); err != nil {
			return err
		}
	}
	switch k.ID {
	case KindEmail:
		list, _ := p["recipients"].([]any)
		if len(list) > 50 {
			return apperr.Bad("收件人最多 50 个")
		}
		for _, it := range list {
			if _, err := parseAddress(fmt.Sprint(it)); err != nil {
				return apperr.Bad("收件人地址不正确：%s", truncRunes(fmt.Sprint(it), 80))
			}
		}
		useSystem, has := p["use_system"].(bool)
		if !has {
			useSystem = true
			p["use_system"] = true
		}
		if !useSystem {
			if str("host") == "" {
				return apperr.Bad("请填写「SMTP 主机」")
			}
			if str("from") == "" {
				return apperr.Bad("请填写「发件人」")
			}
			srv := smtpServerFrom(p)
			if err := srv.validate(); err != nil {
				return err
			}
			if err := netsafe.CheckHost(srv.Host, s.Egress.AllowPrivate != nil && s.Egress.AllowPrivate()); err != nil {
				return apperr.Bad("SMTP 主机不可用：%v", err)
			}
		}
	case KindWebhook:
		if str("method") == "" {
			p["method"] = "POST"
		}
		if str("content_type") == "" {
			p["content_type"] = "application/json"
		}
		if h := str("headers"); h != "" && !crypt.IsSealed(h) {
			if _, err := parseHeaders(h); err != nil {
				return err
			}
		}
		if body := str("body"); body != "" && p["method"] == "POST" {
			if err := checkTemplate(body, str("content_type")); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkURL 校验 Webhook 类地址：http/https、不带用户名口令、出站检查（真正的防线在拨号时按实际 IP 检查）。
func (s *Service) checkURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, apperr.Bad("地址格式不正确")
	}
	if err := netsafe.CheckURLWith(u, s.Egress.AllowPrivate != nil && s.Egress.AllowPrivate()); err != nil {
		if errors.Is(err, netsafe.ErrBlocked) {
			return nil, apperr.Bad("地址是内网或保留地址，已拦截（实例管理员可在系统设置里允许访问内网）")
		}
		return nil, apperr.Bad("%v", err)
	}
	return u, nil
}

// FilterChannels 返回 ids 里仍然存在且属于该空间的渠道 ID（保持原顺序；渠道被删除后配置里残留的 ID 会被去掉）。
func (s *Service) FilterChannels(ctx context.Context, wsID string, idList []string) []string {
	idList = dedupe(idList)
	out := []string{}
	if len(idList) == 0 {
		return out
	}
	list, err := s.channelsByIDs(ctx, wsID, idList)
	if err != nil {
		return idList
	}
	ok := map[string]bool{}
	for _, c := range list {
		ok[c.ID] = true
	}
	for _, id := range idList {
		if ok[id] {
			out = append(out, id)
		}
	}
	return out
}

func parseAddress(s string) (*mail.Address, error) {
	if strings.ContainsAny(s, "\r\n") {
		return nil, errors.New("地址不能包含换行")
	}
	a, err := mail.ParseAddress(strings.TrimSpace(s))
	if err != nil {
		return nil, err
	}
	return a, nil
}
