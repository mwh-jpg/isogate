package gateway

import (
	"context"
	"strings"
	"time"

	"github.com/michaelchou/isolated-ai-gateway/internal/config"
	"github.com/michaelchou/isolated-ai-gateway/internal/repository"
)

// ClassifyUpstreamError 对上游错误分类并给出处置策略。
// 核心原则：只作用于本账号，绝不自动切换到其他用户的账号。
type Action string

const (
	ActionNone       Action = "none"
	ActionCooldown   Action = "cooldown"
	ActionCredential Action = "credential_error"
)

type ErrorDecision struct {
	Action   Action
	Cooldown time.Duration
	Reason   string
}

func ClassifyUpstreamError(status int, body string, cfg *config.Config) ErrorDecision {
	switch {
	case status == 401 || status == 403:
		return ErrorDecision{Action: ActionCredential, Reason: "上游凭证无效(401/403)，需用户重新绑定"}
	case status == 429:
		return ErrorDecision{Action: ActionCooldown, Cooldown: cfg.Cooldown429, Reason: "上游限流(429)"}
	case status == 529 || status >= 500:
		return ErrorDecision{Action: ActionCooldown, Cooldown: cfg.Cooldown5xx, Reason: "上游过载/错误(5xx)"}
	case status >= 400:
		// 避免把完整上游响应（可能含敏感信息）写入日志
		lower := strings.ToLower(body)
		if strings.Contains(lower, "overloaded") {
			return ErrorDecision{Action: ActionCooldown, Cooldown: cfg.Cooldown5xx, Reason: "上游 overloaded"}
		}
	}
	return ErrorDecision{Action: ActionNone}
}

// ApplyDecision 将熔断决策落库（只作用于该账号自身）。
func ApplyDecision(ctx context.Context, repo *repository.Repo, accountID string, d ErrorDecision) error {
	switch d.Action {
	case ActionCooldown:
		return repo.SetAccountCooldown(ctx, accountID, time.Now().Add(d.Cooldown), "cooldown", d.Reason)
	case ActionCredential:
		return repo.SetAccountCooldown(ctx, accountID, time.Now().Add(24*time.Hour), "disabled", d.Reason)
	}
	return nil
}
