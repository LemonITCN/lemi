package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
	"github.com/LemonITCN/lemi/lemi-service/internal/ids"
	"github.com/LemonITCN/lemi/lemi-service/internal/netsafe"
)

// smtpRootCAs 校验 SMTP 服务器证书用的根证书（nil 为系统根证书；测试里换成自签证书）。
var smtpRootCAs *x509.CertPool

// smtpServer 是一个 SMTP 服务器的配置（口令为明文，只在内存里）。
type smtpServer struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	TLS      string `json:"tls"` // ssl | starttls | none
	Username string `json:"username"`
	Password string `json:"password"`
	From     string `json:"from"`
}

func smtpServerFrom(m map[string]any) smtpServer {
	str := func(k string) string { v, _ := m[k].(string); return v }
	srv := smtpServer{Host: str("host"), TLS: str("tls"), Username: str("username"), Password: str("password"), From: str("from")}
	switch v := m["port"].(type) {
	case float64:
		srv.Port = int(v)
	case int64:
		srv.Port = int(v)
	case int:
		srv.Port = v
	case json.Number:
		n, _ := v.Int64()
		srv.Port = int(n)
	case string:
		srv.Port, _ = strconv.Atoi(v)
	}
	srv.normalize()
	return srv
}

func (srv *smtpServer) normalize() {
	srv.Host = strings.TrimSpace(srv.Host)
	srv.TLS = strings.ToLower(strings.TrimSpace(srv.TLS))
	if srv.TLS == "" {
		switch srv.Port {
		case 465:
			srv.TLS = "ssl"
		case 25:
			srv.TLS = "none"
		default:
			srv.TLS = "starttls"
		}
	}
	if srv.Port == 0 {
		switch srv.TLS {
		case "ssl":
			srv.Port = 465
		case "none":
			srv.Port = 25
		default:
			srv.Port = 587
		}
	}
	srv.Username = strings.TrimSpace(srv.Username)
	srv.From = strings.TrimSpace(srv.From)
}

// validate 校验（口令可能是密文，只看有没有）。
func (srv smtpServer) validate() error {
	if srv.Host == "" || strings.ContainsAny(srv.Host, " /\\\r\n") || len(srv.Host) > 253 {
		return apperr.Bad("SMTP 主机不正确")
	}
	if srv.Port <= 0 || srv.Port > 65535 {
		return apperr.Bad("SMTP 端口不正确")
	}
	switch srv.TLS {
	case "ssl", "starttls", "none":
	default:
		return apperr.Bad("加密方式只能是 ssl、starttls 或 none")
	}
	if srv.TLS == "none" && srv.Username != "" {
		return apperr.Bad("不加密的连接不能发送口令：请把加密方式改为 SSL/TLS 或 STARTTLS，或者不填用户名")
	}
	if srv.From == "" {
		return apperr.Bad("请填写发件人")
	}
	if _, err := parseAddress(srv.From); err != nil {
		return apperr.Bad("发件人地址不正确")
	}
	return nil
}

// mailMessage 是一封纯文本邮件。
type mailMessage struct {
	To      []string
	Subject string
	Body    string
}

// buildMessage 生成 RFC 5322 邮件：头部按 RFC 2047 编码（去掉换行防止头注入），正文 UTF-8 + base64。
func buildMessage(from *mail.Address, to []*mail.Address, subject, body string, now time.Time) []byte {
	var b bytes.Buffer
	hdr := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	hdr("From", from.String())
	tos := make([]string, len(to))
	for i, a := range to {
		tos[i] = a.String()
	}
	hdr("To", strings.Join(tos, ", "))
	hdr("Subject", mime.BEncoding.Encode("UTF-8", oneLine(subject)))
	hdr("Date", now.Format(time.RFC1123Z))
	domain := "lemi.local"
	if i := strings.LastIndex(from.Address, "@"); i >= 0 {
		domain = from.Address[i+1:]
	}
	hdr("Message-ID", "<"+ids.New()+"@"+domain+">")
	hdr("MIME-Version", "1.0")
	hdr("Content-Type", "text/plain; charset=UTF-8")
	hdr("Content-Transfer-Encoding", "base64")
	hdr("Auto-Submitted", "auto-generated")
	b.WriteString("\r\n")
	enc := base64.StdEncoding.EncodeToString([]byte(strings.ReplaceAll(body, "\n", "\r\n")))
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\r\n")
	return b.Bytes()
}

// heloName 是 EHLO 里报的主机名。
func heloName() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "localhost"
	}
	for _, r := range h {
		if !(r == '-' || r == '.' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return "localhost"
		}
	}
	return h
}

