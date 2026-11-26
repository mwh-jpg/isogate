// Package proxy 提供 BYO-IP 代理隔离、SSRF 防护与出口探测。
package proxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

var (
	ErrPrivateAddress  = errors.New("SSRF: 目标解析为私网/回环/链路本地地址")
	ErrBlockedHost     = errors.New("SSRF: 目标主机被拒绝")
	ErrInvalidProxyURL = errors.New("代理 URL 非法")
)

// SSRFGuard 在 Dial 阶段做 IP 级校验（防 DNS Rebinding：不用字符串解析结果，
// 而是对 net.Dialer 返回的真实 conn.RemoteAddr 再校验一次）。
type SSRFGuard struct {
	allowPrivate bool
	resolver     *net.Resolver
}

func NewSSRFGuard(allowPrivate bool) *SSRFGuard {
	return &SSRFGuard{allowPrivate: allowPrivate, resolver: net.DefaultResolver}
}

// ValidateHost 字符串阶段预校验（主机字面量 / 已知保留域名）。
func (g *SSRFGuard) ValidateHost(host string) error {
	if g.allowPrivate {
		return nil
	}
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") {
		return fmt.Errorf("%w: %s", ErrBlockedHost, host)
	}
	if ip := net.ParseIP(h); ip != nil {
		return g.validateIP(ip)
	}
	return nil // 域名在 Dial 阶段解析后再校验
}

// ValidateIP 对已解析出的 IP 做最终判定。
func (g *SSRFGuard) validateIP(ip net.IP) error {
	if g.allowPrivate {
		return nil
	}
	v4 := ip.To4()
	if v4 != nil {
		// 回环 / 私网 / 链路本地 / 云元数据 / 0.0.0.0 / 广播
		if v4[0] == 127 || v4[0] == 10 ||
			(v4[0] == 172 && v4[1] >= 16 && v4[1] <= 31) ||
			(v4[0] == 192 && v4[1] == 168) ||
			(v4[0] == 169 && v4[1] == 254) ||
			v4[0] == 0 || (v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127) {
			return fmt.Errorf("%w: %s", ErrPrivateAddress, v4.String())
		}
		return nil
	}
	v6 := ip.To16()
	if v6 != nil {
		if v6.IsLoopback() || v6.IsLinkLocalUnicast() || v6.IsInterfaceLocalMulticast() {
			return fmt.Errorf("%w: %s", ErrPrivateAddress, v6.String())
		}
		// IPv4-mapped IPv6
		if mapped := v6.To4(); mapped != nil {
			return g.validateIP(mapped)
		}
		// fc00::/7 唯一本地地址
		if v6[0]&0xfe == 0xfc {
			return fmt.Errorf("%w: %s", ErrPrivateAddress, v6.String())
		}
	}
	return nil
}

// DialContext 是带 SSRF 校验的拨号器：解析 IP → 校验 → 连接 → 再校验实际远端 IP。
func (g *SSRFGuard) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if err := g.ValidateHost(host); err != nil {
		return nil, err
	}

	// 解析所有 A/AAAA，任一命中黑名单即整体拒绝（防 DNS Rebinding 第一轮）
	ips, err := g.resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("%w: DNS 无结果", ErrBlockedHost)
	}
	for _, ipa := range ips {
		if err := g.validateIP(ipa.IP); err != nil {
			return nil, err
		}
	}

	d := &net.Dialer{}
	conn, err := d.DialContext(ctx, network, net.JoinHostPort(host, port))
	if err != nil {
		return nil, err
	}

	// 第二轮：对真实建立的远端 IP 再校验（防 DNS Rebinding 第二轮）
	remote := conn.RemoteAddr().String()
	rh, _, splitErr := net.SplitHostPort(remote)
	if splitErr != nil {
		_ = conn.Close()
		return nil, splitErr
	}
	if rip := net.ParseIP(rh); rip != nil {
		if err := g.validateIP(rip); err != nil {
			_ = conn.Close()
			return nil, err
		}
	}
	return conn, nil
}

// ParseProxyURL 解析并校验代理 URL，拆出 host/port/scheme。
func ParseProxyURL(raw string) (scheme, host string, port int, err error) {
	u, parseErr := url.Parse(raw)
	if parseErr != nil {
		return "", "", 0, fmt.Errorf("%w: %v", ErrInvalidProxyURL, parseErr)
	}
	scheme = strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" && scheme != "socks5" {
		return "", "", 0, fmt.Errorf("%w: 不支持的协议 %s", ErrInvalidProxyURL, scheme)
	}
	host = u.Hostname()
	portStr := u.Port()
	if portStr == "" {
		if scheme == "https" {
			port = 443
		} else {
			port = 80
		}
	} else {
		port, err = strconv.Atoi(portStr)
		if err != nil || port < 1 || port > 65535 {
			return "", "", 0, fmt.Errorf("%w: 非法端口 %s", ErrInvalidProxyURL, portStr)
		}
	}
	if host == "" {
		return "", "", 0, fmt.Errorf("%w: 缺少主机", ErrInvalidProxyURL)
	}
	return scheme, host, port, nil
}
