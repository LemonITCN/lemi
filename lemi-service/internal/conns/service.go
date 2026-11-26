// Package conns 管理数据库连接：配置（密钥加密）、权限计算、连接池、控制台会话、表格分页与变更集、导入导出。
// 智能体、MCP、备份、健康检查都通过这里拿连接，权限规则只有这一份。
package conns

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
	"github.com/LemonITCN/lemi/lemi-service/internal/crypt"
	"github.com/LemonITCN/lemi/lemi-service/internal/driver"
	"github.com/LemonITCN/lemi/lemi-service/internal/ids"
	"github.com/LemonITCN/lemi/lemi-service/internal/netdial"
	"github.com/LemonITCN/lemi/lemi-service/internal/settings"
	"github.com/LemonITCN/lemi/lemi-service/internal/store"
)

// 用户对连接的权限级别。
const (
	LevelNone   = "none"
	LevelRead   = "read"
	LevelWrite  = "write"
	LevelManage = "manage"
)

var levelRank = map[string]int{LevelNone: 0, LevelRead: 1, LevelWrite: 2, LevelManage: 3}

// LevelAtLeast a 是否不低于 b。
func LevelAtLeast(a, b string) bool { return levelRank[a] >= levelRank[b] }

// AI 档位（§16.4）。
const (
	AIHidden       = "hidden"
	AIRead         = "read"
	AIWriteConfirm = "write_confirm"
	AIWriteAuto    = "write_auto"
	AIFull         = "full"
)

var aiRank = map[string]int{AIHidden: 0, AIRead: 1, AIWriteConfirm: 2, AIWriteAuto: 3, AIFull: 4}

// ValidAILevel 档位是否合法。
func ValidAILevel(l string) bool { _, ok := aiRank[l]; return ok }

// MinAI 取两个档位中更严的一个。
func MinAI(a, b string) string {
	if aiRank[a] <= aiRank[b] {
		return a
	}
	return b
}

// AIRank 档位序号。
func AIRank(l string) int { return aiRank[l] }

// Connection 是保存的连接。
type Connection struct {
	ID          string `db:"id" json:"id"`
	WorkspaceID string `db:"workspace_id" json:"workspace_id"`
	Name        string `db:"name" json:"name"`
	Driver      string `db:"driver" json:"driver"`
	Params      string `db:"params" json:"-"`
	Network     string `db:"network" json:"-"`
	Color       string `db:"color" json:"color"`
	Folder      string `db:"folder" json:"folder"`
	Env         string `db:"env" json:"env"`
	ReadOnly    bool   `db:"read_only" json:"read_only"`
	AILevel     string `db:"ai_level" json:"ai_level"`
	AIAllow     string `db:"ai_allow" json:"-"`
	Notes       string `db:"notes" json:"notes"`
	Sort        int    `db:"sort" json:"sort"`
	CreatedBy   string `db:"created_by" json:"created_by"`
	CreatedAt   int64  `db:"created_at" json:"created_at"`
	UpdatedAt   int64  `db:"updated_at" json:"updated_at"`
}

// AIAllowList AI 库表白名单。
func (c *Connection) AIAllowList() []string {
	var out []string
	_ = json.Unmarshal([]byte(c.AIAllow), &out)
	return out
}

// AllowsTable 白名单是否允许某表（空白名单 = 不限）。模式形如 库.表，可用 * 与 ? 通配；SQLite 的库是 main。
func AllowsTable(patterns []string, db, table string) bool {
	if len(patterns) == 0 {
		return true
	}
	full := strings.ToLower(db + "." + table)
	for _, p := range patterns {
		p = strings.ToLower(strings.TrimSpace(p))
		if !strings.Contains(p, ".") {
			p = p + ".*"
		}
		if ok, _ := path.Match(p, full); ok {
			return true
		}
	}
	return false
}

// AllowsDatabase 白名单里是否有该库的任何表。
func AllowsDatabase(patterns []string, db string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		p = strings.ToLower(strings.TrimSpace(p))
		dbp := strings.SplitN(p, ".", 2)[0]
		if ok, _ := path.Match(dbp, strings.ToLower(db)); ok {
			return true
		}
	}
	return false
}

// Service 连接服务。
type Service struct {
	st       *store.Store
	acc      *accounts.Service
	set      *settings.Service
	box      *crypt.Box
	audit    *audit.Log
	log      *slog.Logger
	sqlite   driver.SQLiteEnv
	mu       sync.Mutex
	pools    map[string]*poolEntry
	sessions *registry
}

