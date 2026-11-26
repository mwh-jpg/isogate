package gateway

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/michaelchou/isolated-ai-gateway/internal/config"
	"github.com/michaelchou/isolated-ai-gateway/internal/crypto"
	"github.com/michaelchou/isolated-ai-gateway/internal/model"
	"github.com/michaelchou/isolated-ai-gateway/internal/proxy"
	"github.com/michaelchou/isolated-ai-gateway/internal/ratelimit"
	"github.com/michaelchou/isolated-ai-gateway/internal/repository"
)

var (
	ErrKeyInvalid    = errors.New("API Key 无效或已吊销")
	ErrNoAccount     = errors.New("未绑定上游账号")
	ErrProxyRequired = errors.New("账号要求代理出站但未绑定健康代理（Fail-Closed）")
	ErrPlanInactive  = errors.New("套餐未开通或已停用（Fail-Closed）")
	ErrAccountDown   = errors.New("账号冷却中")
	ErrModelNotAllow = errors.New("模型不在 Key 允许列表")
)

// Resolved 是鉴权+隔离解析结果。
type Resolved struct {
	Key        *model.GatewayKey
	Account    *model.UpstreamAccount
	Proxy      *model.ProxyEndpoint
	ProxyURL   string // 明文，仅内存
	Credential string // 明文，仅内存
	EgressIP   string
}

// Resolver 强制租户隔离与 Fail-Closed。
type Resolver struct {
	cfg  *config.Config
	repo *repository.Repo
}

func NewResolver(cfg *config.Config, repo *repository.Repo) *Resolver {
	return &Resolver{cfg: cfg, repo: repo}
}

