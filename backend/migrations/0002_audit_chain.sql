-- Phase 5: 审计哈希链，用于自证零掺水（任何记录篡改都会断链）
CREATE TABLE IF NOT EXISTS audit_chain (
    seq         BIGSERIAL PRIMARY KEY,
    user_id     UUID,
    log_id      BIGINT,
    event_type  TEXT NOT NULL,
    payload     JSONB NOT NULL,
    prev_hash   TEXT NOT NULL,
    hash        TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_audit_chain_user ON audit_chain(user_id, seq);
