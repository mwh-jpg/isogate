package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type ProbeResult struct {
	EgressIP  string `json:"egress_ip"`
	Country   string `json:"country"`
	LatencyMS int    `json:"latency_ms"`
	Health    string `json:"health_status"`
	Error     string `json:"error,omitempty"`
}

// Probe 通过指定代理访问 IP 探测服务，验证真实出口 IP / 国家 / 延迟。
// 限制响应体 ≤ maxBody，防止恶意探测端点返回超大响应。
func (m *Manager) Probe(ctx context.Context, proxyURL, probeURL string, maxBody int64) ProbeResult {
	client, err := m.GetClient(proxyURL)
	if err != nil {
		return ProbeResult{Health: "unhealthy", Error: err.Error()}
	}

	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
	if err != nil {
		return ProbeResult{Health: "unhealthy", Error: err.Error()}
	}
	resp, err := client.Do(req)
	if err != nil {
		return ProbeResult{Health: "unhealthy", Error: err.Error()}
	}
	defer resp.Body.Close()
	latency := int(time.Since(start).Milliseconds())

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ProbeResult{Health: "unhealthy", LatencyMS: latency, Error: fmt.Sprintf("probe HTTP %d", resp.StatusCode)}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return ProbeResult{Health: "unhealthy", LatencyMS: latency, Error: err.Error()}
	}

	var data struct {
		IP      string `json:"ip"`
		Country string `json:"country"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return ProbeResult{Health: "unhealthy", LatencyMS: latency, Error: "probe 响应非JSON"}
	}
	if data.IP == "" {
		return ProbeResult{Health: "unhealthy", LatencyMS: latency, Error: "probe 无 ip 字段"}
	}
	return ProbeResult{
		EgressIP: data.IP, Country: data.Country,
		LatencyMS: latency, Health: "healthy",
	}
}
