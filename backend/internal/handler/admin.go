package handler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/michaelchou/isolated-ai-gateway/internal/config"
	"github.com/michaelchou/isolated-ai-gateway/internal/crypto"
	"github.com/michaelchou/isolated-ai-gateway/internal/model"
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

// ProvisionIP 为单个用户创建/替换「占位」静态 IP，并立即探测。
// Phase 8「一账号一 IP」后正式业务由 CreateAccountWithProxyFromPool / AssignAccountIP 承担；
// 本入口仅为兼容保留：UpsertAutoProxy 只替换占位出口，ReleasePlaceholderIPPoolByUser
// 只释放占位池占用，绝不触碰任何账号专属 IP。
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
	// 只释放「占位」池占用；账号专属池条目必须保留，否则会把该 IP 放回库存被别的账号复用。
	if _, err := h.repo.ReleasePlaceholderIPPoolByUser(c.Request.Context(), userID); err != nil {
		log.Printf("release placeholder ip_pool for user failed: user_id=%s err=%v", userID, err)
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
	// 按 ID 精确取刚创建的占位出口；GetAutoProxy 按 created_at 取最新，在账号级模型下
	// 可能取到别的账号的专属出口。
	fresh, _ := h.repo.GetProxyByID(c.Request.Context(), p.ID)
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

// RevokeIP 用户级整体回收：删除该用户全部 source='auto' 出口（含账号专属），
// 并把全部 assigned 池条目释放回库存，最后套餐置 expired。数据一致但粒度较粗，
// 账号级精细回收请使用 RevokeAccountIP。
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
	// 若该用户的出口来自 ip_pool，同步把池中条目释放回 available，避免库存泄漏。
	released, err := h.repo.ReleaseIPPoolByUser(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	expired := "expired"
	if err := h.repo.UpdateUserAdmin(c.Request.Context(), userID, &expired, nil, nil, nil); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"revoked": n, "pool_released": released, "plan_status": "expired"})
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

// ---------- 静态 IP 资源池（库存）----------
// ip_pool 是「待分配的库存」，proxy_endpoints(source='auto') 是「已分配给用户的出口」。
// 管理员：批量录入 -> 探测验健康 -> 从池中自动分配；分配后条目变为 assigned，回收时释放回 available。

