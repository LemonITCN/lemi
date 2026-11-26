package netsafe

// 备份目标、通知渠道等「出站到用户填写的地址」的统一入口。与包级 AllowPrivate 不同，这里每次连接都调用
// allowPrivate() 读取当前设置（管理员在界面上改了「允许访问内网」立即生效，不用重启）。
// 无论是否允许内网，云元数据地址、未指定地址与组播地址一律拒绝。

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// metadataAddrs 云厂商元数据服务地址，任何设置下都不允许访问。
var metadataAddrs = []netip.Prefix{
	netip.MustParsePrefix("169.254.169.254/32"),
	netip.MustParsePrefix("169.254.170.2/32"),
	netip.MustParsePrefix("100.100.100.200/32"),
	netip.MustParsePrefix("fd00:ec2::254/128"),
}

// BlockedIP 判断出站目标 IP 是否被拦截。allowPrivate 为真时只拦截未指定、组播与云元数据地址。
func BlockedIP(ip netip.Addr, allowPrivate bool) bool {
	ip = ip.Unmap()
	if ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	for _, p := range metadataAddrs {
		if p.Contains(ip) {
			return true
		}
	}
	if allowPrivate {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	for _, p := range reserved {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// CheckHost 检查用户填写的主机名或 IP（建立连接前的快速检查；真正的防线是拨号时按实际 IP 检查）。
func CheckHost(host string, allowPrivate bool) error {
	h := strings.ToLower(strings.Trim(strings.TrimSpace(host), "[]"))
	if h == "" {
		return errors.New("地址缺少主机名")
	}
	if !allowPrivate && (h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".internal")) {
		return ErrBlocked
	}
	if ip, err := netip.ParseAddr(h); err == nil && BlockedIP(ip, allowPrivate) {
		return ErrBlocked
	}
	return nil
}

// CheckURLWith 检查 URL 的协议与主机（http/https，不带用户名口令）。
func CheckURLWith(u *url.URL, allowPrivate bool) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("只支持 http 与 https 地址")
	}
	if u.User != nil {
		return errors.New("地址里不能带用户名口令")
	}
	return CheckHost(u.Hostname(), allowPrivate)
}

// Egress 是带出站地址检查的拨号器与 HTTP 客户端工厂。
type Egress struct {
	// AllowPrivate 返回当前是否允许访问内网（为 nil 时不允许）。
	AllowPrivate func() bool
}

func (e Egress) allow() bool { return e.AllowPrivate != nil && e.AllowPrivate() }

func (e Egress) control(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	if BlockedIP(ip, e.allow()) {
		return fmt.Errorf("%w：%s", ErrBlocked, ip)
	}
	return nil
}

// DialContext 建立 TCP 连接；按实际连接的 IP 检查（防 DNS 重绑定）。
func (e Egress) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if err := CheckHost(host, e.allow()); err != nil {
		return nil, err
	}
	d := &net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second, Control: e.control}
	return d.DialContext(ctx, network, addr)
}

// Transport 返回带地址检查的 HTTP Transport（不走环境代理，避免绕过检查）。
func (e Egress) Transport() *http.Transport {
	return &http.Transport{
		DialContext:           e.DialContext,
		Proxy:                 nil,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 5 * time.Minute,
		ExpectContinueTimeout: time.Second,
		ForceAttemptHTTP2:     true,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
}

// Client 返回带地址检查的 HTTP 客户端；重定向逐跳检查。timeout 为 0 表示不限（大文件上传下载）。
func (e Egress) Client(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: e.Transport(), CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("重定向次数过多")
		}
		return CheckURLWith(req.URL, e.allow())
	}}
}
