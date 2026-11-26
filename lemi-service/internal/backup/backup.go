// Package backup 是数据库备份：存储目标、备份计划、执行（数据库 → 导出流 → 压缩 → 加密 → 同时写多个目标）、
// 保留策略、还原、还原校验、下载（docs/spec.md §12）。
package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
	"github.com/LemonITCN/lemi/lemi-service/internal/backup/target"
	"github.com/LemonITCN/lemi/lemi-service/internal/conns"
	"github.com/LemonITCN/lemi/lemi-service/internal/crypt"
	"github.com/LemonITCN/lemi/lemi-service/internal/downloads"
	"github.com/LemonITCN/lemi/lemi-service/internal/dumptools"
	"github.com/LemonITCN/lemi/lemi-service/internal/ids"
	"github.com/LemonITCN/lemi/lemi-service/internal/netsafe"
	"github.com/LemonITCN/lemi/lemi-service/internal/notify"
	"github.com/LemonITCN/lemi/lemi-service/internal/realtime"
	"github.com/LemonITCN/lemi/lemi-service/internal/settings"
	"github.com/LemonITCN/lemi/lemi-service/internal/store"
)

// Notifier 是通知服务（notify.Service 实现它）。
type Notifier interface {
	Send(ctx context.Context, wsID string, channelIDs []string, ev notify.Event)
	CheckChannels(ctx context.Context, wsID string, ids []string) error
}

// Deps 是备份服务的依赖。
type Deps struct {
	Store     *store.Store
	Conns     *conns.Service
	Accounts  *accounts.Service
	Settings  *settings.Service
	Box       *crypt.Box
	Audit     *audit.Log
	Hub       *realtime.Hub
	Log       *slog.Logger
	Notify    Notifier
	Tools     *dumptools.Manager
	Downloads *downloads.Store
	// DataDir 数据目录：本地目录目标的默认根 backups/、暂存与临时文件 tmp/backup/
	DataDir string
	// HTTPClient 覆盖 HTTP 类目标的出站客户端（测试用）；为空时按「允许访问内网」设置做地址检查。
	HTTPClient *http.Client
	// Location 计划的默认时区
	Location *time.Location
}

// 全局限制（docs/spec.md §12.1、§12.2）。
var (
	MaxConcurrent = 3
	RunTimeout    = 23 * time.Hour
	RetryDelays   = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute}
	SchedTick     = 30 * time.Second
	// RetentionGrace 新备份多久之内不被保留策略删除。
	RetentionGrace = time.Hour
)

// Service 是备份服务。
type Service struct {
	Deps
	sem     chan struct{}
	mu      sync.Mutex
	busy    map[string]string             // 连接 ID → 正在运行的任务 ID（备份、还原、校验共用）
	cancels map[string]context.CancelFunc // 任务 ID → 取消
	prog    map[string]*progress          // 任务 ID → 进度
	wg      sync.WaitGroup
	stopCtx context.Context
	stop    context.CancelFunc
	now     func() time.Time
}

// New 创建备份服务。
func New(d Deps) *Service {
	if d.Location == nil {
		d.Location = time.Local
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{Deps: d, sem: make(chan struct{}, MaxConcurrent), busy: map[string]string{}, cancels: map[string]context.CancelFunc{},
		prog: map[string]*progress{}, stopCtx: ctx, stop: cancel, now: time.Now}
}

// Close 取消所有运行中的任务并等待收尾。
func (s *Service) Close() error {
	s.stop()
	s.mu.Lock()
	for _, c := range s.cancels {
		c()
	}
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
	}
	return nil
}

func (s *Service) tmpDir(elem ...string) string {
	return filepath.Join(append([]string{s.DataDir, "tmp", "backup"}, elem...)...)
}

func (s *Service) egress() netsafe.Egress {
	return netsafe.Egress{AllowPrivate: func() bool { return s.Settings.System(context.Background()).AllowPrivateEgress }}
}

// ---------------- 存储目标 ----------------

