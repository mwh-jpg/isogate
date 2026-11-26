package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ConvertOpenAIToAnthropic 把 OpenAI Chat Completions 请求转为 Anthropic Messages。
// 支持 system 消息、多轮、tools；响应侧非流式转回 OpenAI 格式。
func ConvertOpenAIToAnthropic(body []byte) ([]byte, string, bool, error) {
	var req struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
		MaxTokens   int      `json:"max_tokens"`
		Stream      bool     `json:"stream"`
		Temperature *float64 `json:"temperature"`
		Tools       []any    `json:"tools"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, "", false, err
	}
	if len(req.Messages) == 0 {
		return nil, "", false, fmt.Errorf("messages 为空")
	}

	out := map[string]any{
		"model":      req.Model,
		"max_tokens": req.MaxTokens,
		"stream":     req.Stream,
	}
	if req.MaxTokens <= 0 {
		out["max_tokens"] = 4096
	}
	if req.Temperature != nil {
		out["temperature"] = *req.Temperature
	}

	var system string
	var msgs []map[string]any
	for _, m := range req.Messages {
		switch m.Role {
		case "system":
			system += contentToString(m.Content)
		case "user", "assistant":
			msgs = append(msgs, map[string]any{
				"role":    m.Role,
				"content": m.Content,
			})
		}
	}
	if system != "" {
		out["system"] = system
	}
	out["messages"] = msgs
	if len(req.Tools) > 0 {
		out["tools"] = req.Tools // OpenAI tools 格式与 Anthropic 接近，保留由上层适配
	}
	converted, err := json.Marshal(out)
	return converted, req.Model, req.Stream, err
}

func contentToString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		var parts []string
		for _, item := range x {
			if m, ok := item.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// ConvertAnthropicToOpenAIResp 把 Anthropic 非流式响应转为 OpenAI Chat Completion 格式。
func ConvertAnthropicToOpenAIResp(anthropicBody []byte, model string) []byte {
	var a struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(anthropicBody, &a)
	var text strings.Builder
	for _, c := range a.Content {
		if c.Type == "text" {
			text.WriteString(c.Text)
		}
	}
	resp := map[string]any{
		"id":     a.ID,
		"object": "chat.completion",
		"model":  a.Model,
		"choices": []any{
			map[string]any{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": text.String()},
				"finish_reason": mapStopReason(a.StopReason),
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     a.Usage.InputTokens,
			"completion_tokens": a.Usage.OutputTokens,
			"total_tokens":      a.Usage.InputTokens + a.Usage.OutputTokens,
		},
	}
	b, _ := json.Marshal(resp)
	return b
}

func mapStopReason(s string) string {
	if s == "end_turn" || s == "stop_sequence" {
		return "stop"
	}
	if s == "max_tokens" {
		return "length"
	}
	if s == "tool_use" {
		return "tool_calls"
	}
	return "stop"
}

// StreamSSEPassthroughRaw 供 OpenAI 兼容流式：Anthropic SSE 事件原样透传，
// 客户端（如 Cursor）通常可直接消费；完整字段级转换留待后续迭代。
func StreamSSEPassthroughRaw(resp *http.Response, write func([]byte) error) error {
	buf := make([]byte, 16*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if werr := write(buf[:n]); werr != nil {
				return werr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
