package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/crypt"
	"github.com/LemonITCN/lemi/lemi-service/internal/ids"
	"github.com/LemonITCN/lemi/lemi-service/internal/settings"
	"github.com/LemonITCN/lemi/lemi-service/internal/store"
	"github.com/jmoiron/sqlx"
)

// 会话阶段。
const (
	StageMFA       = "mfa"        // 口令正确，等待两步验证码
	StageTOTPSetup = "totp_setup" // 口令正确，但必须先绑定两步验证
	StageDone      = "done"
)

var (
	ErrBadCredentials = errors.New("用户名或口令不正确")
	ErrBadCode        = errors.New("验证码不正确")
	ErrDisabled       = errors.New("账号已停用")
	ErrSessionInvalid = errors.New("登录已失效，请重新登录")
)

// ErrLocked 登录被暂时锁定。
type ErrLocked struct{ RetryAfter time.Duration }

func (e *ErrLocked) Error() string {
	s := int(e.RetryAfter.Seconds()) + 1
	if s >= 120 {
		return fmt.Sprintf("尝试次数过多，请 %d 分钟后再试", s/60+1)
	}
	return fmt.Sprintf("尝试次数过多，请 %d 秒后再试", s)
}

// Session 是登录会话。
type Session struct {
	ID         string `db:"id" json:"-"`
	UserID     string `db:"user_id" json:"user_id"`
	CSRF       string `db:"csrf" json:"-"`
	Stage      string `db:"stage" json:"stage"`
	CreatedAt  int64  `db:"created_at" json:"created_at"`
	LastSeenAt int64  `db:"last_seen_at" json:"last_seen_at"`
	ExpiresAt  int64  `db:"expires_at" json:"expires_at"`
	ReauthAt   int64  `db:"reauth_at" json:"-"`
	IP         string `db:"ip" json:"ip"`
	UA         string `db:"ua" json:"ua"`
}

// Service 认证服务。
type Service struct {
	st      *store.Store
	acc     *accounts.Service
	set     *settings.Service
	box     *crypt.Box
	now     func() time.Time
	OnEvent func(ctx context.Context, kind string, u *accounts.User, ip, ua string) // 安全事件（锁定、新设备登录）回调，由通知模块注入
}

// New 创建认证服务。
func New(st *store.Store, acc *accounts.Service, set *settings.Service, box *crypt.Box) *Service {
	return &Service{st: st, acc: acc, set: set, box: box, now: time.Now}
}

func hashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// HashToken 对外暴露令牌哈希（邀请、下载令牌等复用）。
func HashToken(tok string) string { return hashToken(tok) }

// ---------- 限速 ----------

type attempt struct {
	Key         string `db:"key"`
	Failures    int    `db:"failures"`
	Locks       int    `db:"locks"`
	FirstAt     int64  `db:"first_at"`
	LastAt      int64  `db:"last_at"`
	LockedUntil int64  `db:"locked_until"`
}

// 账号：连续失败 5 次锁 15 分钟，再次触发翻倍，最长 24 小时；IP：1 分钟内失败 10 次锁 5 分钟，翻倍到 1 小时。
func policy(key string) (limit int, window time.Duration, base, max time.Duration) {
	if strings.HasPrefix(key, "ip:") {
		return 10, time.Minute, 5 * time.Minute, time.Hour
	}
	return 5, 24 * time.Hour, 15 * time.Minute, 24 * time.Hour
}

func (s *Service) checkLock(ctx context.Context, keys ...string) error {
	now := s.now().UnixMilli()
	for _, k := range keys {
		var a attempt
		if err := s.st.GetW(ctx, &a, `SELECT * FROM login_attempts WHERE key = ?`, k); err == nil && a.LockedUntil > now {
			return &ErrLocked{RetryAfter: time.Duration(a.LockedUntil-now) * time.Millisecond}
		}
	}
	return nil
}

