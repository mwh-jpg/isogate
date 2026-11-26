package proxy

import (
	"context"
	"net"
	"testing"
)

func TestBlockPrivateIPs(t *testing.T) {
	g := NewSSRFGuard(false)
	cases := []string{
		"127.0.0.1", "10.0.0.1", "172.16.5.4", "172.31.255.255",
		"192.168.1.1", "169.254.169.254", "0.0.0.0", "100.64.0.1",
		"::1", "fe80::1", "fc00::1",
	}
	for _, ip := range cases {
		if err := g.ValidateHost(ip); err == nil {
			t.Errorf("应拦截私网IP: %s", ip)
		}
	}
}

func TestAllowPublicIPs(t *testing.T) {
	g := NewSSRFGuard(false)
	for _, ip := range []string{"1.1.1.1", "8.8.8.8", "13.107.42.14", "2606:4700:4700::1111"} {
		if err := g.ValidateHost(ip); err != nil {
			t.Errorf("公网IP不应拦截: %s -> %v", ip, err)
		}
	}
}

func TestBlockLocalhostNames(t *testing.T) {
	g := NewSSRFGuard(false)
	for _, h := range []string{"localhost", "foo.localhost", "api.local"} {
		if err := g.ValidateHost(h); err == nil {
			t.Errorf("应拦截: %s", h)
		}
	}
}

func TestAllowPrivateWhenConfigured(t *testing.T) {
	g := NewSSRFGuard(true)
	if err := g.ValidateHost("127.0.0.1"); err != nil {
		t.Errorf("allowPrivate=true 应放行: %v", err)
	}
}

func TestDialStageRebinding(t *testing.T) {
	// 用一个自定义 resolver 模拟"解析阶段返回公网 IP，但实际连接落到私网"
	g := NewSSRFGuard(false)
	// 直接校验远端地址（模拟 Dial 后的第二轮校验）
	connRemote := "127.0.0.1:443"
	rh, _, _ := net.SplitHostPort(connRemote)
	if err := g.validateIP(net.ParseIP(rh)); err == nil {
		t.Fatal("Dial 后对私网远端地址应拒绝（防 DNS Rebinding 第二轮）")
	}
}

func TestParseProxyURL(t *testing.T) {
	cases := []struct {
		raw    string
		scheme string
		host   string
		port   int
		ok     bool
	}{
		{"http://u:p@1.2.3.4:8080", "http", "1.2.3.4", 8080, true},
		{"socks5://5.6.7.8:1080", "socks5", "5.6.7.8", 1080, true},
		{"https://proxy.example.com", "https", "proxy.example.com", 443, true},
		{"ftp://1.2.3.4:21", "", "", 0, false},
		{"http://:99999", "", "", 0, false},
	}
	for _, tc := range cases {
		s, h, p, err := ParseProxyURL(tc.raw)
		if tc.ok && err != nil {
			t.Errorf("%s 应成功: %v", tc.raw, err)
		} else if !tc.ok && err == nil {
			t.Errorf("%s 应失败", tc.raw)
		} else if tc.ok && (s != tc.scheme || h != tc.host || p != tc.port) {
			t.Errorf("%s 解析错误: %s %s %d", tc.raw, s, h, p)
		}
	}
}

func TestDialContextBlocksPrivate(t *testing.T) {
	g := NewSSRFGuard(false)
	_, err := g.DialContext(context.Background(), "tcp", "127.0.0.1:80")
	if err == nil {
		t.Fatal("DialContext 应拒绝私网地址")
	}
}
