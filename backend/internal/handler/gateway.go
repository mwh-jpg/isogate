package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/michaelchou/isolated-ai-gateway/internal/config"
	"github.com/michaelchou/isolated-ai-gateway/internal/crypto"
	"github.com/michaelchou/isolated-ai-gateway/internal/gateway"
	"github.com/michaelchou/isolated-ai-gateway/internal/repository"
)

type GatewayHandler struct {
	cfg        *config.Config
	repo       *repository.Repo
	resolver   *gateway.Resolver
	engine     *gateway.Engine
	auditChain *gateway.AuditChain
}

func NewGateway(cfg *config.Config, repo *repository.Repo, resolver *gateway.Resolver, engine *gateway.Engine, chain *gateway.AuditChain) *GatewayHandler {
	return &GatewayHandler{cfg: cfg, repo: repo, resolver: resolver, engine: engine, auditChain: chain}
}

func (h *GatewayHandler) resolve(c *gin.Context) (*gateway.Resolved, bool) {
	raw := extractKey(c)
	if raw == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing api key"})
		return nil, false
	}
	res, err := h.resolver.Resolve(c.Request.Context(), crypto.HashAPIKey(raw))
	if err != nil {
		status := http.StatusForbidden
		switch {
		case err == gateway.ErrKeyInvalid:
			status = http.StatusUnauthorized
		case err == gateway.ErrAccountDown:
			status = http.StatusServiceUnavailable
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return nil, false
	}
	return res, true
}

func extractKey(c *gin.Context) string {
	for _, src := range []string{c.GetHeader("Authorization"), c.GetHeader("x-api-key")} {
		if strings.HasPrefix(src, "Bearer ") {
			src = strings.TrimSpace(src[7:])
		}
		if strings.HasPrefix(src, "sk-iso-") {
			return src
		}
	}
	return ""
}

// Messages 透传：支持 stream 与非流式。
func (h *GatewayHandler) Messages(c *gin.Context) {
	res, ok := h.resolve(c)
	if !ok {
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, h.cfg.MaxUpstreamBody))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "read body failed"})
		return
	}
	var probe struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(body, &probe); err != nil || probe.Model == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid messages body"})
		return
	}
	// 模型白名单校验
	if len(res.Key.AllowedModels) > 0 && !contains(res.Key.AllowedModels, probe.Model) {
		c.JSON(http.StatusForbidden, gin.H{"error": gateway.ErrModelNotAllow.Error()})
		return
	}

	setAuditHeaders(c, res)

	// Phase 4: RPM 限流 + 账号级并发锁（TTL 自动释放）
	release, err := h.engine.Guard(c.Request.Context(), res)
	if err != nil {
		status := http.StatusTooManyRequests
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	defer release(c.Request.Context())

	if probe.Stream {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("X-Accel-Buffering", "no")
		result, err := h.engine.ForwardStream(c.Request.Context(), res, body, c.Request.Header, func(chunk []byte) error {
			_, werr := c.Writer.Write(chunk)
			if werr == nil {
				c.Writer.Flush() // 低延迟透传，禁止缓冲
			}
			return werr
		})
		if err != nil {
			return
		}
		h.engine.HandleUpstreamStatus(c.Request.Context(), res, result.StatusCode, result.Body)
		c.Header("X-Gateway-Upstream-Request-Id", result.UpstreamRequestID)
		c.Header("X-Gateway-Actual-Model", result.ActualModel)
		h.writeAudit(result, res, probe.Model)
		return
	}

	result, err := h.engine.Forward(c.Request.Context(), res, body, c.Request.Header)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	h.engine.HandleUpstreamStatus(c.Request.Context(), res, result.StatusCode, result.Body)
	c.Header("X-Gateway-Upstream-Request-Id", result.UpstreamRequestID)
	c.Header("X-Gateway-Actual-Model", result.ActualModel)
	for k, vals := range result.Header {
		if strings.HasPrefix(strings.ToLower(k), "content-") || strings.HasPrefix(strings.ToLower(k), "x-gateway-") {
			for _, v := range vals {
				c.Header(k, v)
			}
		}
	}
	h.writeAudit(result, res, probe.Model)
	c.Data(result.StatusCode, "application/json", result.Body)
}

