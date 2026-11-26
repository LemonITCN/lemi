package backup

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
	"github.com/LemonITCN/lemi/lemi-service/internal/conns"
	"github.com/LemonITCN/lemi/lemi-service/internal/crypt"
	"github.com/LemonITCN/lemi/lemi-service/internal/ids"
	"github.com/LemonITCN/lemi/lemi-service/internal/sched"
	"github.com/LemonITCN/lemi/lemi-service/internal/store"
)

// 备份方式。
const (
	MethodMysqldump  = "mysqldump"
	MethodLemi       = "lemi"
	MethodSQLiteFile = "sqlite_file"
)

// Plan 是备份计划。
type Plan struct {
	ID               string `db:"id" json:"id"`
	WorkspaceID      string `db:"workspace_id" json:"workspace_id"`
	ConnectionID     string `db:"connection_id" json:"connection_id"`
	Name             string `db:"name" json:"name"`
	Enabled          bool   `db:"enabled" json:"enabled"`
	Method           string `db:"method" json:"method"`
	Databases        string `db:"databases" json:"-"`
	Compression      string `db:"compression" json:"compression"`
	CompressionLevel int    `db:"compression_level" json:"compression_level"`
	Encrypt          bool   `db:"encrypt" json:"encrypt"`
	Passphrase       string `db:"passphrase" json:"-"`
	Targets          string `db:"targets" json:"-"`
	Schedule         string `db:"schedule" json:"-"`
	Retention        string `db:"retention" json:"-"`
	Verify           string `db:"verify" json:"-"`
	NotifySuccess    string `db:"notify_success" json:"-"`
	NotifyFailure    string `db:"notify_failure" json:"-"`
	NextRunAt        int64  `db:"next_run_at" json:"next_run_at"`
	RetryAt          int64  `db:"retry_at" json:"retry_at"`
	RetryAttempt     int    `db:"retry_attempt" json:"retry_attempt"`
	NextVerifyAt     int64  `db:"next_verify_at" json:"next_verify_at"`
	CreatedBy        string `db:"created_by" json:"created_by"`
	CreatedAt        int64  `db:"created_at" json:"created_at"`
	UpdatedAt        int64  `db:"updated_at" json:"updated_at"`
}

// TargetIDs 存储目标 ID。
func (p *Plan) TargetIDs() []string { return jsonStrings(p.Targets) }

// DatabaseList 选的库（空 = 全部用户库）。
func (p *Plan) DatabaseList() []string { return jsonStrings(p.Databases) }

// Spec 计划表达式。
func (p *Plan) Spec() *sched.Spec {
	sp, err := sched.Parse(p.Schedule)
	if err != nil {
		return &sched.Spec{Kind: "manual"}
	}
	return sp
}

// RetentionRule 保留规则。
func (p *Plan) RetentionRule() Retention {
	var r Retention
	_ = json.Unmarshal([]byte(p.Retention), &r)
	return r
}

// VerifyConfig 是还原校验设置。
type VerifyConfig struct {
	AfterBackup bool        `json:"after_backup"`
	Schedule    *sched.Spec `json:"schedule,omitempty"` // 按计划校验最近一份成功的备份
}

// VerifyRule 还原校验设置。
func (p *Plan) VerifyRule() VerifyConfig {
	var v VerifyConfig
	_ = json.Unmarshal([]byte(p.Verify), &v)
	return v
}

func planAAD(id string) string { return crypt.AAD("backup_plans", id, "passphrase") }
func runAAD(id string) string  { return crypt.AAD("backup_runs", id, "passphrase") }

// PlanInput 是创建或修改计划的请求。
type PlanInput struct {
	ConnectionID     string        `json:"connection_id"`
	Name             string        `json:"name"`
	Enabled          *bool         `json:"enabled"`
	Method           string        `json:"method"`
	Databases        []string      `json:"databases"`
	Compression      string        `json:"compression"`
	CompressionLevel int           `json:"compression_level"`
	Encrypt          bool          `json:"encrypt"`
	Passphrase       string        `json:"passphrase"` // 只写；不传表示保持原值
	Targets          []string      `json:"targets"`
	Schedule         sched.Spec    `json:"schedule"`
	Retention        Retention     `json:"retention"`
	Verify           VerifyConfig  `json:"verify"`
	NotifySuccess    []string      `json:"notify_success"`
	NotifyFailure    []string      `json:"notify_failure"`
	connection       *conns.Connection
}

