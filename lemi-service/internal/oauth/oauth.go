// Package oauth 是给 MCP 客户端（Claude Code、Cursor、Codex 等）用的 OAuth 2.1 授权服务器与个人访问令牌：
// 动态注册（RFC 7591）、授权码 + PKCE（只支持 S256）、刷新令牌轮换与重用检测、撤销（RFC 7009），
// 受保护资源元数据（RFC 9728）与授权服务器元数据（RFC 8414）。
//
// 每次授权（OAuth 应用授权或个人访问令牌）都选定一个工作空间与每个连接的 AI 档位（只能是不可见 / 只读 /
// 读写·增删改自动 / 完全放开，且不高于连接上的档位），存为 mcp_grants 的一行；令牌都挂在授权上，吊销授权即全部失效。
// 只支持公开客户端（没有客户端密钥）。授权码 10 分钟、一次性；访问令牌 1 小时；刷新令牌 60 天（不超过授权有效期），
// 每用一次换新，旧的再被拿来用视为泄露，整条链作废。
package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/conns"
	"github.com/LemonITCN/lemi/lemi-service/internal/settings"
	"github.com/LemonITCN/lemi/lemi-service/internal/store"
	"github.com/LemonITCN/lemi/lemi-service/internal/tools"
	"github.com/LemonITCN/lemi/lemi-service/internal/web"
)

// 有效期与令牌前缀。
const (
	CodeTTL    = 10 * time.Minute
	AccessTTL  = time.Hour
	RefreshTTL = 60 * 24 * time.Hour

	AccessPrefix  = "lemi_oat_"
	RefreshPrefix = "lemi_ort_"
	PATPrefix     = "lemi_pat_"

	ScopeMCP = "mcp"
)

// MCP 授权可选的档位（没有「逐条确认」：MCP 客户端没有 Lemi 的审批界面）。
var mcpLevels = tools.GrantLevels

// Service OAuth 与访问令牌服务。
type Service struct {
	st    *store.Store
	acc   *accounts.Service
	conns *conns.Service
	set   *settings.Service
	log   *slog.Logger
	// Base 返回对外访问地址（不带结尾斜杠）
	Base func(r *http.Request) string

	lastUsedMu sync.Mutex
	lastUsed   map[string]time.Time
}

// New 创建服务。
func New(st *store.Store, acc *accounts.Service, cs *conns.Service, set *settings.Service, log *slog.Logger, base func(r *http.Request) string) *Service {
	return &Service{st: st, acc: acc, conns: cs, set: set, log: log, Base: base, lastUsed: map[string]time.Time{}}
}

// Grant 是一次授权。
type Grant struct {
	ID          string `db:"id" json:"id"`
	UserID      string `db:"user_id" json:"-"`
	Kind        string `db:"kind" json:"kind"` // oauth | pat
	ClientID    string `db:"client_id" json:"client_id,omitempty"`
	Name        string `db:"name" json:"name"`
	WorkspaceID string `db:"workspace_id" json:"workspace_id"`
	LevelsJSON  string `db:"levels" json:"-"`
	TokenHash   string `db:"token_hash" json:"-"`
	TokenHint   string `db:"token_hint" json:"token_hint,omitempty"`
	ExpiresAt   int64  `db:"expires_at" json:"expires_at"`
	CreatedAt   int64  `db:"created_at" json:"created_at"`
	LastUsedAt  int64  `db:"last_used_at" json:"last_used_at"`
	RevokedAt   int64  `db:"revoked_at" json:"revoked_at,omitempty"`
}

// Levels 解析连接档位。
func (g *Grant) Levels() tools.Grant {
	m := tools.Grant{}
	_ = json.Unmarshal([]byte(g.LevelsJSON), &m)
	return m
}

// Identity 是 MCP 请求的身份。
type Identity struct {
	User  *accounts.User
	Grant *Grant
}

func randToken(prefix string) string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func toJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

// ErrInvalidToken 令牌无效、过期或已吊销。
var ErrInvalidToken = errors.New("令牌无效、已过期或已吊销")

// Lookup 校验 Bearer 令牌（OAuth 访问令牌或个人访问令牌），返回身份。
func (s *Service) Lookup(ctx context.Context, raw string) (*Identity, error) {
	if len(raw) < 20 || len(raw) > 120 {
		return nil, ErrInvalidToken
	}
	now := store.Now()
	var g Grant
	switch {
	case strings.HasPrefix(raw, PATPrefix):
		if err := s.st.GetW(ctx, &g, `SELECT * FROM mcp_grants WHERE token_hash = ? AND kind = 'pat'`, hash(raw)); err != nil {
			return nil, ErrInvalidToken
		}
	case strings.HasPrefix(raw, AccessPrefix):
		var t token
		if err := s.st.GetW(ctx, &t, `SELECT * FROM oauth_tokens WHERE id = ? AND kind = 'access'`, hash(raw)); err != nil {
			return nil, ErrInvalidToken
		}
		if t.RevokedAt != 0 || t.ExpiresAt < now {
			return nil, ErrInvalidToken
		}
		if err := s.st.GetW(ctx, &g, `SELECT * FROM mcp_grants WHERE id = ?`, t.GrantID); err != nil {
			return nil, ErrInvalidToken
		}
	default:
		return nil, ErrInvalidToken
	}
	if g.RevokedAt != 0 || (g.ExpiresAt > 0 && g.ExpiresAt < now) {
		return nil, ErrInvalidToken
	}
	u, err := s.acc.UserByID(ctx, g.UserID)
	if err != nil || u.Status != "active" {
		return nil, ErrInvalidToken
	}
	s.touch(g.ID)
	return &Identity{User: u, Grant: &g}, nil
}

