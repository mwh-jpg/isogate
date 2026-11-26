# Isolated AI Gateway

强隔离、带官方转发审计日志的 BYO-Account AI API 网关。用户自带上游凭证，**每个上游账号独占一条由平台从静态 IP 资源池自动分配的出口 IP（一账号一 IP）**；一个用户可有多个账号，账号之间不共用出口。网关只做**隔离转发 + 计量审计**，不混池、不换模型、不裸连。用户不再自行配置代理 IP。

> 这是依据 `claude_isolated_gateway_saas_plan.md` 落地的分 Phase 工程。合规定位：**默认优先官方 API Key / AWS Bedrock / Vertex AI**；个人订阅 OAuth 通道是一只高危可选通道，上游 ToS 风险由用户自行承担。

## 实现状态

| Phase | 内容 | 状态 |
|---|---|---|
| 1 | 脚手架、配置强校验、AES-256-GCM、argon2id、5 张核心表、JWT 鉴权 | ✅ |
| 2 | Dial 阶段 SSRF 防护（防 DNS Rebinding）、独立 Transport 池、代理 CRUD+探测、防 IDOR | ✅ |
| 3 | `/v1/messages` 透传、SSE 逐块转发+Tee 解析 usage、Fail-Closed、审计响应头 | ✅ |
| 4 | Redis 并发锁+RPM 滑窗（Lua 原子）、错误分类熔断、`/v1/chat/completions` 兼容 | ✅ |
| 5 | 免构建控制台、多阶段 Dockerfile、compose、Nginx（SSE 优化）、真哈希链审计 | ✅ |
| 6 | 付款后自动分配静态 IP、用户侧只读 IP、管理员后台、强制 Fail-Closed | ✅ |
| 7 | 静态 IP 资源池（`ip_pool` 库存）：批量录入、SSRF 拦截、探测验健康、停用/启用、从池自动分配、回收释放库存 | ✅（待接入真实静态 IP 获取接口） |
| 8 | 隔离粒度下沉到账号级：一个用户可有多个上游账号，**每个账号独立一条静态 IP**；创建时原子分配、删除/回收释放库存、管理端按账号管理 | ✅ |

验收：每个 Phase 均通过 `go build ./...` / `go test ./...`，前端 `node --check` 无错误。

测试机（`38.175.205.191`，systemd + PostgreSQL 16 + Redis 7）实测结果：

| 验证 | 结果 |
|---|---|
| 部署内置约束（登录/只读 IP/禁止用户自建代理/未付款不能建账号/管理端权限） | 12/12 PASS |
| 静态 IP 资源池 + 一账号一 IP 闭环 | 48/48 PASS |

闭环覆盖：批量录入与 SSRF 拦截、探测健康落库、停用/启用影响可分配数、**创建账号时原子分配独立 IP**、
**池耗尽时第二个账号被拒且不复用同一条 IP**、删除/回收自动释放库存、管理端按账号更换/回收/重新分配、
回收后立即 Fail-Closed，以及经专属出口抵达上游的真实 `request_id` 证据。

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
- `GET /api/v1/proxies`（只读，列出该用户所有账号各自的专属静态 IP；用户不可创建/删除）
- `POST/GET/DELETE /api/v1/accounts` · `POST /api/v1/accounts/:id/test`（套餐 `active` 时，每创建一个账号自动从资源池分配一条独立静态 IP，强制 Fail-Closed；池空返回 409 + `code=ip_pool_empty`，用户侧提示「无法添加上游账号，请联系管理员」）
- `POST/GET/DELETE /api/v1/keys`
- `GET /api/v1/logs` `GET /api/v1/stats/overview`

