// Package netdial 为数据库连接建立网络通道：直连、SOCKS5 / HTTP CONNECT 代理、SSH 隧道（可叠加：代理 → SSH → 数据库），
// 并对 Lemi 主机实际拨出的第一跳执行网络策略（允许/拒绝网段，云元数据地址永远拒绝），按解析出的 IP 拨号防 DNS 重绑定。
package netdial

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// Policy 是出站网络策略。
type Policy struct {
	Allow []netip.Prefix // 为空表示不限
	Deny  []netip.Prefix
}

// alwaysDeny 云厂商元数据服务地址，任何时候都不允许连接。
var alwaysDeny = []netip.Prefix{
	netip.MustParsePrefix("169.254.169.254/32"),
	netip.MustParsePrefix("169.254.170.2/32"),
	netip.MustParsePrefix("100.100.100.200/32"), // 阿里云
	netip.MustParsePrefix("fd00:ec2::254/128"),
}

// ParsePolicy 从 CIDR 文本构造策略。
func ParsePolicy(allow, deny []string) Policy {
	var p Policy
	for _, s := range allow {
		if pr, err := netip.ParsePrefix(strings.TrimSpace(s)); err == nil {
			p.Allow = append(p.Allow, pr.Masked())
		}
	}
	for _, s := range deny {
		if pr, err := netip.ParsePrefix(strings.TrimSpace(s)); err == nil {
			p.Deny = append(p.Deny, pr.Masked())
		}
	}
	return p
}

// ErrBlocked 目标地址被网络策略拒绝。
var ErrBlocked = errors.New("目标地址被网络策略拒绝")

// Check 检查一个 IP 是否允许连接。
func (p Policy) Check(ip netip.Addr) error {
	ip = ip.Unmap()
	if ip.IsUnspecified() || ip.IsMulticast() {
		return fmt.Errorf("%w：%s", ErrBlocked, ip)
	}
	for _, d := range alwaysDeny {
		if d.Contains(ip) {
			return fmt.Errorf("%w：%s 是云元数据服务地址", ErrBlocked, ip)
		}
	}
	for _, d := range p.Deny {
		if d.Contains(ip) {
			return fmt.Errorf("%w：%s 在拒绝网段 %s 内", ErrBlocked, ip, d)
		}
	}
	if len(p.Allow) == 0 {
		return nil
	}
	for _, a := range p.Allow {
		if a.Contains(ip) {
			return nil
		}
	}
	return fmt.Errorf("%w：%s 不在允许的网段内", ErrBlocked, ip)
}

// dialChecked 解析主机名，检查每个地址，按 IP 拨号（防止解析与连接之间被换成别的地址）。
func dialChecked(ctx context.Context, p Policy, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	var ips []netip.Addr
	if ip, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		ips = []netip.Addr{ip}
	} else {
		res, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("解析主机名 %s 失败：%w", host, err)
		}
		ips = res
	}
	var lastErr error
	d := &net.Dialer{}
	for _, ip := range ips {
		if err := p.Check(ip); err != nil {
			lastErr = err
			continue
		}
		c, err := d.DialContext(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
		if err == nil {
			if tc, ok := c.(*net.TCPConn); ok {
				_ = tc.SetKeepAlive(true)
			}
			return c, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("没有可用的地址：%s", host)
	}
	return nil, lastErr
}
