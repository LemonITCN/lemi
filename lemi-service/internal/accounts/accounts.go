// Package accounts 管理用户、工作空间与成员，并计算权限。
package accounts

import (
	"context"
	"errors"
	"strings"

	"github.com/LemonITCN/lemi/lemi-service/internal/ids"
	"github.com/LemonITCN/lemi/lemi-service/internal/store"
	"github.com/jmoiron/sqlx"
)

// 实例角色。
const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

// 空间角色（逐级包含）。
const (
	WSViewer = "viewer"
	WSMember = "member"
	WSAdmin  = "admin"
	WSOwner  = "owner"
)

var wsRank = map[string]int{WSViewer: 1, WSMember: 2, WSAdmin: 3, WSOwner: 4}

// RoleRank 返回空间角色的级别（未知为 0）。
func RoleRank(role string) int { return wsRank[role] }

// ValidWSRole 判断空间角色是否合法。
func ValidWSRole(r string) bool { return wsRank[r] > 0 }

// User 是用户。
type User struct {
	ID                 string `db:"id" json:"id"`
	Username           string `db:"username" json:"username"`
	DisplayName        string `db:"display_name" json:"display_name"`
	Email              string `db:"email" json:"email"`
	Role               string `db:"role" json:"role"`
	PasswordHash       string `db:"password_hash" json:"-"`
	PasswordChangedAt  int64  `db:"password_changed_at" json:"password_changed_at"`
	MustChangePassword bool   `db:"must_change_password" json:"must_change_password"`
	TOTPSecret         string `db:"totp_secret" json:"-"`
	TOTPEnabled        bool   `db:"totp_enabled" json:"totp_enabled"`
	TOTPLastStep       int64  `db:"totp_last_step" json:"-"`
	RecoveryCodes      string `db:"recovery_codes" json:"-"`
	Status             string `db:"status" json:"status"`
	Locale             string `db:"locale" json:"locale"`
	Prefs              string `db:"prefs" json:"-"`
	LastLoginAt        int64  `db:"last_login_at" json:"last_login_at"`
	LastLoginIP        string `db:"last_login_ip" json:"last_login_ip"`
	CreatedAt          int64  `db:"created_at" json:"created_at"`
	UpdatedAt          int64  `db:"updated_at" json:"updated_at"`
}

// IsAdmin 是否实例管理员。
func (u *User) IsAdmin() bool { return u != nil && u.Role == RoleAdmin }

// Name 显示名（没有则用用户名）。
func (u *User) Name() string {
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.Username
}

// Workspace 是工作空间。
type Workspace struct {
	ID          string `db:"id" json:"id"`
	Name        string `db:"name" json:"name"`
	Description string `db:"description" json:"description"`
	Settings    string `db:"settings" json:"-"`
	CreatedBy   string `db:"created_by" json:"created_by"`
	CreatedAt   int64  `db:"created_at" json:"created_at"`
	UpdatedAt   int64  `db:"updated_at" json:"updated_at"`
	// 当前用户在该空间的角色（查询时填充）
	MyRole string `db:"my_role" json:"my_role,omitempty"`
}

// Member 是空间成员。
type Member struct {
	WorkspaceID string `db:"workspace_id" json:"workspace_id"`
	UserID      string `db:"user_id" json:"user_id"`
	Role        string `db:"role" json:"role"`
	CreatedAt   int64  `db:"created_at" json:"created_at"`
	Username    string `db:"username" json:"username"`
	DisplayName string `db:"display_name" json:"display_name"`
	Email       string `db:"email" json:"email"`
}

// Service 账号服务。
type Service struct{ st *store.Store }

// New 创建账号服务。
func New(st *store.Store) *Service { return &Service{st: st} }

// Store 返回底层存储。
func (s *Service) Store() *store.Store { return s.st }

var (
	ErrNotMember   = errors.New("不是该工作空间的成员")
	ErrForbidden   = errors.New("没有权限")
	ErrLastOwner   = errors.New("空间至少要保留一位所有者")
	ErrNameInvalid = errors.New("名称不能为空，且不超过 64 个字符")
)

// NormalizeUsername 用户名统一小写去空白。
func NormalizeUsername(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// ValidUsername 3–32 位小写字母、数字、下划线、点、减号。
func ValidUsername(s string) bool {
	if len(s) < 3 || len(s) > 32 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == '-') {
			return false
		}
	}
	return true
}

// UserByID 按 ID 取用户。
func (s *Service) UserByID(ctx context.Context, id string) (*User, error) {
	var u User
	if err := s.st.GetW(ctx, &u, `SELECT * FROM users WHERE id = ?`, id); err != nil {
		return nil, err
	}
	return &u, nil
}

// UserByName 按用户名取用户。
func (s *Service) UserByName(ctx context.Context, name string) (*User, error) {
	var u User
	if err := s.st.GetW(ctx, &u, `SELECT * FROM users WHERE username = ?`, NormalizeUsername(name)); err != nil {
		return nil, err
	}
	return &u, nil
}

