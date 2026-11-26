package notify

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"fmt"
	"io"
	"math/big"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/config"
)

// 测试里写的进程内最小 SMTP 服务器：支持 none / STARTTLS / SSL，AUTH PLAIN 与 LOGIN。

type smtpMsg struct {
	From   string
	To     []string
	Data   string
	Auth   string // 解码后的 PLAIN 凭据（\x00user\x00pass）或 LOGIN 的 user:pass
	TLS    bool
	Helo   string
	header mail.Header
	body   string
}

type testSMTP struct {
	ln    net.Listener
	mode  string // none | starttls | ssl
	mechs string // EHLO 里通告的 AUTH 机制，空为 "LOGIN PLAIN"（读写都持 mu）
	tls   *tls.Config
	mu    sync.Mutex
	msgs  []smtpMsg
	port  int
}

func testCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "lemi test smtp"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

func startSMTP(t *testing.T, mode string) *testSMTP {
	t.Helper()
	cert, pool := testCert(t)
	old := smtpRootCAs
	smtpRootCAs = pool
	t.Cleanup(func() { smtpRootCAs = old })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	m := &testSMTP{ln: ln, mode: mode, tls: &tls.Config{Certificates: []tls.Certificate{cert}}, port: ln.Addr().(*net.TCPAddr).Port}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go m.serve(c)
		}
	}()
	return m
}

func (m *testSMTP) messages() []smtpMsg {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]smtpMsg(nil), m.msgs...)
}

func (m *testSMTP) serve(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	tlsOn := false
	if m.mode == "ssl" {
		tc := tls.Server(conn, m.tls)
		if tc.Handshake() != nil {
			return
		}
		conn, tlsOn = tc, true
	}
	r := bufio.NewReader(conn)
	write := func(s string) { fmt.Fprintf(conn, "%s\r\n", s) }
	readLine := func() (string, bool) {
		l, err := r.ReadString('\n')
		if err != nil {
			return "", false
		}
		return strings.TrimRight(l, "\r\n"), true
	}
	write("220 lemi-test ESMTP")
	var msg smtpMsg
	for {
		line, ok := readLine()
		if !ok {
			return
		}
		cmd := strings.ToUpper(strings.SplitN(line, " ", 2)[0])
		switch cmd {
		case "EHLO", "HELO":
			msg.Helo = strings.TrimSpace(line[4:])
			write("250-lemi-test")
			if m.mode == "starttls" && !tlsOn {
				write("250-STARTTLS")
			}
			if tlsOn {
				m.mu.Lock()
				mechs := m.mechs
				m.mu.Unlock()
				if mechs == "" {
					mechs = "LOGIN PLAIN"
				}
				write("250-AUTH " + mechs)
			}
			write("250 8BITMIME")
		case "STARTTLS":
			write("220 go ahead")
			tc := tls.Server(conn, m.tls)
			if tc.Handshake() != nil {
				return
			}
			conn, tlsOn = tc, true
			r = bufio.NewReader(tc)
		case "AUTH":
			parts := strings.Fields(line)
			if len(parts) >= 3 && strings.EqualFold(parts[1], "PLAIN") {
				b, _ := base64.StdEncoding.DecodeString(parts[2])
				msg.Auth = string(b)
				write("235 ok")
			} else if len(parts) >= 2 && strings.EqualFold(parts[1], "LOGIN") {
				write("334 " + base64.StdEncoding.EncodeToString([]byte("Username:")))
				u, _ := readLine()
				write("334 " + base64.StdEncoding.EncodeToString([]byte("Password:")))
				p, _ := readLine()
				ub, _ := base64.StdEncoding.DecodeString(u)
				pb, _ := base64.StdEncoding.DecodeString(p)
				msg.Auth = string(ub) + ":" + string(pb)
				write("235 ok")
			} else {
				write("504 unsupported")
			}
		case "MAIL":
			from := line[len("MAIL FROM:"):]
			if i, j := strings.Index(from, "<"), strings.Index(from, ">"); i >= 0 && j > i {
				from = from[i+1 : j]
			}
			msg.From = from
			write("250 ok")
		case "RCPT":
			msg.To = append(msg.To, strings.Trim(line[len("RCPT TO:"):], "<> "))
			write("250 ok")
		case "DATA":
			write("354 go ahead")
			var b strings.Builder
			for {
				l, ok := readLine()
				if !ok {
					return
				}
				if l == "." {
					break
				}
				b.WriteString(strings.TrimPrefix(l, ".") + "\r\n")
			}
			msg.Data, msg.TLS = b.String(), tlsOn
			m.mu.Lock()
			m.msgs = append(m.msgs, msg)
			m.mu.Unlock()
			msg = smtpMsg{Helo: msg.Helo}
			write("250 queued")
		case "RSET", "NOOP":
			write("250 ok")
		case "QUIT":
			write("221 bye")
			return
		default:
			write("502 unknown")
		}
	}
}

