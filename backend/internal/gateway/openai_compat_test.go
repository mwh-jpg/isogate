package gateway

import (
	"strings"
	"testing"
)

func TestConvertOpenAIToAnthropic(t *testing.T) {
	body := []byte(`{
		"model":"claude-sonnet-4-5",
		"max_tokens":100,
		"messages":[
			{"role":"system","content":"You are helpful"},
			{"role":"user","content":"hello"}
		],
		"stream":false
	}`)
	converted, model, stream, err := ConvertOpenAIToAnthropic(body)
	if err != nil {
		t.Fatal(err)
	}
	if model != "claude-sonnet-4-5" || stream {
		t.Fatalf("元数据错误: %s %v", model, stream)
	}
	s := string(converted)
	if !strings.Contains(s, `"system":"You are helpful"`) {
		t.Errorf("system 消息未转换: %s", s)
	}
	if !strings.Contains(s, `"role":"user"`) || !strings.Contains(s, `"max_tokens":100`) {
		t.Errorf("消息转换错误: %s", s)
	}
}

func TestConvertAnthropicToOpenAIResp(t *testing.T) {
	anthropic := []byte(`{
		"id":"msg_1","model":"claude-opus-4-5",
		"content":[{"type":"text","text":"Hello!"}],
		"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":3}
	}`)
	got := string(ConvertAnthropicToOpenAIResp(anthropic, "claude-opus-4-5"))
	for _, want := range []string{`"object":"chat.completion"`, `"content":"Hello!"`, `"finish_reason":"stop"`, `"prompt_tokens":10`, `"completion_tokens":3`} {
		if !strings.Contains(got, want) {
			t.Errorf("缺少 %s: %s", want, got)
		}
	}
}

func TestContentToStringArray(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"system","content":[{"type":"text","text":"A"},{"type":"text","text":"B"}]},{"role":"user","content":"hi"}]}`)
	converted, _, _, _ := ConvertOpenAIToAnthropic(body)
	if !strings.Contains(string(converted), `"A\nB"`) {
		t.Errorf("数组 system 未合并: %s", converted)
	}
}