// Users 列出全部用户。
func (s *Service) Users(ctx context.Context) ([]User, error) {
	var out []User
	err := s.st.Select(ctx, &out, `SELECT * FROM users ORDER BY created_at`)
	return out, err
}

// CountUsers 用户总数。
func (s *Service) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.st.GetW(ctx, &n, `SELECT COUNT(*) FROM users`)
	return n, err
}

// CreateUser 新建用户（口令哈希由调用方算好）。
func (s *Service) CreateUser(ctx context.Context, u *User) error {
	u.Username = NormalizeUsername(u.Username)
	if !ValidUsername(u.Username) {
		return errors.New("用户名只能是 3–32 位小写字母、数字、下划线、点、减号")
	}
	if u.ID == "" {
		u.ID = ids.New()
	}
	if u.Role == "" {
		u.Role = RoleUser
	}
	if u.Status == "" {
		u.Status = "active"
	}
	if u.RecoveryCodes == "" {
		u.RecoveryCodes = "[]"
	}
	if u.Prefs == "" {
		u.Prefs = "{}"
	}
	now := store.Now()
	u.CreatedAt, u.UpdatedAt = now, now
	if u.PasswordChangedAt == 0 {
		u.PasswordChangedAt = now
	}
	if err := s.st.Add(ctx, "users", u); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return errors.New("用户名已存在")
		}
		return err
	}
	return nil
}

// SaveUser 保存用户。
func (s *Service) SaveUser(ctx context.Context, u *User) error {
	u.UpdatedAt = store.Now()
	return s.st.Put(ctx, "users", u)
}

// DeleteUser 删除用户。拒绝删除最后一位管理员；用户是某空间唯一 owner 时也拒绝。
func (s *Service) DeleteUser(ctx context.Context, id string) error {
	return s.st.Tx(ctx, func(tx *sqlx.Tx) error {
		var u User
		if err := tx.GetContext(ctx, &u, `SELECT * FROM users WHERE id = ?`, id); err != nil {
			return store.ErrNotFound
		}
		if u.Role == RoleAdmin {
			var n int
			_ = tx.GetContext(ctx, &n, `SELECT COUNT(*) FROM users WHERE role = 'admin' AND status = 'active' AND id <> ?`, id)
			if n == 0 {
				return errors.New("至少要保留一位实例管理员")
			}
		}
		var sole []string
		_ = tx.SelectContext(ctx, &sole, `SELECT m.workspace_id FROM workspace_members m WHERE m.user_id = ? AND m.role = 'owner'
			AND NOT EXISTS (SELECT 1 FROM workspace_members o WHERE o.workspace_id = m.workspace_id AND o.role = 'owner' AND o.user_id <> m.user_id)`, id)
		if len(sole) > 0 {
			return errors.New("该用户是某些工作空间唯一的所有者，请先转让所有权")
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
		return err
	})
}

// ---------- 工作空间 ----------

// CreateWorkspace 新建空间，创建者成为 owner。
func (s *Service) CreateWorkspace(ctx context.Context, name, desc, ownerID string) (*Workspace, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 64 {
		return nil, ErrNameInvalid
	}
	now := store.Now()
	w := &Workspace{ID: ids.New(), Name: name, Description: strings.TrimSpace(desc), Settings: "{}", CreatedBy: ownerID, CreatedAt: now, UpdatedAt: now}
	err := s.st.Tx(ctx, func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO workspaces (id, name, description, settings, created_by, created_at, updated_at) VALUES (?,?,?,?,?,?,?)`,
			w.ID, w.Name, w.Description, w.Settings, w.CreatedBy, now, now); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO workspace_members (workspace_id, user_id, role, created_at) VALUES (?,?,?,?)`, w.ID, ownerID, WSOwner, now)
		return err
	})
	if err != nil {
		return nil, err
	}
	w.MyRole = WSOwner
	return w, nil
}

// Workspace 取空间。
func (s *Service) Workspace(ctx context.Context, id string) (*Workspace, error) {
	var w Workspace
	if err := s.st.Get(ctx, &w, `SELECT *, '' AS my_role FROM workspaces WHERE id = ?`, id); err != nil {
		return nil, err
	}
	return &w, nil
}

// UpdateWorkspace 改名与说明。
func (s *Service) UpdateWorkspace(ctx context.Context, id, name, desc string) error {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 64 {
		return ErrNameInvalid
	}
	_, err := s.st.Exec(ctx, `UPDATE workspaces SET name = ?, description = ?, updated_at = ? WHERE id = ?`, name, strings.TrimSpace(desc), store.Now(), id)
	return err
}

// DeleteWorkspace 删除空间（各模块的数据由外键级联或模块自行清理）。
func (s *Service) DeleteWorkspace(ctx context.Context, id string) error {
	_, err := s.st.Exec(ctx, `DELETE FROM workspaces WHERE id = ?`, id)
	return err
}