// recordFailure 记一次失败，返回是否因此进入锁定。
func (s *Service) recordFailure(ctx context.Context, keys ...string) bool {
	now := s.now().UnixMilli()
	lockedNow := false
	for _, k := range keys {
		limit, window, base, max := policy(k)
		var a attempt
		err := s.st.GetW(ctx, &a, `SELECT * FROM login_attempts WHERE key = ?`, k)
		if err != nil {
			a = attempt{Key: k, FirstAt: now}
		}
		if now-a.FirstAt > window.Milliseconds() && a.LockedUntil < now {
			a.Failures, a.FirstAt = 0, now
		}
		a.Failures++
		a.LastAt = now
		if a.Failures >= limit {
			d := base << uint(min(a.Locks, 10))
			if d > max {
				d = max
			}
			a.LockedUntil = now + d.Milliseconds()
			a.Locks++
			a.Failures = 0
			a.FirstAt = now
			if strings.HasPrefix(k, "u:") {
				lockedNow = true
			}
		}
		_, _ = s.st.Exec(ctx, `INSERT INTO login_attempts (key, failures, locks, first_at, last_at, locked_until) VALUES (?,?,?,?,?,?)
			ON CONFLICT(key) DO UPDATE SET failures=excluded.failures, locks=excluded.locks, first_at=excluded.first_at, last_at=excluded.last_at, locked_until=excluded.locked_until`,
			a.Key, a.Failures, a.Locks, a.FirstAt, a.LastAt, a.LockedUntil)
	}
	return lockedNow
}

func (s *Service) clearFailures(ctx context.Context, keys ...string) {
	for _, k := range keys {
		_, _ = s.st.Exec(ctx, `UPDATE login_attempts SET failures = 0 WHERE key = ?`, k)
	}
}

// Unlock 解除锁定（命令行 lemi admin unlock 用）。key 为用户名或 IP。
func (s *Service) Unlock(ctx context.Context, key string) error {
	_, err := s.st.Exec(ctx, `DELETE FROM login_attempts WHERE key IN (?, ?)`, "u:"+accounts.NormalizeUsername(key), "ip:"+key)
	return err
}

// ---------- 登录 ----------

// CheckPassword 校验用户名口令（带限速），成功返回用户。
func (s *Service) CheckPassword(ctx context.Context, username, password, ip, ua string) (*accounts.User, error) {
	username = accounts.NormalizeUsername(username)
	keys := []string{"u:" + username, "ip:" + ip}
	if err := s.checkLock(ctx, keys...); err != nil {
		return nil, err
	}
	u, err := s.acc.UserByName(ctx, username)
	if err != nil {
		DummyVerify(password)
		s.recordFailure(ctx, keys...)
		return nil, ErrBadCredentials
	}
	if !VerifyPassword(password, u.PasswordHash) {
		if s.recordFailure(ctx, keys...) && s.OnEvent != nil {
			s.OnEvent(ctx, "account_locked", u, ip, ua)
		}
		return nil, ErrBadCredentials
	}
	if u.Status != "active" {
		return nil, ErrDisabled
	}
	s.clearFailures(ctx, "u:"+username)
	return u, nil
}

// CheckCode 校验两步验证码或恢复码（带限速）。成功后记录时间步 / 消耗恢复码。
func (s *Service) CheckCode(ctx context.Context, u *accounts.User, code, ip string) error {
	keys := []string{"mfa:" + u.ID, "ip:" + ip}
	if err := s.checkLock(ctx, keys...); err != nil {
		return err
	}
	code = strings.TrimSpace(code)
	if u.TOTPEnabled && len(code) == 6 {
		secret, err := s.box.Open(u.TOTPSecret, crypt.AAD("users", u.ID, "totp_secret"))
		if err == nil {
			if step, ok := MatchTOTP(secret, code, u.TOTPLastStep, s.now()); ok {
				res, err := s.st.Exec(ctx, `UPDATE users SET totp_last_step = ? WHERE id = ? AND totp_last_step < ?`, step, u.ID, step)
				if err == nil {
					if n, _ := res.RowsAffected(); n == 1 {
						u.TOTPLastStep = step
						s.clearFailures(ctx, "mfa:"+u.ID)
						return nil
					}
				}
			}
		}
	}
	if len(code) >= 10 {
		h := HashRecoveryCode(code)
		ok := false
		err := s.st.Tx(ctx, func(tx *sqlx.Tx) error {
			var raw string
			if err := tx.GetContext(ctx, &raw, `SELECT recovery_codes FROM users WHERE id = ?`, u.ID); err != nil {
				return err
			}
			var hashes []string
			_ = json.Unmarshal([]byte(raw), &hashes)
			for i, x := range hashes {
				if x == h {
					hashes = append(hashes[:i], hashes[i+1:]...)
					b, _ := json.Marshal(hashes)
					ok = true
					_, err := tx.ExecContext(ctx, `UPDATE users SET recovery_codes = ? WHERE id = ?`, string(b), u.ID)
					return err
				}
			}
			return nil
		})
		if err == nil && ok {
			s.clearFailures(ctx, "mfa:"+u.ID)
			return nil
		}
	}
	s.recordFailure(ctx, keys...)
	return ErrBadCode
}