type poolEntry struct {
	conn     driver.Conn
	dialer   *netdial.Dialer
	version  int64
	lastUsed time.Time
}

// New 创建连接服务。
func New(st *store.Store, acc *accounts.Service, set *settings.Service, box *crypt.Box, au *audit.Log, log *slog.Logger, sqlite driver.SQLiteEnv) *Service {
	s := &Service{st: st, acc: acc, set: set, box: box, audit: au, log: log, sqlite: sqlite, pools: map[string]*poolEntry{}}
	s.sessions = newRegistry()
	return s
}

// SQLiteEnv SQLite 文件范围。
func (s *Service) SQLiteEnv() driver.SQLiteEnv { return s.sqlite }

// Audit 审计。
func (s *Service) Audit() *audit.Log { return s.audit }

// Settings 系统设置。
func (s *Service) Settings() *settings.Service { return s.set }

// Accounts 账号服务。
func (s *Service) Accounts() *accounts.Service { return s.acc }

// ---------- 读写配置 ----------

// Get 取连接。
func (s *Service) Get(ctx context.Context, id string) (*Connection, error) {
	var c Connection
	if err := s.st.Get(ctx, &c, `SELECT * FROM connections WHERE id = ?`, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, apperr.NotFound("连接")
		}
		return nil, err
	}
	return &c, nil
}

// List 空间的全部连接。
func (s *Service) List(ctx context.Context, wsID string) ([]Connection, error) {
	var out []Connection
	err := s.st.Select(ctx, &out, `SELECT * FROM connections WHERE workspace_id = ? ORDER BY folder, sort, name`, wsID)
	return out, err
}

// ListAll 全部连接（健康检查等后台任务用）。
func (s *Service) ListAll(ctx context.Context) ([]Connection, error) {
	var out []Connection
	err := s.st.Select(ctx, &out, `SELECT * FROM connections ORDER BY workspace_id, name`)
	return out, err
}

func aadBase(id, field string) string { return "connections\x00" + id + "\x00" + field }

// secretKeysNetwork 网络配置里需要加密的字段。
var secretKeysNetwork = []string{"ssh.password", "ssh.private_key", "ssh.passphrase", "proxy.password"}

// DecryptParams 解密驱动参数。
func (s *Service) DecryptParams(c *Connection) (map[string]any, error) {
	d, ok := driver.Get(c.Driver)
	if !ok {
		return nil, fmt.Errorf("不支持的数据库类型 %q", c.Driver)
	}
	m := map[string]any{}
	if err := json.Unmarshal([]byte(c.Params), &m); err != nil {
		return nil, err
	}
	if err := s.box.OpenFields(m, d.Info().SecretKeys(), aadBase(c.ID, "params")); err != nil {
		return nil, err
	}
	return m, nil
}

// DecryptNetwork 解密网络配置。
func (s *Service) DecryptNetwork(c *Connection) (netdial.Config, error) {
	var raw map[string]map[string]any
	_ = json.Unmarshal([]byte(c.Network), &raw)
	for _, k := range secretKeysNetwork {
		sec, field, _ := strings.Cut(k, ".")
		if m := raw[sec]; m != nil {
			if err := s.box.OpenFields(m, []string{field}, aadBase(c.ID, "network")); err != nil {
				return netdial.Config{}, err
			}
		}
	}
	b, _ := json.Marshal(raw)
	var nc netdial.Config
	_ = json.Unmarshal(b, &nc)
	return nc, nil
}

// Input 是创建或修改连接的请求。
type Input struct {
	Name     string                    `json:"name"`
	Driver   string                    `json:"driver"`
	Params   map[string]any            `json:"params"`
	Network  map[string]map[string]any `json:"network"`
	Color    string                    `json:"color"`
	Folder   string                    `json:"folder"`
	Env      string                    `json:"env"`
	ReadOnly bool                      `json:"read_only"`
	AILevel  string                    `json:"ai_level"`
	AIAllow  []string                  `json:"ai_allow"`
	Notes    string                    `json:"notes"`
}

