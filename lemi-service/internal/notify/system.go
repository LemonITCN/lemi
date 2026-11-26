package notify

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
	"github.com/LemonITCN/lemi/lemi-service/internal/config"
	"github.com/LemonITCN/lemi/lemi-service/internal/crypt"
)

// 实例级「系统邮件服务器」：存在 settings 表的 notify.smtp 键里，口令用主密钥加密。
// 只有实例管理员能改，所以它允许连接内网地址（仍拒绝云元数据地址）。

const smtpSettingsKey = "notify.smtp"

func smtpPasswordAAD() string { return crypt.AAD("settings", smtpSettingsKey, "password") }

// SMTPInput 是系统邮件服务器的设置请求（口令只写不读：不传表示保持原值）。
type SMTPInput struct {
	Host     string   `json:"host"`
	Port     int      `json:"port"`
	TLS      string   `json:"tls"`
	Username string   `json:"username"`
	Password string   `json:"password"`
	From     string   `json:"from"`
	Clear    []string `json:"clear"` // ["password"] 清除口令
}

// systemSMTPRaw 读保存的配置（口令为密文）；exists 表示设置项存在（哪怕主机为空，表示管理员清空过）。
func (s *Service) systemSMTPRaw(ctx context.Context) (srv smtpServer, exists bool, err error) {
	exists, err = s.set.GetJSON(ctx, smtpSettingsKey, &srv)
	return srv, exists, err
}

// systemSMTP 读系统邮件服务器（口令已解密）；没有配置时 ok 为假。
func (s *Service) systemSMTP(ctx context.Context) (smtpServer, bool, error) {
	srv, _, err := s.systemSMTPRaw(ctx)
	if err != nil {
		return smtpServer{}, false, err
	}
	if srv.Host == "" {
		return smtpServer{}, false, nil
	}
	if srv.Password != "" {
		p, err := s.box.Open(srv.Password, smtpPasswordAAD())
		if err != nil {
			return smtpServer{}, false, permanent(errors.New("系统邮件服务器的口令无法解密（主密钥不匹配或数据被篡改），请重新填写"))
		}
		srv.Password = p
	}
	srv.normalize()
	return srv, true, nil
}

// SystemSMTPConfigured 系统邮件服务器是否已配置。
func (s *Service) SystemSMTPConfigured(ctx context.Context) bool {
	srv, _, err := s.systemSMTPRaw(ctx)
	return err == nil && srv.Host != ""
}

// SystemSMTPView 给管理员看的系统邮件服务器设置（不含口令）。
func (s *Service) SystemSMTPView(ctx context.Context) (map[string]any, error) {
	srv, _, err := s.systemSMTPRaw(ctx)
	if err != nil {
		return nil, err
	}
	set := []string{}
	if srv.Password != "" {
		set = append(set, "password")
	}
	out := map[string]any{"configured": srv.Host != "", "host": srv.Host, "port": srv.Port, "tls": srv.TLS,
		"username": srv.Username, "from": srv.From, "secrets_set": set}
	return out, nil
}

// prepareSystemSMTP 校验输入并合并已保存的口令，返回要保存的配置（口令为密文）。
// 主机或端口变了，已保存的口令不能沿用，必须重新输入或显式清除。
func (s *Service) prepareSystemSMTP(ctx context.Context, in SMTPInput) (smtpServer, error) {
	old, _, err := s.systemSMTPRaw(ctx)
	if err != nil {
		return smtpServer{}, err
	}
	srv := smtpServer{Host: in.Host, Port: in.Port, TLS: in.TLS, Username: in.Username, From: in.From}
	srv.normalize()
	if srv.Host == "" {
		return smtpServer{}, nil // 清空 = 不使用系统邮件服务器
	}
	if err := srv.validate(); err != nil {
		return smtpServer{}, err
	}
	clear := false
	for _, c := range in.Clear {
		if c == "password" {
			clear = true
		}
	}
	switch {
	case in.Password != "":
		if crypt.IsSealed(in.Password) {
			return smtpServer{}, apperr.Bad("口令格式不正确")
		}
		srv.Password = s.box.Seal(in.Password, smtpPasswordAAD())
	case clear || old.Password == "":
	default:
		o := old
		o.normalize()
		if !strings.EqualFold(o.Host, srv.Host) || o.Port != srv.Port {
			return smtpServer{}, apperr.Bad("SMTP 主机或端口改了，请重新输入口令（或清除它）")
		}
		srv.Password = old.Password
	}
	return srv, nil
}