// ---------- 会话 ----------

// CreateSession 新建会话，返回 Cookie 里放的令牌。
func (s *Service) CreateSession(ctx context.Context, u *accounts.User, stage, ip, ua string) (string, *Session, error) {
	sys := s.set.System(ctx)
	tok := ids.RandomToken(43)
	now := s.now()
	sess := &Session{ID: hashToken(tok), UserID: u.ID, CSRF: ids.RandomToken(32), Stage: stage,
		CreatedAt: now.UnixMilli(), LastSeenAt: now.UnixMilli(),
		ExpiresAt: now.Add(time.Duration(sys.SessionMaxDays) * 24 * time.Hour).UnixMilli(),
		IP:        ip, UA: truncate(ua, 300)}
	if stage == StageDone {
		sess.ReauthAt = now.UnixMilli()
	}
	if err := s.st.Add(ctx, "sessions", sess); err != nil {
		return "", nil, err
	}
	return tok, sess, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// LookupSession 通过令牌找会话与用户；过期、闲置超时、账号停用都视为失效。
func (s *Service) LookupSession(ctx context.Context, tok string) (*Session, *accounts.User, error) {
	if tok == "" || len(tok) > 100 {
		return nil, nil, ErrSessionInvalid
	}
	var sess Session
	if err := s.st.Get(ctx, &sess, `SELECT * FROM sessions WHERE id = ?`, hashToken(tok)); err != nil {
		return nil, nil, ErrSessionInvalid
	}
	now := s.now().UnixMilli()
	sys := s.set.System(ctx)
	idle := int64(sys.SessionIdleHours) * 3600 * 1000
	if sess.Stage != StageDone {
		idle = 15 * 60 * 1000 // 未完成两步验证的半登录状态只保留 15 分钟
	}
	if now > sess.ExpiresAt || now-sess.LastSeenAt > idle {
		_, _ = s.st.Exec(ctx, `DELETE FROM sessions WHERE id = ?`, sess.ID)
		return nil, nil, ErrSessionInvalid
	}
	u, err := s.acc.UserByID(ctx, sess.UserID)
	if err != nil || u.Status != "active" {
		return nil, nil, ErrSessionInvalid
	}
	if now-sess.LastSeenAt > 60*1000 {
		_, _ = s.st.Exec(ctx, `UPDATE sessions SET last_seen_at = ? WHERE id = ?`, now, sess.ID)
		sess.LastSeenAt = now
	}
	return &sess, u, nil
}

// SetStage 推进会话阶段。完成登录时同时记录 re-auth 时间。
func (s *Service) SetStage(ctx context.Context, sess *Session, stage string) error {
	now := s.now().UnixMilli()
	reauth := sess.ReauthAt
	if stage == StageDone {
		reauth = now
	}
	_, err := s.st.Exec(ctx, `UPDATE sessions SET stage = ?, reauth_at = ? WHERE id = ?`, stage, reauth, sess.ID)
	if err == nil {
		sess.Stage, sess.ReauthAt = stage, reauth
	}
	return err
}

// Reauthed 会话在 10 分钟内是否输过口令或验证码。
func (s *Service) Reauthed(sess *Session) bool {
	return sess != nil && s.now().UnixMilli()-sess.ReauthAt < 10*60*1000
}

// MarkReauth 记录二次验证时间。
func (s *Service) MarkReauth(ctx context.Context, sess *Session) error {
	now := s.now().UnixMilli()
	_, err := s.st.Exec(ctx, `UPDATE sessions SET reauth_at = ? WHERE id = ?`, now, sess.ID)
	if err == nil {
		sess.ReauthAt = now
	}
	return err
}

// Reauth 用口令或两步验证码做二次验证。
func (s *Service) Reauth(ctx context.Context, sess *Session, u *accounts.User, password, code, ip string) error {
	if code != "" && u.TOTPEnabled {
		if err := s.CheckCode(ctx, u, code, ip); err != nil {
			return err
		}
	} else {
		if err := s.checkLock(ctx, "u:"+u.Username, "ip:"+ip); err != nil {
			return err
		}
		if !VerifyPassword(password, u.PasswordHash) {
			s.recordFailure(ctx, "u:"+u.Username, "ip:"+ip)
			return ErrBadCredentials
		}
	}
	return s.MarkReauth(ctx, sess)
}

// DeleteSession 退出登录。
func (s *Service) DeleteSession(ctx context.Context, sess *Session) {
	_, _ = s.st.Exec(ctx, `DELETE FROM sessions WHERE id = ?`, sess.ID)
}

// Sessions 用户的全部会话。
func (s *Service) Sessions(ctx context.Context, userID string) ([]Session, error) {
	var out []Session
	err := s.st.Select(ctx, &out, `SELECT * FROM sessions WHERE user_id = ? ORDER BY last_seen_at DESC`, userID)
	return out, err
}

// SessionKey 给前端标识会话用的短 ID（会话主键哈希的前 16 位，不能反推令牌）。
func SessionKey(sess *Session) string { return sess.ID[:16] }

// RevokeSession 按短 ID 删除用户的某个会话。
func (s *Service) RevokeSession(ctx context.Context, userID, key string) error {
	if len(key) != 16 {
		return store.ErrNotFound
	}
	_, err := s.st.Exec(ctx, `DELETE FROM sessions WHERE user_id = ? AND substr(id, 1, 16) = ?`, userID, key)
	return err
}

// RevokeOthers 删除用户除 keep 以外的全部会话（keep 为空则全部删除）。
func (s *Service) RevokeOthers(ctx context.Context, userID, keepID string) {
	_, _ = s.st.Exec(ctx, `DELETE FROM sessions WHERE user_id = ? AND id <> ?`, userID, keepID)
}

// CleanupExpired 清理过期会话与陈旧的限速记录。
func (s *Service) CleanupExpired(ctx context.Context) {
	now := s.now().UnixMilli()
	_, _ = s.st.Exec(ctx, `DELETE FROM sessions WHERE expires_at < ?`, now)
	_, _ = s.st.Exec(ctx, `DELETE FROM login_attempts WHERE locked_until < ? AND last_at < ?`, now, now-7*24*3600*1000)
}

// ---------- 新设备 ----------

// SeenDevice 记录登录设备；返回是否是新设备（首次登录不算新设备）。
func (s *Service) SeenDevice(ctx context.Context, u *accounts.User, ip, ua string) bool {
	fp := hashToken(ipPrefix(ip) + "|" + uaFamily(ua))[:32]
	now := s.now().UnixMilli()
	var n int
	_ = s.st.GetW(ctx, &n, `SELECT COUNT(*) FROM known_devices WHERE user_id = ?`, u.ID)
	res, _ := s.st.Exec(ctx, `UPDATE known_devices SET last_at = ?, ip = ? WHERE user_id = ? AND fingerprint = ?`, now, ip, u.ID, fp)
	if res != nil {
		if k, _ := res.RowsAffected(); k > 0 {
			return false
		}
	}
	_, _ = s.st.Exec(ctx, `INSERT OR IGNORE INTO known_devices (user_id, fingerprint, ip, ua, first_at, last_at) VALUES (?,?,?,?,?,?)`, u.ID, fp, ip, truncate(ua, 300), now, now)
	return n > 0
}

func ipPrefix(ip string) string {
	if i := strings.LastIndex(ip, "."); i > 0 && strings.Count(ip, ".") == 3 {
		return ip[:i] // IPv4 /24
	}
	parts := strings.Split(ip, ":")
	if len(parts) > 4 {
		return strings.Join(parts[:4], ":") // IPv6 /64
	}
	return ip
}

func uaFamily(ua string) string {
	ua = strings.ToLower(ua)
	os := "other"
	for _, k := range []string{"windows", "mac os", "iphone", "ipad", "android", "linux"} {
		if strings.Contains(ua, k) {
			os = k
			break
		}
	}
	br := "other"
	for _, k := range []string{"edg/", "chrome/", "firefox/", "safari/"} {
		if strings.Contains(ua, k) {
			br = k
			break
		}
	}
	return os + "/" + br
}

// ---------- 两步验证绑定 ----------

// BeginTOTP 生成新的密钥（暂存为未启用），返回明文密钥与 otpauth 链接。
func (s *Service) BeginTOTP(ctx context.Context, u *accounts.User) (string, string, error) {
	secret := NewTOTPSecret()
	sealed := s.box.Seal(secret, crypt.AAD("users", u.ID, "totp_secret"))
	if _, err := s.st.Exec(ctx, `UPDATE users SET totp_secret = ?, totp_enabled = 0, totp_last_step = 0, updated_at = ? WHERE id = ?`, sealed, store.Now(), u.ID); err != nil {
		return "", "", err
	}
	u.TOTPSecret, u.TOTPEnabled = sealed, false
	return secret, TOTPURL("Lemi", u.Username, secret), nil
}

// ConfirmTOTP 用第一个验证码确认绑定，生成恢复码。
func (s *Service) ConfirmTOTP(ctx context.Context, u *accounts.User, code, ip string) ([]string, error) {
	if err := s.checkLock(ctx, "mfa:"+u.ID, "ip:"+ip); err != nil {
		return nil, err
	}
	secret, err := s.box.Open(u.TOTPSecret, crypt.AAD("users", u.ID, "totp_secret"))
	if err != nil || secret == "" {
		return nil, errors.New("请先生成两步验证密钥")
	}
	step, ok := MatchTOTP(secret, code, 0, s.now())
	if !ok {
		s.recordFailure(ctx, "mfa:"+u.ID, "ip:"+ip)
		return nil, ErrBadCode
	}
	plain, hashes := NewRecoveryCodes()
	b, _ := json.Marshal(hashes)
	if _, err := s.st.Exec(ctx, `UPDATE users SET totp_enabled = 1, totp_last_step = ?, recovery_codes = ?, updated_at = ? WHERE id = ?`, step, string(b), store.Now(), u.ID); err != nil {
		return nil, err
	}
	u.TOTPEnabled, u.TOTPLastStep = true, step
	return plain, nil
}

// ResetTOTP 清除两步验证（管理员重置或用户关闭）。
func (s *Service) ResetTOTP(ctx context.Context, userID string) error {
	_, err := s.st.Exec(ctx, `UPDATE users SET totp_secret = '', totp_enabled = 0, totp_last_step = 0, recovery_codes = '[]', updated_at = ? WHERE id = ?`, store.Now(), userID)
	return err
}

// RegenerateRecovery 重新生成恢复码。
func (s *Service) RegenerateRecovery(ctx context.Context, u *accounts.User) ([]string, error) {
	plain, hashes := NewRecoveryCodes()
	b, _ := json.Marshal(hashes)
	_, err := s.st.Exec(ctx, `UPDATE users SET recovery_codes = ?, updated_at = ? WHERE id = ?`, string(b), store.Now(), u.ID)
	return plain, err
}

// RecoveryLeft 剩余恢复码数量。
func RecoveryLeft(u *accounts.User) int {
	var hashes []string
	_ = json.Unmarshal([]byte(u.RecoveryCodes), &hashes)
	return len(hashes)
}

// SetPassword 改口令（校验强度），吊销其他会话。
func (s *Service) SetPassword(ctx context.Context, u *accounts.User, pw string, keepSession string) error {
	if err := CheckPasswordPolicy(pw, u.Username); err != nil {
		return err
	}
	h, err := HashPassword(pw)
	if err != nil {
		return err
	}
	now := store.Now()
	if _, err := s.st.Exec(ctx, `UPDATE users SET password_hash = ?, password_changed_at = ?, must_change_password = 0, updated_at = ? WHERE id = ?`, h, now, now, u.ID); err != nil {
		return err
	}
	u.PasswordHash, u.MustChangePassword = h, false
	s.RevokeOthers(ctx, u.ID, keepSession)
	return nil
}