// parse 解析收到的邮件：标题按 RFC 2047 解码，正文 base64 解码。
func (msg *smtpMsg) parse(t *testing.T) (subject, body string, h mail.Header) {
	t.Helper()
	pm, err := mail.ReadMessage(strings.NewReader(msg.Data))
	if err != nil {
		t.Fatalf("邮件格式不正确：%v\n%s", err, msg.Data)
	}
	dec := new(mime.WordDecoder)
	subject, err = dec.DecodeHeader(pm.Header.Get("Subject"))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(pm.Body)
	b, err := base64.StdEncoding.DecodeString(strings.NewReplacer("\r", "", "\n", "").Replace(string(raw)))
	if err != nil {
		t.Fatalf("正文不是 base64：%v", err)
	}
	return subject, strings.ReplaceAll(string(b), "\r\n", "\n"), pm.Header
}

func TestEmailChannelPlainSMTP(t *testing.T) {
	f := setup(t)
	f.allowPrivate(t, true)
	m := startSMTP(t, "none")
	c := f.create(t, KindEmail, map[string]any{"recipients": "ops@example.com\nboss@example.com", "use_system": false,
		"host": "127.0.0.1", "port": m.port, "tls": "none", "from": "Lemi 通知 <noreply@example.com>"})
	f.s.Send(context.Background(), f.ws, []string{c.ID}, trickyEvent)
	f.s.wait()
	msgs := m.messages()
	if len(msgs) != 1 {
		t.Fatalf("应收到 1 封邮件，实际 %d；记录 %+v", len(msgs), f.logs(t))
	}
	msg := msgs[0]
	if msg.From != "noreply@example.com" || len(msg.To) != 2 || msg.To[0] != "ops@example.com" || msg.TLS {
		t.Fatalf("信封不对：%+v", msg)
	}
	subject, body, h := msg.parse(t)
	if subject != "[Lemi] "+trickyEvent.Title {
		t.Fatalf("标题不对：%q", subject)
	}
	if !strings.Contains(body, "第二行") || !strings.Contains(body, "级别：错误") || !strings.Contains(body, "https://lemi.example.com/w/ws1/backups?x=1&y=2") {
		t.Fatalf("正文不对：%s", body)
	}
	from, _ := mail.ParseAddress(h.Get("From"))
	if from == nil || from.Name != "Lemi 通知" || !strings.HasPrefix(h.Get("Content-Type"), "text/plain") {
		t.Fatalf("头部不对：%v", h)
	}
	if l := f.logs(t)[0]; l.Status != "ok" || l.Kind != KindEmail {
		t.Fatalf("发送记录不对：%+v", l)
	}
	// 标题里的换行不能注入头部
	f.s.Send(context.Background(), f.ws, []string{c.ID}, Event{Title: "a\r\nBcc: evil@example.com", Message: "x"})
	f.s.wait()
	msgs = m.messages()
	if len(msgs) != 2 {
		t.Fatal("应收到第二封")
	}
	if _, _, h := msgs[1].parse(t); h.Get("Bcc") != "" {
		t.Fatal("标题换行注入了头部")
	}
}

