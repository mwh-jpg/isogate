package gateway

import (
	"testing"
	"time"
)

func TestClassifyErrors(t *testing.T) {
	cfg := testConfig()
	cfg.Cooldown429 = 30 * time.Second
	cfg.Cooldown5xx = 60 * time.Second

	cases := []struct {
		status int
		body   string
		action Action
	}{
		{401, "invalid api key", ActionCredential},
		{403, "forbidden", ActionCredential},
		{429, "rate limit", ActionCooldown},
		{529, "overloaded", ActionCooldown},
		{500, "internal", ActionCooldown},
		{400, "bad request", ActionNone},
		{200, "ok", ActionNone},
	}
	for _, tc := range cases {
		d := ClassifyUpstreamError(tc.status, tc.body, cfg)
		if d.Action != tc.action {
			t.Errorf("status=%d 期望 %s 实际 %s", tc.status, tc.action, d.Action)
		}
	}
	if d := ClassifyUpstreamError(429, "", cfg); d.Cooldown != 30*time.Second {
		t.Errorf("429 冷却时间错误: %v", d.Cooldown)
	}
}