// SetSystemSMTP 保存系统邮件服务器设置。
func (s *Service) SetSystemSMTP(ctx context.Context, in SMTPInput) error {
	srv, err := s.prepareSystemSMTP(ctx, in)
	if err != nil {
		return err
	}
	return s.set.SetJSON(ctx, smtpSettingsKey, srv)
}

// SeedSystemSMTP 首次启动写入（LEMI_SMTP_*）：只在设置项从未保存过时写入，之后以界面为准，重启不覆盖。
func (s *Service) SeedSystemSMTP(ctx context.Context, c config.SMTP) error {
	if strings.TrimSpace(c.Host) == "" {
		return nil
	}
	_, exists, err := s.systemSMTPRaw(ctx)
	if err != nil || exists {
		return err
	}
	in := SMTPInput{Host: c.Host, TLS: c.TLS, Username: c.Username, Password: c.Password, From: c.From}
	if c.Port != "" {
		p, err := strconv.Atoi(strings.TrimSpace(c.Port))
		if err != nil {
			return fmt.Errorf("LEMI_SMTP_PORT 不是数字")
		}
		in.Port = p
	}
	if in.From == "" && strings.Contains(in.Username, "@") {
		in.From = in.Username
	}
	if err := s.SetSystemSMTP(ctx, in); err != nil {
		return fmt.Errorf("LEMI_SMTP_* 配置不正确：%w", err)
	}
	return nil
}

// SendSystemMail 用系统邮件服务器发一封纯文本邮件（同步，不重试；找回口令、邀请等以后也用它）。
func (s *Service) SendSystemMail(ctx context.Context, to []string, subject, body string) error {
	srv, ok, err := s.systemSMTP(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return permanent(errors.New("实例管理员还没有配置系统邮件服务器"))
	}
	ctx, cancel := context.WithTimeout(ctx, SendTimeout)
	defer cancel()
	return sendMail(ctx, s.SystemEgress, srv, mailMessage{To: to, Subject: subject, Body: body})
}

// TestSystemSMTP 用表单内容（in 为 nil 时用已保存的设置）给 to 发一封测试邮件。
func (s *Service) TestSystemSMTP(ctx context.Context, in *SMTPInput, to string) error {
	var srv smtpServer
	if in != nil {
		saved, err := s.prepareSystemSMTP(ctx, *in)
		if err != nil {
			return err
		}
		if saved.Host == "" {
			return apperr.Bad("请填写 SMTP 主机")
		}
		if saved.Password != "" {
			if saved.Password, err = s.box.Open(saved.Password, smtpPasswordAAD()); err != nil {
				return apperr.Bad("已保存的口令无法解密，请重新填写")
			}
		}
		srv = saved
	} else {
		var ok bool
		var err error
		if srv, ok, err = s.systemSMTP(ctx); err != nil {
			return err
		} else if !ok {
			return apperr.Bad("还没有配置系统邮件服务器")
		}
	}
	if _, err := parseAddress(to); err != nil {
		return apperr.Bad("收件地址不正确")
	}
	ctx, cancel := context.WithTimeout(ctx, SendTimeout)
	defer cancel()
	subject, body := emailContent(Event{Kind: EvTest, Level: LevelInfo, Title: "Lemi 测试邮件",
		Message: "这是一封来自 Lemi 的测试邮件，收到说明系统邮件服务器配置正确。\nThis is a test email from Lemi."}, s.now())
	return sendMail(ctx, s.SystemEgress, srv, mailMessage{To: []string{to}, Subject: subject, Body: body})
}

// ---------- 安全事件 ----------

