package netdial

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/net/proxy"
)

// ProxyConfig 是代理设置。
type ProxyConfig struct {
	Type     string `json:"type"` // "" | socks5 | http
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// SSHConfig 是 SSH 隧道设置。
type SSHConfig struct {
	Enabled    bool   `json:"enabled"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	User       string `json:"user"`
	AuthType   string `json:"auth_type"` // password | key
	Password   string `json:"password"`
	PrivateKey string `json:"private_key"`
	Passphrase string `json:"passphrase"`
	HostKey    string `json:"host_key"` // 已确认的主机公钥指纹（SHA256:...）
}

// Config 是一条连接的网络通道设置。
type Config struct {
	Proxy  ProxyConfig `json:"proxy"`
	SSH    SSHConfig   `json:"ssh"`
	Policy Policy      `json:"-"`
	// Timeout 建立通道的超时，默认 15 秒
	Timeout time.Duration `json:"-"`
}

// ErrHostKeyUnknown 首次连接 SSH 主机，需要用户确认指纹。
type ErrHostKeyUnknown struct {
	Fingerprint string
	KeyType     string
}

func (e *ErrHostKeyUnknown) Error() string {
	return "首次连接该 SSH 主机，请确认主机指纹：" + e.Fingerprint
}

// ErrHostKeyMismatch SSH 主机指纹与已确认的不一致（可能被中间人攻击或主机重装）。
type ErrHostKeyMismatch struct{ Got, Want string }

func (e *ErrHostKeyMismatch) Error() string {
	return fmt.Sprintf("SSH 主机指纹与已确认的不一致（现在是 %s，之前确认的是 %s），已拒绝连接。若主机确实重装过，请在连接设置里清除指纹后重新确认", e.Got, e.Want)
}

// Fingerprint 返回公钥的 SHA256 指纹（与 ssh-keygen -lf 一致）。
func Fingerprint(k ssh.PublicKey) string {
	sum := sha256.Sum256(k.Marshal())
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// Dialer 按配置建立到目标地址的连接。Close 释放 SSH 连接。
type Dialer struct {
	cfg Config
	mu  sync.Mutex
	ssh *ssh.Client
}

// New 创建拨号器（不立即连接）。
func New(cfg Config) *Dialer {
	if cfg.Timeout == 0 {
		cfg.Timeout = 15 * time.Second
	}
	return &Dialer{cfg: cfg}
}

// Direct 是否直连（没有代理与 SSH）。
func (d *Dialer) Direct() bool { return d.cfg.Proxy.Type == "" && !d.cfg.SSH.Enabled }

// firstHop 拨第一跳（经代理或直连），对 Lemi 主机实际连接的地址执行网络策略。
func (d *Dialer) firstHop(ctx context.Context, addr string) (net.Conn, error) {
	p := d.cfg.Proxy
	switch p.Type {
	case "":
		return dialChecked(ctx, d.cfg.Policy, "tcp", addr)
	case "socks5":
		var auth *proxy.Auth
		if p.Username != "" {
			auth = &proxy.Auth{User: p.Username, Password: p.Password}
		}
		fwd := forwardDialer{ctx: ctx, policy: d.cfg.Policy}
		sd, err := proxy.SOCKS5("tcp", net.JoinHostPort(p.Host, strconv.Itoa(p.Port)), auth, fwd)
		if err != nil {
			return nil, err
		}
		if cd, ok := sd.(proxy.ContextDialer); ok {
			return cd.DialContext(ctx, "tcp", addr)
		}
		return sd.Dial("tcp", addr)
	case "http":
		c, err := dialChecked(ctx, d.cfg.Policy, "tcp", net.JoinHostPort(p.Host, strconv.Itoa(p.Port)))
		if err != nil {
			return nil, fmt.Errorf("连接 HTTP 代理失败：%w", err)
		}
		if dl, ok := ctx.Deadline(); ok {
			_ = c.SetDeadline(dl)
		}
		req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: addr}, Host: addr, Header: http.Header{}}
		if p.Username != "" {
			req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(p.Username+":"+p.Password)))
		}
		if err := req.Write(c); err != nil {
			c.Close()
			return nil, err
		}
		br := bufio.NewReader(c)
		resp, err := http.ReadResponse(br, req)
		if err != nil {
			c.Close()
			return nil, fmt.Errorf("HTTP 代理没有正确响应：%w", err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			c.Close()
			return nil, fmt.Errorf("HTTP 代理拒绝了连接：%s", resp.Status)
		}
		_ = c.SetDeadline(time.Time{})
		if br.Buffered() > 0 {
			return &bufConn{Conn: c, r: br}, nil
		}
		return c, nil
	}
	return nil, fmt.Errorf("不支持的代理类型 %q", p.Type)
}

type forwardDialer struct {
	ctx    context.Context
	policy Policy
}

func (f forwardDialer) Dial(network, addr string) (net.Conn, error) {
	return dialChecked(f.ctx, f.policy, network, addr)
}

func (f forwardDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return dialChecked(ctx, f.policy, network, addr)
}

type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufConn) Read(p []byte) (int, error) { return b.r.Read(p) }

func (d *Dialer) sshClient(ctx context.Context) (*ssh.Client, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ssh != nil {
		// 连接还活着就复用
		if _, _, err := d.ssh.SendRequest("keepalive@lemi", true, nil); err == nil {
			return d.ssh, nil
		}
		_ = d.ssh.Close()
		d.ssh = nil
	}
	s := d.cfg.SSH
	if s.Host == "" || s.User == "" {
		return nil, errors.New("SSH 隧道需要主机与用户名")
	}
	port := s.Port
	if port == 0 {
		port = 22
	}
	var auths []ssh.AuthMethod
	switch s.AuthType {
	case "key":
		var signer ssh.Signer
		var err error
		if s.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(s.PrivateKey), []byte(s.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(s.PrivateKey))
		}
		if err != nil {
			var pe *ssh.PassphraseMissingError
			if errors.As(err, &pe) {
				return nil, errors.New("SSH 私钥有口令保护，请填写私钥口令")
			}
			return nil, fmt.Errorf("SSH 私钥无法解析：%w", err)
		}
		auths = append(auths, ssh.PublicKeys(signer))
	default:
		auths = append(auths, ssh.Password(s.Password), ssh.KeyboardInteractive(func(_, _ string, qs []string, _ []bool) ([]string, error) {
			ans := make([]string, len(qs))
			for i := range qs {
				ans[i] = s.Password
			}
			return ans, nil
		}))
	}
	var unknown *ErrHostKeyUnknown
	var mismatch *ErrHostKeyMismatch
	cc := &ssh.ClientConfig{
		User: s.User, Auth: auths, Timeout: d.cfg.Timeout,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			fp := Fingerprint(key)
			if s.HostKey == "" {
				unknown = &ErrHostKeyUnknown{Fingerprint: fp, KeyType: key.Type()}
				return unknown
			}
			if fp != s.HostKey {
				mismatch = &ErrHostKeyMismatch{Got: fp, Want: s.HostKey}
				return mismatch
			}
			return nil
		},
	}
	addr := net.JoinHostPort(s.Host, strconv.Itoa(port))
	raw, err := d.firstHop(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("连接 SSH 主机失败：%w", err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = raw.SetDeadline(dl)
	}
	conn, chans, reqs, err := ssh.NewClientConn(raw, addr, cc)
	if err != nil {
		raw.Close()
		switch {
		case unknown != nil:
			return nil, unknown
		case mismatch != nil:
			return nil, mismatch
		}
		if strings.Contains(err.Error(), "unable to authenticate") {
			return nil, errors.New("SSH 认证失败：用户名、口令或私钥不正确")
		}
		return nil, fmt.Errorf("SSH 握手失败：%w", err)
	}
	_ = raw.SetDeadline(time.Time{})
	cl := ssh.NewClient(conn, chans, reqs)
	d.ssh = cl
	go keepalive(cl)
	return cl, nil
}

func keepalive(c *ssh.Client) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for range t.C {
		if _, _, err := c.SendRequest("keepalive@lemi", true, nil); err != nil {
			return
		}
	}
}

// DialContext 连接目标地址（数据库主机:端口）。
func (d *Dialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d.cfg.Timeout)
		defer cancel()
	}
	if !d.cfg.SSH.Enabled {
		return d.firstHop(ctx, addr)
	}
	cl, err := d.sshClient(ctx)
	if err != nil {
		return nil, err
	}
	c, err := cl.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("经 SSH 隧道连接 %s 失败：%w", addr, err)
	}
	return c, nil
}

// Close 关闭 SSH 连接。
func (d *Dialer) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ssh != nil {
		err := d.ssh.Close()
		d.ssh = nil
		return err
	}
	return nil
}
