#!/usr/bin/env bash
# Phase 3 端到端验证脚本：证明流式与非流式透传均正常、审计日志完整入库。
# 前置：服务已启动（go run ./cmd/server），且已在控制台添加账号/代理/API Key。
set -euo pipefail

BASE="${BASE:-http://localhost:8080}"
KEY="${KEY:-sk-iso-xxxxxxxxxxxxxxxx}"
MODEL="${MODEL:-claude-sonnet-4-5-20250929}"

echo "=== 1. 健康检查 ==="
curl -sS "$BASE/healthz" && echo

echo "=== 2. 非流式 /v1/messages ==="
curl -sS -D /tmp/gw-headers.txt "$BASE/v1/messages" \
  -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d "{\"model\":\"$MODEL\",\"max_tokens\":64,\"messages\":[{\"role\":\"user\",\"content\":\"Say OK\"}]}" \
  | tee /tmp/gw-body.json
echo
echo "--- 审计响应头（官方转发审计日志核验）---"
grep -i 'X-Gateway-' /tmp/gw-headers.txt || true

echo "=== 3. 流式 SSE /v1/messages ==="
curl -sS -N -D /tmp/gw-stream-headers.txt "$BASE/v1/messages" \
  -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d "{\"model\":\"$MODEL\",\"max_tokens\":64,\"stream\":true,\"messages\":[{\"role\":\"user\",\"content\":\"Count 1 to 3\"}]}" \
  | tee /tmp/gw-stream.txt
echo
echo "--- 流事件类型 ---"
grep '"type"' /tmp/gw-stream.txt | head -10 || true

echo "=== 4. /v1/models ==="
curl -sS "$BASE/v1/models" -H "Authorization: Bearer $KEY" && echo

echo "=== 5. Fail-Closed 验证：无效 Key 应 401 ==="
curl -sS -o /dev/null -w "HTTP %{http_code}\n" "$BASE/v1/messages" \
  -H "Authorization: Bearer sk-iso-invalid" \
  -H "Content-Type: application/json" \
  -d "{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"x\"}]}"

echo "=== 完成：请到控制台 /api/v1/logs 核验 actual_model 与 upstream_request_id ==="
