-- Phase 8: 隔离粒度从「用户级」下沉到「账号级」——一个上游账号独占一个静态 IP。
-- 一个用户可拥有多个上游账号，每个账号各自从 ip_pool 取一条独立静态出口。
-- 幂等要求：服务每次启动都会重跑全部迁移。

-- 1) 移除「一用户一 IP」的 DB 兜底约束，由下面的账号级部分索引取代。
--    0003/0004 已改为不再创建这两个索引；这里 DROP 用于升级已存在的旧库。
DROP INDEX IF EXISTS uq_auto_proxy_user;
DROP INDEX IF EXISTS uq_ip_pool_assigned_user;

-- 2) proxy_endpoints 增加所属账号；ip_pool 增加被分配账号。
--    assigned_user_id 保留作归属冗余（统计 / 权限过滤）。
ALTER TABLE proxy_endpoints
    ADD COLUMN IF NOT EXISTS upstream_account_id UUID;
ALTER TABLE ip_pool
    ADD COLUMN IF NOT EXISTS assigned_account_id UUID;

-- 3) 账号级唯一约束：一个账号最多一条专属 auto 出口；一个池条目最多绑定一个账号。
--    使用部分索引，legacy 数据（account_id IS NULL）不阻塞升级。
CREATE UNIQUE INDEX IF NOT EXISTS uq_auto_proxy_account
    ON proxy_endpoints(upstream_account_id)
    WHERE source = 'auto' AND upstream_account_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_ip_pool_assigned_account
    ON ip_pool(assigned_account_id)
    WHERE status = 'assigned' AND assigned_account_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_proxy_account ON proxy_endpoints(upstream_account_id);
CREATE INDEX IF NOT EXISTS idx_ip_pool_account ON ip_pool(assigned_account_id);

-- 4) 历史数据 best-effort 回填：把旧 auto 出口挂到引用它的最小账号 id。
--    旧「一用户一 IP」下一个出口可能被多个账号共用，只有第一个账号成为 owner，
--    其余账号仍指向同一出口；需管理员在账号维度逐个「更换 IP」完成拆分。
UPDATE proxy_endpoints p
SET upstream_account_id = x.account_id, updated_at = now()
FROM (
    SELECT proxy_id, min(id::text)::uuid AS account_id
    FROM upstream_accounts
    WHERE proxy_id IS NOT NULL AND require_proxy = true
    GROUP BY proxy_id
) x
WHERE p.id = x.proxy_id
  AND p.source = 'auto'
  AND p.upstream_account_id IS NULL;