// Normalize 校验并补全输入；old 不为空时，没有传的密钥字段保持原值，主机或端口变了必须重新输入密钥。
func (s *Service) prepare(ctx context.Context, id string, in *Input, old *Connection) (params, network string, err error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len([]rune(in.Name)) > 100 {
		return "", "", apperr.Bad("连接名称不能为空，且不超过 100 个字符")
	}
	d, ok := driver.Get(in.Driver)
	if !ok {
		return "", "", apperr.Bad("不支持的数据库类型")
	}
	info := d.Info()
	switch in.Env {
	case "", "prod", "test", "dev":
	default:
		return "", "", apperr.Bad("环境标签不正确")
	}
	if in.AILevel == "" {
		in.AILevel = AIRead
	}
	if !ValidAILevel(in.AILevel) {
		return "", "", apperr.Bad("AI 档位不正确")
	}
	if in.AILevel == AIFull && !s.set.System(ctx).AllowAIFullAccess {
		return "", "", apperr.Forbidden("实例管理员没有允许「完全放开」档位")
	}
	// 只保留表单里声明过的参数，防止塞进驱动不认识的参数
	allowed := map[string]driver.FormField{}
	for _, f := range info.Form {
		allowed[f.Key] = f
	}
	p := map[string]any{}
	for k, v := range in.Params {
		if _, ok := allowed[k]; ok {
			p[k] = v
		}
	}
	var oldParams map[string]any
	if old != nil {
		_ = json.Unmarshal([]byte(old.Params), &oldParams)
	}
	addrChanged := false
	if old != nil && old.Driver == in.Driver {
		if nd, ok := d.(driver.NetworkDriver); ok {
			oh, op := nd.HostPort(oldParams)
			nh, np := nd.HostPort(p)
			addrChanged = !strings.EqualFold(oh, nh) || op != np
		}
		if info.FileBased && fmt.Sprint(oldParams["file"]) != fmt.Sprint(p["file"]) {
			addrChanged = true
		}
	}
	for _, f := range info.Form {
		if !f.Secret {
			continue
		}
		v, has := p[f.Key].(string)
		if (!has || v == "") && old != nil && old.Driver == in.Driver && !addrChanged {
			if ov, ok := oldParams[f.Key]; ok {
				p[f.Key] = ov // 保留原密文
			}
			continue
		}
		if has && v != "" && crypt.IsSealed(v) {
			delete(p, f.Key) // 不接受客户端传来的密文
		}
	}
	for _, f := range info.Form {
		if f.Required {
			if v, ok := p[f.Key]; !ok || fmt.Sprint(v) == "" {
				return "", "", apperr.Bad("请填写「%s」", f.Label)
			}
		}
	}
	s.box.SealFields(p, info.SecretKeys(), aadBase(id, "params"))
	pb, _ := json.Marshal(p)
	// 网络
	net := map[string]map[string]any{}
	var oldNet map[string]map[string]any
	if old != nil {
		_ = json.Unmarshal([]byte(old.Network), &oldNet)
	}
	if _, isNet := d.(driver.NetworkDriver); isNet {
		for _, sec := range []string{"ssh", "proxy"} {
			m := map[string]any{}
			for k, v := range in.Network[sec] {
				m[k] = v
			}
			net[sec] = m
		}
		sshOld, sshNew := oldNet["ssh"], net["ssh"]
		sshAddrChanged := sshOld == nil || fmt.Sprint(sshOld["host"]) != fmt.Sprint(sshNew["host"]) || fmt.Sprint(sshOld["port"]) != fmt.Sprint(sshNew["port"])
		proxyOld, proxyNew := oldNet["proxy"], net["proxy"]
		proxyAddrChanged := proxyOld == nil || fmt.Sprint(proxyOld["host"]) != fmt.Sprint(proxyNew["host"]) || fmt.Sprint(proxyOld["port"]) != fmt.Sprint(proxyNew["port"])
		for _, k := range secretKeysNetwork {
			sec, field, _ := strings.Cut(k, ".")
			m := net[sec]
			v, has := m[field].(string)
			changed := sshAddrChanged
			if sec == "proxy" {
				changed = proxyAddrChanged
			}
			if (!has || v == "") && !changed && oldNet[sec] != nil {
				if ov, ok := oldNet[sec][field]; ok {
					m[field] = ov
				}
				continue
			}
			if has && crypt.IsSealed(v) {
				delete(m, field)
			}
		}
		// SSH 主机变了：已确认的指纹作废
		if sshAddrChanged && old != nil {
			if hk, ok := sshNew["host_key"].(string); ok && sshOld != nil && hk == fmt.Sprint(sshOld["host_key"]) {
				delete(sshNew, "host_key")
			}
		}
		for _, k := range secretKeysNetwork {
			sec, field, _ := strings.Cut(k, ".")
			s.box.SealFields(net[sec], []string{field}, aadBase(id, "network"))
		}
	}
	nb, _ := json.Marshal(net)
	return string(pb), string(nb), nil
}

