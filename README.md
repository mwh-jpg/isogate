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
| 6 | 付款后自动分配静态 IP（一用户一 IP）、用户侧只读 IP、管理员后台、强制 Fail-Closed | ✅ |
| 7 | 静态 IP 资源池（`ip_pool` 库存）：批量录入、SSRF 拦截、探测验健康、停用/启用、从池自动分配、回收释放库存 | ✅（待接入真实静态 IP 获取接口） |

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
- 独立登录页：`http://localhost:8080/console/login.html`（未登录访问控制台会自动跳转到此）
- Swagger 风格接口见下方 API 列表（无自动生成文档，按此 README 调用）

### 4. 全栈部署（含 Nginx）

```bash
docker compose -f deploy/docker-compose.full.yml --env-file deploy/.env up -d
```

## 控制台与登录

- 控制台为免构建静态页面（`frontend/index.html` + `app.js`），登录后 JWT 存于 `localStorage.gw_token`。
- 未登录访问 `/console/` 会自动跳转到 `/console/login.html?next=...`，登录/注册成功后跳回原页面；`next` 做了白名单校验，禁止 `//` 开头的外链跳转。
- 独立登录页 `login.html`（暗色极光 + 玻璃拟态）支持登录/注册切换、已登录 token 自动校验、错误内联提示；管理员账号登录后自动显示「管理后台」入口。
- 退出登录会清空本地 token 与内存状态并跳回登录页。

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
- `PATCH /api/v1/admin/users/:id/plan` — 管理套餐：`plan_status`（unpaid/active/expired/cancelled）、`plan_days`（续期天数）、`max_concurrency`、`status`（active/disabled）；套餐非 `active` 时网关立即 Fail-Closed
- `DELETE /api/v1/admin/users/:id/ip` — 回收专属静态 IP，并把套餐同步置为 `expired`
- `POST /api/v1/admin/proxies/:id/probe` — 重新探测某条静态 IP 的健康状态与出口 IP
- `GET /api/v1/admin/ip-pool` — 静态 IP 资源池条目与统计（`total/available/healthy/assigned/disabled/allocatable`）
- `POST /api/v1/admin/ip-pool` — 批量往池里加 IP：`lines` 每行一条（也支持 `items` 结构化数组），可带 `scheme`（默认协议）与 `note`；逐条校验、端点去重
- `POST /api/v1/admin/ip-pool/:id/probe` — 经该代理探测真实出口 IP / 国家 / 延迟并落库，只有 `healthy` 才可被分配
- `PATCH /api/v1/admin/ip-pool/:id` — 停用 / 启用（`status=disabled|available`）
- `DELETE /api/v1/admin/ip-pool/:id` — 删除未分配条目
- `POST /api/v1/admin/users/:id/assign-from-pool` — 从池中自动取一条 `available + healthy` 的 IP 分配给用户（付款后自动设置 IP 的落地入口，可带 `plan_days`）

管理后台 UI（`role=admin` 可见）：用户与套餐表格支持「套餐」「从池分配」「分配/更换 IP」「回收」；静态 IP 资源池区块支持「批量添加 IP」、逐条「探测 / 停用 / 启用 / 删除」并显示库存统计；「已分配出口」区块保留重新探测与回收。

网关面（`sk-iso-...` API Key）：
- `POST /v1/messages` · `POST /v1/messages/count_tokens` · `POST /v1/chat/completions` · `GET /v1/models`

公开：
- `GET /api/audit/verify` — 任何人可校验审计哈希链

## 静态 IP 分配模型

- 用户侧不再有“自带代理/BYO-IP”配置入口；即使直接调用旧接口也会被拒绝。
- 付款成功后，管理端或支付回调调用 `POST /api/v1/admin/users/:id/provision-ip`，网关创建 `source='auto'` 的代理记录并立即探测。
- 数据库用部分唯一索引 `uq_auto_proxy_user` 保证每个用户最多一条自动分配静态 IP。
- 上游账号创建时强制绑定到该静态 IP，并强制 `require_proxy=true`，用户无法关闭 Fail-Closed。
- `provision-ip` 目前是**手动占位入口**；正式路径是从静态 IP 资源池自动分配，见下一节。

## 静态 IP 资源池（管理员如何往池里加 IP）

`ip_pool` 是**库存表**，`proxy_endpoints(source='auto')` 是**已分配给用户的出口**，两者严格分开。管理端闭环如下：

1. **批量录入**：管理后台「静态 IP 资源池」→「批量添加 IP」，或调用 `POST /api/v1/admin/ip-pool`。
   `lines` 每行一条，支持三种格式（`#` 开头为注释）：

   ```text
   216.223.21.54:8022:qdqrxwzcez:iormiiumiomty   # host:port:user:pass
   1.2.3.4:8080                                  # host:port（无认证）
   http://user:pass@1.2.3.4:8080                 # 完整 URL，可写 socks5://
   ```

   也支持 `items` 结构化数组；`scheme` 指定文本未写协议时的默认值，`note` 为本批统一备注。
   `(scheme, host, port)` 唯一，重复端点自动跳过；私网 / 回环 / 链路本地地址在 SSRF 校验阶段直接拒绝；
   用户名与密码 AES-256-GCM 加密落库，绝不出现在日志与接口响应中。

2. **探测验健康**：新条目是 `available / unknown`，**必须**逐条点「探测」或调用
   `POST /api/v1/admin/ip-pool/:id/probe`。网关经该代理访问 `ipinfo.io/json`，记录真实出口 IP / 国家 / 延迟；
   只有 `healthy` 才进入可分配队列（`stats.allocatable` 即 `available + healthy` 的条数）。

3. **从池自动分配**：用户表「从池分配」或 `POST /api/v1/admin/users/:id/assign-from-pool`（可带 `plan_days`）。
   事务内按入库顺序 `FOR UPDATE SKIP LOCKED` 取一条 `available + healthy`，标记 `assigned` 并绑定用户，
   写入 `proxy_endpoints(source='auto')`，自动重绑该用户所有强制代理账号，套餐置 `active`。
   这正是「用户付款后自动设置 IP」的落地入口。

4. **回收释放库存**：`DELETE /api/v1/admin/users/:id/ip` 在删除出口的同时，把池中对应条目释放回
   `available`，避免库存泄漏；已分配条目禁止停用 / 删除，必须先回收。
   二次分配会先释放该用户旧的池占用，不会撞 `uq_ip_pool_assigned_user` 唯一索引。

状态机：`available →(分配) assigned →(回收) available`，另有 `disabled` 表示人工停用（不参与自动分配）。

> 当前是**自建资源池占位实现**：管理员手工批量录入静态 IP。待提供静态 IP 供应商获取接口后，
> 把「取 IP」逻辑接到 `AdminHandler.AssignFromPool` 或支付回调即可，第 3 步之后的流程无需改动。

## 安全设计要点

1. **Fail-Closed**：所有上游账号强制 `require_proxy=true`；系统分配的静态 IP 缺失/不健康/被删时一律 403/502，绝不降级到宿主机 IP；套餐状态非 `active` 或账号被 `disabled` 时，Resolver 在解析阶段即拒绝出站。回收 IP 后外键把账号 `proxy_id` 置空，重新分配时 `UpsertAutoProxy` 会自动重绑该用户的账号，无需重建。
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

## 加群交流

扫码添加微信，备注 **IsoGate** 拉你进交流群。部署踩坑、静态 IP 接口对接、官方转发审计日志的使用反馈都可以在群里聊。

<p align="center">
  <img src="docs/wechat-qr.jpg" alt="IsoGate 加群交流微信二维码" width="260" />
</p>