// touch 记录最近使用时间（每个授权每分钟最多写一次）。
func (s *Service) touch(id string) {
	s.lastUsedMu.Lock()
	last := s.lastUsed[id]
	if time.Since(last) < time.Minute {
		s.lastUsedMu.Unlock()
		return
	}
	s.lastUsed[id] = time.Now()
	s.lastUsedMu.Unlock()
	go func() { _, _ = s.st.Exec(context.Background(), `UPDATE mcp_grants SET last_used_at = ? WHERE id = ?`, store.Now(), id) }()
}

// MaxLevel 一个连接在 MCP 授权里最高能选的档位：不高于用户对它的实际 AI 档位，且只能是 MCP 可用的档位。
func MaxLevel(eff string) string { return tools.MaxGrantLevel(eff) }

func validMCPLevel(l string) bool { return tools.ValidGrantLevel(l) }

// ConnChoice 是授权页上一个连接的可选范围。
type ConnChoice struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Driver   string `json:"driver"`
	Env      string `json:"env"`
	AILevel  string `json:"ai_level"`  // 连接上的 AI 档位
	MaxLevel string `json:"max_level"` // MCP 授权最高可选
}

// Choices 工作空间里每个连接在 MCP 授权里可选的最高档位。
func (s *Service) Choices(ctx context.Context, u *accounts.User, wsID string) ([]ConnChoice, error) {
	list, err := s.conns.List(ctx, wsID)
	if err != nil {
		return nil, err
	}
	allowFull := s.set.System(ctx).AllowAIFullAccess
	out := []ConnChoice{}
	for i := range list {
		c := &list[i]
		ul := s.conns.Access(ctx, u, c)
		eff := tools.EffectiveAI(c, ul, nil, false, allowFull)
		if eff == conns.AIHidden {
			continue
		}
		out = append(out, ConnChoice{ID: c.ID, Name: c.Name, Driver: c.Driver, Env: c.Env, AILevel: c.AILevel, MaxLevel: MaxLevel(eff)})
	}
	return out, nil
}

// CheckLevels 校验授权里的连接档位：只能是 MCP 可用的档位，且不高于该连接的上限；没列出的连接为不可见。
func (s *Service) CheckLevels(ctx context.Context, u *accounts.User, wsID string, levels map[string]string) (tools.Grant, error) {
	if _, err := s.acc.Require(ctx, wsID, u, accounts.WSViewer); err != nil {
		return nil, errors.New("你不是这个工作空间的成员")
	}
	choices, err := s.Choices(ctx, u, wsID)
	if err != nil {
		return nil, err
	}
	max := map[string]string{}
	for _, c := range choices {
		max[c.ID] = c.MaxLevel
	}
	out := tools.Grant{}
	for id, l := range levels {
		if l == "" || l == conns.AIHidden {
			continue
		}
		if !validMCPLevel(l) {
			return nil, errors.New("MCP 授权的档位只能是 hidden、read、write_auto 或 full")
		}
		m, ok := max[id]
		if !ok {
			return nil, errors.New("连接 " + id + " 不存在或对智能体不可见")
		}
		if conns.AIRank(l) > conns.AIRank(m) {
			return nil, errors.New("连接 " + id + " 的授权档位不能高于 " + m)
		}
		out[id] = l
	}
	if len(out) == 0 {
		return nil, errors.New("请至少给一个连接授予只读或更高的档位")
	}
	return out, nil
}

// createGrant 保存一次授权。
func (s *Service) createGrant(ctx context.Context, g *Grant, levels tools.Grant) error {
	g.LevelsJSON = toJSON(levels)
	g.CreatedAt = store.Now()
	return s.st.Add(ctx, "mcp_grants", g)
}

// RevokeGrant 吊销授权与其全部令牌。
func (s *Service) RevokeGrant(ctx context.Context, userID, id string) error {
	now := store.Now()
	res, err := s.st.Exec(ctx, `UPDATE mcp_grants SET revoked_at = ? WHERE id = ? AND user_id = ? AND revoked_at = 0`, now, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("授权不存在或已吊销")
	}
	_, _ = s.st.Exec(ctx, `UPDATE oauth_tokens SET revoked_at = ? WHERE grant_id = ? AND revoked_at = 0`, now, id)
	return nil
}

