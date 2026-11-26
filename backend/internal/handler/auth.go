package handler

import (
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/michaelchou/isolated-ai-gateway/internal/config"
	"github.com/michaelchou/isolated-ai-gateway/internal/crypto"
	"github.com/michaelchou/isolated-ai-gateway/internal/middleware"
	"github.com/michaelchou/isolated-ai-gateway/internal/repository"
)

type AuthHandler struct {
	cfg  *config.Config
	repo *repository.Repo
}

func NewAuth(cfg *config.Config, repo *repository.Repo) *AuthHandler {
	return &AuthHandler{cfg: cfg, repo: repo}
}

var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

type registerReq struct {
	Email    string `json:"email" binding:"required"`
	Password string `json:"password" binding:"required,min=8"`
}

func (h *AuthHandler) Register(c *gin.Context) {
	var req registerReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if !emailRe.MatchString(req.Email) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid email"})
		return
	}
	hash, err := crypto.HashPassword(req.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "hash failed"})
		return
	}
	u, err := h.repo.CreateUser(c.Request.Context(), req.Email, hash)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			c.JSON(http.StatusConflict, gin.H{"error": "email exists"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	token, err := middleware.IssueJWT(h.cfg, u.ID, u.Role)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "jwt failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"token": token, "user_id": u.ID, "role": u.Role})
}

type loginReq struct {
	Email    string `json:"email" binding:"required"`
	Password string `json:"password" binding:"required"`
}

func (h *AuthHandler) Login(c *gin.Context) {
	var req loginReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	u, err := h.repo.GetUserByEmail(c.Request.Context(), strings.ToLower(strings.TrimSpace(req.Email)))
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !crypto.VerifyPassword(req.Password, u.PasswordHash) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}
	token, err := middleware.IssueJWT(h.cfg, u.ID, u.Role)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "jwt failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"token": token, "user_id": u.ID, "role": u.Role})
}

func (h *AuthHandler) Me(c *gin.Context) {
	u, err := h.repo.GetUserByID(c.Request.Context(), middleware.CurrentUserID(c))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"id": u.ID, "email": u.Email, "role": u.Role, "status": u.Status,
		"plan_status": u.PlanStatus, "plan_expires_at": u.PlanExpiresAt,
		"max_concurrency": u.MaxConcurrency,
	})
}
