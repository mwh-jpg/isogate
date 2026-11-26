package handler

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/michaelchou/isolated-ai-gateway/internal/config"
	"github.com/michaelchou/isolated-ai-gateway/internal/crypto"
	"github.com/michaelchou/isolated-ai-gateway/internal/proxy"
	"github.com/michaelchou/isolated-ai-gateway/internal/repository"
)

// AdminHandler 管理员后台。所有路由必须在 UserAuth + AdminOnly 之后注册。
type AdminHandler struct {
	cfg  *config.Config
	repo *repository.Repo
	res  *ResourceHandler
}

func NewAdmin(cfg *config.Config, repo *repository.Repo, res *ResourceHandler) *AdminHandler {
	return &AdminHandler{cfg: cfg, repo: repo, res: res}
}

func (h *AdminHandler) Overview(c *gin.Context) {
	ctx := c.Request.Context()
	users, err := h.repo.ListUsers(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	proxies, err := h.repo.ListAllProxies(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	logs, err := h.repo.ListAllUsage(ctx, 1000)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	active := 0
	for _, u := range users {
		if u.PlanStatus == "active" {
			active++
		}
	}
	mismatch := 0
	for _, l := range logs {
		if l.ActualModel != "" && l.ActualModel != l.RequestedModel {
			mismatch++
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"users": len(users), "active_plans": active, "assigned_ips": len(proxies),
		"requests": len(logs), "model_mismatch": mismatch,
	})
}

func (h *AdminHandler) Users(c *gin.Context) {
	users, err := h.repo.ListUsers(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	out := make([]gin.H, 0, len(users))
	for _, u := range users {
		row := gin.H{
			"id": u.ID, "email": u.Email, "role": u.Role, "status": u.Status,
			"plan_status": u.PlanStatus, "plan_expires_at": u.PlanExpiresAt,
			"max_concurrency": u.MaxConcurrency, "created_at": u.CreatedAt,
		}
		p, err := h.repo.GetAutoProxy(c.Request.Context(), u.ID)
		if err == nil {
			row["proxy_id"] = p.ID
			row["assigned_ip"] = strPtr(p.LastEgressIP)
			row["proxy_health"] = p.HealthStatus
		}
		out = append(out, row)
	}
	c.JSON(http.StatusOK, out)
}

func strPtr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (h *AdminHandler) Proxies(c *gin.Context) {
	list, err := h.repo.ListAllProxies(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, list)
}

func (h *AdminHandler) Logs(c *gin.Context) {
	limit := 200
	if v := c.Query("limit"); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	list, err := h.repo.ListAllUsage(c.Request.Context(), limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, list)
}

type provisionReq struct {
	Name     string `json:"name"`
	Scheme   string `json:"scheme" binding:"required"`
	Host     string `json:"host" binding:"required"`
	Port     int    `json:"port" binding:"required"`
	Username string `json:"username"`
	Password string `json:"password"`
	PlanDays int    `json:"plan_days"`
}

// ProvisionIP 为单个用户创建/替换自动分配的静态 IP，并立即探测。
// 当前为管理员手动占位入口；接入用户提供的静态 IP 获取接口后，由付款回调调用同样逻辑。
func (h *AdminHandler) ProvisionIP(c *gin.Context) {
	userID := c.Param("id")
	if _, err := h.repo.GetUserByID(c.Request.Context(), userID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	var req provisionReq
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
	if err := proxy.NewSSRFGuard(h.cfg.AllowPrivateUpstream).ValidateHost(req.Host); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	name := req.Name
	if name == "" {
		name = "专属静态 IP"
	}
	var userEnc, passEnc *string
	if req.Username != "" {
		v, err := crypto.Encrypt(h.cfg.EncryptionKey, []byte(req.Username))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "用户名加密失败"})
			return
		}
		userEnc = &v
	}
	if req.Password != "" {
		v, err := crypto.Encrypt(h.cfg.EncryptionKey, []byte(req.Password))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "密码加密失败"})
			return
		}
		passEnc = &v
	}
	p, err := h.repo.UpsertAutoProxy(c.Request.Context(), userID, name, req.Scheme, req.Host, req.Port, userEnc, passEnc)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	days := req.PlanDays
	if days <= 0 {
		days = 30
	}
	expires := time.Now().Add(time.Duration(days) * 24 * time.Hour)
	if err := h.repo.SetUserPlan(c.Request.Context(), userID, "active", &expires); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	probe := h.res.doProbe(c.Request.Context(), userID, p.ID)
	fresh, _ := h.repo.GetAutoProxy(c.Request.Context(), userID)
	c.JSON(http.StatusOK, gin.H{"proxy": fresh, "probe": probe})
}

type planReq struct {
	PlanStatus     *string `json:"plan_status"`
	PlanDays       *int    `json:"plan_days"`
	MaxConcurrency *int    `json:"max_concurrency"`
	Status         *string `json:"status"`
}

// UpdatePlan 管理员修改套餐状态、续期天数、并发上限与账号状态。
// 套餐状态参与网关出站鉴权：非 active 立即 Fail-Closed。
func (h *AdminHandler) UpdatePlan(c *gin.Context) {
	userID := c.Param("id")
	if _, err := h.repo.GetUserByID(c.Request.Context(), userID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	var req planReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.PlanStatus != nil {
		switch *req.PlanStatus {
		case "unpaid", "active", "expired", "cancelled":
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "plan_status 非法"})
			return
		}
	}
	if req.Status != nil {
		switch *req.Status {
		case "active", "disabled":
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "status 非法"})
			return
		}
	}
	if req.MaxConcurrency != nil && *req.MaxConcurrency < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "max_concurrency 必须大于 0"})
		return
	}
	var expires *time.Time
	if req.PlanDays != nil && *req.PlanDays > 0 {
		t := time.Now().Add(time.Duration(*req.PlanDays) * 24 * time.Hour)
		expires = &t
	}
	if err := h.repo.UpdateUserAdmin(c.Request.Context(), userID, req.PlanStatus, expires, req.MaxConcurrency, req.Status); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	u, _ := h.repo.GetUserByID(c.Request.Context(), userID)
	c.JSON(http.StatusOK, gin.H{
		"id": u.ID, "email": u.Email, "role": u.Role, "status": u.Status,
		"plan_status": u.PlanStatus, "plan_expires_at": u.PlanExpiresAt,
		"max_concurrency": u.MaxConcurrency,
	})
}

// RevokeIP 回收用户的专属静态 IP，并同步把套餐置为 expired。
// 删除后上游账号 proxy_id 由外键 ON DELETE SET NULL 置空，网关立即 Fail-Closed。
func (h *AdminHandler) RevokeIP(c *gin.Context) {
	userID := c.Param("id")
	if _, err := h.repo.GetUserByID(c.Request.Context(), userID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	n, err := h.repo.DeleteAutoProxy(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	expired := "expired"
	if err := h.repo.UpdateUserAdmin(c.Request.Context(), userID, &expired, nil, nil, nil); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"revoked": n, "plan_status": "expired"})
}

// ReprobeIP 重新探测某条静态 IP 的健康状态（管理端可跨用户操作）。
func (h *AdminHandler) ReprobeIP(c *gin.Context) {
	proxyID := c.Param("id")
	p, err := h.repo.GetProxyByID(c.Request.Context(), proxyID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "proxy not found"})
		return
	}
	probe := h.res.doProbe(c.Request.Context(), p.UserID, p.ID)
	fresh, _ := h.repo.GetProxyByID(c.Request.Context(), p.ID)
	c.JSON(http.StatusOK, gin.H{"proxy": fresh, "probe": probe})
}