// securityText 各安全事件的邮件标题、中文说明与英文一行。
var securityText = map[string][3]string{
	"account_locked":   {"账号因多次登录失败被暂时锁定", "你的 Lemi 账号连续多次输错口令，已被暂时锁定。锁定期间无法登录，到期后自动解除。", "Your Lemi account was temporarily locked after repeated failed sign-in attempts."},
	"new_device_login": {"新设备登录提醒", "你的 Lemi 账号在一台新的设备或新的网络地址上登录。", "Your Lemi account was signed in from a new device or network address."},
	"password_changed": {"登录口令已修改", "你的 Lemi 账号登录口令刚刚被修改，其他设备上的登录已失效。", "The password of your Lemi account was just changed."},
	"totp_disabled":    {"两步验证已关闭", "你的 Lemi 账号刚刚关闭了两步验证，账号安全性降低。", "Two-factor authentication was just disabled on your Lemi account."},
}

// SecurityEvent 安全事件回调（设置给 auth.Service.OnEvent）：异步用系统邮件服务器给用户本人发邮件，不阻塞登录。
// 用户没有邮箱或没有配置系统邮件服务器时只记日志。邮件里只有时间、IP、设备，不含任何令牌。
func (s *Service) SecurityEvent(_ context.Context, kind string, u *accounts.User, ip, ua string) {
	if u == nil || s.ctx.Err() != nil {
		return
	}
	user := *u
	at := s.now()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.sendSecurityMail(kind, &user, ip, ua, at)
	}()
}

func (s *Service) sendSecurityMail(kind string, u *accounts.User, ip, ua string, at time.Time) {
	txt, ok := securityText[kind]
	if !ok {
		txt = [3]string{"账号安全提醒", "你的 Lemi 账号发生了一个安全相关的操作（" + kind + "）。", "A security-related event (" + kind + ") occurred on your Lemi account."}
	}
	if strings.TrimSpace(u.Email) == "" {
		s.log.Info("安全事件：用户没有填写邮箱，只记日志", "event", kind, "user", u.Username, "ip", ip)
		return
	}
	if !s.SystemSMTPConfigured(context.Background()) {
		s.log.Info("安全事件：没有配置系统邮件服务器，只记日志", "event", kind, "user", u.Username, "ip", ip)
		return
	}
	ua = truncRunes(oneLine(ua), 200)
	if ua == "" {
		ua = "（未知）"
	}
	when := at.Format("2006-01-02 15:04:05 -0700")
	var b strings.Builder
	b.WriteString(u.Name() + "，你好：\n\n")
	b.WriteString(txt[1] + "\n\n")
	b.WriteString("时间：" + when + "\n")
	b.WriteString("IP：" + oneLine(ip) + "\n")
	b.WriteString("设备：" + ua + "\n\n")
	b.WriteString("如果这不是你本人的操作，请尽快修改口令，并联系实例管理员。\n\n")
	b.WriteString(txt[2] + " Time: " + when + ", IP: " + oneLine(ip) + ". If this wasn't you, change your password and contact your administrator.\n")
	if s.publicURL != "" {
		b.WriteString("\n-- \nLemi " + s.publicURL + "\n")
	} else {
		b.WriteString("\n-- \nLemi\n")
	}
	ev := Event{Kind: EvSecurity, Level: LevelWarning, Title: txt[0]}
	logID := s.logStart("", "", "系统邮件服务器", KindEmail, ev)
	attempts := 0
	var err error
	for i := 0; ; i++ {
		attempts++
		err = s.SendSystemMail(s.ctx, []string{u.Email}, "[Lemi] "+txt[0], b.String())
		if err == nil || isPermanent(err) || i >= len(RetryDelays) || s.ctx.Err() != nil {
			break
		}
		s.logProgress(logID, attempts, err)
		select {
		case <-time.After(RetryDelays[i]):
		case <-s.ctx.Done():
		}
	}
	s.logFinish(logID, attempts, err)
	if err != nil {
		s.log.Warn("安全事件邮件发送失败", "event", kind, "user", u.Username, "attempts", attempts, "err", err)
	}
}

// auditDetail 设置变更审计用的摘要（不含口令）。
func (srv smtpServer) auditDetail() map[string]any {
	return map[string]any{"host": srv.Host, "port": srv.Port, "tls": srv.TLS, "username": srv.Username, "from": srv.From, "password_set": srv.Password != ""}
}
