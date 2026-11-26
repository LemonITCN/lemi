// Package settings 读写实例级系统设置（settings 表，键值 JSON）。
package settings

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/store"
)

// System 是实例级系统设置。
type System struct {
	// 安全
	RequireMemberTOTP  bool `json:"require_member_totp"`  // 普通成员也必须绑定两步验证（管理员与空间 owner/admin 永远必须）
	SessionIdleHours   int  `json:"session_idle_hours"`   // 空闲多久会话失效
	SessionMaxDays     int  `json:"session_max_days"`     // 会话最长有效期
	AllowPublicShare   bool `json:"allow_public_share"`   // 允许生成报告公开链接
	AllowAIFullAccess  bool `json:"allow_ai_full_access"` // 允许把连接的 AI 档位设为「完全放开」
	AllowPrivateEgress bool `json:"allow_private_egress"` // Webhook、通知、自定义模型地址允许访问内网
	AuditRetentionDays int  `json:"audit_retention_days"`
	UsersCanCreateWS   bool `json:"users_can_create_workspace"` // 普通用户可以自己建工作空间
	// 连接目标网络策略（CIDR）。Allow 为空表示不限；Deny 优先。云元数据地址永远拒绝。
	ConnAllow []string `json:"conn_allow"`
	ConnDeny  []string `json:"conn_deny"`
	// 查询默认限制
	QueryTimeoutSec   int `json:"query_timeout_sec"`
	AgentTimeoutSec   int `json:"agent_timeout_sec"`
	MaxResultRows     int `json:"max_result_rows"`
	AgentMaxSteps     int `json:"agent_max_steps"`
	AgentPreviewRows  int `json:"agent_preview_rows"`
	AuditSQLMaxLength int `json:"audit_sql_max_length"`
}

// Defaults 默认系统设置。
func Defaults() System {
	return System{
		RequireMemberTOTP:  true,
		SessionIdleHours:   12,
		SessionMaxDays:     7,
		AllowPublicShare:   true,
		AllowAIFullAccess:  false,
		AuditRetentionDays: 365,
		QueryTimeoutSec:    300,
		AgentTimeoutSec:    60,
		MaxResultRows:      100000,
		AgentMaxSteps:      200,
		AgentPreviewRows:   50,
		AuditSQLMaxLength:  20000,
	}
}

func (s *System) normalize() {
	d := Defaults()
	if s.SessionIdleHours <= 0 {
		s.SessionIdleHours = d.SessionIdleHours
	}
	if s.SessionMaxDays <= 0 {
		s.SessionMaxDays = d.SessionMaxDays
	}
	if s.AuditRetentionDays <= 0 {
		s.AuditRetentionDays = d.AuditRetentionDays
	}
	if s.QueryTimeoutSec <= 0 {
		s.QueryTimeoutSec = d.QueryTimeoutSec
	}
	if s.AgentTimeoutSec <= 0 {
		s.AgentTimeoutSec = d.AgentTimeoutSec
	}
	if s.MaxResultRows <= 0 || s.MaxResultRows > 1000000 {
		s.MaxResultRows = d.MaxResultRows
	}
	if s.AgentMaxSteps <= 0 {
		s.AgentMaxSteps = d.AgentMaxSteps
	}
	if s.AgentPreviewRows <= 0 || s.AgentPreviewRows > 500 {
		s.AgentPreviewRows = d.AgentPreviewRows
	}
	if s.AuditSQLMaxLength <= 0 {
		s.AuditSQLMaxLength = d.AuditSQLMaxLength
	}
}

// Service 带缓存的设置读写。
type Service struct {
	st    *store.Store
	mu    sync.RWMutex
	cache map[string]json.RawMessage
}

// New 创建设置服务。
func New(st *store.Store) *Service { return &Service{st: st, cache: map[string]json.RawMessage{}} }

// GetJSON 读取键值到 dst；不存在时返回 false。
func (s *Service) GetJSON(ctx context.Context, key string, dst any) (bool, error) {
	s.mu.RLock()
	raw, ok := s.cache[key]
	s.mu.RUnlock()
	if !ok {
		var v string
		err := s.st.GetW(ctx, &v, `SELECT value FROM settings WHERE key = ?`, key)
		if err == store.ErrNotFound {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		raw = json.RawMessage(v)
		s.mu.Lock()
		s.cache[key] = raw
		s.mu.Unlock()
	}
	return true, json.Unmarshal(raw, dst)
}

// SetJSON 写入键值。
func (s *Service) SetJSON(ctx context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.st.Exec(ctx, `INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, key, string(b), time.Now().UnixMilli())
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.cache[key] = b
	s.mu.Unlock()
	return nil
}

// System 读系统设置（读失败时返回默认值）。
func (s *Service) System(ctx context.Context) System {
	sys := Defaults()
	_, _ = s.GetJSON(ctx, "system", &sys)
	sys.normalize()
	return sys
}

// SetSystem 保存系统设置。
func (s *Service) SetSystem(ctx context.Context, sys System) error {
	sys.normalize()
	return s.SetJSON(ctx, "system", sys)
}