// Create 新建连接。
func (s *Service) Create(ctx context.Context, wsID, userID string, in *Input) (*Connection, error) {
	id := ids.New()
	params, network, err := s.prepare(ctx, id, in, nil)
	if err != nil {
		return nil, err
	}
	allow, _ := json.Marshal(cleanAllow(in.AIAllow))
	now := store.Now()
	c := &Connection{ID: id, WorkspaceID: wsID, Name: in.Name, Driver: in.Driver, Params: params, Network: network, Color: in.Color, Folder: strings.TrimSpace(in.Folder),
		Env: in.Env, ReadOnly: in.ReadOnly, AILevel: in.AILevel, AIAllow: string(allow), Notes: in.Notes, CreatedBy: userID, CreatedAt: now, UpdatedAt: now}
	if err := s.st.Add(ctx, "connections", c); err != nil {
		return nil, err
	}
	return c, nil
}

func cleanAllow(list []string) []string {
	out := []string{}
	for _, p := range list {
		if p = strings.TrimSpace(p); p != "" && len(p) <= 200 {
			out = append(out, p)
		}
	}
	return out
}

// Update 修改连接（关闭旧连接池）。
func (s *Service) Update(ctx context.Context, c *Connection, in *Input) error {
	params, network, err := s.prepare(ctx, c.ID, in, c)
	if err != nil {
		return err
	}
	allow, _ := json.Marshal(cleanAllow(in.AIAllow))
	c.Name, c.Driver, c.Params, c.Network = in.Name, in.Driver, params, network
	c.Color, c.Folder, c.Env, c.ReadOnly, c.AILevel, c.AIAllow, c.Notes = in.Color, strings.TrimSpace(in.Folder), in.Env, in.ReadOnly, in.AILevel, string(allow), in.Notes
	c.UpdatedAt = store.Now()
	if err := s.st.Put(ctx, "connections", c); err != nil {
		return err
	}
	s.closePool(c.ID)
	return nil
}

// SetHostKey 用户确认 SSH 主机指纹后保存。
func (s *Service) SetHostKey(ctx context.Context, c *Connection, fp string) error {
	var raw map[string]map[string]any
	_ = json.Unmarshal([]byte(c.Network), &raw)
	if raw == nil || raw["ssh"] == nil {
		return apperr.Bad("该连接没有启用 SSH 隧道")
	}
	raw["ssh"]["host_key"] = fp
	b, _ := json.Marshal(raw)
	c.Network = string(b)
	c.UpdatedAt = store.Now()
	if err := s.st.Put(ctx, "connections", c); err != nil {
		return err
	}
	s.closePool(c.ID)
	return nil
}

// Delete 删除连接。
func (s *Service) Delete(ctx context.Context, c *Connection) error {
	s.sessions.closeConn(c.ID)
	s.closePool(c.ID)
	_, err := s.st.Exec(ctx, `DELETE FROM connections WHERE id = ?`, c.ID)
	return err
}

// View 给前端的连接描述（密钥只给「是否已设置」）。
func (s *Service) View(c *Connection, level string) map[string]any {
	d, _ := driver.Get(c.Driver)
	var params map[string]any
	_ = json.Unmarshal([]byte(c.Params), &params)
	secretSet := []string{}
	if d != nil {
		for _, k := range d.Info().SecretKeys() {
			if v, ok := params[k].(string); ok && v != "" {
				secretSet = append(secretSet, k)
			}
			delete(params, k)
		}
	}
	var net map[string]map[string]any
	_ = json.Unmarshal([]byte(c.Network), &net)
	for _, k := range secretKeysNetwork {
		sec, field, _ := strings.Cut(k, ".")
		if m := net[sec]; m != nil {
			if v, ok := m[field].(string); ok && v != "" {
				secretSet = append(secretSet, k)
			}
			delete(m, field)
		}
	}
	out := map[string]any{
		"id": c.ID, "workspace_id": c.WorkspaceID, "name": c.Name, "driver": c.Driver, "color": c.Color, "folder": c.Folder, "env": c.Env,
		"read_only": c.ReadOnly, "ai_level": c.AILevel, "ai_allow": c.AIAllowList(), "notes": c.Notes, "created_at": c.CreatedAt, "updated_at": c.UpdatedAt,
		"access": level,
	}
	if LevelAtLeast(level, LevelManage) {
		out["params"] = params
		out["network"] = net
		out["secrets_set"] = secretSet
	} else if d != nil {
		// 非管理者只看到地址类信息
		summary := map[string]any{}
		for _, k := range []string{"host", "port", "database", "file"} {
			if v, ok := params[k]; ok {
				summary[k] = v
			}
		}
		out["params"] = summary
	}
	return out
}

