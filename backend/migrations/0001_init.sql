-- Phase 1: 5 张核心表 + 索引
-- 设计原则：所有查询强制带 user_id；凭证/代理密码只存密文（BYTEA/text 密文串）。

CREATE EXTENSION IF NOT EXISTS "pgcrypto";

CREATE TABLE IF NOT EXISTS users (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email           TEXT NOT NULL UNIQUE,
    password_hash   TEXT NOT NULL,                       -- argon2id
    role            TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('user','admin')),
    status          TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
    max_concurrency INT NOT NULL DEFAULT 5 CHECK (max_concurrency > 0),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS proxy_endpoints (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id             UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name                VARCHAR(128) NOT NULL,
    scheme              TEXT NOT NULL CHECK (scheme IN ('http','https','socks5')),
    host                TEXT NOT NULL,
    port                INT NOT NULL CHECK (port BETWEEN 1 AND 65535),
    username_enc        TEXT,                            -- AES-256-GCM 密文
    password_enc        TEXT,                            -- AES-256-GCM 密文
    last_egress_ip      VARCHAR(64),
    last_egress_country VARCHAR(8),
    last_latency_ms     INT,
    health_status       TEXT NOT NULL DEFAULT 'unknown' CHECK (health_status IN ('unknown','healthy','unhealthy')),
    last_checked_at     TIMESTAMPTZ,
    last_error          TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_proxy_user ON proxy_endpoints(user_id);

CREATE TABLE IF NOT EXISTS upstream_accounts (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id          UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    proxy_id         UUID REFERENCES proxy_endpoints(id) ON DELETE SET NULL,
    provider         TEXT NOT NULL CHECK (provider IN ('anthropic_api','aws_bedrock','vertex_ai','custom_anthropic')),
    name             VARCHAR(128) NOT NULL,
    base_url         TEXT NOT NULL DEFAULT 'https://api.anthropic.com',
    credential_enc   TEXT NOT NULL,                      -- AES-256-GCM 密文（API Key 或结构化凭证 JSON）
    require_proxy    BOOLEAN NOT NULL DEFAULT true,
    max_concurrency  INT NOT NULL DEFAULT 2 CHECK (max_concurrency > 0),
    status           TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','cooldown','disabled')),
    cooldown_until   TIMESTAMPTZ,
    last_error       TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_account_user ON upstream_accounts(user_id);
CREATE INDEX IF NOT EXISTS idx_account_proxy ON upstream_accounts(proxy_id);

-- 防越权：proxy_id 必须属于同一 user（在应用层强校验，DB 层用复合外键兜底）
-- 注意：唯一索引必须先于外键创建，且列顺序必须与被引用列一致
CREATE UNIQUE INDEX IF NOT EXISTS uq_proxy_owner ON proxy_endpoints(user_id, id);
ALTER TABLE upstream_accounts
    ADD CONSTRAINT fk_account_proxy_same_user
    FOREIGN KEY (user_id, proxy_id) REFERENCES proxy_endpoints(user_id, id) ON DELETE SET NULL;

CREATE TABLE IF NOT EXISTS gateway_api_keys (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id             UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    upstream_account_id UUID NOT NULL REFERENCES upstream_accounts(id) ON DELETE CASCADE,
    name                VARCHAR(128) NOT NULL,
    key_prefix          VARCHAR(32) NOT NULL,
    key_hash            VARCHAR(64) NOT NULL UNIQUE,     -- SHA-256，不存明文
    allowed_models      TEXT[] NOT NULL DEFAULT '{}',    -- 空=不限
    rpm_limit           INT NOT NULL DEFAULT 60 CHECK (rpm_limit > 0),
    status              TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','revoked')),
    last_used_at        TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_key_user ON gateway_api_keys(user_id);

-- 防越权：API Key 绑定的账号必须属于同一 user（唯一索引先于外键）
CREATE UNIQUE INDEX IF NOT EXISTS uq_account_owner ON upstream_accounts(user_id, id);
ALTER TABLE gateway_api_keys
    ADD CONSTRAINT fk_key_account_same_user
    FOREIGN KEY (user_id, upstream_account_id) REFERENCES upstream_accounts(user_id, id) ON DELETE CASCADE;

CREATE TABLE IF NOT EXISTS usage_logs (
    id                     BIGSERIAL PRIMARY KEY,
    user_id                UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    api_key_id             UUID REFERENCES gateway_api_keys(id) ON DELETE SET NULL,
    upstream_account_id    UUID REFERENCES upstream_accounts(id) ON DELETE SET NULL,
    proxy_id               UUID REFERENCES proxy_endpoints(id) ON DELETE SET NULL,
    requested_model        VARCHAR(128),
    actual_model           VARCHAR(128),                 -- 上游响应真实 model（官方转发审计）
    upstream_request_id    VARCHAR(128),                 -- 上游 request-id
    egress_ip_snapshot     VARCHAR(64),
    status_code            INT,
    is_stream              BOOLEAN NOT NULL DEFAULT false,
    input_tokens           INT NOT NULL DEFAULT 0,
    output_tokens          INT NOT NULL DEFAULT 0,
    cache_creation_tokens  INT NOT NULL DEFAULT 0,
    cache_read_tokens      INT NOT NULL DEFAULT 0,
    ttft_ms                INT,
    total_latency_ms       INT,
    error_message          TEXT,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_usage_user_time ON usage_logs(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_usage_account ON usage_logs(upstream_account_id, created_at DESC);
