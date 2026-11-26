package model

import "time"

type User struct {
	ID             string     `json:"id"`
	Email          string     `json:"email"`
	PasswordHash   string     `json:"-"`
	Role           string     `json:"role"`
	Status         string     `json:"status"`
	PlanStatus     string     `json:"plan_status"`
	PlanExpiresAt  *time.Time `json:"plan_expires_at"`
	MaxConcurrency int        `json:"max_concurrency"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type ProxyEndpoint struct {
	ID                string     `json:"id"`
	UserID            string     `json:"user_id"`
	Name              string     `json:"name"`
	Scheme            string     `json:"scheme"`
	Host              string     `json:"host"`
	Port              int        `json:"port"`
	UsernameEnc       *string    `json:"-"`
	PasswordEnc       *string    `json:"-"`
	LastEgressIP      *string    `json:"last_egress_ip"`
	LastEgressCountry *string    `json:"last_egress_country"`
	LastLatencyMS     *int       `json:"last_latency_ms"`
	HealthStatus      string     `json:"health_status"`
	Source            string     `json:"source"`
	LastCheckedAt     *time.Time `json:"last_checked_at"`
	LastError         *string    `json:"-"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// ProxyURL 返回给拨号器使用的完整代理 URL（明文，仅在内存中，绝不落日志）。
func (p *ProxyEndpoint) ProxyURL(decrypt func([]byte) (string, error)) (string, error) {
	user := ""
	pass := ""
	if p.UsernameEnc != nil && *p.UsernameEnc != "" {
		v, err := decrypt([]byte(*p.UsernameEnc))
		if err != nil {
			return "", err
		}
		user = v
	}
	if p.PasswordEnc != nil && *p.PasswordEnc != "" {
		v, err := decrypt([]byte(*p.PasswordEnc))
		if err != nil {
			return "", err
		}
		pass = v
	}
	cred := ""
	if user != "" || pass != "" {
		cred = user + ":" + pass + "@"
	}
	scheme := p.Scheme
	if scheme == "socks5" {
		return "socks5://" + cred + p.Host + ":" + itoa(p.Port), nil
	}
	return scheme + "://" + cred + p.Host + ":" + itoa(p.Port), nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

type UpstreamAccount struct {
	ID             string     `json:"id"`
	UserID         string     `json:"user_id"`
	ProxyID        *string    `json:"proxy_id"`
	Provider       string     `json:"provider"`
	Name           string     `json:"name"`
	BaseURL        string     `json:"base_url"`
	CredentialEnc  string     `json:"-"`
	RequireProxy   bool       `json:"require_proxy"`
	MaxConcurrency int        `json:"max_concurrency"`
	Status         string     `json:"status"`
	CooldownUntil  *time.Time `json:"cooldown_until"`
	LastError      *string    `json:"-"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type GatewayKey struct {
	ID                string     `json:"id"`
	UserID            string     `json:"user_id"`
	UpstreamAccountID string     `json:"upstream_account_id"`
	Name              string     `json:"name"`
	KeyPrefix         string     `json:"key_prefix"`
	KeyHash           string     `json:"-"`
	AllowedModels     []string   `json:"allowed_models"`
	RPM               int        `json:"rpm_limit"`
	Status            string     `json:"status"`
	LastUsedAt        *time.Time `json:"last_used_at"`
	CreatedAt         time.Time  `json:"created_at"`
}

type UsageLog struct {
	ID                  int64     `json:"id"`
	UserID              string    `json:"user_id"`
	APIKeyID            *string   `json:"api_key_id"`
	UpstreamAccountID   *string   `json:"upstream_account_id"`
	ProxyID             *string   `json:"proxy_id"`
	RequestedModel      string    `json:"requested_model"`
	ActualModel         string    `json:"actual_model"`
	UpstreamRequestID   string    `json:"upstream_request_id"`
	EgressIPSnapshot    string    `json:"egress_ip_snapshot"`
	StatusCode          int       `json:"status_code"`
	IsStream            bool      `json:"is_stream"`
	InputTokens         int       `json:"input_tokens"`
	OutputTokens        int       `json:"output_tokens"`
	CacheCreationTokens int       `json:"cache_creation_tokens"`
	CacheReadTokens     int       `json:"cache_read_tokens"`
	TTFTMS              int       `json:"ttft_ms"`
	TotalLatencyMS      int       `json:"total_latency_ms"`
	ErrorMessage        string    `json:"error_message"`
	CreatedAt           time.Time `json:"created_at"`
}
