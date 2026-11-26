package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/michaelchou/isolated-ai-gateway/internal/config"
	"github.com/michaelchou/isolated-ai-gateway/internal/gateway"
	"github.com/michaelchou/isolated-ai-gateway/internal/handler"
	"github.com/michaelchou/isolated-ai-gateway/internal/middleware"
	"github.com/michaelchou/isolated-ai-gateway/internal/proxy"
	"github.com/michaelchou/isolated-ai-gateway/internal/ratelimit"
	"github.com/michaelchou/isolated-ai-gateway/internal/repository"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("配置错误: %v", err)
	}
	if !cfg.Debug {
		gin.SetMode(gin.ReleaseMode)
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("数据库连接失败: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("数据库不可达: %v", err)
	}

	rdb, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		log.Fatalf("REDIS_URL 非法: %v", err)
	}
	rds := redis.NewClient(rdb)
	defer rds.Close()
	if err := rds.Ping(ctx).Err(); err != nil {
		log.Fatalf("Redis 不可达: %v", err)
	}

	repo := repository.New(pool)
	guard := proxy.NewSSRFGuard(cfg.AllowPrivateUpstream)
	transportMgr := proxy.NewManager(guard, cfg.UpstreamTimeout)
	defer transportMgr.CloseAll()

	for _, f := range []string{"migrations/0001_init.sql", "migrations/0002_audit_chain.sql", "migrations/0003_auto_ip.sql", "migrations/0004_ip_pool.sql"} {
		migration, err := os.ReadFile(f)
		if err != nil {
			log.Fatalf("读取迁移文件失败 %s: %v", f, err)
		}
		if err := repo.Migrate(ctx, string(migration)); err != nil {
			log.Fatalf("迁移失败 %s: %v", f, err)
		}
	}
	auditChain := gateway.NewAuditChain(repo)

	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())
	if len(cfg.TrustedProxies) > 0 {
		_ = r.SetTrustedProxies(cfg.TrustedProxies)
	} else {
		_ = r.SetTrustedProxies(nil)
	}

	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// 公开审计校验：任何人可验证官方转发审计日志的哈希链完整性
	r.GET("/api/audit/verify", func(c *gin.Context) {
		res, err := auditChain.Verify(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, res)
	})

	// 控制台静态资源（免构建前端）
	r.Static("/console", "./frontend")

	auth := handler.NewAuth(cfg, repo)
	api := r.Group("/api/v1")
	{
		api.POST("/auth/register", auth.Register)
		api.POST("/auth/login", auth.Login)
		protected := api.Group("")
		protected.Use(middleware.UserAuth(cfg, repo))
		protected.GET("/auth/me", auth.Me)

		res := handler.NewResource(cfg, repo, transportMgr)
		// Proxies
		protected.POST("/proxies", res.CreateProxy)
		protected.GET("/proxies", res.ListProxies)
		protected.DELETE("/proxies/:id", res.DeleteProxy)
		protected.POST("/proxies/:id/probe", res.ProbeProxy)
		// Accounts
		protected.POST("/accounts", res.CreateAccount)
		protected.GET("/accounts", res.ListAccounts)
		protected.DELETE("/accounts/:id", res.DeleteAccount)
		protected.POST("/accounts/:id/test", res.TestAccount)
		// Keys
		protected.POST("/keys", res.CreateKey)
		protected.GET("/keys", res.ListKeys)
		protected.DELETE("/keys/:id", res.RevokeKey)
		// Logs & stats
		protected.GET("/logs", res.ListLogs)
		protected.GET("/stats/overview", res.Stats)

		// Admin-only 管理后台
		adminH := handler.NewAdmin(cfg, repo, res)
		admin := protected.Group("/admin")
		admin.Use(middleware.AdminOnly())
		admin.GET("/overview", adminH.Overview)
		admin.GET("/users", adminH.Users)
		admin.GET("/proxies", adminH.Proxies)
		admin.GET("/logs", adminH.Logs)
		admin.POST("/users/:id/provision-ip", adminH.ProvisionIP)
		admin.PATCH("/users/:id/plan", adminH.UpdatePlan)
		admin.DELETE("/users/:id/ip", adminH.RevokeIP)
		admin.POST("/proxies/:id/probe", adminH.ReprobeIP)
		// 静态 IP 资源池（库存）与自动分配
		admin.GET("/ip-pool", adminH.ListIPPool)
		admin.POST("/ip-pool", adminH.AddIPPool)
		admin.POST("/ip-pool/:id/probe", adminH.ProbeIPPool)
		admin.PATCH("/ip-pool/:id", adminH.SetIPPoolStatus)
		admin.DELETE("/ip-pool/:id", adminH.DeleteIPPool)
		admin.POST("/users/:id/assign-from-pool", adminH.AssignFromPool)
	}

	// Phase 3-4: 隔离透传网关 + 限流熔断 + OpenAI 兼容
	resolver := gateway.NewResolver(cfg, repo)
	engine := gateway.NewEngine(cfg, repo, transportMgr)
	engine.SetLimiter(ratelimit.New(rds))
	gw := handler.NewGateway(cfg, repo, resolver, engine, auditChain)
	r.POST("/v1/messages", gw.Messages)
	r.POST("/v1/messages/count_tokens", gw.CountTokens)
	r.GET("/v1/models", gw.Models)
	r.POST("/v1/chat/completions", gw.ChatCompletions)

	// Phase 5 路由在此注册

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("服务启动: %s (public=%s)", cfg.Addr, cfg.PublicBaseURL)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("服务异常: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("优雅停机中...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("停机错误: %v", err)
	}
	fmt.Println("已停止")
}
