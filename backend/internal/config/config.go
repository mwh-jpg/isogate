// Package config 加载并校验运行配置。
// 安全基线：ENCRYPTION_KEY 必须 32 字节，JWT_SECRET 与 PUBLIC_BASE_URL 必填，
// 所有对外链接只从 PUBLIC_BASE_URL 生成，绝不信任 Host 头（防 Host Header 投毒）。
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	// 服务
	Addr           string
	PublicBaseURL  string // 如 https://gw.example.com，认证回调/OAuth重定向唯一来源
	TrustedProxies []string
	Debug          bool

	// 安全
	EncryptionKey []byte // 必须 32 字节（AES-256）
	JWTSecret     []byte
	JWTExpiry     time.Duration

	// 数据库
	DatabaseURL string // postgres://user:pass@host:5432/db?sslmode=disable
	RedisURL    string // redis://host:6379/0

	// 网关行为
	RequireProxyDefault bool  // 默认强制代理出站
	MaxUpstreamBody     int64 // 上游响应体上限（字节）
	UpstreamTimeout     time.Duration
	ProbeMaxBody        int64 // 探测响应体上限
	Cooldown429         time.Duration
	Cooldown5xx         time.Duration

	// 上游默认
	AnthropicBaseURL string

	// SSRF
	AllowPrivateUpstream bool // 默认 false；仅本地测试可开
}

func getenv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func getBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func getInt64(key string, def int64) int64 {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def
	}
	return n
}

func getDuration(key string, def time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

// Load 读取环境变量并强校验。任何不合法配置直接拒绝启动（fail-closed）。
func Load() (*Config, error) {
	key := getenv("ENCRYPTION_KEY", "")
	if len(key) != 32 {
		return nil, fmt.Errorf("ENCRYPTION_KEY 必须恰好 32 字节，当前 %d 字节", len(key))
	}
	secret := getenv("JWT_SECRET", "")
	if len(secret) < 32 {
		return nil, fmt.Errorf("JWT_SECRET 至少 32 字节，当前 %d 字节", len(secret))
	}

	publicBase := getenv("PUBLIC_BASE_URL", "")
	if publicBase == "" {
		return nil, fmt.Errorf("PUBLIC_BASE_URL 必填（认证链接唯一来源，禁止信任 Host 头）")
	}
	u, err := url.Parse(publicBase)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("PUBLIC_BASE_URL 非法: %q", publicBase)
	}

	dbURL := getenv("DATABASE_URL", "")
	if dbURL == "" {
		return nil, fmt.Errorf("DATABASE_URL 必填")
	}

	redisURL := getenv("REDIS_URL", "redis://localhost:6379/0")

	trust := []string{}
	for _, s := range strings.Split(getenv("TRUSTED_PROXIES", ""), ",") {
		if s = strings.TrimSpace(s); s != "" {
			trust = append(trust, s)
		}
	}

	return &Config{
		Addr:                 getenv("ADDR", ":8080"),
		PublicBaseURL:        strings.TrimRight(publicBase, "/"),
		TrustedProxies:       trust,
		Debug:                getBool("DEBUG", false),
		EncryptionKey:        []byte(key),
		JWTSecret:            []byte(secret),
		JWTExpiry:            getDuration("JWT_EXPIRY", 7*24*time.Hour),
		DatabaseURL:          dbURL,
		RedisURL:             redisURL,
		RequireProxyDefault:  getBool("REQUIRE_PROXY_DEFAULT", true),
		MaxUpstreamBody:      getInt64("MAX_UPSTREAM_BODY", 16<<20),
		UpstreamTimeout:      getDuration("UPSTREAM_TIMEOUT", 300*time.Second),
		ProbeMaxBody:         getInt64("PROBE_MAX_BODY", 1<<20),
		Cooldown429:          getDuration("COOLDOWN_429", 30*time.Second),
		Cooldown5xx:          getDuration("COOLDOWN_5XX", 60*time.Second),
		AnthropicBaseURL:     strings.TrimRight(getenv("ANTHROPIC_BASE_URL", "https://api.anthropic.com"), "/"),
		AllowPrivateUpstream: getBool("ALLOW_PRIVATE_UPSTREAM", false),
	}, nil
}
