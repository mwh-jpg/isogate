package handler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/michaelchou/isolated-ai-gateway/internal/config"
	"github.com/michaelchou/isolated-ai-gateway/internal/crypto"
	"github.com/michaelchou/isolated-ai-gateway/internal/middleware"
	"github.com/michaelchou/isolated-ai-gateway/internal/proxy"
	"github.com/michaelchou/isolated-ai-gateway/internal/repository"
)

type ResourceHandler struct {
	cfg      *config.Config
	repo     *repository.Repo
	mgr      *proxy.Manager
	probeURL string
}

func NewResource(cfg *config.Config, repo *repository.Repo, mgr *proxy.Manager) *ResourceHandler {
	return &ResourceHandler{cfg: cfg, repo: repo, mgr: mgr, probeURL: "https://ipinfo.io/json"}
}

// decryptStr 解密 DB 中的密文串。
func (h *ResourceHandler) decryptStr(enc *string) (string, error) {
	if enc == nil || *enc == "" {
		return "", nil
	}
	b, err := crypto.Decrypt(h.cfg.EncryptionKey, *enc)
	return string(b), err
}

// ---------- Proxies ----------

type proxyReq struct {
	Name        string `json:"name" binding:"required"`
	Scheme      string `json:"scheme" binding:"required"`
	Host        string `json:"host" binding:"required"`
	Port        int    `json:"port" binding:"required"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	ProbeOnSave bool   `json:"probe_on_save"`
}

func (h *ResourceHandler) CreateProxy(c *gin.Context) {
	if middleware.CurrentRole(c) != "admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "静态 IP 由系统付款后自动分配，用户不可自行添加代理"})
		return
	}
	uid := middleware.CurrentUserID(c)
	var req proxyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Scheme != "http" && req.Scheme != "https" && req.Scheme != "socks5" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "scheme 仅支持 http/https/socks5"})
		return
	}
	if req.Port < 1 || req.Port > 65535 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "port 非法"})
		return
	}
	// SSRF: 字面量私网代理地址直接拒绝
	guard := proxy.NewSSRFGuard(h.cfg.AllowPrivateUpstream)
	if err := guard.ValidateHost(req.Host); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var userEnc, passEnc *string
	if req.Username != "" {
		v, _ := crypto.Encrypt(h.cfg.EncryptionKey, []byte(req.Username))
		userEnc = &v
	}
	if req.Password != "" {
		v, _ := crypto.Encrypt(h.cfg.EncryptionKey, []byte(req.Password))
		passEnc = &v
	}
	p, err := h.repo.CreateProxy(c.Request.Context(), uid, req.Name, req.Scheme, req.Host, req.Port, userEnc, passEnc)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	result := gin.H{"proxy": p}
	if req.ProbeOnSave {
		result["probe"] = h.doProbe(c.Request.Context(), uid, p.ID)
		// 返回探测后的最新快照，避免响应里仍显示 unknown 造成误判
		if fresh, err := h.repo.GetProxy(c.Request.Context(), uid, p.ID); err == nil {
			result["proxy"] = fresh
		}
	}
	c.JSON(http.StatusOK, result)
}

func (h *ResourceHandler) ListProxies(c *gin.Context) {
	list, err := h.repo.ListProxies(c.Request.Context(), middleware.CurrentUserID(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, list)
}

func (h *ResourceHandler) DeleteProxy(c *gin.Context) {
	if middleware.CurrentRole(c) != "admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "静态 IP 由系统统一管理，用户不可自行删除"})
		return
	}
	uid := middleware.CurrentUserID(c)
	if err := h.repo.DeleteProxy(c.Request.Context(), uid, c.Param("id")); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "proxy not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *ResourceHandler) ProbeProxy(c *gin.Context) {
	uid := middleware.CurrentUserID(c)
	res := h.doProbe(c.Request.Context(), uid, c.Param("id"))
	c.JSON(http.StatusOK, res)
}

func (h *ResourceHandler) doProbe(ctx context.Context, uid, proxyID string) proxy.ProbeResult {
	p, err := h.repo.GetProxy(ctx, uid, proxyID)
	if err != nil {
		return proxy.ProbeResult{Health: "unhealthy", Error: "proxy not found"}
	}
	user, _ := h.decryptStr(p.UsernameEnc)
	pass, _ := h.decryptStr(p.PasswordEnc)
	cred := ""
	if user != "" || pass != "" {
		cred = user + ":" + pass + "@"
	}
	raw := p.Scheme + "://" + cred + p.Host + ":" + itoa(p.Port)
	if p.Scheme == "socks5" {
		raw = "socks5://" + cred + p.Host + ":" + itoa(p.Port)
	}

	res := h.mgr.Probe(ctx, raw, h.probeURL, h.cfg.ProbeMaxBody)
	if err := h.repo.UpdateProxyHealth(ctx, uid, proxyID, res.EgressIP, res.Country, res.LatencyMS, res.Health, res.Error); err != nil {
		// 健康状态是 Fail-Closed 的唯一依据：落库失败必须视为不健康，并留下可排查日志。
		log.Printf("proxy health persist failed: proxy_id=%s user_id=%s err=%v", proxyID, uid, err)
		res.Health = "unhealthy"
		if res.Error == "" {
			res.Error = "健康状态落库失败"
		} else {
			res.Error += "；健康状态落库失败"
		}
	}
	return res
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

// ---------- Upstream accounts ----------

type accountReq struct {
	ProxyID      *string `json:"proxy_id"`
	Provider     string  `json:"provider" binding:"required"`
	Name         string  `json:"name" binding:"required"`
	BaseURL      string  `json:"base_url"`
	Credential   string  `json:"credential" binding:"required"`
	RequireProxy *bool   `json:"require_proxy"`
	MaxConc      int     `json:"max_concurrency"`
}

func (h *ResourceHandler) CreateAccount(c *gin.Context) {
	uid := middleware.CurrentUserID(c)
	var req accountReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	validProviders := map[string]bool{"anthropic_api": true, "aws_bedrock": true, "vertex_ai": true, "custom_anthropic": true}
	if !validProviders[req.Provider] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "非法 provider"})
		return
	}
	// 用户不可自选代理：强制绑定到系统付款后自动分配的专属静态 IP（零 IDOR）。
	p, err := h.repo.GetAutoProxy(c.Request.Context(), uid)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "套餐未开通或静态 IP 未分配，付款后系统将自动分配"})
		return
	}
	req.ProxyID = &p.ID
	base := req.BaseURL
	if base == "" {
		base = h.cfg.AnthropicBaseURL
	}
	// SSRF: 上游 base_url 字面量校验
	guard := proxy.NewSSRFGuard(h.cfg.AllowPrivateUpstream)
	if host := hostOf(base); host != "" {
		if err := guard.ValidateHost(host); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	credEnc, err := crypto.Encrypt(h.cfg.EncryptionKey, []byte(req.Credential))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "凭证加密失败"})
		return
	}
	// Fail-Closed 强制开启，用户不可关闭。
	requireProxy := true
	_ = req.RequireProxy
	maxConc := req.MaxConc
	if maxConc <= 0 {
		maxConc = 2
	}
	a, err := h.repo.CreateAccount(c.Request.Context(), uid, req.ProxyID, req.Provider, req.Name, base, credEnc, requireProxy, maxConc)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, a)
}

func (h *ResourceHandler) ListAccounts(c *gin.Context) {
	list, err := h.repo.ListAccounts(c.Request.Context(), middleware.CurrentUserID(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, list)
}

func (h *ResourceHandler) DeleteAccount(c *gin.Context) {
	uid := middleware.CurrentUserID(c)
	if err := h.repo.DeleteAccount(c.Request.Context(), uid, c.Param("id")); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "account not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *ResourceHandler) TestAccount(c *gin.Context) {
	// Phase 3 网关完成后提供真实上游连通性验证；此处先返回待实现占位
	c.JSON(http.StatusOK, gin.H{"status": "placeholder", "note": "Phase 3 网关完成后启用"})
}

// ---------- API keys ----------

type keyReq struct {
	AccountID     string   `json:"account_id" binding:"required"`
	Name          string   `json:"name" binding:"required"`
	AllowedModels []string `json:"allowed_models"`
	RPMLimit      int      `json:"rpm_limit"`
}

func (h *ResourceHandler) CreateKey(c *gin.Context) {
	uid := middleware.CurrentUserID(c)
	var req keyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// IDOR 防护：账号必须属于当前用户
	if _, err := h.repo.GetAccount(c.Request.Context(), uid, req.AccountID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "account 不属于当前用户"})
		return
	}
	full, prefix, hash, err := crypto.GenerateAPIKey()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	rpm := req.RPMLimit
	if rpm <= 0 {
		rpm = 60
	}
	k, err := h.repo.CreateKey(c.Request.Context(), uid, req.AccountID, req.Name, prefix, hash, req.AllowedModels, rpm)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// 明文仅此一次返回
	c.JSON(http.StatusOK, gin.H{
		"id": k.ID, "key": full, "key_prefix": prefix,
		"account_id": req.AccountID, "rpm_limit": rpm,
	})
}

func (h *ResourceHandler) ListKeys(c *gin.Context) {
	list, err := h.repo.ListKeys(c.Request.Context(), middleware.CurrentUserID(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, list)
}

func (h *ResourceHandler) RevokeKey(c *gin.Context) {
	uid := middleware.CurrentUserID(c)
	if err := h.repo.RevokeKey(c.Request.Context(), uid, c.Param("id")); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "key not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ---------- 日志 / 统计 ----------

func (h *ResourceHandler) ListLogs(c *gin.Context) {
	uid := middleware.CurrentUserID(c)
	limit := 100
	if v := c.Query("limit"); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	list, err := h.repo.ListUsage(c.Request.Context(), uid, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, list)
}

func (h *ResourceHandler) Stats(c *gin.Context) {
	uid := middleware.CurrentUserID(c)
	res, err := h.repo.StatsOverview(c.Request.Context(), uid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, res)
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}
