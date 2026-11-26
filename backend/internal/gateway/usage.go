// Package gateway 提供 Anthropic Messages 透传引擎。
package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"time"
)

// UsageCapture 从 SSE 流中解析真实 usage（官方转发审计日志）。
// 实现 io.Writer，作为 TeeReader 的接收端，不改动透传给客户端的字节。
type UsageCapture struct {
	mu            sync.Mutex
	buf           bytes.Buffer
	ActualModel   string
	InputTokens   int
	OutputTokens  int
	CacheCreation int
	CacheRead     int
	FirstByteAt   time.Time
	startTime     time.Time
}

func NewUsageCapture() *UsageCapture {
	return &UsageCapture{startTime: time.Now()}
}

// Write 收集流式字节用于解析；首字节时记录 TTFT。
func (u *UsageCapture) Write(p []byte) (int, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.FirstByteAt.IsZero() && len(p) > 0 {
		u.FirstByteAt = time.Now()
	}
	// 限制解析缓冲，防止异常大流吃内存（只解析 usage 相关小事件）
	if u.buf.Len() < 4<<20 {
		u.buf.Write(p)
	}
	return len(p), nil
}

// TTFT 返回首字延迟（毫秒）。
func (u *UsageCapture) TTFT() int {
	if u.FirstByteAt.IsZero() {
		return 0
	}
	return int(u.FirstByteAt.Sub(u.startTime).Milliseconds())
}

// Parse 扫描已收集的 SSE 文本，提取 model 与各 token 字段。
func (u *UsageCapture) Parse() {
	u.mu.Lock()
	data := u.buf.String()
	u.mu.Unlock()

	scanner := bufio.NewScanner(strings.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var ev struct {
			Type    string `json:"type"`
			Message struct {
				Model string `json:"model"`
				Usage struct {
					InputTokens              int `json:"input_tokens"`
					CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
					CacheReadInputTokens     int `json:"cache_read_input_tokens"`
					OutputTokens             int `json:"output_tokens"`
				} `json:"usage"`
			} `json:"message"`
			Usage struct {
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "message_start":
			if ev.Message.Model != "" {
				u.ActualModel = ev.Message.Model
			}
			u.InputTokens = ev.Message.Usage.InputTokens
			u.CacheCreation = ev.Message.Usage.CacheCreationInputTokens
			u.CacheRead = ev.Message.Usage.CacheReadInputTokens
			u.OutputTokens = ev.Message.Usage.OutputTokens
		case "message_delta":
			if ev.Usage.OutputTokens > u.OutputTokens {
				u.OutputTokens = ev.Usage.OutputTokens
			}
		}
	}
}

func (u *UsageCapture) Snapshot() UsageSnapshot {
	return UsageSnapshot{
		ActualModel:   u.ActualModel,
		InputTokens:   u.InputTokens,
		OutputTokens:  u.OutputTokens,
		CacheCreation: u.CacheCreation,
		CacheRead:     u.CacheRead,
		TTFTMS:        u.TTFT(),
	}
}

type UsageSnapshot struct {
	ActualModel   string
	InputTokens   int
	OutputTokens  int
	CacheCreation int
	CacheRead     int
	TTFTMS        int
}

// ParseNonStream 从非流式 JSON 响应解析 usage 与真实 model。
func ParseNonStream(body []byte) UsageSnapshot {
	var data struct {
		Model string `json:"model"`
		Usage struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(body, &data)
	return UsageSnapshot{
		ActualModel:   data.Model,
		InputTokens:   data.Usage.InputTokens,
		OutputTokens:  data.Usage.OutputTokens,
		CacheCreation: data.Usage.CacheCreationInputTokens,
		CacheRead:     data.Usage.CacheReadInputTokens,
	}
}

// TeeReader 包装：透传原字节同时写入 capture。
func TeeReader(r io.Reader, w io.Writer) io.Reader {
	return io.TeeReader(r, w)
}