// Target 是保存的存储目标。
type Target struct {
	ID          string `db:"id" json:"id"`
	WorkspaceID string `db:"workspace_id" json:"workspace_id"`
	Name        string `db:"name" json:"name"`
	Kind        string `db:"kind" json:"kind"`
	Config      string `db:"config" json:"-"`
	CreatedBy   string `db:"created_by" json:"created_by"`
	CreatedAt   int64  `db:"created_at" json:"created_at"`
	UpdatedAt   int64  `db:"updated_at" json:"updated_at"`
}

func targetAAD(id string) string { return crypt.AAD("backup_targets", id, "config") }

// GetTarget 取存储目标。
func (s *Service) GetTarget(ctx context.Context, id string) (*Target, error) {
	var t Target
	if err := s.Store.Get(ctx, &t, `SELECT * FROM backup_targets WHERE id = ?`, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, apperr.NotFound("存储目标")
		}
		return nil, err
	}
	return &t, nil
}

// ListTargets 空间的全部存储目标。
func (s *Service) ListTargets(ctx context.Context, wsID string) ([]Target, error) {
	out := []Target{}
	err := s.Store.Select(ctx, &out, `SELECT * FROM backup_targets WHERE workspace_id = ? ORDER BY name`, wsID)
	return out, err
}

// TargetInput 是创建或修改存储目标的请求。密钥字段不传（或传空串）表示保持原值。
type TargetInput struct {
	Name   string         `json:"name"`
	Kind   string         `json:"kind"`
	Config map[string]any `json:"config"`
	// Clear 显式清除的可选密钥字段
	Clear []string `json:"clear"`
}

// addressKeys 是决定「连到哪里」的配置键：它们变了，旧密钥不能带过去，必须重新输入。
var addressKeys = []string{"host", "port", "endpoint", "url", "bucket", "region", "account", "share", "container", "app_key"}

// prepareTarget 校验输入并返回要保存的配置 JSON（密钥已加密）。
func (s *Service) prepareTarget(ctx context.Context, id string, in *TargetInput, old *Target) (string, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len([]rune(in.Name)) > 100 {
		return "", apperr.Bad("名称不能为空，且不超过 100 个字符")
	}
	info, ok := target.Info(in.Kind)
	if !ok {
		return "", apperr.Bad("不支持的存储目标类型")
	}
	if old != nil && old.Kind != in.Kind {
		return "", apperr.Bad("不能修改存储目标的类型")
	}
	allowed := map[string]target.Field{}
	for _, f := range info.Fields {
		allowed[f.Key] = f
	}
	cfg := map[string]any{}
	for k, v := range in.Config {
		if _, ok := allowed[k]; ok {
			cfg[k] = v
		}
	}
	var oldCfg map[string]any
	if old != nil {
		_ = json.Unmarshal([]byte(old.Config), &oldCfg)
		// 百度网盘授权后写进配置的令牌过期时间不在表单里，保留
		if v, ok := oldCfg["expires_at"]; ok {
			cfg["expires_at"] = v
		}
	}
	addrChanged := false
	if old != nil {
		for _, k := range addressKeys {
			if fmt.Sprint(oldCfg[k]) != fmt.Sprint(cfg[k]) && (oldCfg[k] != nil || cfg[k] != nil) {
				addrChanged = true
			}
		}
	}
	clear := map[string]bool{}
	for _, k := range in.Clear {
		clear[k] = true
	}
	for _, f := range info.Fields {
		if !f.Secret {
			continue
		}
		v, has := cfg[f.Key].(string)
		if has && v != "" && crypt.IsSealed(v) {
			delete(cfg, f.Key) // 不接受客户端传来的密文
			has, v = false, ""
		}
		if (!has || v == "") && old != nil && !clear[f.Key] {
			if ov, ok := oldCfg[f.Key].(string); ok && ov != "" {
				if addrChanged {
					return "", apperr.Bad("地址变了，请重新输入「%s」", f.Label)
				}
				cfg[f.Key] = ov
			}
		}
	}
	for _, f := range info.Fields {
		if f.Required {
			if v, ok := cfg[f.Key]; !ok || fmt.Sprint(v) == "" {
				return "", apperr.Bad("请填写「%s」", f.Label)
			}
		}
	}
	// 用解密后的配置让后端校验一遍（只校验、不连网）
	plain := map[string]any{}
	for k, v := range cfg {
		plain[k] = v
	}
	for _, k := range info.SecretKeys() {
		// 只有沿用的旧值是密文，新填的是明文
		if v, ok := plain[k].(string); ok && crypt.IsSealed(v) {
			if err := s.Box.OpenFields(plain, []string{k}, targetAAD(id)); err != nil {
				return "", err
			}
		}
	}
	raw, _ := json.Marshal(plain)
	if _, err := target.Open(in.Kind, raw, s.targetDeps(ctx, nil)); err != nil {
		return "", apperr.Bad("%s", err.Error())
	}
	s.Box.SealFields(cfg, info.SecretKeys(), targetAAD(id))
	b, _ := json.Marshal(cfg)
	return string(b), nil
}

