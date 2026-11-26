package gateway

import (
	"strings"
	"testing"
	"time"
)

func TestParseSSEUsage(t *testing.T) {
	stream := `event: message_start
data: {"type":"message_start","message":{"model":"claude-sonnet-4-5-20250929","usage":{"input_tokens":100,"cache_creation_input_tokens":50,"cache_read_input_tokens":20,"output_tokens":1}}}

event: content_block_delta
data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}

event: message_delta
data: {"type":"message_delta","usage":{"output_tokens":42},"delta":{"stop_reason":"end_turn"}}

event: message_stop
data: {"type":"message_stop"}

`
	cap := NewUsageCapture()
	_, _ = cap.Write([]byte(stream))
	cap.Parse()
	snap := cap.Snapshot()
	if snap.ActualModel != "claude-sonnet-4-5-20250929" {
		t.Errorf("model 错误: %s", snap.ActualModel)
	}
	if snap.InputTokens != 100 || snap.CacheCreation != 50 || snap.CacheRead != 20 || snap.OutputTokens != 42 {
		t.Errorf("usage 错误: %+v", snap)
	}
}

func TestParseNonStream(t *testing.T) {
	body := []byte(`{"id":"msg_x","model":"claude-opus-4-5","usage":{"input_tokens":13,"output_tokens":7,"cache_read_input_tokens":100}}`)
	snap := ParseNonStream(body)
	if snap.ActualModel != "claude-opus-4-5" || snap.InputTokens != 13 || snap.OutputTokens != 7 || snap.CacheRead != 100 {
		t.Errorf("解析错误: %+v", snap)
	}
}

func TestTTFT(t *testing.T) {
	cap := NewUsageCapture()
	time.Sleep(5 * time.Millisecond)
	_, _ = cap.Write([]byte("data: x"))
	if cap.TTFT() < 4 {
		t.Errorf("TTFT 应约 5ms，实际 %d", cap.TTFT())
	}
}

func TestTeeDoesNotAlter(t *testing.T) {
	cap := NewUsageCapture()
	src := strings.NewReader("hello world")
	tee := TeeReader(src, cap)
	buf := make([]byte, 11)
	_, _ = tee.Read(buf)
	if string(buf) != "hello world" {
		t.Errorf("Tee 改动了数据: %s", buf)
	}
}