// ---------- 权限 ----------

// Access 用户对连接的有效权限：空间角色（查看者=读、成员=写、管理员/所有者=管理）与连接上的个人收紧取最严；
// 连接设为只读时最多为读（管理配置的权限不受影响，返回 manage 时调用方另看 ReadOnly）。
func (s *Service) Access(ctx context.Context, u *accounts.User, c *Connection) string {
	role := s.acc.EffectiveRole(ctx, c.WorkspaceID, u)
	lvl := LevelNone
	switch role {
	case accounts.WSViewer:
		lvl = LevelRead
	case accounts.WSMember:
		lvl = LevelWrite
	case accounts.WSAdmin, accounts.WSOwner:
		lvl = LevelManage
	}
	var limit string
	if err := s.st.GetW(ctx, &limit, `SELECT level FROM connection_limits WHERE connection_id = ? AND user_id = ?`, c.ID, u.ID); err == nil {
		if levelRank[limit] < levelRank[lvl] && lvl != LevelManage {
			lvl = limit
		}
	}
	return lvl
}

// CanWrite 是否可以写数据（考虑连接级只读）。
func CanWrite(c *Connection, level string) bool { return !c.ReadOnly && LevelAtLeast(level, LevelWrite) }

// Require 取连接并要求至少 min 权限，返回连接与权限。
func (s *Service) Require(ctx context.Context, u *accounts.User, id, min string) (*Connection, string, error) {
	c, err := s.Get(ctx, id)
	if err != nil {
		return nil, "", err
	}
	lvl := s.Access(ctx, u, c)
	if lvl == LevelNone {
		return nil, "", apperr.NotFound("连接")
	}
	if !LevelAtLeast(lvl, min) {
		return nil, lvl, apperr.Forbidden("你对这个连接没有足够的权限")
	}
	if min == LevelWrite && c.ReadOnly {
		return nil, lvl, apperr.Forbidden("该连接设为只读，不能修改数据或结构")
	}
	return c, lvl, nil
}

// SetLimit 设置成员在连接上的收紧（none / read / write，空表示取消）。
func (s *Service) SetLimit(ctx context.Context, connID, userID, level string) error {
	if level == "" {
		_, err := s.st.Exec(ctx, `DELETE FROM connection_limits WHERE connection_id = ? AND user_id = ?`, connID, userID)
		return err
	}
	if level != LevelNone && level != LevelRead && level != LevelWrite {
		return apperr.Bad("权限级别不正确")
	}
	_, err := s.st.Exec(ctx, `INSERT INTO connection_limits (connection_id, user_id, level) VALUES (?,?,?)
		ON CONFLICT(connection_id, user_id) DO UPDATE SET level = excluded.level`, connID, userID, level)
	return err
}

// Limits 连接上的成员收紧。
func (s *Service) Limits(ctx context.Context, connID string) (map[string]string, error) {
	type row struct {
		UserID string `db:"user_id"`
		Level  string `db:"level"`
	}
	var rows []row
	if err := s.st.Select(ctx, &rows, `SELECT user_id, level FROM connection_limits WHERE connection_id = ?`, connID); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, r := range rows {
		out[r.UserID] = r.Level
	}
	return out, nil
}

// ---------- 连接池 ----------

// Open 取连接的数据源（按需打开，配置变化后自动重开）。
func (s *Service) Open(ctx context.Context, c *Connection) (driver.Conn, error) {
	s.mu.Lock()
	e := s.pools[c.ID]
	if e != nil && e.version == c.UpdatedAt {
		e.lastUsed = time.Now()
		s.mu.Unlock()
		return e.conn, nil
	}
	s.mu.Unlock()
	if e != nil {
		s.closePool(c.ID)
	}
	conn, dialer, err := s.openFresh(ctx, c)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur := s.pools[c.ID]; cur != nil && cur.version == c.UpdatedAt {
		// 并发打开：用先打开的那个
		conn.Close()
		if dialer != nil {
			dialer.Close()
		}
		cur.lastUsed = time.Now()
		return cur.conn, nil
	}
	s.pools[c.ID] = &poolEntry{conn: conn, dialer: dialer, version: c.UpdatedAt, lastUsed: time.Now()}
	return conn, nil
}