// CreateTarget 新建存储目标。
func (s *Service) CreateTarget(ctx context.Context, wsID, userID string, in *TargetInput) (*Target, error) {
	id := ids.New()
	cfg, err := s.prepareTarget(ctx, id, in, nil)
	if err != nil {
		return nil, err
	}
	now := store.Now()
	t := &Target{ID: id, WorkspaceID: wsID, Name: in.Name, Kind: in.Kind, Config: cfg, CreatedBy: userID, CreatedAt: now, UpdatedAt: now}
	if err := s.Store.Add(ctx, "backup_targets", t); err != nil {
		return nil, err
	}
	return t, nil
}

// UpdateTarget 修改存储目标。
func (s *Service) UpdateTarget(ctx context.Context, t *Target, in *TargetInput) error {
	cfg, err := s.prepareTarget(ctx, t.ID, in, t)
	if err != nil {
		return err
	}
	t.Name, t.Config, t.UpdatedAt = in.Name, cfg, store.Now()
	return s.Store.Put(ctx, "backup_targets", t)
}

// TargetInUse 返回使用该目标的计划名称。
func (s *Service) TargetInUse(ctx context.Context, t *Target) ([]string, error) {
	var plans []Plan
	if err := s.Store.Select(ctx, &plans, `SELECT * FROM backup_plans WHERE workspace_id = ?`, t.WorkspaceID); err != nil {
		return nil, err
	}
	var names []string
	for _, p := range plans {
		for _, id := range p.TargetIDs() {
			if id == t.ID {
				names = append(names, p.Name)
			}
		}
	}
	return names, nil
}

// DeleteTarget 删除存储目标（被计划使用时拒绝；目标上的备份文件不删）。
func (s *Service) DeleteTarget(ctx context.Context, t *Target) error {
	names, err := s.TargetInUse(ctx, t)
	if err != nil {
		return err
	}
	if len(names) > 0 {
		return apperr.Conflict("该存储目标正被备份计划使用：" + strings.Join(names, "、"))
	}
	_, err = s.Store.Exec(ctx, `DELETE FROM backup_targets WHERE id = ?`, t.ID)
	return err
}

// TargetView 给前端的目标描述（密钥只给「是否已设置」）。
func (s *Service) TargetView(t *Target, full bool) map[string]any {
	out := map[string]any{"id": t.ID, "workspace_id": t.WorkspaceID, "name": t.Name, "kind": t.Kind, "created_by": t.CreatedBy, "created_at": t.CreatedAt, "updated_at": t.UpdatedAt}
	if !full {
		return out
	}
	info, _ := target.Info(t.Kind)
	var cfg map[string]any
	_ = json.Unmarshal([]byte(t.Config), &cfg)
	set := []string{}
	for _, k := range info.SecretKeys() {
		if v, ok := cfg[k].(string); ok && v != "" {
			set = append(set, k)
		}
		delete(cfg, k)
	}
	delete(cfg, "expires_at")
	out["config"] = cfg
	out["secrets_set"] = set
	return out
}

