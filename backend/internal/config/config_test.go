package config

import (
	"strings"
	"testing"
)

func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

func TestLoadRejectsShortEncryptionKey(t *testing.T) {
	setEnv(t, map[string]string{
		"ENCRYPTION_KEY":  strings.Repeat("a", 16),
		"JWT_SECRET":      strings.Repeat("b", 32),
		"PUBLIC_BASE_URL": "https://gw.example.com",
		"DATABASE_URL":    "postgres://u:p@h:5432/db",
	})
	if _, err := Load(); err == nil {
		t.Fatal("短 ENCRYPTION_KEY 应拒绝启动")
	}
}

func TestLoadRejectsMissingPublicBaseURL(t *testing.T) {
	setEnv(t, map[string]string{
		"ENCRYPTION_KEY":  strings.Repeat("a", 32),
		"JWT_SECRET":      strings.Repeat("b", 32),
		"PUBLIC_BASE_URL": "",
		"DATABASE_URL":    "postgres://u:p@h:5432/db",
	})
	if _, err := Load(); err == nil {
		t.Fatal("缺 PUBLIC_BASE_URL 应拒绝启动")
	}
}

func TestLoadRejectsInvalidPublicBaseURL(t *testing.T) {
	setEnv(t, map[string]string{
		"ENCRYPTION_KEY":  strings.Repeat("a", 32),
		"JWT_SECRET":      strings.Repeat("b", 32),
		"PUBLIC_BASE_URL": "not-a-url",
		"DATABASE_URL":    "postgres://u:p@h:5432/db",
	})
	if _, err := Load(); err == nil {
		t.Fatal("非法 PUBLIC_BASE_URL 应拒绝启动")
	}
}

func TestLoadOK(t *testing.T) {
	setEnv(t, map[string]string{
		"ENCRYPTION_KEY":        strings.Repeat("a", 32),
		"JWT_SECRET":            strings.Repeat("b", 40),
		"PUBLIC_BASE_URL":       "https://gw.example.com/",
		"DATABASE_URL":          "postgres://u:p@h:5432/db",
		"REQUIRE_PROXY_DEFAULT": "true",
	})
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PublicBaseURL != "https://gw.example.com" {
		t.Fatalf("PUBLIC_BASE_URL 应去掉尾部斜杠: %s", cfg.PublicBaseURL)
	}
	if !cfg.RequireProxyDefault {
		t.Fatal("默认应强制代理")
	}
	if cfg.MaxUpstreamBody != 16<<20 {
		t.Fatal("默认响应体上限应为16MB")
	}
}