// Resolve 用 API Key hash 解析出绑定的账号与代理，任何不满足隔离条件的直接拒绝。
func (r *Resolver) Resolve(ctx context.Context, keyHash string) (*Resolved, error) {
	key, err := r.repo.GetKeyByHash(ctx, keyHash)
	if err != nil {
		return nil, ErrKeyInvalid
	}
	if key.Status != "active" {
		return nil, ErrKeyInvalid
	}
	// 套餐状态参与出站鉴权：管理员停用/取消套餐后立即 Fail-Closed。
	// 放在解析账号之前，避免为未付费/已停用租户做任何上游凭证解密。
	user, err := r.repo.GetUserByID(ctx, key.UserID)
	if err != nil || user.Status == "disabled" || user.PlanStatus != "active" {
		return nil, ErrPlanInactive
	}
	// 账号必须属于同一 user（DB 复合外键 + 这里再次校验，零 IDOR）
	account, err := r.repo.GetAccount(ctx, key.UserID, key.UpstreamAccountID)
	if err != nil || account.Status == "disabled" {
		return nil, ErrNoAccount
	}
	if account.Status == "cooldown" && account.CooldownUntil != nil && account.CooldownUntil.After(time.Now()) {
		return nil, ErrAccountDown
	}

	dec := func(enc string) (string, error) {
		b, err := crypto.Decrypt(r.cfg.EncryptionKey, enc)
		return string(b), err
	}
	cred, err := dec(account.CredentialEnc)
	if err != nil {
		return nil, fmt.Errorf("凭证解密失败: %w", err)
	}

	out := &Resolved{Key: key, Account: account, Credential: cred}
	if account.ProxyID != nil {
		p, err := r.repo.GetProxy(ctx, key.UserID, *account.ProxyID)
		if err != nil {
			return nil, ErrProxyRequired // 代理被删/越权
		}
		if p.HealthStatus != "healthy" {
			return nil, ErrProxyRequired // Fail-Closed：不健康代理 = 拒绝
		}
		user, _ := dec(str(p.UsernameEnc))
		pass, _ := dec(str(p.PasswordEnc))
		credential := ""
		if user != "" || pass != "" {
			credential = user + ":" + pass + "@"
		}
		scheme := p.Scheme
		out.ProxyURL = scheme + "://" + credential + p.Host + ":" + itoa(p.Port)
		out.Proxy = p
		if p.LastEgressIP != nil {
			out.EgressIP = *p.LastEgressIP
		}
	} else if account.RequireProxy {
		return nil, ErrProxyRequired
	}
	return out, nil
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// Relay 执行透传。返回响应字节与元数据（审计头）。
type RelayResult struct {
	Body              []byte
	StatusCode        int
	Header            http.Header
	Usage             UsageSnapshot
	ActualModel       string
	UpstreamRequestID string
	TotalLatencyMS    int
	IsStream          bool
}

type Engine struct {
	cfg     *config.Config
	repo    *repository.Repo
	mgr     *proxy.Manager
	limiter *ratelimit.Limiter
}

func NewEngine(cfg *config.Config, repo *repository.Repo, mgr *proxy.Manager) *Engine {
	return &Engine{cfg: cfg, repo: repo, mgr: mgr}
}

func (e *Engine) SetLimiter(l *ratelimit.Limiter) { e.limiter = l }

// Guard 在转发前执行 RPM 限流与账号并发锁，返回 release 函数。
func (e *Engine) Guard(ctx context.Context, res *Resolved) (func(context.Context), error) {
	if e.limiter == nil {
		return func(context.Context) {}, nil
	}
	if err := e.limiter.AllowRPM(ctx, res.Key.ID, res.Key.RPM); err != nil {
		return nil, err
	}
	ttl := 5 * time.Minute
	release, err := e.limiter.Acquire(ctx, res.Account.ID, res.Account.MaxConcurrency, ttl)
	if err != nil {
		return nil, err
	}
	return release, nil
}

// HandleUpstreamStatus 在上游返回非 2xx 时分类熔断（只作用于本账号）。
func (e *Engine) HandleUpstreamStatus(ctx context.Context, res *Resolved, status int, body []byte) {
	if status >= 200 && status < 300 {
		// 成功时若账号在冷却中，清除冷却
		if res.Account.Status == "cooldown" && e.repo != nil {
			_ = e.repo.ClearAccountCooldown(ctx, res.Account.ID)
		}
		return
	}
	if e.repo == nil {
		return
	}
	d := ClassifyUpstreamError(status, string(body), e.cfg)
	_ = ApplyDecision(ctx, e.repo, res.Account.ID, d)
}

// Forward 非流式透传。
func (e *Engine) Forward(ctx context.Context, res *Resolved, body []byte, clientHeaders http.Header) (*RelayResult, error) {
	client, err := e.buildClient(res)
	if err != nil {
		return nil, err
	}
	target := strings.TrimRight(res.Account.BaseURL, "/") + "/v1/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	applyHeaders(req, res, clientHeaders)

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	limited := io.LimitReader(resp.Body, e.cfg.MaxUpstreamBody)
	respBody, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	latency := int(time.Since(start).Milliseconds())

	snap := ParseNonStream(respBody)
	return &RelayResult{
		Body: respBody, StatusCode: resp.StatusCode,
		Header: resp.Header, Usage: snap, ActualModel: snap.ActualModel,
		UpstreamRequestID: resp.Header.Get("request-id"),
		TotalLatencyMS:    latency, IsStream: false,
	}, nil
}

// ForwardStream 流式透传：客户端边收边转发，同时 Tee 解析 usage。
func (e *Engine) ForwardStream(ctx context.Context, res *Resolved, body []byte, clientHeaders http.Header, write func([]byte) error) (*RelayResult, error) {
	client, err := e.buildClient(res)
	if err != nil {
		return nil, err
	}
	target := strings.TrimRight(res.Account.BaseURL, "/") + "/v1/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	applyHeaders(req, res, clientHeaders)

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	capture := NewUsageCapture()
	tee := io.TeeReader(resp.Body, capture)
	reader := bufio.NewReader(tee)

	// 先透传响应头里的关键审计字段给调用方
	reqID := resp.Header.Get("request-id")

	chunk := make([]byte, 32*1024)
	for {
		n, readErr := reader.Read(chunk)
		if n > 0 {
			if err := write(chunk[:n]); err != nil {
				return nil, err // 客户端断开
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	capture.Parse()
	latency := int(time.Since(start).Milliseconds())
	snap := capture.Snapshot()

	return &RelayResult{
		Header: resp.Header, StatusCode: resp.StatusCode,
		Usage: snap, ActualModel: snap.ActualModel,
		UpstreamRequestID: reqID, TotalLatencyMS: latency, IsStream: true,
	}, nil
}

func (e *Engine) buildClient(res *Resolved) (*http.Client, error) {
	// Fail-Closed 最终闸门：require_proxy 且无有效代理 URL => 拒绝
	if res.Account.RequireProxy && res.ProxyURL == "" {
		return nil, ErrProxyRequired
	}
	if res.ProxyURL != "" {
		return e.mgr.GetClient(res.ProxyURL)
	}
	// 未要求代理的场景：仍用带 SSRF 校验的直连 client（不走宿主机裸奔无校验）
	return e.mgr.GetClientDirect(), nil
}

// applyHeaders 精确过滤客户端头，注入上游认证；保留原生协议头。
func applyHeaders(req *http.Request, res *Resolved, client http.Header) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+res.Credential)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20,claude-code-20250219,interleaved-thinking-2025-05-14,fine-grained-tool-streaming-2025-05-14")
	req.Header.Set("User-Agent", "claude-cli/1.0.58 (external, cli)")

	// 透传客户端原生协议头（如 anthropic-beta 覆盖），但绝不带入任何用户侧代理/认证头
	for _, k := range []string{"Anthropic-Version", "Anthropic-Beta"} {
		if v := client.Get(k); v != "" {
			req.Header.Set(k, v)
		}
	}
}

// ProxyIDForHeader 用于审计响应头（不敏感）。
func (r *Resolved) ProxyIDForHeader() string {
	if r.Proxy == nil {
		return ""
	}
	return r.Proxy.ID
}

// ClientFor 供 count_tokens 等辅助端点复用隔离客户端。
func (e *Engine) ClientFor(res *Resolved) (*http.Client, error) {
	return e.buildClient(res)
}

// LogFromResult 构造 usage_logs 记录。
func (r *Resolved) LogFromResult(result *RelayResult, requestedModel string) *model.UsageLog {
	log := &model.UsageLog{
		UserID:              r.Key.UserID,
		APIKeyID:            &r.Key.ID,
		UpstreamAccountID:   &r.Account.ID,
		RequestedModel:      requestedModel,
		ActualModel:         result.ActualModel,
		UpstreamRequestID:   result.UpstreamRequestID,
		EgressIPSnapshot:    r.EgressIP,
		StatusCode:          result.StatusCode,
		IsStream:            result.IsStream,
		InputTokens:         result.Usage.InputTokens,
		OutputTokens:        result.Usage.OutputTokens,
		CacheCreationTokens: result.Usage.CacheCreation,
		CacheReadTokens:     result.Usage.CacheRead,
		TTFTMS:              result.Usage.TTFTMS,
		TotalLatencyMS:      result.TotalLatencyMS,
	}
	if r.Proxy != nil {
		log.ProxyID = &r.Proxy.ID
	}
	return log
}

// Fingerprint 便于审计：API Key hash 前缀。
func KeyFingerprint(hash string) string {
	s := sha256.Sum256([]byte(hash))
	return hex.EncodeToString(s[:8])
}