// openFresh 按配置打开一个新的数据源（测试连接、健康检查也用它）。
func (s *Service) openFresh(ctx context.Context, c *Connection) (driver.Conn, *netdial.Dialer, error) {
	d, ok := driver.Get(c.Driver)
	if !ok {
		return nil, nil, fmt.Errorf("不支持的数据库类型 %q", c.Driver)
	}
	params, err := s.DecryptParams(c)
	if err != nil {
		return nil, nil, err
	}
	cfg := &driver.ConnConfig{ID: c.ID, Params: params, ReadOnly: c.ReadOnly, SQLite: s.sqlite}
	var dialer *netdial.Dialer
	if _, isNet := d.(driver.NetworkDriver); isNet {
		nc, err := s.DecryptNetwork(c)
		if err != nil {
			return nil, nil, err
		}
		sys := s.set.System(ctx)
		nc.Policy = netdial.ParsePolicy(sys.ConnAllow, sys.ConnDeny)
		dialer = netdial.New(nc)
		cfg.Dialer = dialer
	}
	octx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, err := d.Open(octx, cfg)
	if err != nil {
		if dialer != nil {
			dialer.Close()
		}
		return nil, nil, wrapOpenErr(err)
	}
	return conn, dialer, nil
}

// OpenTemp 打开一个不进连接池的数据源，用完由调用方关闭（测试连接、健康检查）。
func (s *Service) OpenTemp(ctx context.Context, c *Connection) (driver.Conn, func(), error) {
	conn, dialer, err := s.openFresh(ctx, c)
	if err != nil {
		return nil, nil, err
	}
	return conn, func() {
		conn.Close()
		if dialer != nil {
			dialer.Close()
		}
	}, nil
}

func wrapOpenErr(err error) error {
	var unk *netdial.ErrHostKeyUnknown
	if errors.As(err, &unk) {
		return &apperr.Error{Status: 409, Code: "ssh_host_key_unknown", Message: unk.Error(), Extra: map[string]any{"fingerprint": unk.Fingerprint, "key_type": unk.KeyType}}
	}
	var mis *netdial.ErrHostKeyMismatch
	if errors.As(err, &mis) {
		return &apperr.Error{Status: 409, Code: "ssh_host_key_mismatch", Message: mis.Error(), Extra: map[string]any{"fingerprint": mis.Got}}
	}
	if errors.Is(err, netdial.ErrBlocked) {
		return apperr.Forbidden(err.Error())
	}
	return apperr.Wrap("connect_failed", fmt.Errorf("连接失败：%w", err))
}

func (s *Service) closePool(id string) {
	s.mu.Lock()
	e := s.pools[id]
	delete(s.pools, id)
	s.mu.Unlock()
	if e != nil {
		go func() {
			e.conn.Close()
			if e.dialer != nil {
				e.dialer.Close()
			}
		}()
	}
}

// Reap 关闭空闲的连接池与会话（后台定时调用）。
func (s *Service) Reap() {
	s.sessions.reap(30*time.Minute, s.audit)
	s.mu.Lock()
	var idle []string
	for id, e := range s.pools {
		if time.Since(e.lastUsed) > 10*time.Minute && !s.sessions.usesConn(id) {
			idle = append(idle, id)
		}
	}
	s.mu.Unlock()
	for _, id := range idle {
		s.closePool(id)
	}
}

// Run 后台清理。
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Reap()
		}
	}
}

// Close 关闭全部。
func (s *Service) Close() error {
	s.sessions.closeAll()
	s.mu.Lock()
	ids := make([]string, 0, len(s.pools))
	for id := range s.pools {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		s.closePool(id)
	}
	return nil
}

// SQL 取关系型数据源。
func (s *Service) SQL(ctx context.Context, c *Connection) (driver.SQLConn, error) {
	conn, err := s.Open(ctx, c)
	if err != nil {
		return nil, err
	}
	sc, ok := conn.(driver.SQLConn)
	if !ok {
		return nil, apperr.Bad("该数据库类型不支持 SQL")
	}
	return sc, nil
}

// Folders 空间里用到的文件夹名（排序）。
func Folders(list []Connection) []string {
	m := map[string]bool{}
	for _, c := range list {
		if c.Folder != "" {
			m[c.Folder] = true
		}
	}
	out := make([]string, 0, len(m))
	for f := range m {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}