// GetPlan 取计划。
func (s *Service) GetPlan(ctx context.Context, id string) (*Plan, error) {
	var p Plan
	if err := s.Store.Get(ctx, &p, `SELECT * FROM backup_plans WHERE id = ?`, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, apperr.NotFound("备份计划")
		}
		return nil, err
	}
	return &p, nil
}

// ListPlans 空间的计划（connID 非空时只列该连接的）。
func (s *Service) ListPlans(ctx context.Context, wsID, connID string) ([]Plan, error) {
	out := []Plan{}
	q, args := `SELECT * FROM backup_plans WHERE workspace_id = ?`, []any{wsID}
	if connID != "" {
		q += ` AND connection_id = ?`
		args = append(args, connID)
	}
	err := s.Store.Select(ctx, &out, q+` ORDER BY name`, args...)
	return out, err
}

func dedup(list []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range list {
		v = strings.TrimSpace(v)
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// preparePlan 校验计划并填好要保存的字段。
func (s *Service) preparePlan(ctx context.Context, wsID string, p *Plan, in *PlanInput, isNew bool) error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len([]rune(in.Name)) > 100 {
		return apperr.Bad("计划名称不能为空，且不超过 100 个字符")
	}
	c, err := s.Conns.Get(ctx, in.ConnectionID)
	if err != nil || c.WorkspaceID != wsID {
		return apperr.Bad("连接不存在")
	}
	if !isNew && c.ID != p.ConnectionID {
		return apperr.Bad("不能修改计划所属的连接")
	}
	in.connection = c
	switch c.Driver {
	case "mysql":
		if in.Method != MethodMysqldump && in.Method != MethodLemi {
			return apperr.Bad("MySQL 连接的备份方式只能是 mysqldump 或 Lemi 自研导出器")
		}
		if in.Method == MethodMysqldump && s.Tools != nil {
			if ok, why := s.Tools.Available(); !ok {
				return apperr.Bad("%s", why)
			}
		}
	case "sqlite":
		if in.Method != MethodLemi && in.Method != MethodSQLiteFile {
			return apperr.Bad("SQLite 连接的备份方式只能是 Lemi 自研导出器或文件快照")
		}
		in.Databases = nil
	default:
		return apperr.Bad("该数据库类型不支持备份")
	}
	in.Databases = dedup(in.Databases)
	for _, d := range in.Databases {
		if len(d) > 64 || strings.ContainsRune(d, 0) {
			return apperr.Bad("库名 %q 不正确", d)
		}
	}
	switch in.Compression {
	case "":
		in.Compression = "zstd"
	case "zstd", "gzip", "none":
	default:
		return apperr.Bad("压缩方式只能是 zstd、gzip 或不压缩")
	}
	if in.CompressionLevel == 0 {
		in.CompressionLevel = map[string]int{"zstd": 5, "gzip": 6}[in.Compression]
	}
	if (in.Compression == "zstd" && (in.CompressionLevel < 1 || in.CompressionLevel > 22)) || (in.Compression == "gzip" && (in.CompressionLevel < 1 || in.CompressionLevel > 9)) {
		return apperr.Bad("压缩级别超出范围（zstd 1–22，gzip 1–9）")
	}
	in.Targets = dedup(in.Targets)
	if len(in.Targets) == 0 {
		return apperr.Bad("请至少选择一个存储目标")
	}
	for _, id := range in.Targets {
		t, err := s.GetTarget(ctx, id)
		if err != nil || t.WorkspaceID != wsID {
			return apperr.Bad("存储目标不存在")
		}
	}
	if in.Schedule.Kind == "" {
		in.Schedule.Kind = "manual"
	}
	if err := in.Schedule.Validate(); err != nil {
		return apperr.Bad("计划：%s", err.Error())
	}
	if err := in.Retention.Validate(); err != nil {
		return err
	}
	if in.Verify.Schedule != nil {
		if in.Verify.Schedule.Kind == "" || in.Verify.Schedule.Kind == "manual" {
			in.Verify.Schedule = nil
		} else if err := in.Verify.Schedule.Validate(); err != nil {
			return apperr.Bad("校验计划：%s", err.Error())
		}
	}
	in.NotifySuccess, in.NotifyFailure = dedup(in.NotifySuccess), dedup(in.NotifyFailure)
	if s.Notify != nil {
		if err := s.Notify.CheckChannels(ctx, wsID, append(append([]string{}, in.NotifySuccess...), in.NotifyFailure...)); err != nil {
			return err
		}
	}
	// 备份密码：至少 12 位；不传保持原值
	if in.Encrypt {
		if in.Passphrase != "" {
			if len([]rune(in.Passphrase)) < 12 {
				return apperr.Bad("备份密码至少 12 位")
			}
			if len(in.Passphrase) > 1024 {
				return apperr.Bad("备份密码太长")
			}
			p.Passphrase = s.Box.Seal(in.Passphrase, planAAD(p.ID))
		} else if p.Passphrase == "" {
			return apperr.Bad("开启加密时必须设置备份密码")
		}
	} else {
		p.Passphrase = ""
	}
	oldSchedule := p.Schedule
	oldVerify := p.Verify
	p.ConnectionID, p.Name, p.Method = c.ID, in.Name, in.Method
	p.Databases, p.Compression, p.CompressionLevel, p.Encrypt = toJSON(in.Databases), in.Compression, in.CompressionLevel, in.Encrypt
	p.Targets, p.Schedule, p.Retention, p.Verify = toJSON(in.Targets), toJSON(in.Schedule), toJSON(in.Retention), toJSON(in.Verify)
	p.NotifySuccess, p.NotifyFailure = toJSON(in.NotifySuccess), toJSON(in.NotifyFailure)
	if in.Enabled != nil {
		p.Enabled = *in.Enabled
	} else if isNew {
		p.Enabled = true
	}
	now := s.now()
	if isNew || oldSchedule != p.Schedule || p.NextRunAt == 0 {
		p.NextRunAt = nextAt(&in.Schedule, now)
	}
	if isNew || oldVerify != p.Verify {
		p.NextVerifyAt = 0
		if in.Verify.Schedule != nil {
			p.NextVerifyAt = nextAt(in.Verify.Schedule, now)
		}
	}
	p.UpdatedAt = store.Now()
	return nil
}

func nextAt(sp *sched.Spec, after time.Time) int64 {
	if sp == nil || sp.Kind == "manual" {
		return 0
	}
	t := sp.Next(after)
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// CreatePlan 新建计划。
func (s *Service) CreatePlan(ctx context.Context, wsID, userID string, in *PlanInput) (*Plan, error) {
	p := &Plan{ID: ids.New(), WorkspaceID: wsID, CreatedBy: userID, CreatedAt: store.Now()}
	if err := s.preparePlan(ctx, wsID, p, in, true); err != nil {
		return nil, err
	}
	if err := s.Store.Add(ctx, "backup_plans", p); err != nil {
		return nil, err
	}
	return p, nil
}

// UpdatePlan 修改计划。
func (s *Service) UpdatePlan(ctx context.Context, p *Plan, in *PlanInput) error {
	if err := s.preparePlan(ctx, p.WorkspaceID, p, in, false); err != nil {
		return err
	}
	return s.Store.Put(ctx, "backup_plans", p)
}

// DeletePlan 删除计划（已有的备份记录与文件保留，仍可下载与还原）。
func (s *Service) DeletePlan(ctx context.Context, p *Plan) error {
	_, err := s.Store.Exec(ctx, `DELETE FROM backup_plans WHERE id = ?`, p.ID)
	return err
}

// PlanView 给前端的计划描述（密码只给「是否已设置」）。
func (s *Service) PlanView(p *Plan) map[string]any {
	return map[string]any{
		"id": p.ID, "workspace_id": p.WorkspaceID, "connection_id": p.ConnectionID, "name": p.Name, "enabled": p.Enabled, "method": p.Method,
		"databases": p.DatabaseList(), "compression": p.Compression, "compression_level": p.CompressionLevel, "encrypt": p.Encrypt,
		"passphrase_set": p.Passphrase != "", "targets": p.TargetIDs(), "schedule": p.Spec(), "schedule_text": p.Spec().Describe(),
		"retention": p.RetentionRule(), "verify": p.VerifyRule(), "notify_success": jsonStrings(p.NotifySuccess), "notify_failure": jsonStrings(p.NotifyFailure),
		"next_run_at": p.NextRunAt, "retry_at": p.RetryAt, "retry_attempt": p.RetryAttempt, "next_verify_at": p.NextVerifyAt,
		"created_by": p.CreatedBy, "created_at": p.CreatedAt, "updated_at": p.UpdatedAt,
	}
}