func TestSystemSMTPStartTLSAndSecurityEvent(t *testing.T) {
	f := setup(t)
	f.allowPrivate(t, false) // 系统邮件服务器只有实例管理员能改，允许内网，不受这个开关影响
	m := startSMTP(t, "starttls")
	ctx := context.Background()
	// 首次启动写入
	if err := f.s.SeedSystemSMTP(ctx, config.SMTP{Host: "127.0.0.1", Port: fmt.Sprint(m.port), Username: "lemi", Password: "p@ss word", From: "Lemi <noreply@example.com>", TLS: "starttls"}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SeedSystemSMTP(ctx, config.SMTP{Host: "other.example.com"}); err != nil {
		t.Fatal(err)
	}
	v, _ := f.s.SystemSMTPView(ctx)
	if v["host"] != "127.0.0.1" || v["configured"] != true {
		t.Fatalf("首次写入后不应被覆盖：%v", v)
	}
	if set, _ := v["secrets_set"].([]string); len(set) != 1 {
		t.Fatalf("口令应显示为已设置：%v", v)
	}
	var raw string
	_ = f.st.Get(ctx, &raw, `SELECT value FROM settings WHERE key = 'notify.smtp'`)
	if strings.Contains(raw, "p@ss word") {
		t.Fatal("系统邮件服务器口令应加密保存")
	}
	// 渠道用系统邮件服务器
	c := f.create(t, KindEmail, map[string]any{"recipients": []any{"team@example.com"}})
	f.s.Send(ctx, f.ws, []string{c.ID}, Event{Kind: EvBackupSuccess, Title: "备份成功", Message: "全部完成"})
	f.s.wait()
	msgs := m.messages()
	if len(msgs) != 1 || !msgs[0].TLS || msgs[0].Auth != "\x00lemi\x00p@ss word" || msgs[0].To[0] != "team@example.com" {
		t.Fatalf("STARTTLS + AUTH PLAIN 不对：%+v；记录 %+v", msgs, f.logs(t))
	}
	if subj, _, _ := msgs[0].parse(t); subj != "[Lemi] 备份成功" {
		t.Fatalf("标题不对：%q", subj)
	}
	// 管理员测试发送（已保存的设置）
	if err := f.s.TestSystemSMTP(ctx, nil, "admin@example.com"); err != nil {
		t.Fatal(err)
	}
	// 安全事件：给用户本人发邮件，异步
	u := &accounts.User{ID: "u1", Username: "alice", Email: "alice@example.com"}
	f.s.SecurityEvent(ctx, "new_device_login", u, "203.0.113.9", "Mozilla/5.0 Test\r\nInjected: 1")
	f.s.wait()
	msgs = m.messages()
	if len(msgs) != 3 || msgs[2].To[0] != "alice@example.com" {
		t.Fatalf("安全事件邮件没发给用户本人：%+v", msgs)
	}
	subj, body, _ := msgs[2].parse(t)
	if !strings.Contains(subj, "新设备登录") || !strings.Contains(body, "203.0.113.9") || !strings.Contains(body, "Mozilla/5.0 Test Injected: 1") ||
		!strings.Contains(body, "new device") || !strings.Contains(body, "时间：") {
		t.Fatalf("安全事件邮件内容不对：%s / %s", subj, body)
	}
	// 没有邮箱的用户只记日志，不发
	f.s.SecurityEvent(ctx, "password_changed", &accounts.User{ID: "u2", Username: "bob"}, "1.1.1.1", "ua")
	f.s.wait()
	if len(m.messages()) != 3 {
		t.Fatal("没有邮箱时不应发邮件")
	}
	// 改端口不重输口令应拒绝；清除口令后可以保存
	err := f.s.SetSystemSMTP(ctx, SMTPInput{Host: "127.0.0.1", Port: m.port + 1, TLS: "starttls", Username: "lemi", From: "noreply@example.com"})
	if err == nil || !strings.Contains(err.Error(), "口令") {
		t.Fatalf("端口改了应要求重输口令：%v", err)
	}
	if err := f.s.SetSystemSMTP(ctx, SMTPInput{Host: "127.0.0.1", Port: m.port, TLS: "starttls", Username: "lemi", From: "noreply@example.com"}); err != nil {
		t.Fatalf("主机端口没变时应保留口令：%v", err)
	}
	srv, ok, err := f.s.systemSMTP(ctx)
	if err != nil || !ok || srv.Password != "p@ss word" {
		t.Fatalf("口令应保留：%v %v", err, srv.Password)
	}
	// 管理员清空后，重启时不再按环境变量写入
	if err := f.s.SetSystemSMTP(ctx, SMTPInput{}); err != nil {
		t.Fatal(err)
	}
	_ = f.s.SeedSystemSMTP(ctx, config.SMTP{Host: "127.0.0.1"})
	if f.s.SystemSMTPConfigured(ctx) {
		t.Fatal("管理员清空后不应被环境变量重新写入")
	}
	// 没有系统邮件服务器时安全事件只记日志
	f.s.SecurityEvent(ctx, "account_locked", u, "1.1.1.1", "ua")
	f.s.wait()
	if len(m.messages()) != 3 {
		t.Fatal("没有系统邮件服务器时不应发邮件")
	}
}

func TestSMTPSSLAndLoginAuth(t *testing.T) {
	f := setup(t)
	f.allowPrivate(t, true)
	m := startSMTP(t, "ssl")
	// 服务器同时支持 LOGIN 与 PLAIN 时优先 PLAIN；这里测 SSL（隐式 TLS）+ PLAIN
	err := sendMail(context.Background(), f.s.Egress, smtpServer{Host: "127.0.0.1", Port: m.port, TLS: "ssl", Username: "x", Password: "y", From: "a@example.com"},
		mailMessage{To: []string{"b@example.com"}, Subject: "主题", Body: "正文"})
	if err != nil {
		t.Fatal(err)
	}
	msgs := m.messages()
	if len(msgs) != 1 || !msgs[0].TLS || msgs[0].Auth != "\x00x\x00y" {
		t.Fatalf("SSL 发信不对：%+v", msgs)
	}
	// 不加密时拒绝发送口令
	err = sendMail(context.Background(), f.s.Egress, smtpServer{Host: "127.0.0.1", Port: m.port, TLS: "none", Username: "x", Password: "y", From: "a@example.com"},
		mailMessage{To: []string{"b@example.com"}, Subject: "主题", Body: "正文"})
	if err == nil || !isPermanent(err) {
		t.Fatalf("不加密时带口令应拒绝：%v", err)
	}
	// 服务器只支持 LOGIN 时走 AUTH LOGIN
	m.mu.Lock()
	m.mechs = "LOGIN"
	m.mu.Unlock()
	if err := sendMail(context.Background(), f.s.Egress, smtpServer{Host: "127.0.0.1", Port: m.port, TLS: "ssl", Username: "lu", Password: "lp", From: "a@example.com"},
		mailMessage{To: []string{"b@example.com"}, Subject: "主题", Body: "正文"}); err != nil {
		t.Fatal(err)
	}
	if msgs := m.messages(); len(msgs) != 2 || msgs[1].Auth != "lu:lp" {
		t.Fatalf("AUTH LOGIN 不对：%+v", msgs)
	}
	// LOGIN 认证单独验证
	la := &loginAuth{user: "u", pass: "p", host: "h"}
	if _, _, err := la.Start(&smtp.ServerInfo{Name: "h", TLS: false}); err == nil {
		t.Fatal("LOGIN 在未加密连接上不应发送口令")
	}
	if p, _ := la.Next([]byte("Password:"), true); string(p) != "p" {
		t.Fatal("LOGIN 应答不对")
	}
}
