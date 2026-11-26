package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

// entry 缓存一个账号专属的 Transport。
type entry struct {
	transport *http.Transport
	dialer    *net.Dialer
	createdAt time.Time
}

// Manager 按 proxy_id（实际按完整代理 URL 指纹）缓存独立 Transport。
// 每个账号绑定自己的代理 => 独立连接池 => 出口 IP 与会话严格隔离，绝不混池。
type Manager struct {
	mu      sync.RWMutex
	pool    map[string]*entry
	guard   *SSRFGuard
	maxIdle int
	timeout time.Duration
}

func NewManager(guard *SSRFGuard, timeout time.Duration) *Manager {
	return &Manager{
		pool:    make(map[string]*entry),
		guard:   guard,
		maxIdle: 20,
		timeout: timeout,
	}
}

// clientKey 用 proxy URL 做 key（密码密文解密后得到明文 URL，仅内存）。
func (m *Manager) GetClient(proxyURL string) (*http.Client, error) {
	key := fingerprint(proxyURL)

	m.mu.RLock()
	e, ok := m.pool[key]
	m.mu.RUnlock()
	if ok {
		return &http.Client{Transport: e.transport, Timeout: m.timeout}, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok = m.pool[key]; ok {
		return &http.Client{Transport: e.transport, Timeout: m.timeout}, nil
	}

	tr, err := m.buildTransport(proxyURL)
	if err != nil {
		return nil, err
	}
	e = &entry{transport: tr, createdAt: time.Now()}
	m.pool[key] = e
	return &http.Client{Transport: tr, Timeout: m.timeout}, nil
}

// GetClientDirect 返回一个走 SSRF 校验拨号器的直连 client。
// 注意：仅在账号 require_proxy=false 时使用；默认配置下 require_proxy=true 禁止直连。
func (m *Manager) GetClientDirect() *http.Client {
	tr := &http.Transport{
		MaxIdleConns:        50,
		MaxIdleConnsPerHost: m.maxIdle,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 15 * time.Second,
		DialContext:         m.guard.DialContext,
		ForceAttemptHTTP2:   true,
	}
	return &http.Client{Transport: tr, Timeout: m.timeout}
}

// Invalidate 代理配置变更时调用：关闭旧连接池。
func (m *Manager) Invalidate(proxyURL string) {
	key := fingerprint(proxyURL)
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.pool[key]; ok {
		e.transport.CloseIdleConnections()
		delete(m.pool, key)
	}
}

func (m *Manager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.pool {
		e.transport.CloseIdleConnections()
	}
	m.pool = make(map[string]*entry)
}

// buildTransport 构造带 SSRF 校验的拨号器，支持 http/https/socks5。
func (m *Manager) buildTransport(proxyURL string) (*http.Transport, error) {
	scheme, _, _, err := ParseProxyURL(proxyURL)
	if err != nil {
		return nil, err
	}

	tr := &http.Transport{
		MaxIdleConns:          50,
		MaxIdleConnsPerHost:   m.maxIdle,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ForceAttemptHTTP2:     true,
	}

	// 基础拨号器带 SSRF 校验（直连场景）
	baseDial := m.guard.DialContext

	switch scheme {
	case "socks5":
		u, err := url.Parse(proxyURL)
		if err != nil {
			return nil, err
		}
		var auth *proxy.Auth
		if u.User != nil {
			p, _ := u.User.Password()
			auth = &proxy.Auth{User: u.User.Username(), Password: p}
		}
		socksDialer, err := proxy.SOCKS5("tcp", u.Host, auth, proxy.Direct)
		if err != nil {
			return nil, fmt.Errorf("SOCKS5 拨号器构造失败: %w", err)
		}
		ctxDialer, ok := socksDialer.(proxy.ContextDialer)
		if !ok {
			return nil, fmt.Errorf("SOCKS5 拨号器不支持 Context")
		}
		// 先校验目标 host，再走 SOCKS5（SOCKS5 由代理解析域名，服务端无法二次校验远端 IP，
		// 因此这里只做字符串/字面量阶段校验，生产建议代理侧也做出口白名单）
		tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, splitErr := net.SplitHostPort(addr)
			if splitErr != nil {
				return nil, splitErr
			}
			if err := m.guard.ValidateHost(host); err != nil {
				return nil, err
			}
			return ctxDialer.DialContext(ctx, network, addr)
		}
	case "http", "https":
		u, err := url.Parse(proxyURL)
		if err != nil {
			return nil, err
		}
		tr.Proxy = http.ProxyURL(u)
		// HTTP CONNECT 代理：拨号到代理本身时校验代理地址不是私网（防 SSRF 到内网代理）
		tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, splitErr := net.SplitHostPort(addr)
			if splitErr != nil {
				return nil, splitErr
			}
			// 如果拨号目标是代理本身且是字面量 IP，校验其非私网
			if ip := net.ParseIP(host); ip != nil {
				if err := m.guard.validateIP(ip); err != nil {
					return nil, err
				}
			}
			return baseDial(ctx, network, net.JoinHostPort(host, port))
		}
	default:
		return nil, fmt.Errorf("不支持的代理协议: %s", scheme)
	}
	return tr, nil
}

// fingerprint 用 URL 的 SHA-256 做缓存 key，避免明文 key 出现在日志。
func fingerprint(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