管理员面（JWT role=admin）：
- `GET /api/v1/admin/overview` `GET /api/v1/admin/users` `GET /api/v1/admin/proxies` `GET /api/v1/admin/logs`
- `POST /api/v1/admin/users/:id/provision-ip` — **兼容入口**：手动指定一条占位静态 IP 并探测；只替换占位出口、只释放占位池占用，不覆盖也不释放任何账号的专属出口
- `PATCH /api/v1/admin/users/:id/plan` — 管理套餐：`plan_status`（unpaid/active/expired/cancelled）、`plan_days`（续期天数）、`max_concurrency`、`status`（active/disabled）；套餐非 `active` 时网关立即 Fail-Closed
- `DELETE /api/v1/admin/users/:id/ip` — 回收该用户**全部**静态 IP 并释放池占用，套餐同步置 `expired`
- `POST /api/v1/admin/proxies/:id/probe` — 重新探测某条静态 IP 的健康状态与出口 IP
- `GET /api/v1/admin/ip-pool` — 静态 IP 资源池条目与统计（`total/available/healthy/assigned/disabled/allocatable`）
- `POST /api/v1/admin/ip-pool` — 批量往池里加 IP：`lines` 每行一条（也支持 `items` 结构化数组），可带 `scheme`（默认协议）与 `note`；逐条校验、端点去重
- `POST /api/v1/admin/ip-pool/:id/probe` — 经该代理探测真实出口 IP / 国家 / 延迟并落库，只有 `healthy` 才可被分配
- `PATCH /api/v1/admin/ip-pool/:id` — 停用 / 启用（`status=disabled|available`）
- `DELETE /api/v1/admin/ip-pool/:id` — 删除未分配条目
- `POST /api/v1/admin/users/:id/assign-from-pool` — **兼容入口**：给用户分配一条占位 IP、补绑尚无出口的账号并置套餐 `active`（可带 `plan_days`）
- `GET /api/v1/admin/accounts` — **账号维度看板**：列出全部上游账号及其专属静态 IP、健康、是否独占
- `POST /api/v1/admin/accounts/:id/assign-ip` — 从池给指定账号分配 / 更换一条独立静态 IP（不影响同用户其他账号）
- `DELETE /api/v1/admin/accounts/:id/ip` — 回收该账号 IP：删除其出口、释放池条目回 `available`，账号保留但立即 Fail-Closed

管理后台 UI（`role=admin` 可见）：用户与套餐表格管套餐及用户级兼容操作；「账号与 IP」表格按账号分配 / 更换 / 回收独立出口；静态 IP 资源池区块支持「批量添加 IP」、逐条「探测 / 停用 / 启用 / 删除」并显示库存统计；「已分配出口」区块保留重新探测与回收。

网关面（`sk-iso-...` API Key）：
- `POST /v1/messages` · `POST /v1/messages/count_tokens` · `POST /v1/chat/completions` · `GET /v1/models`

公开：
- `GET /api/audit/verify` — 任何人可校验审计哈希链

## 静态 IP 分配模型（一账号一 IP）

- 用户侧不再有“自带代理/BYO-IP”配置入口；即使直接调用旧接口也会被拒绝，客户端传入的 `proxy_id` 一律忽略。
- **分配单位是上游账号，不是用户**：一个用户可创建多个账号，每个账号各自独占一条静态出口 IP，账号之间不共用。
- 前提：用户 `plan_status='active'`（付款开通）。创建账号时 `CreateAccountWithProxyFromPool` 在**单个事务**内完成：
  `FOR UPDATE SKIP LOCKED` 取一条 `available + healthy` 的池条目 → 建账号 → 池条目绑定 `assigned_account_id` →
  建 `proxy_endpoints(source='auto', upstream_account_id=账号)` → 回填 `account.proxy_id`。
  池中没有健康 IP 时整个事务回滚并返回 409（`code=ip_pool_empty`），绝不产生「无 IP 的账号」。
  用户侧提示「静态 IP 资源池暂无可用 IP，当前无法添加上游账号。请联系管理员补充静态 IP 后再试」，
  不暴露池探测 / healthy 判定等运维细节（控制台弹窗内持久显示，不会一闪而过）；
  管理员在管理端按账号分配时仍能看到具体技术原因，便于定位补库存。
- DB 层用部分唯一索引 `uq_auto_proxy_account`（按账号）与 `uq_ip_pool_assigned_account`（按账号）兜底；
  一个用户可同时持有多条 auto 出口，但同一账号 / 同一池条目最多一条。
- 所有账号强制 `require_proxy=true`，用户无法关闭 Fail-Closed；Resolver 按 `account.proxy_id` 取出站口，
  因此多账号各自走不同 IP 天然生效。