// targetDeps 出站依赖：HTTP 与 TCP 都经地址检查（「允许访问内网」实时生效）。
func (s *Service) targetDeps(ctx context.Context, t *Target) target.Deps {
	d := target.Deps{SpoolDir: s.tmpDir("spool"), LocalRoot: filepath.Join(s.DataDir, "backups")}
	if s.HTTPClient != nil {
		d.HTTPClient = s.HTTPClient
	} else {
		d.HTTPClient = &http.Client{Transport: s.egress().Transport(), CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("重定向次数过多")
			}
			return netsafe.CheckURLWith(req.URL, s.Settings.System(context.Background()).AllowPrivateEgress)
		}}
	}
	d.Dial = s.egress().DialContext
	if t != nil {
		id := t.ID
		info, _ := target.Info(t.Kind)
		d.SaveConfig = func(newCfg []byte) error {
			// 百度网盘刷新令牌后回写（重新加密密钥字段）
			var m map[string]any
			if err := json.Unmarshal(newCfg, &m); err != nil {
				return err
			}
			s.Box.SealFields(m, info.SecretKeys(), targetAAD(id))
			b, _ := json.Marshal(m)
			_, err := s.Store.Exec(context.Background(), `UPDATE backup_targets SET config = ?, updated_at = ? WHERE id = ?`, string(b), store.Now(), id)
			return err
		}
	}
	return d
}

// plainConfig 解密目标配置。
func (s *Service) plainConfig(t *Target) ([]byte, error) {
	info, ok := target.Info(t.Kind)
	if !ok {
		return nil, fmt.Errorf("不支持的存储目标类型 %q", t.Kind)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(t.Config), &m); err != nil {
		return nil, err
	}
	if err := s.Box.OpenFields(m, info.SecretKeys(), targetAAD(t.ID)); err != nil {
		return nil, err
	}
	return json.Marshal(m)
}

// OpenTarget 打开存储目标。
func (s *Service) OpenTarget(ctx context.Context, t *Target) (target.Target, error) {
	raw, err := s.plainConfig(t)
	if err != nil {
		return nil, err
	}
	if t.Kind == target.KindLocal {
		// 本地目录的默认根在第一次使用时创建
		_ = os.MkdirAll(filepath.Join(s.DataDir, "backups"), 0o700)
	}
	tg, err := target.Open(t.Kind, raw, s.targetDeps(ctx, t))
	if err != nil {
		return nil, apperr.Bad("存储目标「%s」配置不正确：%s", t.Name, err.Error())
	}
	return tg, nil
}

// targetErr 把目标错误转成面向用户的错误（SFTP 指纹确认给出 409 与指纹）。
func targetErr(name string, err error) error {
	var unk *target.UnknownHostKeyError
	if errors.As(err, &unk) {
		return &apperr.Error{Status: 409, Code: "sftp_host_key_unknown", Message: unk.Error(), Extra: map[string]any{"fingerprint": unk.Fingerprint, "key_type": unk.KeyType}}
	}
	var mis *target.HostKeyMismatchError
	if errors.As(err, &mis) {
		return &apperr.Error{Status: 409, Code: "sftp_host_key_mismatch", Message: mis.Error(), Extra: map[string]any{"fingerprint": mis.Got}}
	}
	if ae, ok := apperr.As(err); ok {
		return ae
	}
	return apperr.Wrap("target_failed", fmt.Errorf("存储目标「%s」：%w", name, err))
}

// SetHostKey 确认 SFTP 主机指纹。
func (s *Service) SetHostKey(ctx context.Context, t *Target, fp string) error {
	if t.Kind != target.KindSFTP {
		return apperr.Bad("只有 SFTP 目标需要确认主机指纹")
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(t.Config), &m)
	m["host_key_sha256"] = strings.TrimSpace(fp)
	b, _ := json.Marshal(m)
	t.Config, t.UpdatedAt = string(b), store.Now()
	return s.Store.Put(ctx, "backup_targets", t)
}

