package middleware

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	"github.com/michaelchou/isolated-ai-gateway/internal/config"
	"github.com/michaelchou/isolated-ai-gateway/internal/repository"
)

type ctxKey string

const (
	CtxUserID ctxKey = "user_id"
	CtxRole   ctxKey = "role"
)

type Claims struct {
	UserID string `json:"uid"`
	Role   string `json:"role"`
	jwt.RegisteredClaims
}

func IssueJWT(cfg *config.Config, userID, role string) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID: userID,
		Role:   role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(cfg.JWTExpiry)),
			IssuedAt:  jwt.NewNumericDate(now),
			Issuer:    "isolated-ai-gateway",
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(cfg.JWTSecret)
}

// UserAuth JWT 中间间：用于管理后台 API。
func UserAuth(cfg *config.Config, repo *repository.Repo) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.GetHeader("Authorization")
		if !strings.HasPrefix(h, "Bearer ") || strings.Contains(h, "sk-iso-") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing bearer token"})
			return
		}
		raw := strings.TrimSpace(h[7:])
		claims := &Claims{}
		tok, err := jwt.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
			if t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
				return nil, jwt.ErrSignatureInvalid
			}
			return cfg.JWTSecret, nil
		})
		if err != nil || !tok.Valid {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
			return
		}
		u, err := repo.GetUserByID(c.Request.Context(), claims.UserID)
		if err != nil || u.Status != "active" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "user inactive"})
			return
		}
		c.Set(string(CtxUserID), u.ID)
		c.Set(string(CtxRole), u.Role)
		c.Next()
	}
}

func CurrentUserID(c *gin.Context) string {
	v, _ := c.Get(string(CtxUserID))
	s, _ := v.(string)
	return s
}

func CurrentRole(c *gin.Context) string {
	v, _ := c.Get(string(CtxRole))
	s, _ := v.(string)
	return s
}

// AdminOnly 必须在 UserAuth 之后使用。
func AdminOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		if CurrentRole(c) != "admin" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "admin only"})
			return
		}
		c.Next()
	}
}

// APIKeyAuth 在 Phase 3 由 gateway 使用：解析 sk-iso- key。
// 这里提供公共解析，具体绑定校验在 gateway 层完成（Fail-Closed）。
func ParseAPIKey(h string) (string, bool) {
	if !strings.HasPrefix(h, "Bearer ") && !strings.HasPrefix(h, "x-api-key ") {
		return "", false
	}
	raw := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(h, "Bearer "), "x-api-key "))
	if !strings.HasPrefix(raw, "sk-iso-") {
		return "", false
	}
	return raw, true
}

// ContextWithUser 便于异步 goroutine 传递。
func ContextWithUser(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, CtxUserID, userID)
}