// MyWorkspaces 用户所在的空间；实例管理员能看到全部（未加入的 my_role 为空）。
func (s *Service) MyWorkspaces(ctx context.Context, u *User) ([]Workspace, error) {
	var out []Workspace
	var err error
	if u.IsAdmin() {
		err = s.st.Select(ctx, &out, `SELECT w.*, COALESCE(m.role, '') AS my_role FROM workspaces w
			LEFT JOIN workspace_members m ON m.workspace_id = w.id AND m.user_id = ? ORDER BY w.created_at`, u.ID)
	} else {
		err = s.st.Select(ctx, &out, `SELECT w.*, m.role AS my_role FROM workspaces w
			JOIN workspace_members m ON m.workspace_id = w.id AND m.user_id = ? ORDER BY w.created_at`, u.ID)
	}
	return out, err
}

// Members 空间成员。
func (s *Service) Members(ctx context.Context, wsID string) ([]Member, error) {
	var out []Member
	err := s.st.Select(ctx, &out, `SELECT m.workspace_id, m.user_id, m.role, m.created_at, u.username, u.display_name, u.email
		FROM workspace_members m JOIN users u ON u.id = m.user_id WHERE m.workspace_id = ? ORDER BY m.created_at`, wsID)
	return out, err
}

// SetMember 添加成员或改角色。
func (s *Service) SetMember(ctx context.Context, wsID, userID, role string) error {
	if !ValidWSRole(role) {
		return errors.New("角色不正确")
	}
	return s.st.Tx(ctx, func(tx *sqlx.Tx) error {
		var cur string
		_ = tx.GetContext(ctx, &cur, `SELECT role FROM workspace_members WHERE workspace_id = ? AND user_id = ?`, wsID, userID)
		if cur == WSOwner && role != WSOwner {
			var n int
			_ = tx.GetContext(ctx, &n, `SELECT COUNT(*) FROM workspace_members WHERE workspace_id = ? AND role = 'owner' AND user_id <> ?`, wsID, userID)
			if n == 0 {
				return ErrLastOwner
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO workspace_members (workspace_id, user_id, role, created_at) VALUES (?,?,?,?)
			ON CONFLICT(workspace_id, user_id) DO UPDATE SET role = excluded.role`, wsID, userID, role, store.Now())
		return err
	})
}

// RemoveMember 移除成员。
func (s *Service) RemoveMember(ctx context.Context, wsID, userID string) error {
	return s.st.Tx(ctx, func(tx *sqlx.Tx) error {
		var cur string
		_ = tx.GetContext(ctx, &cur, `SELECT role FROM workspace_members WHERE workspace_id = ? AND user_id = ?`, wsID, userID)
		if cur == WSOwner {
			var n int
			_ = tx.GetContext(ctx, &n, `SELECT COUNT(*) FROM workspace_members WHERE workspace_id = ? AND role = 'owner' AND user_id <> ?`, wsID, userID)
			if n == 0 {
				return ErrLastOwner
			}
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM workspace_members WHERE workspace_id = ? AND user_id = ?`, wsID, userID)
		return err
	})
}

// RoleIn 返回用户在空间里的角色（不是成员返回空）。实例管理员不自动获得角色，
// 需要管理时由调用方用 EffectiveRole。
func (s *Service) RoleIn(ctx context.Context, wsID, userID string) string {
	var r string
	_ = s.st.GetW(ctx, &r, `SELECT role FROM workspace_members WHERE workspace_id = ? AND user_id = ?`, wsID, userID)
	return r
}

// EffectiveRole 用户在空间里的有效角色：成员按角色；实例管理员不是成员时视为 admin（可管理但会记审计）。
func (s *Service) EffectiveRole(ctx context.Context, wsID string, u *User) string {
	if r := s.RoleIn(ctx, wsID, u.ID); r != "" {
		return r
	}
	if u.IsAdmin() {
		if _, err := s.Workspace(ctx, wsID); err == nil {
			return WSAdmin
		}
	}
	return ""
}

// Require 要求用户在空间里至少有 minRole，返回有效角色。
func (s *Service) Require(ctx context.Context, wsID string, u *User, minRole string) (string, error) {
	r := s.EffectiveRole(ctx, wsID, u)
	if r == "" {
		return "", ErrNotMember
	}
	if RoleRank(r) < RoleRank(minRole) {
		return r, ErrForbidden
	}
	return r, nil
}

// NeedsTOTP 用户是否必须绑定两步验证：实例管理员、任一空间的 owner/admin 一律必须；其他用户按系统设置。
func (s *Service) NeedsTOTP(ctx context.Context, u *User, requireMembers bool) bool {
	if u.IsAdmin() || requireMembers {
		return true
	}
	var n int
	_ = s.st.GetW(ctx, &n, `SELECT COUNT(*) FROM workspace_members WHERE user_id = ? AND role IN ('owner','admin')`, u.ID)
	return n > 0
}