// TestTarget 测试连接：写、查、列、读回、删一个探测文件。
func (s *Service) TestTarget(ctx context.Context, t *Target) error {
	tg, err := s.OpenTarget(ctx, t)
	if err != nil {
		return err
	}
	defer tg.Close()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := target.Probe(ctx, tg); err != nil {
		return targetErr(t.Name, err)
	}
	return nil
}

// TargetUsage 按 Lemi 自己的记录汇总目标占用。
func (s *Service) TargetUsage(ctx context.Context, wsID string) (map[string]map[string]int64, error) {
	type row struct {
		TargetID string `db:"target_id"`
		Files    int64  `db:"files"`
		Bytes    int64  `db:"bytes"`
	}
	var rows []row
	err := s.Store.Select(ctx, &rows, `SELECT f.target_id, COUNT(*) AS files, COALESCE(SUM(f.size),0) AS bytes FROM backup_files f
		JOIN backup_runs r ON r.id = f.run_id WHERE r.workspace_id = ? AND f.status = 'ok' GROUP BY f.target_id`, wsID)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]int64{}
	for _, r := range rows {
		out[r.TargetID] = map[string]int64{"files": r.Files, "bytes": r.Bytes}
	}
	return out, nil
}

// ---------------- 空间设置 ----------------

// WSSettings 是空间级备份设置。
type WSSettings struct {
	VerifyConnectionID string `json:"verify_connection_id"`
}

// GetWSSettings 读空间设置。
func (s *Service) GetWSSettings(ctx context.Context, wsID string) WSSettings {
	var v string
	_ = s.Store.GetW(ctx, &v, `SELECT verify_connection_id FROM backup_settings WHERE workspace_id = ?`, wsID)
	return WSSettings{VerifyConnectionID: v}
}

// SetWSSettings 保存空间设置（校验连接必须是本空间的 MySQL 连接）。
func (s *Service) SetWSSettings(ctx context.Context, wsID string, in WSSettings) error {
	if in.VerifyConnectionID != "" {
		c, err := s.Conns.Get(ctx, in.VerifyConnectionID)
		if err != nil || c.WorkspaceID != wsID {
			return apperr.Bad("校验连接不存在")
		}
		if c.Driver != "mysql" {
			return apperr.Bad("校验连接必须是 MySQL / MariaDB 连接")
		}
		if c.ReadOnly {
			return apperr.Bad("校验连接不能是只读连接（需要建临时库）")
		}
	}
	_, err := s.Store.Exec(ctx, `INSERT INTO backup_settings (workspace_id, verify_connection_id, updated_at) VALUES (?,?,?)
		ON CONFLICT(workspace_id) DO UPDATE SET verify_connection_id = excluded.verify_connection_id, updated_at = excluded.updated_at`, wsID, in.VerifyConnectionID, store.Now())
	return err
}

// ---------------- 实时推送 ----------------

// broadcast 推给空间成员。
func (s *Service) broadcast(wsID, t string, data any) {
	if s.Hub == nil {
		return
	}
	s.Hub.Broadcast(t, data, func(userID string) bool {
		return s.Accounts.RoleIn(context.Background(), wsID, userID) != ""
	})
}

func (s *Service) notifyWS(wsID string, channels []string, ev notify.Event) {
	if s.Notify == nil || len(channels) == 0 {
		return
	}
	s.Notify.Send(context.Background(), wsID, channels, ev)
}

func jsonStrings(raw string) []string {
	var out []string
	_ = json.Unmarshal([]byte(raw), &out)
	if out == nil {
		out = []string{}
	}
	return out
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func jsonIndent(v any) ([]byte, error) { return json.MarshalIndent(v, "", "  ") }