// sendMail 经出站检查连接 SMTP 服务器发信。整个过程受 ctx 限制。
func sendMail(ctx context.Context, eg netsafe.Egress, srv smtpServer, m mailMessage) error {
	if err := srv.validate(); err != nil {
		return permanent(err)
	}
	from, _ := parseAddress(srv.From)
	var to []*mail.Address
	for _, r := range m.To {
		a, err := parseAddress(r)
		if err != nil {
			return permanent(fmt.Errorf("收件人地址不正确：%s", truncRunes(r, 80)))
		}
		to = append(to, a)
	}
	if len(to) == 0 {
		return permanent(errors.New("没有收件人"))
	}
	conn, err := eg.DialContext(ctx, "tcp", net.JoinHostPort(srv.Host, strconv.Itoa(srv.Port)))
	if err != nil {
		if errors.Is(err, netsafe.ErrBlocked) {
			return permanent(fmt.Errorf("SMTP 服务器地址被拦截：%w", err))
		}
		return fmt.Errorf("连接 SMTP 服务器失败：%v", err)
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(SendTimeout)
	}
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	defer conn.Close()

	tlsCfg := &tls.Config{ServerName: srv.Host, MinVersion: tls.VersionTLS12, RootCAs: smtpRootCAs}
	if srv.TLS == "ssl" {
		tc := tls.Client(conn, tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			return fmt.Errorf("SMTP TLS 握手失败：%v", err)
		}
		conn = tc
	}
	c, err := smtp.NewClient(conn, srv.Host)
	if err != nil {
		return fmt.Errorf("SMTP 服务器没有正确响应：%v", err)
	}
	defer c.Close()
	if err := c.Hello(heloName()); err != nil {
		return fmt.Errorf("SMTP 握手失败：%v", err)
	}
	if srv.TLS == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return permanent(errors.New("SMTP 服务器不支持 STARTTLS，请改用 SSL/TLS"))
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("STARTTLS 失败：%v", err)
		}
	}
	if srv.Username != "" {
		ok, mechs := c.Extension("AUTH")
		if !ok {
			return permanent(errors.New("SMTP 服务器不支持登录认证，请不填用户名口令"))
		}
		var auth smtp.Auth
		switch {
		case strings.Contains(" "+strings.ToUpper(mechs)+" ", " PLAIN "):
			auth = smtp.PlainAuth("", srv.Username, srv.Password, srv.Host)
		case strings.Contains(" "+strings.ToUpper(mechs)+" ", " LOGIN "):
			auth = &loginAuth{user: srv.Username, pass: srv.Password, host: srv.Host}
		default:
			return permanent(fmt.Errorf("SMTP 服务器只支持 %s 认证，Lemi 支持 PLAIN 与 LOGIN", truncRunes(mechs, 60)))
		}
		if err := c.Auth(auth); err != nil {
			return permanent(fmt.Errorf("SMTP 登录失败：%v", err))
		}
	}
	if err := c.Mail(from.Address); err != nil {
		return fmt.Errorf("SMTP 拒绝发件人：%v", err)
	}
	for _, a := range to {
		if err := c.Rcpt(a.Address); err != nil {
			return fmt.Errorf("SMTP 拒绝收件人 %s：%v", a.Address, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("SMTP 发送正文失败：%v", err)
	}
	if _, err := w.Write(buildMessage(from, to, m.Subject, m.Body, time.Now())); err != nil {
		return fmt.Errorf("SMTP 发送正文失败：%v", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("SMTP 服务器拒收：%v", err)
	}
	_ = c.Quit()
	return nil
}

// loginAuth 是 AUTH LOGIN（部分邮件服务只支持它）。与 smtp.PlainAuth 一样，只在 TLS 连接或本机上发送口令。
type loginAuth struct{ user, pass, host string }

func (a *loginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if !server.TLS && !isLocalhost(server.Name) {
		return "", nil, errors.New("未加密的连接不能发送口令")
	}
	if server.Name != a.host {
		return "", nil, errors.New("服务器名称不一致")
	}
	return "LOGIN", nil, nil
}

func (a *loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	p := strings.ToLower(strings.TrimSpace(string(fromServer)))
	switch {
	case strings.Contains(p, "username"), strings.Contains(p, "user"):
		return []byte(a.user), nil
	case strings.Contains(p, "password"), strings.Contains(p, "pass"):
		return []byte(a.pass), nil
	}
	return nil, fmt.Errorf("无法识别的登录提示：%s", truncRunes(p, 40))
}

func isLocalhost(name string) bool {
	return name == "localhost" || name == "127.0.0.1" || name == "::1"
}

// ---------- 渠道：邮件 ----------

// emailContent 把事件排成邮件标题与正文。
func emailContent(ev Event, now time.Time) (string, string) {
	subject := "[Lemi] " + ev.Title
	var b strings.Builder
	b.WriteString(ev.Title + "\n\n")
	if ev.Message != "" {
		b.WriteString(ev.Message + "\n\n")
	}
	b.WriteString("级别：" + levelName(ev.Level) + "\n")
	b.WriteString("事件：" + eventName(ev.Kind) + "\n")
	b.WriteString("时间：" + now.Format("2006-01-02 15:04:05 -0700") + "\n")
	if ev.Link != "" {
		b.WriteString("查看：" + ev.Link + "\n")
	}
	b.WriteString("\n-- \nLemi\n")
	return subject, b.String()
}

func (s *Service) sendEmailChannel(ctx context.Context, cfg map[string]any, ev Event) error {
	var to []string
	list, _ := cfg["recipients"].([]any)
	for _, it := range list {
		to = append(to, fmt.Sprint(it))
	}
	subject, body := emailContent(ev, s.now())
	msg := mailMessage{To: to, Subject: subject, Body: body}
	if use, ok := cfg["use_system"].(bool); !ok || use {
		srv, ok, err := s.systemSMTP(ctx)
		if err != nil {
			return err
		}
		if !ok {
			return permanent(errors.New("实例管理员还没有配置系统邮件服务器"))
		}
		return sendMail(ctx, s.SystemEgress, srv, msg)
	}
	return sendMail(ctx, s.Egress, smtpServerFrom(cfg), msg)
}