// Grants 用户的授权（kind 为空 = 全部）。
func (s *Service) Grants(ctx context.Context, userID, kind string) ([]Grant, error) {
	var out []Grant
	q := `SELECT * FROM mcp_grants WHERE user_id = ? AND revoked_at = 0`
	args := []any{userID}
	if kind != "" {
		q += ` AND kind = ?`
		args = append(args, kind)
	}
	err := s.st.Select(ctx, &out, q+` ORDER BY created_at DESC`, args...)
	return out, err
}

// ---------- 客户端与令牌存储 ----------

type client struct {
	ID           string `db:"id"`
	Name         string `db:"name"`
	RedirectURIs string `db:"redirect_uris"`
	Data         string `db:"data"`
	CreatedAt    int64  `db:"created_at"`
}

func (c *client) uris() []string {
	var out []string
	_ = json.Unmarshal([]byte(c.RedirectURIs), &out)
	return out
}

type code struct {
	ID        string `db:"id"`
	ClientID  string `db:"client_id"`
	UserID    string `db:"user_id"`
	Data      string `db:"data"`
	ExpiresAt int64  `db:"expires_at"`
}

type codeData struct {
	RedirectURI string `json:"redirect_uri"`
	Challenge   string `json:"code_challenge"`
	GrantID     string `json:"grant_id"`
	Resource    string `json:"resource,omitempty"`
}

type token struct {
	ID        string `db:"id"`
	Kind      string `db:"kind"`
	ClientID  string `db:"client_id"`
	UserID    string `db:"user_id"`
	GrantID   string `db:"grant_id"`
	Family    string `db:"family"`
	CodeID    string `db:"code_id"`
	ExpiresAt int64  `db:"expires_at"`
	CreatedAt int64  `db:"created_at"`
	RevokedAt int64  `db:"revoked_at"`
}

func (s *Service) client(ctx context.Context, id string) (*client, error) {
	if id == "" || len(id) > 100 {
		return nil, errors.New("client_id 不存在")
	}
	var c client
	if err := s.st.GetW(ctx, &c, `SELECT * FROM oauth_clients WHERE id = ?`, id); err != nil {
		return nil, errors.New("client_id 不存在")
	}
	return &c, nil
}

var errBadRedirect = errors.New("回调地址不合法")

// validRedirect 允许：https；本机回环地址的 http（任意端口）；自定义协议（桌面应用回调，如 cursor://）。
func validRedirect(raw string) error {
	if len(raw) > 2000 {
		return errBadRedirect
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Fragment != "" {
		return errBadRedirect
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		if u.Host == "" {
			return errBadRedirect
		}
	case "http":
		if !isLoopback(u.Hostname()) {
			return errors.New("http 回调只允许本机地址（localhost / 127.0.0.1 / [::1]）")
		}
	case "javascript", "data", "file", "vbscript", "about", "blob", "ws", "wss", "ftp":
		return errBadRedirect
	}
	return nil
}

func isLoopback(h string) bool { return h == "localhost" || h == "127.0.0.1" || h == "::1" }

// redirectMatches 按 RFC 8252：回环地址忽略端口，其余必须完全一致。
func redirectMatches(registered []string, got string) bool {
	gu, err := url.Parse(got)
	if err != nil {
		return false
	}
	for _, r := range registered {
		if r == got {
			return true
		}
		ru, err := url.Parse(r)
		if err != nil {
			continue
		}
		if strings.EqualFold(ru.Scheme, "http") && strings.EqualFold(gu.Scheme, "http") && isLoopback(ru.Hostname()) &&
			ru.Hostname() == gu.Hostname() && ru.Path == gu.Path && ru.RawQuery == gu.RawQuery {
			return true
		}
	}
	return false
}

// ---------- 限速 ----------

type limiter struct {
	mu sync.Mutex
	m  map[string][]time.Time
}

func (l *limiter) allow(key string, n int, per time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.m == nil || len(l.m) > 20000 {
		l.m = map[string][]time.Time{}
	}
	now := time.Now()
	var kept []time.Time
	for _, t := range l.m[key] {
		if now.Sub(t) < per {
			kept = append(kept, t)
		}
	}
	if len(kept) >= n {
		l.m[key] = kept
		return false
	}
	l.m[key] = append(kept, now)
	return true
}

var ipLimit limiter

// BaseURL 对外地址：配置了 LEMI_PUBLIC_URL 就用它，否则按请求推断。
func BaseURL(publicURL string) func(r *http.Request) string {
	return func(r *http.Request) string {
		if publicURL != "" {
			return strings.TrimRight(publicURL, "/")
		}
		scheme := "http"
		if web.IsHTTPS(r) {
			scheme = "https"
		}
		return scheme + "://" + r.Host
	}
}
