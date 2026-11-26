-- Phase 6: 用户不再自带代理；付款后由系统自动分配专属静态 IP（一用户一 IP）。

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS plan_status TEXT NOT NULL DEFAULT 'unpaid'
        CHECK (plan_status IN ('unpaid', 'active', 'expired', 'cancelled'));
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS plan_expires_at TIMESTAMPTZ;

ALTER TABLE proxy_endpoints
    ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT 'manual'
        CHECK (source IN ('manual', 'auto'));

-- 兼容历史数据：已被强制代理账号使用的代理升级为系统分配，每用户只取最新一条。
WITH ranked AS (
    SELECT p.id, row_number() OVER (PARTITION BY p.user_id ORDER BY p.created_at DESC) AS rn
    FROM proxy_endpoints p
    WHERE EXISTS (
        SELECT 1 FROM upstream_accounts a
        WHERE a.proxy_id = p.id AND a.require_proxy = true
    )
)
UPDATE proxy_endpoints p
SET source = 'auto'
FROM ranked r
WHERE p.id = r.id AND r.rn = 1;

-- DB 层兜底：一个用户最多一条自动分配的静态 IP。
CREATE UNIQUE INDEX IF NOT EXISTS uq_auto_proxy_user ON proxy_endpoints(user_id) WHERE source = 'auto';