- 删除账号时同步删除其专属出口并把池条目释放回 `available`，避免库存泄漏。
- 旧「一用户一 IP」数据由 `0005_account_ip.sql` best-effort 回填：共用同一出口的多个账号中只有第一个成为 owner，
  其余在管理后台标记为 `legacy 共用`，建议逐个「更换 IP」完成拆分。
- `provision-ip` / `assign-from-pool`（用户级）保留为**兼容占位入口**，不会覆盖已有账号的专属出口。

## 静态 IP 资源池（管理员如何往池里加 IP）

`ip_pool` 是**库存表**，`proxy_endpoints(source='auto')` 是**已分配给具体上游账号的出口**，两者严格分开；一个用户可持有多条出口。管理端闭环如下：

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

3. **每个账号创建时自动分配**：用户 `POST /api/v1/accounts` 创建上游账号时，网关在一个事务内
   按入库顺序 `FOR UPDATE SKIP LOCKED` 取一条 `available + healthy`，标记 `assigned` 并绑定
   `assigned_account_id`，写入 `proxy_endpoints(source='auto', upstream_account_id=账号)`，回填 `account.proxy_id`。
   **一个账号一条，绝不复用**；池耗尽时返回 409 并整体回滚。
   管理端可在「账号与 IP」表对指定账号调用 `POST /api/v1/admin/accounts/:id/assign-ip` 单独更换，
   不影响同用户其他账号。用户级 `POST /api/v1/admin/users/:id/assign-from-pool` 仅作占位兼容入口。

4. **回收释放库存**：`DELETE /api/v1/admin/accounts/:id/ip` 回收单个账号的出口并把池条目释放回
   `available`；用户侧 `DELETE /api/v1/accounts/:id` 删除账号时同样自动释放。
   `DELETE /api/v1/admin/users/:id/ip` 则回收该用户全部出口。已分配条目禁止停用 / 删除，必须先回收。
   更换 IP 时事务先释放该账号旧的池占用，不会撞 `uq_ip_pool_assigned_account` 唯一索引。

状态机：`available →(分配) assigned →(回收) available`，另有 `disabled` 表示人工停用（不参与自动分配）。

> 当前是**自建资源池占位实现**：管理员手工批量录入静态 IP。待提供静态 IP 供应商获取接口后，
> 把「取 IP」逻辑接到 `CreateAccountWithProxyFromPool` / `AssignIPFromPoolForAccount` 或支付回调即可，
> 第 3 步之后的流程无需改动。

## 安全设计要点

1. **Fail-Closed**：所有上游账号强制 `require_proxy=true`；账号专属静态 IP 缺失/不健康/被删时一律 403/502，绝不降级到宿主机 IP；套餐状态非 `active` 或账号被 `disabled` 时，Resolver 在解析阶段即拒绝出站。回收 IP 后外键把该账号 `proxy_id` 置空，重新分配时事务回填新出口，无需重建账号。
2. **零 IDOR**：所有资源查询 SQL 强制带 `user_id`，DB 层用复合外键兜底（`proxy` 与 `account` 必须同属一用户）。
3. **SSRF**：Dial 阶段两轮校验（解析后 + 实际连接后），阻断私网/回环/链路本地/云元数据 `169.254.169.254`，防 DNS Rebinding。
4. **凭证加密**：API Key / 代理密码 AES-256-GCM 密文落库；密码 argon2id；日志不记录任何凭证或提示词正文。
5. **Host 头投毒防护**：所有认证链接只从 `PUBLIC_BASE_URL` 生成，不信任 `Host`/`X-Forwarded-Host`。
6. **官方转发审计日志**：审计响应头带 `X-Gateway-Upstream-Request-Id`、`X-Gateway-Actual-Model`、`X-Gateway-Egress-Proxy-Id`；审计哈希链任何篡改都会断链；控制台逐笔对比 requested vs actual model。
7. **账号级出口隔离**：`uq_auto_proxy_account` / `uq_ip_pool_assigned_account` 保证同一账号、同一池条目最多一条分配；账号创建与 IP 分配在同一事务内原子完成，池空即回滚，杜绝「多账号共用一个 IP」与「无 IP 账号出站」。

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
