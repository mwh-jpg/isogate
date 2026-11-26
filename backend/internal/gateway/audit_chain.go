package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/michaelchou/isolated-ai-gateway/internal/repository"
)

const genesisHash = "0000000000000000000000000000000000000000000000000000000000000000"

// AuditChain 哈希链审计：每条记录含前一条 hash，篡改任意历史记录都会断链。
type AuditChain struct {
	repo *repository.Repo
}

func NewAuditChain(repo *repository.Repo) *AuditChain { return &AuditChain{repo: repo} }

func chainHash(seq int64, eventType string, payload []byte, prevHash string) string {
	material := fmt.Sprintf("%d|%s|%s|%s", seq, eventType, string(payload), prevHash)
	s := sha256.Sum256([]byte(material))
	return hex.EncodeToString(s[:])
}

// Append 追加一条审计记录（事务内完成，保证 seq 连续与链完整）。
func (a *AuditChain) Append(ctx context.Context, userID *string, logID int64, eventType string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return a.repo.WithTx(ctx, func(tx pgx.Tx) error {
		// 串行化追加：并发事务若同时读到同一个 head，会写出分叉链。
		if _, err := tx.Exec(ctx, `LOCK TABLE audit_chain IN EXCLUSIVE MODE`); err != nil {
			return err
		}
		var prevHash string
		if err := tx.QueryRow(ctx,
			`SELECT COALESCE((SELECT hash FROM audit_chain ORDER BY seq DESC LIMIT 1), $1)`,
			genesisHash).Scan(&prevHash); err != nil {
			return err
		}
		// payload 是 jsonb，落库时会被规范化（键重排/去空格）。必须用
		// RETURNING payload::text 拿到实际存储文本作为哈希材料，否则 Verify
		// 用 ::text 重算时会与 json.Marshal 的字节不一致，导致首条即断链。
		// seq 交给 BIGSERIAL 自动分配，避免并发下显式 seq 主键冲突。
		var seq int64
		var storedPayload string
		if err := tx.QueryRow(ctx, `
			INSERT INTO audit_chain (user_id,log_id,event_type,payload,prev_hash,hash)
			VALUES ($1,$2,$3,$4::jsonb,$5,'')
			RETURNING seq, payload::text`,
			userID, nullableInt64(logID), eventType, string(data), prevHash,
		).Scan(&seq, &storedPayload); err != nil {
			return err
		}
		hash := chainHash(seq, eventType, []byte(storedPayload), prevHash)
		_, err := tx.Exec(ctx, `UPDATE audit_chain SET hash=$1 WHERE seq=$2`, hash, seq)
		return err
	})
}

// Verify 校验整条链，返回是否完整与首个断点。
func (a *AuditChain) Verify(ctx context.Context) (map[string]any, error) {
	rows, err := a.repo.Query(ctx, `SELECT seq,event_type,payload::text,prev_hash,hash FROM audit_chain ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	prev := genesisHash
	total := 0
	for rows.Next() {
		var seq int64
		var eventType, payloadText, prevHash, hash string
		if err := rows.Scan(&seq, &eventType, &payloadText, &prevHash, &hash); err != nil {
			return nil, err
		}
		expected := chainHash(seq, eventType, []byte(payloadText), prevHash)
		if prevHash != prev || hash != expected {
			return map[string]any{"ok": false, "broken_at_seq": seq, "total": total + 1}, nil
		}
		prev = hash
		total++
	}
	return map[string]any{"ok": true, "total": total, "head": prev}, nil
}

func nullableInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}