type ipPoolItemReq struct {
	Name     string `json:"name"`
	Scheme   string `json:"scheme"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	Note     string `json:"note"`
}

type ipPoolAddReq struct {
	Items  []ipPoolItemReq `json:"items"`
	Lines  string          `json:"lines"`  // 批量文本，每行一条，支持 # 注释
	Scheme string          `json:"scheme"` // 文本未写 scheme 时的默认协议
	Note   string          `json:"note"`   // 本批统一备注
}

type ipPoolStatusReq struct {
	Status string  `json:"status" binding:"required"`
	Note   *string `json:"note"`
}

type assignFromPoolReq struct {
	PlanDays int `json:"plan_days"`
}

// ListIPPool 返回池内全部条目与库存统计。
func (h *AdminHandler) ListIPPool(c *gin.Context) {
	list, err := h.repo.ListIPPool(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	available, healthy, assigned, disabled, allocatable := 0, 0, 0, 0, 0
	for _, p := range list {
		switch p.Status {
		case "available":
			available++
		case "assigned":
			assigned++
		case "disabled":
			disabled++
		}
		if p.HealthStatus == "healthy" {
			healthy++
			if p.Status == "available" {
				allocatable++
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"items": list,
		"stats": gin.H{
			"total": len(list), "available": available, "healthy": healthy,
			"assigned": assigned, "disabled": disabled, "allocatable": allocatable,
		},
	})
}

// parsePoolLine 解析批量录入的一行，支持三种格式：
//
//	scheme://user:pass@host:port   如 http://u:p@1.2.3.4:8022
//	host:port:user:pass            如 1.2.3.4:8022:u:p（默认 http）
//	host:port                      如 1.2.3.4:8022（默认 http，无认证）
//
// 空行与 # 开头的行为跳过，返回 nil 表示跳过。
func parsePoolLine(line, defaultScheme string) (*ipPoolItemReq, error) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return nil, nil
	}
	if defaultScheme == "" {
		defaultScheme = "http"
	}
	it := &ipPoolItemReq{Scheme: defaultScheme}
	if strings.Contains(line, "://") {
		u, err := url.Parse(line)
		if err != nil || u.Hostname() == "" {
			return nil, fmt.Errorf("无法解析 %q", line)
		}
		switch u.Scheme {
		case "http", "https", "socks5":
			it.Scheme = u.Scheme
		default:
			return nil, fmt.Errorf("不支持的 scheme %q", u.Scheme)
		}
		it.Host = u.Hostname()
		n, err := strconv.Atoi(u.Port())
		if err != nil {
			return nil, fmt.Errorf("%q 端口非法", line)
		}
		it.Port = n
		if u.User != nil {
			it.Username = u.User.Username()
			it.Password, _ = u.User.Password()
		}
		return it, nil
	}
	parts := strings.Split(line, ":")
	switch len(parts) {
	case 2:
		it.Host = parts[0]
	case 4:
		it.Host, it.Username, it.Password = parts[0], parts[2], parts[3]
	default:
		return nil, fmt.Errorf("格式应为 host:port 或 host:port:user:pass，得到 %q", line)
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil {
		return nil, fmt.Errorf("%q 端口非法", line)
	}
	it.Port = n
	return it, nil
}

// AddIPPool 批量往池里加 IP。逐条校验、逐条报错，端点重复由唯一索引自动去重。
// 只入库、不自动探测：新条目 health_status=unknown，需管理员点「探测」变为 healthy 后才可被分配。
func (h *AdminHandler) AddIPPool(c *gin.Context) {
	var req ipPoolAddReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	items := append([]ipPoolItemReq{}, req.Items...)
	parseErrors := []string{}
	lines := strings.Split(strings.ReplaceAll(req.Lines, "\r\n", "\n"), "\n")
	for i, line := range lines {
		it, err := parsePoolLine(line, req.Scheme)
		if err != nil {
			parseErrors = append(parseErrors, fmt.Sprintf("第 %d 行: %v", i+1, err))
			continue
		}
		if it != nil {
			items = append(items, *it)
		}
	}
	if len(items) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "没有可添加的 IP", "parse_errors": parseErrors})
		return
	}

	guard := proxy.NewSSRFGuard(h.cfg.AllowPrivateUpstream)
	poolItems := make([]*model.IPPoolItem, 0, len(items))
	validationErrors := []string{}
	for i := range items {
		it := &items[i]
		if it.Scheme == "" {
			it.Scheme = req.Scheme
		}
		if it.Scheme == "" {
			it.Scheme = "http"
		}
		if it.Note == "" {
			it.Note = req.Note
		}
		if it.Scheme != "http" && it.Scheme != "https" && it.Scheme != "socks5" {
			validationErrors = append(validationErrors, fmt.Sprintf("%s:%d scheme 非法", it.Host, it.Port))
			continue
		}
		if it.Host == "" || it.Port < 1 || it.Port > 65535 {
			validationErrors = append(validationErrors, fmt.Sprintf("%s:%d host/port 非法", it.Host, it.Port))
			continue
		}
		// 字面量私网/回环地址直接拒绝，域名在 Dial 阶段再做 IP 级校验。
		if err := guard.ValidateHost(it.Host); err != nil {
			validationErrors = append(validationErrors, fmt.Sprintf("%s:%d %v", it.Host, it.Port, err))
			continue
		}
		var userEnc, passEnc *string
		if it.Username != "" {
			v, err := crypto.Encrypt(h.cfg.EncryptionKey, []byte(it.Username))
			if err != nil {
				validationErrors = append(validationErrors, fmt.Sprintf("%s:%d 用户名加密失败", it.Host, it.Port))
				continue
			}
			userEnc = &v
		}
		if it.Password != "" {
			v, err := crypto.Encrypt(h.cfg.EncryptionKey, []byte(it.Password))
			if err != nil {
				validationErrors = append(validationErrors, fmt.Sprintf("%s:%d 密码加密失败", it.Host, it.Port))
				continue
			}
			passEnc = &v
		}
		poolItems = append(poolItems, &model.IPPoolItem{
			Name: it.Name, Scheme: it.Scheme, Host: it.Host, Port: it.Port,
			UsernameEnc: userEnc, PasswordEnc: passEnc, Note: it.Note,
		})
	}
	if len(poolItems) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "所有条目均未通过校验", "parse_errors": parseErrors, "errors": validationErrors,
		})
		return
	}
	added, err := h.repo.AddIPPoolBatch(c.Request.Context(), poolItems)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"added": added, "submitted": len(poolItems),
		"duplicates":   len(poolItems) - added,
		"parse_errors": parseErrors, "errors": validationErrors,
	})
}

// probePoolItem 探测池中单条 IP 并把出口/健康状态落库。
// 落库失败必须视为 unhealthy——健康状态是自动分配与 Fail-Closed 的唯一依据。
func (h *AdminHandler) probePoolItem(ctx context.Context, p *model.IPPoolItem) proxy.ProbeResult {
	user, _ := h.res.decryptStr(p.UsernameEnc)
	pass, _ := h.res.decryptStr(p.PasswordEnc)
	cred := ""
	if user != "" || pass != "" {
		cred = url.UserPassword(user, pass).String() + "@"
	}
	raw := p.Scheme + "://" + cred + p.Host + ":" + itoa(p.Port)
	if p.Scheme == "socks5" {
		raw = "socks5://" + cred + p.Host + ":" + itoa(p.Port)
	}
	res := h.res.mgr.Probe(ctx, raw, h.res.probeURL, h.cfg.ProbeMaxBody)
	if err := h.repo.UpdateIPPoolHealth(ctx, p.ID, res.EgressIP, res.Country, res.LatencyMS, res.Health, res.Error); err != nil {
		log.Printf("ip_pool health persist failed: id=%s err=%v", p.ID, err)
		res.Health = "unhealthy"
		if res.Error == "" {
			res.Error = "健康状态落库失败"
		} else {
			res.Error += "；健康状态落库失败"
		}
	}
	return res
}

// ProbeIPPool 探测池中某条 IP（已分配条目也允许重探，不改变分配状态）。
func (h *AdminHandler) ProbeIPPool(c *gin.Context) {
	p, err := h.repo.GetIPPool(c.Request.Context(), c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "pool item not found"})
		return
	}
	probe := h.probePoolItem(c.Request.Context(), p)
	fresh, _ := h.repo.GetIPPool(c.Request.Context(), p.ID)
	c.JSON(http.StatusOK, gin.H{"item": fresh, "probe": probe})
}

// SetIPPoolStatus 停用 / 重新启用池中条目；assigned 由自动分配产生，不允许手工指定。
func (h *AdminHandler) SetIPPoolStatus(c *gin.Context) {
	id := c.Param("id")
	var req ipPoolStatusReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Status != "available" && req.Status != "disabled" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "status 仅支持 available/disabled（assigned 由自动分配产生）"})
		return
	}
	cur, err := h.repo.GetIPPool(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "pool item not found"})
		return
	}
	if cur.Status == "assigned" {
		c.JSON(http.StatusConflict, gin.H{"error": "该 IP 已分配给用户，请先回收用户 IP 后再修改"})
		return
	}
	if err := h.repo.SetIPPoolStatus(c.Request.Context(), id, req.Status, req.Note); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "pool item not found or already assigned"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	fresh, _ := h.repo.GetIPPool(c.Request.Context(), id)
	c.JSON(http.StatusOK, gin.H{"item": fresh})
}

// DeleteIPPool 删除池中条目；已分配条目禁止删除，避免正在使用的出口凭空消失。
func (h *AdminHandler) DeleteIPPool(c *gin.Context) {
	id := c.Param("id")
	cur, err := h.repo.GetIPPool(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "pool item not found"})
		return
	}
	if cur.Status == "assigned" {
		c.JSON(http.StatusConflict, gin.H{"error": "该 IP 已分配给用户，请先回收用户 IP 后再删除"})
		return
	}
	if err := h.repo.DeleteIPPool(c.Request.Context(), id); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "pool item not found or already assigned"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "deleted": id})
}

// AssignFromPool 从池中自动取一条 available+healthy 的 IP 分配给用户。
// 这是「付款后自动设置 IP」的落地入口；接入真实静态 IP 供应商后，可由付款回调直接调用。
func (h *AdminHandler) AssignFromPool(c *gin.Context) {
	userID := c.Param("id")
	if _, err := h.repo.GetUserByID(c.Request.Context(), userID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	var req assignFromPoolReq
	_ = c.ShouldBindJSON(&req) // body 允许为空
	if req.PlanDays < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "plan_days 不能为负"})
		return
	}
	item, p, err := h.repo.AssignFromPool(c.Request.Context(), userID)
	if err != nil {
		if errors.Is(err, repository.ErrPoolEmpty) {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if req.PlanDays > 0 {
		expires := time.Now().Add(time.Duration(req.PlanDays) * 24 * time.Hour)
		if err := h.repo.SetUserPlan(c.Request.Context(), userID, "active", &expires); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"pool_item": item, "proxy": p, "plan_status": "active",
		"message": "已从池中分配占位静态 IP，并自动补绑该用户尚无出口的账号",
	})
}

// ---------- 账号维度 IP 管理（一个上游账号一个 IP）----------

// AdminAccounts 管理端列出全部上游账号及其专属静态 IP。
func (h *AdminHandler) AdminAccounts(c *gin.Context) {
	accounts, err := h.repo.ListAllAccounts(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	proxies, err := h.repo.ListAllProxies(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	byAccount := map[string]*model.ProxyEndpoint{}
	byID := map[string]*model.ProxyEndpoint{}
	for _, p := range proxies {
		byID[p.ID] = p
		if p.UpstreamAccountID != nil {
			byAccount[*p.UpstreamAccountID] = p
		}
	}
	out := make([]gin.H, 0, len(accounts))
	for _, a := range accounts {
		row := gin.H{
			"id": a.ID, "user_id": a.UserID, "proxy_id": a.ProxyID,
			"provider": a.Provider, "name": a.Name, "base_url": a.BaseURL,
			"status": a.Status, "max_concurrency": a.MaxConcurrency, "created_at": a.CreatedAt,
		}
		p := byAccount[a.ID]
		if p == nil && a.ProxyID != nil {
			p = byID[*a.ProxyID] // legacy 出口（upstream_account_id 为 NULL）兜底
		}
		if p != nil {
			row["assigned_ip"] = strPtr(p.LastEgressIP)
			row["proxy_health"] = p.HealthStatus
			row["assigned_proxy_id"] = p.ID
			row["exclusive"] = p.UpstreamAccountID != nil
		}
		out = append(out, row)
	}
	c.JSON(http.StatusOK, out)
}

// AssignAccountIP 从池中给指定账号分配/更换一条独立静态 IP。
func (h *AdminHandler) AssignAccountIP(c *gin.Context) {
	accountID := c.Param("id")
	acc, err := h.repo.GetAccountByID(c.Request.Context(), accountID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "account not found"})
		return
	}
	item, p, err := h.repo.AssignIPFromPoolForAccount(c.Request.Context(), acc.UserID, accountID)
	if err != nil {
		if errors.Is(err, repository.ErrPoolEmpty) {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "account not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"account_id": accountID, "user_id": acc.UserID,
		"pool_item": item, "proxy": p,
		"message": "已为该账号分配独立静态 IP，其他账号的出口不受影响",
	})
}

// RevokeAccountIP 回收某账号的专属 IP：删除出口、释放池条目；账号保留但立即 Fail-Closed。
func (h *AdminHandler) RevokeAccountIP(c *gin.Context) {
	accountID := c.Param("id")
	acc, err := h.repo.GetAccountByID(c.Request.Context(), accountID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "account not found"})
		return
	}
	proxies, released, err := h.repo.RevokeAccountIP(c.Request.Context(), acc.UserID, accountID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"proxy_deleted": proxies, "pool_released": released, "fail_closed": true,
	})
}
