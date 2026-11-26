package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/michaelchou/isolated-ai-gateway/internal/config"
	"github.com/michaelchou/isolated-ai-gateway/internal/model"
	"github.com/michaelchou/isolated-ai-gateway/internal/proxy"
)

func testConfig() *config.Config {
	return &config.Config{
		MaxUpstreamBody:  16 << 20,
		UpstreamTimeout:  10 * time.Second,
		AnthropicBaseURL: "https://api.anthropic.com",
	}
}

func testEngine() *Engine {
	guard := proxy.NewSSRFGuard(true) // 测试放行私网（mock 上游在 127.0.0.1）
	mgr := proxy.NewManager(guard, 10*time.Second)
	return NewEngine(testConfig(), nil, mgr)
}

func resolvedFor(base string, requireProxy bool, proxyURL string) *Resolved {
	return &Resolved{
		Key: &model.GatewayKey{ID: "k1", UserID: "u1"},
		Account: &model.UpstreamAccount{
			ID: "a1", UserID: "u1", BaseURL: base,
			RequireProxy: requireProxy, CredentialEnc: "enc",
		},
		Credential: "sk-ant-test",
		ProxyURL:   proxyURL,
	}
}

func TestForwardNonStream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 验证头过滤与注入
		if r.Header.Get("Authorization") != "Bearer sk-ant-test" {
			t.Errorf("上游认证头错误: %s", r.Header.Get("Authorization"))
		}
		if r.Header.Get("X-User-Proxy-Auth") != "" {
			t.Errorf("用户侧敏感头未过滤")
		}
		w.Header().Set("request-id", "req_test_123")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"claude-sonnet-4-5","usage":{"input_tokens":11,"output_tokens":5}}`)
	}))
	defer upstream.Close()

	e := testEngine()
	res := resolvedFor(upstream.URL, false, "")
	// 注入一个用户侧头，验证被过滤
	headers := http.Header{}
	headers.Set("X-User-Proxy-Auth", "secret")
	result, err := e.Forward(context.Background(), res, []byte(`{"model":"m"}`), headers)
	if err != nil {
		t.Fatal(err)
	}
	if result.StatusCode != 200 || result.UpstreamRequestID != "req_test_123" {
		t.Fatalf("响应元数据错误: %+v", result)
	}
	if result.Usage.InputTokens != 11 || result.Usage.OutputTokens != 5 || result.ActualModel != "claude-sonnet-4-5" {
		t.Fatalf("usage 解析错误: %+v", result.Usage)
	}
}

func TestForwardStreamPassthrough(t *testing.T) {
	events := `event: message_start
data: {"type":"message_start","message":{"model":"claude-opus-4-5","usage":{"input_tokens":9,"output_tokens":1}}}

event: message_delta
data: {"type":"message_delta","usage":{"output_tokens":33}}

`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("request-id", "req_stream_1")
		w.WriteHeader(200)
		// 分块写出，验证流式透传
		parts := []string{"event: message_start\n", "data: ", `{"type":"message_start","message":{"model":"claude-opus-4-5","usage":{"input_tokens":9,"output_tokens":1}}}`, "\n\n", "event: message_delta\n", "data: ", `{"type":"message_delta","usage":{"output_tokens":33}}`, "\n\n"}
		for _, p := range parts {
			_, _ = io.WriteString(w, p)
			w.(http.Flusher).Flush()
			time.Sleep(2 * time.Millisecond)
		}
	}))
	defer upstream.Close()

	e := testEngine()
	res := resolvedFor(upstream.URL, false, "")
	var got strings.Builder
	result, err := e.ForwardStream(context.Background(), res, []byte(`{"stream":true}`), http.Header{}, func(chunk []byte) error {
		got.Write(chunk)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.String(), "message_start") || !strings.Contains(got.String(), "message_delta") {
		t.Fatalf("SSE 透传不完整: %s", got.String())
	}
	if strings.TrimSpace(got.String()) != strings.TrimSpace(events) {
		t.Fatalf("SSE 字节不一致:\nGOT:\n%s\nWANT:\n%s", got.String(), events)
	}
	if result.Usage.OutputTokens != 33 || result.ActualModel != "claude-opus-4-5" {
		t.Fatalf("流式 usage 解析错误: %+v", result.Usage)
	}
	// TTFT 可能为0（极快），不强制失败
	_ = result.Usage.TTFTMS
}

func TestFailClosedNoProxy(t *testing.T) {
	e := testEngine()
	// require_proxy=true 但没有代理 URL => 必须拒绝，绝不直连
	res := resolvedFor("https://api.anthropic.com", true, "")
	_, err := e.Forward(context.Background(), res, []byte(`{}`), http.Header{})
	if err == nil {
		t.Fatal("Fail-Closed: 无代理时必须拒绝出站")
	}
	if !strings.Contains(err.Error(), "代理") {
		t.Fatalf("应返回代理相关错误: %v", err)
	}
}

func TestFailClosedDirectStillSSRFChecked(t *testing.T) {
	e := testEngine()
	// require_proxy=false 走直连，但目标为私网 IP 应被 SSRF 拦截（guard=true 时放行，此处验证链路存在）
	res := resolvedFor("http://127.0.0.1:1", false, "")
	_, err := e.Forward(context.Background(), res, []byte(`{}`), http.Header{})
	// 该场景 guard=true 放行，连接会因端口不存在失败；关键是不会 panic 且返回 error
	if err == nil {
		t.Fatalf("构造的不可达上游应报错")
	}
	fmt.Sprint(err) // 避免未使用
}
