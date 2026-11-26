# Isolated AI Gateway

强隔离、带官方转发审计日志的 BYO-Account AI API 网关。用户自带上游凭证，**平台在付款后自动分配专属静态出口 IP（一用户一 IP）**；网关只做**隔离转发 + 计量审计**，不混池、不换模型、不裸连。用户不再自行配置代理 IP。

> 这是依据 `claude_isolated_gateway_saas_plan.md` 落地的分 Phase 工程。合规定位：**默认优先官方 API Key / AWS Bedrock / Vertex AI**；个人订阅 OAuth 通道是一只高危可选通道，上游 ToS 风险由用户自行承担。

## 实现状态

| Phase | 内容 | 状态 |
|---|---|---|
| 1 | 脚手架、配置强校验、AES-256-GCM、argon2id、5 张核心表、JWT 鉴权 | ✅ |
| 2 | Dial 阶段 SSRF 防护（防 DNS Rebinding）、独立 Transport 池、代理 CRUD+探测、防 IDOR | ✅ |
| 3 | `/v1/messages` 透传、SSE 逐块转发+Tee 解析 usage、Fail-Closed、审计响应头 | ✅ |
| 4 | Redis 并发锁+RPM 滑窗（Lua 原子）、错误分类熔断、`/v1/chat/completions` 兼容 | ✅ |
| 5 | 免构建控制台、多阶段 Dockerfile、compose、Nginx（SSE 优化）、真哈希链审计 | ✅ |
| 6 | 付款后自动分配静态 IP（一用户一 IP）、用户侧只读 IP、管理员后台、强制 Fail-Closed | ✅（手动分配占位，待接入真实静态 IP API） |

验收：每个 Phase 均通过 `go test ./...`。

## 快速启动

### 1. 起基础设施

```bash
cd isolated-ai-gateway
docker compose -f deploy/docker-compose.yml up -d      # 仅 PG + Redis
```

### 2. 配置

```bash
cp deploy/.env.example deploy/.env
# 必改: ENCRYPTION_KEY(恰好32字节)、JWT_SECRET(≥32字节)、PUBLIC_BASE_URL、DATABASE_URL
```

生成密钥：
```bash
python3 -c "import secrets;print(secrets.token_hex(16))"   # 32字节 ENCRYPTION_KEY
```

### 3. 启动网关

```bash
cd backend
set -a && source ../deploy/.env && set +a
go run ./cmd/server
```

- 健康检查：`http://localhost:8080/healthz`
- 控制台：`http://localhost:8080/console/`
- Swagger 风格接口见下方 API 列表（无自动生成文档，按此 README 调用）

### 4. 全栈部署（含 Nginx）

```bash
docker compose -f deploy/docker-compose.full.yml --env-file deploy/.env up -d
```

## 核心 API

管理面（JWT Bearer）：
- `POST /api/v1/auth/register` `POST /api/v1/auth/login` `GET /api/v1/auth/me`
- `GET /api/v1/proxies`（只读，展示系统分配的静态 IP；用户不可创建/删除）
- `POST/GET/DELETE /api/v1/accounts` · `POST /api/v1/accounts/:id/test`（自动绑定专属静态 IP，强制 Fail-Closed）
- `POST/GET/DELETE /api/v1/keys`
- `GET /api/v1/logs` `GET /api/v1/stats/overview`

管理员面（JWT role=admin）：
- `GET /api/v1/admin/overview` `GET /api/v1/admin/users` `GET /api/v1/admin/proxies` `GET /api/v1/admin/logs`
- `POST /api/v1/admin/users/:id/provision-ip` — 为指定用户创建/替换自动分配的静态 IP 并探测（当前为手动占位入口）

网关面（`sk-iso-...` API Key）：
- `POST /v1/messages` · `POST /v1/messages/count_tokens` · `POST /v1/chat/completions` · `GET /v1/models`

公开：
- `GET /api/audit/verify` — 任何人可校验审计哈希链

## 静态 IP 分配模型

- 用户侧不再有“自带代理/BYO-IP”配置入口；即使直接调用旧接口也会被拒绝。
- 付款成功后，管理端或支付回调调用 `POST /api/v1/admin/users/:id/provision-ip`，网关创建 `source='auto'` 的代理记录并立即探测。
- 数据库用部分唯一索引 `uq_auto_proxy_user` 保证每个用户最多一条自动分配静态 IP。
- 上游账号创建时强制绑定到该静态 IP，并强制 `require_proxy=true`，用户无法关闭 Fail-Closed。
- 当前为**手动分配占位实现**；用户提供静态 IP 获取接口后，只需把该接口接到 `AdminHandler.ProvisionIP` 或支付回调，业务逻辑无需改动。

## 安全设计要点

1. **Fail-Closed**：所有上游账号强制 `require_proxy=true`；系统分配的静态 IP 缺失/不健康/被删时一律 403/502，绝不降级到宿主机 IP。
2. **零 IDOR**：所有资源查询 SQL 强制带 `user_id`，DB 层用复合外键兜底（`proxy` 与 `account` 必须同属一用户）。
3. **SSRF**：Dial 阶段两轮校验（解析后 + 实际连接后），阻断私网/回环/链路本地/云元数据 `169.254.169.254`，防 DNS Rebinding。
4. **凭证加密**：API Key / 代理密码 AES-256-GCM 密文落库；密码 argon2id；日志不记录任何凭证或提示词正文。
5. **Host 头投毒防护**：所有认证链接只从 `PUBLIC_BASE_URL` 生成，不信任 `Host`/`X-Forwarded-Host`。
6. **官方转发审计日志**：审计响应头带 `X-Gateway-Upstream-Request-Id`、`X-Gateway-Actual-Model`、`X-Gateway-Egress-Proxy-Id`；审计哈希链任何篡改都会断链；控制台逐笔对比 requested vs actual model。

## 验收脚本

```bash
BASE=http://localhost:8080 KEY=sk-iso-xxx bash deploy/e2e-verify.sh
```

覆盖：非流式、流式 SSE、模型列表、无效 Key 拒绝。

## 合规免责

使用本项目可能违反上游服务条款。本项目仅供技术学习与合规自托管场景。在中国大陆面向公众提供 API 中转/生成式 AI 服务，需自行完成备案、内容安全、实名、日志留存等义务。用户把凭证交给网关即形成逻辑委托关系，平台需在协议中如实说明。

## 技术栈

Go 1.24+ / Gin / pgx/v5 / go-redis/v9 / x/net(proxy) / argon2id；前端为免构建原生 HTML+JS（避免 npm 依赖）；PostgreSQL 16 + Redis 7。

> 本机提示：Go 工具链 1.25.1 + 自动工具链，首次构建会自动拉取 go.mod 指定的 Go 版本；`GOTOOLCHAIN=local` 可禁用自动拉取（需本地版本 ≥ go.mod 指令）。
