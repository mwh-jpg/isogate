# IsoGate 对外定位与造势材料

> 原则：只讲已经实现的能力，不承诺未经验证的效果；不鼓励规避上游服务条款。

## 一句话定位

**IsoGate 是一个隔离式 AI API 网关：用户自带上游凭证，平台从静态 IP 资源池为每个上游账号分配一条独立专属出口 IP（一账号一 IP），逐笔记录上游真实模型与 request-id，提供官方转发审计日志。**

## 目标用户

- 使用 Claude Code / Cursor / Anthropic SDK 的开发者与团队。
- 关心「我买的模型是不是被换成便宜模型」的重度 API 用户。
- 需要固定出口 IP、但不想自己维护代理和封禁风险的个人/小团队。
- 希望统一管理多个上游账号、API Key 与用量的小型工作室。

## 痛点与对应能力

| 痛点 | IsoGate 的做法 |
|---|---|
| 中转服务模型不透明、模型被悄悄替换 | 审计日志同时记录 `requested_model` 与 `actual_model`，并保留上游 `request-id`；审计哈希链可公开校验 |
| 共享 IP 导致账号环境互相污染 | 每个上游账号独占一条系统分配的静态出口 IP（一账号一 IP），账号强制绑定、绝不共用 |
| 代理挂掉后回退服务器 IP，触发风控 | Fail-Closed：代理不健康直接拒绝请求，绝不裸连 |
| 凭证泄露风险 | 上游凭证与代理密码 AES-256-GCM 加密落库；API Key 只存 SHA-256 |
| 用量说不清 | 逐笔记录 input / output / cache / TTFT，并提供租户与管理员两套视图 |

## 产品关键词

`BYO-Account`、`AUTO-IP`、`Per-Account Static IP`、`Official Relay Audit Log`、`Fail-Closed`、`Tenant Isolation`、`Audit Hash Chain`、`SSE Passthrough`。

## GitHub README 首屏文案（建议）

```text
IsoGate — 带官方转发审计日志的隔离式 AI API 网关

用户自带上游凭证；每个上游账号独占一条由系统从静态 IP 资源池分配的出口 IP（一账号一 IP）。
同一用户的多个账号也各自独立，所有请求强制从各自的 IP 出站；代理异常时直接拒绝，不回退服务器 IP。
逐笔记录 requested_model / actual_model / 上游 request-id / 出口 IP / token usage，
审计哈希链公开可校验。

默认支持官方 API Key / AWS Bedrock / Vertex AI。
合规使用由用户自行负责。
```

## 首发推文 / 动态草稿

### 版本 A（技术向）

> 做了一个小网关，想解决「API 中转不透明」这件事。  
> 用户自带上游账号，平台从静态 IP 资源池给每个账号分配一条独立专属出口 IP（一账号一 IP）；请求只从各自的 IP 走，代理挂了就拒绝，不回退服务器 IP。  
> 每一笔请求都记 requested_model / actual_model / 上游 request-id，审计链公开可校验。  
> Go + PostgreSQL + Redis，代码已开源仓库初始化。

### 版本 B（产品向）

> 为什么你买的 API 有时「不像原模型」？  
> IsoGate 的思路很简单：不混池、不换模型、不裸连。  
> 用户自带账号，系统为每个账号分配独立专属静态 IP；每次请求都能拿到上游真实模型名和 request-id 自证。  
> 一账号一 IP，Fail-Closed，官方转发审计日志可核验。

## 公众号 / 长文提纲

1. 标题候选：
   - 《我们为什么做了一个「可审计」的 AI API 网关》
   - 《一账号一 IP：隔离式 AI 网关的设计与实现》
2. 开篇：中转服务的三类不透明——模型、出口、用量。
3. 设计原则：BYO-Account、自动静态 IP、Fail-Closed、零 IDOR。
4. 技术实现：Dial 阶段 SSRF 防护、独立 Transport 池、SSE TeeReader、Redis Lua 原子限流、审计哈希链。
5. 演示：控制台总览、静态 IP 页、审计日志页、管理后台。
6. 合规声明：默认官方 API Key / Bedrock / Vertex；订阅 OAuth 风险自负；需完成备案与内容安全义务。

## 不做的事

- 不宣称「绝对防封」，上游风控是多因素的。
- 不提供盗号、共享账号、规避服务条款的能力。
- 不在日志中保存提示词正文、凭证或代理密码。
- 不把未上线的静态 IP 供应商接口写成已接入。
