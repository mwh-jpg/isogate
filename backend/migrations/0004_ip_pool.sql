-- Phase 7: 静态 IP 资源池（管理员预置 / 批量录入 -> 探测验健康 -> 自动分配）。
-- 与 proxy_endpoints 的区别：ip_pool 是「库存」，proxy_endpoints 是「已分配给用户的出口」。
-- 幂等要求：服务每次启动都会重跑全部迁移。

CREATE TABLE IF NOT EXISTS ip_pool (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name                VARCHAR(128),
    scheme              TEXT NOT NULL CHECK (scheme IN ('http','https','socks5')),
    host                TEXT NOT NULL,
    port                INT NOT NULL CHECK (port BETWEEN 1 AND 65535),
    username_enc        TEXT,                            -- AES-256-GCM 密文
    password_enc        TEXT,                            -- AES-256-GCM 密文
    status              TEXT NOT NULL DEFAULT 'available'
                        CHECK (status IN ('available','assigned','disabled')),
    assigned_user_id    UUID REFERENCES users(id) ON DELETE SET NULL,
    last_egress_ip      VARCHAR(64),
    last_egress_country VARCHAR(8),
    last_latency_ms     INT,
    health_status       TEXT NOT NULL DEFAULT 'unknown'
                        CHECK (health_status IN ('unknown','healthy','unhealthy')),
    last_checked_at     TIMESTAMPTZ,
    last_error          TEXT,
    note                TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 同一端点不重复入库；批量添加时用 ON CONFLICT DO NOTHING 去重。
CREATE UNIQUE INDEX IF NOT EXISTS uq_ip_pool_endpoint ON ip_pool(scheme, host, port);
-- 一个用户最多占用池中一条 IP（与「一用户一静态 IP」一致）。
CREATE UNIQUE INDEX IF NOT EXISTS uq_ip_pool_assigned_user ON ip_pool(assigned_user_id) WHERE status = 'assigned';
-- 自动分配时按 available + healthy 快速取用。
CREATE INDEX IF NOT EXISTS idx_ip_pool_pick ON ip_pool(status, health_status, created_at);