// ChatCompletions OpenAI 兼容端点：转换后走同一隔离转发链路。
func (h *GatewayHandler) ChatCompletions(c *gin.Context) {
	res, ok := h.resolve(c)
	if !ok {
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, h.cfg.MaxUpstreamBody))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "read body failed"})
		return
	}
	converted, model, stream, err := gateway.ConvertOpenAIToAnthropic(body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(res.Key.AllowedModels) > 0 && !contains(res.Key.AllowedModels, model) {
		c.JSON(http.StatusForbidden, gin.H{"error": gateway.ErrModelNotAllow.Error()})
		return
	}
	release, err := h.engine.Guard(c.Request.Context(), res)
	if err != nil {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": err.Error()})
		return
	}
	defer release(c.Request.Context())

	if stream {
		c.Header("Content-Type", "text/event-stream")
		c.Header("X-Accel-Buffering", "no")
		_, err := h.engine.ForwardStream(c.Request.Context(), res, converted, c.Request.Header, func(chunk []byte) error {
			_, werr := c.Writer.Write(chunk)
			if werr == nil {
				c.Writer.Flush()
			}
			return werr
		})
		if err != nil {
			return
		}
		return
	}
	result, err := h.engine.Forward(c.Request.Context(), res, converted, c.Request.Header)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	h.engine.HandleUpstreamStatus(c.Request.Context(), res, result.StatusCode, result.Body)
	c.Data(result.StatusCode, "application/json", gateway.ConvertAnthropicToOpenAIResp(result.Body, model))
}

func (h *GatewayHandler) CountTokens(c *gin.Context) {
	res, ok := h.resolve(c)
	if !ok {
		return
	}
	body, _ := io.ReadAll(io.LimitReader(c.Request.Body, h.cfg.MaxUpstreamBody))
	client, err := h.engine.ClientFor(res)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	target := strings.TrimRight(res.Account.BaseURL, "/") + "/v1/messages/count_tokens"
	req, _ := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, target, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+res.Credential)
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := client.Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, h.cfg.MaxUpstreamBody))
	c.Data(resp.StatusCode, "application/json", data)
}

func (h *GatewayHandler) Models(c *gin.Context) {
	res, ok := h.resolve(c)
	if !ok {
		return
	}
	models := []string{"claude-opus-4-5-20251101", "claude-sonnet-4-5-20250929", "claude-haiku-4-5-20251001"}
	if len(res.Key.AllowedModels) > 0 {
		models = res.Key.AllowedModels
	}
	list := make([]gin.H, 0, len(models))
	for _, m := range models {
		list = append(list, gin.H{"id": m, "object": "model", "type": "model"})
	}
	c.JSON(http.StatusOK, gin.H{"object": "list", "data": list})
}

func (h *GatewayHandler) writeAudit(result *gateway.RelayResult, res *gateway.Resolved, requestedModel string) {
	log := res.LogFromResult(result, requestedModel)
	// 审计写入不阻塞响应：独立 goroutine + 超时 context
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		logID, err := h.repo.InsertUsage(ctx, log)
		if err != nil || h.auditChain == nil {
			return
		}
		// 哈希链只记录元数据摘要，绝不记录提示词正文/凭证
		_ = h.auditChain.Append(ctx, &log.UserID, logID, "usage", map[string]any{
			"requested_model":     log.RequestedModel,
			"actual_model":        log.ActualModel,
			"upstream_request_id": log.UpstreamRequestID,
			"egress_ip":           log.EgressIPSnapshot,
			"input_tokens":        log.InputTokens,
			"output_tokens":       log.OutputTokens,
			"status_code":         log.StatusCode,
		})
	}()
}

func setAuditHeaders(c *gin.Context, res *gateway.Resolved) {
	c.Header("X-Gateway-Egress-Proxy-Id", res.ProxyIDForHeader())
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
