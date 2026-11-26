// Package repository 数据访问层。
// 安全不变量：所有面向用户的查询必须带 user_id 参数（零 IDOR）。
package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/michaelchou/isolated-ai-gateway/internal/model"
)

var ErrNotFound = errors.New("not found")

type Repo struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

func (r *Repo) Close() { r.pool.Close() }

func (r *Repo) Ping(ctx context.Context) error { return r.pool.Ping(ctx) }

func (r *Repo) Migrate(ctx context.Context, schema string) error {
	_, err := r.pool.Exec(ctx, schema)
	return err
}

// WithTx 在事务内执行回调（用于哈希链等需要原子性的操作）。
func (r *Repo) WithTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Query 暴露只读查询（供审计链校验）。
func (r *Repo) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return r.pool.Query(ctx, sql, args...)
}

// ---------- Users ----------

const userCols = `id, email, password_hash, role, status, plan_status, plan_expires_at,
	max_concurrency, created_at, updated_at`

func scanUser(row pgx.Row) (*model.User, error) {
	u := &model.User{}
	err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role, &u.Status,
		&u.PlanStatus, &u.PlanExpiresAt, &u.MaxConcurrency, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

func (r *Repo) CreateUser(ctx context.Context, email, passwordHash string) (*model.User, error) {
	return scanUser(r.pool.QueryRow(ctx, `
		INSERT INTO users (email, password_hash) VALUES ($1,$2)
		RETURNING `+userCols, email, passwordHash))
}

func (r *Repo) GetUserByEmail(ctx context.Context, email string) (*model.User, error) {
	return scanUser(r.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE email=$1`, email))
}

func (r *Repo) GetUserByID(ctx context.Context, id string) (*model.User, error) {
	return scanUser(r.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id=$1`, id))
}

func (r *Repo) SetUserPlan(ctx context.Context, userID, status string, expiresAt *time.Time) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE users SET plan_status=$2, plan_expires_at=$3, updated_at=now() WHERE id=$1`,
		userID, status, expiresAt)
	return err
}

// UpdateUserAdmin 管理员修改套餐与账号属性；只更新传入的非 nil 字段。
func (r *Repo) UpdateUserAdmin(ctx context.Context, userID string, planStatus *string, expiresAt *time.Time, maxConcurrency *int, status *string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE users SET
			plan_status     = COALESCE($2, plan_status),
			plan_expires_at = COALESCE($3, plan_expires_at),
			max_concurrency = COALESCE($4, max_concurrency),
			status          = COALESCE($5, status),
			updated_at      = now()
		WHERE id=$1`,
		userID, planStatus, expiresAt, maxConcurrency, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteAutoProxy 回收用户的自动分配静态 IP。
// 上游账号的 proxy_id 由外键 ON DELETE SET NULL 置空，随后 Fail-Closed 生效。
func (r *Repo) DeleteAutoProxy(ctx context.Context, userID string) (int64, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM proxy_endpoints WHERE user_id=$1 AND source='auto'`, userID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// GetProxyByID 仅供管理端按 ID 查询（不带 user_id 过滤）。
func (r *Repo) GetProxyByID(ctx context.Context, proxyID string) (*model.ProxyEndpoint, error) {
	return scanProxy(r.pool.QueryRow(ctx, `SELECT `+proxyCols+` FROM proxy_endpoints WHERE id=$1`, proxyID))
}

// ---------- IP Pool（静态 IP 资源池，仅管理端）----------

var ErrPoolEmpty = errors.New("IP 池中没有可用的健康静态 IP（请先在池中探测出 healthy 的可用 IP）")

const ipPoolCols = `id, COALESCE(name,''), scheme, host, port, username_enc, password_enc,
	status, assigned_user_id, last_egress_ip, last_egress_country, last_latency_ms,
	health_status, last_checked_at, last_error, COALESCE(note,''), created_at, updated_at`

func scanIPPool(row pgx.Row) (*model.IPPoolItem, error) {
	p := &model.IPPoolItem{}
	err := row.Scan(&p.ID, &p.Name, &p.Scheme, &p.Host, &p.Port,
		&p.UsernameEnc, &p.PasswordEnc, &p.Status, &p.AssignedUserID,
		&p.LastEgressIP, &p.LastEgressCountry, &p.LastLatencyMS,
		&p.HealthStatus, &p.LastCheckedAt, &p.LastError, &p.Note,
		&p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

func (r *Repo) ListIPPool(ctx context.Context) ([]*model.IPPoolItem, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+ipPoolCols+` FROM ip_pool ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*model.IPPoolItem, 0)
	for rows.Next() {
		p, err := scanIPPool(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *Repo) GetIPPool(ctx context.Context, id string) (*model.IPPoolItem, error) {
	return scanIPPool(r.pool.QueryRow(ctx, `SELECT `+ipPoolCols+` FROM ip_pool WHERE id=$1`, id))
}

// AddIPPoolBatch 批量入库，端点重复自动跳过，返回成功插入条数。
func (r *Repo) AddIPPoolBatch(ctx context.Context, items []*model.IPPoolItem) (int, error) {
	added := 0
	err := r.WithTx(ctx, func(tx pgx.Tx) error {
		for _, it := range items {
			tag, err := tx.Exec(ctx, `
				INSERT INTO ip_pool (name,scheme,host,port,username_enc,password_enc,note)
				VALUES ($1,$2,$3,$4,$5,$6,$7)
				ON CONFLICT (scheme,host,port) DO NOTHING`,
				it.Name, it.Scheme, it.Host, it.Port, it.UsernameEnc, it.PasswordEnc, it.Note)
			if err != nil {
				return err
			}
			added += int(tag.RowsAffected())
		}
		return nil
	})
	return added, err
}

func (r *Repo) UpdateIPPoolHealth(ctx context.Context, id, ip, country string, latency int, health, errMsg string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE ip_pool SET last_egress_ip=$2, last_egress_country=$3, last_latency_ms=$4,
		    health_status=$5, last_checked_at=now(), last_error=$6, updated_at=now()
		WHERE id=$1`, id, ip, country, latency, health, nullable(errMsg))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repo) SetIPPoolStatus(ctx context.Context, id, status string, note *string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE ip_pool SET status=$2, note=COALESCE($3, note), updated_at=now()
		WHERE id=$1 AND status <> 'assigned'`, id, status, note)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repo) DeleteIPPool(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM ip_pool WHERE id=$1 AND status <> 'assigned'`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ReleaseIPPoolByUser 回收用户 IP 时把池中对应条目释放回 available，避免库存泄漏。
func (r *Repo) ReleaseIPPoolByUser(ctx context.Context, userID string) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE ip_pool SET status='available', assigned_user_id=NULL, updated_at=now()
		WHERE assigned_user_id=$1 AND status='assigned'`, userID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// AssignFromPool 事务内从池中取一条 healthy+available 的 IP 分配给用户：
// 锁定并标记 assigned -> 写入 auto proxy -> 重绑账号 -> 套餐置 active。
func (r *Repo) AssignFromPool(ctx context.Context, userID string) (*model.IPPoolItem, *model.ProxyEndpoint, error) {
	var item *model.IPPoolItem
	var proxy *model.ProxyEndpoint
	err := r.WithTx(ctx, func(tx pgx.Tx) error {
		// 二次分配（更换 IP）时先释放该用户旧的池占用：
		// 否则 uq_ip_pool_assigned_user 会因同一 user_id 出现两条 assigned 而报 23505，
		// 且旧 IP 会永久滞留在 assigned 状态造成库存泄漏。
		if _, err := tx.Exec(ctx, `
			UPDATE ip_pool SET status='available', assigned_user_id=NULL, updated_at=now()
			WHERE assigned_user_id=$1 AND status='assigned'`, userID); err != nil {
			return err
		}
		var poolID string
		if err := tx.QueryRow(ctx, `
			SELECT id FROM ip_pool
			WHERE status='available' AND health_status='healthy'
			ORDER BY created_at ASC FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&poolID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrPoolEmpty
			}
			return err
		}
		p, err := scanIPPool(tx.QueryRow(ctx, `SELECT `+ipPoolCols+` FROM ip_pool WHERE id=$1`, poolID))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE ip_pool SET status='assigned', assigned_user_id=$2, updated_at=now() WHERE id=$1`,
			p.ID, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM proxy_endpoints WHERE user_id=$1 AND source='auto'`, userID); err != nil {
			return err
		}
		prow := tx.QueryRow(ctx, fmt.Sprintf(`
			INSERT INTO proxy_endpoints
			    (user_id,name,scheme,host,port,username_enc,password_enc,source,
			     last_egress_ip,last_egress_country,last_latency_ms,health_status,last_checked_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,'auto',$8,$9,$10,$11,now())
			RETURNING %s`, proxyCols),
			userID, p.Name, p.Scheme, p.Host, p.Port, p.UsernameEnc, p.PasswordEnc,
			p.LastEgressIP, p.LastEgressCountry, p.LastLatencyMS, p.HealthStatus)
		np, err := scanProxy(prow)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE upstream_accounts SET proxy_id=$2, updated_at=now()
			WHERE user_id=$1 AND require_proxy=true AND (proxy_id IS NULL OR proxy_id <> $2)`,
			userID, np.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE users SET plan_status='active', plan_expires_at=now()+interval '30 days', updated_at=now()
			WHERE id=$1`, userID); err != nil {
			return err
		}
		item = p
		proxy = np
		return nil
	})
	return item, proxy, err
}

func (r *Repo) ListUsers(ctx context.Context) ([]*model.User, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+userCols+` FROM users ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ---------- Proxies（全部带 user_id）----------

func scanProxy(row pgx.Row) (*model.ProxyEndpoint, error) {
	p := &model.ProxyEndpoint{}
	err := row.Scan(&p.ID, &p.UserID, &p.Name, &p.Scheme, &p.Host, &p.Port,
		&p.UsernameEnc, &p.PasswordEnc, &p.LastEgressIP, &p.LastEgressCountry,
		&p.LastLatencyMS, &p.HealthStatus, &p.Source, &p.LastCheckedAt, &p.LastError,
		&p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

const proxyCols = `id, user_id, name, scheme, host, port, username_enc, password_enc,
	last_egress_ip, last_egress_country, last_latency_ms, health_status, source,
	last_checked_at, last_error, created_at, updated_at`

func (r *Repo) CreateProxy(ctx context.Context, userID, name, scheme, host string, port int, userEnc, passEnc *string) (*model.ProxyEndpoint, error) {
	row := r.pool.QueryRow(ctx, fmt.Sprintf(`
		INSERT INTO proxy_endpoints (user_id,name,scheme,host,port,username_enc,password_enc,source)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'manual') RETURNING %s`, proxyCols),
		userID, name, scheme, host, port, userEnc, passEnc)
	return scanProxy(row)
}

// UpsertAutoProxy 为一个用户创建或替换自动分配的静态 IP（每用户唯一）。
func (r *Repo) UpsertAutoProxy(ctx context.Context, userID, name, scheme, host string, port int, userEnc, passEnc *string) (*model.ProxyEndpoint, error) {
	var out *model.ProxyEndpoint
	err := r.WithTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM proxy_endpoints WHERE user_id=$1 AND source='auto'`, userID); err != nil {
			return err
		}
		row := tx.QueryRow(ctx, fmt.Sprintf(`
			INSERT INTO proxy_endpoints (user_id,name,scheme,host,port,username_enc,password_enc,source)
			VALUES ($1,$2,$3,$4,$5,$6,$7,'auto') RETURNING %s`, proxyCols),
			userID, name, scheme, host, port, userEnc, passEnc)
		p, err := scanProxy(row)
		if err != nil {
			return err
		}
		// 分配/替换 IP 后把该用户所有强制代理的账号重新指向新 IP。
		// 回收 IP 时外键会把 proxy_id 置 NULL，若不重绑，重新分配后账号仍会 Fail-Closed。
		if _, err := tx.Exec(ctx, `
			UPDATE upstream_accounts SET proxy_id=$2, updated_at=now()
			WHERE user_id=$1 AND require_proxy=true AND (proxy_id IS NULL OR proxy_id <> $2)`,
			userID, p.ID); err != nil {
			return err
		}
		out = p
		return nil
	})
	return out, err
}

func (r *Repo) GetAutoProxy(ctx context.Context, userID string) (*model.ProxyEndpoint, error) {
	return scanProxy(r.pool.QueryRow(ctx,
		`SELECT `+proxyCols+` FROM proxy_endpoints WHERE user_id=$1 AND source='auto' ORDER BY created_at DESC LIMIT 1`, userID))
}

func (r *Repo) ListAllProxies(ctx context.Context) ([]*model.ProxyEndpoint, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+proxyCols+` FROM proxy_endpoints WHERE source='auto' ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.ProxyEndpoint
	for rows.Next() {
		p, err := scanProxy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *Repo) ListProxies(ctx context.Context, userID string) ([]*model.ProxyEndpoint, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+proxyCols+` FROM proxy_endpoints WHERE user_id=$1 AND source='auto' ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.ProxyEndpoint
	for rows.Next() {
		p, err := scanProxy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetProxy 强制 user_id 过滤——跨租户访问在此被拒。
func (r *Repo) GetProxy(ctx context.Context, userID, proxyID string) (*model.ProxyEndpoint, error) {
	return scanProxy(r.pool.QueryRow(ctx, `SELECT `+proxyCols+` FROM proxy_endpoints WHERE id=$1 AND user_id=$2`, proxyID, userID))
}

func (r *Repo) UpdateProxyHealth(ctx context.Context, userID, proxyID, ip, country string, latency int, health, errMsg string) error {
	// 参数顺序必须与占位符一致：SQL 为 id=$1 AND user_id=$2。
	// 旧实现误传 (userID, proxyID)，两者同为 UUID 故类型不报错，但更新 0 行，
	// 导致探测显示 healthy 而 DB 仍为 unknown，Resolver 触发 Fail-Closed 假阳性。
	tag, err := r.pool.Exec(ctx, `
		UPDATE proxy_endpoints
		SET last_egress_ip=$3, last_egress_country=$4, last_latency_ms=$5,
		    health_status=$6, last_checked_at=now(), last_error=$7, updated_at=now()
		WHERE id=$1 AND user_id=$2`, proxyID, userID, ip, country, latency, health, nullable(errMsg))
	if err != nil {
		return err
	}
	// 0 行更新同样视为失败，避免健康状态被静默丢弃
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repo) DeleteProxy(ctx context.Context, userID, proxyID string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM proxy_endpoints WHERE id=$1 AND user_id=$2`, proxyID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------- Upstream accounts（全部带 user_id）----------

const accountCols = `id, user_id, proxy_id, provider, name, base_url, credential_enc,
	require_proxy, max_concurrency, status, cooldown_until, last_error, created_at, updated_at`

func scanAccount(row pgx.Row) (*model.UpstreamAccount, error) {
	a := &model.UpstreamAccount{}
	err := row.Scan(&a.ID, &a.UserID, &a.ProxyID, &a.Provider, &a.Name, &a.BaseURL,
		&a.CredentialEnc, &a.RequireProxy, &a.MaxConcurrency, &a.Status,
		&a.CooldownUntil, &a.LastError, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

func (r *Repo) CreateAccount(ctx context.Context, userID string, proxyID *string, provider, name, baseURL, credEnc string, requireProxy bool, maxConc int) (*model.UpstreamAccount, error) {
	row := r.pool.QueryRow(ctx, fmt.Sprintf(`
		INSERT INTO upstream_accounts (user_id,proxy_id,provider,name,base_url,credential_enc,require_proxy,max_concurrency)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING %s`, accountCols),
		userID, proxyID, provider, name, baseURL, credEnc, requireProxy, maxConc)
	return scanAccount(row)
}

func (r *Repo) ListAccounts(ctx context.Context, userID string) ([]*model.UpstreamAccount, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+accountCols+` FROM upstream_accounts WHERE user_id=$1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.UpstreamAccount
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *Repo) GetAccount(ctx context.Context, userID, accountID string) (*model.UpstreamAccount, error) {
	return scanAccount(r.pool.QueryRow(ctx, `SELECT `+accountCols+` FROM upstream_accounts WHERE id=$1 AND user_id=$2`, accountID, userID))
}

func (r *Repo) DeleteAccount(ctx context.Context, userID, accountID string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM upstream_accounts WHERE id=$1 AND user_id=$2`, accountID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repo) SetAccountCooldown(ctx context.Context, accountID string, until time.Time, status, errMsg string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE upstream_accounts SET status=$2, cooldown_until=$3, last_error=$4, updated_at=now()
		WHERE id=$1`, accountID, status, until, nullable(errMsg))
	return err
}

func (r *Repo) ClearAccountCooldown(ctx context.Context, accountID string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE upstream_accounts SET status='active', cooldown_until=NULL, last_error=NULL, updated_at=now()
		WHERE id=$1 AND status='cooldown'`, accountID)
	return err
}

// ---------- API keys（全部带 user_id）----------

const keyCols = `id, user_id, upstream_account_id, name, key_prefix, key_hash,
	allowed_models, rpm_limit, status, last_used_at, created_at`

func scanKey(row pgx.Row) (*model.GatewayKey, error) {
	k := &model.GatewayKey{}
	err := row.Scan(&k.ID, &k.UserID, &k.UpstreamAccountID, &k.Name, &k.KeyPrefix, &k.KeyHash,
		&k.AllowedModels, &k.RPM, &k.Status, &k.LastUsedAt, &k.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return k, err
}

func (r *Repo) CreateKey(ctx context.Context, userID, accountID, name, prefix, hash string, models []string, rpm int) (*model.GatewayKey, error) {
	if len(models) == 0 {
		models = []string{}
	}
	row := r.pool.QueryRow(ctx, fmt.Sprintf(`
		INSERT INTO gateway_api_keys (user_id,upstream_account_id,name,key_prefix,key_hash,allowed_models,rpm_limit)
		VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING %s`, keyCols),
		userID, accountID, name, prefix, hash, models, rpm)
	return scanKey(row)
}

func (r *Repo) ListKeys(ctx context.Context, userID string) ([]*model.GatewayKey, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+keyCols+` FROM gateway_api_keys WHERE user_id=$1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.GatewayKey
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// GetKeyByHash 用于鉴权：返回 key 与其绑定的账号/用户信息。
func (r *Repo) GetKeyByHash(ctx context.Context, hash string) (*model.GatewayKey, error) {
	return scanKey(r.pool.QueryRow(ctx, `SELECT `+keyCols+` FROM gateway_api_keys WHERE key_hash=$1`, hash))
}

func (r *Repo) RevokeKey(ctx context.Context, userID, keyID string) error {
	tag, err := r.pool.Exec(ctx, `UPDATE gateway_api_keys SET status='revoked' WHERE id=$1 AND user_id=$2`, keyID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repo) TouchKey(ctx context.Context, keyID string) error {
	_, err := r.pool.Exec(ctx, `UPDATE gateway_api_keys SET last_used_at=now() WHERE id=$1`, keyID)
	return err
}

// ---------- Usage logs ----------

func (r *Repo) InsertUsage(ctx context.Context, log *model.UsageLog) (int64, error) {
	var id int64
	err := r.pool.QueryRow(ctx, `
		INSERT INTO usage_logs (user_id,api_key_id,upstream_account_id,proxy_id,
			requested_model,actual_model,upstream_request_id,egress_ip_snapshot,
			status_code,is_stream,input_tokens,output_tokens,cache_creation_tokens,
			cache_read_tokens,ttft_ms,total_latency_ms,error_message)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		RETURNING id`,
		log.UserID, nullableUUID(log.APIKeyID), nullableUUID(log.UpstreamAccountID), nullableUUID(log.ProxyID),
		log.RequestedModel, log.ActualModel, log.UpstreamRequestID, log.EgressIPSnapshot,
		log.StatusCode, log.IsStream, log.InputTokens, log.OutputTokens, log.CacheCreationTokens,
		log.CacheReadTokens, log.TTFTMS, log.TotalLatencyMS, nullable(log.ErrorMessage)).Scan(&id)
	return id, err
}

func (r *Repo) ListUsage(ctx context.Context, userID string, limit int) ([]*model.UsageLog, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id,user_id,api_key_id,upstream_account_id,proxy_id,requested_model,actual_model,
			upstream_request_id,egress_ip_snapshot,status_code,is_stream,input_tokens,output_tokens,
			cache_creation_tokens,cache_read_tokens,ttft_ms,total_latency_ms,error_message,created_at
		FROM usage_logs WHERE user_id=$1 ORDER BY created_at DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.UsageLog
	for rows.Next() {
		l := &model.UsageLog{}
		var keyID, accountID, proxyID *string
		var errMsg *string
		var ip *string
		var reqModel, actModel, reqID *string
		if err := rows.Scan(&l.ID, &l.UserID, &keyID, &accountID, &proxyID,
			&reqModel, &actModel, &reqID, &ip, &l.StatusCode, &l.IsStream,
			&l.InputTokens, &l.OutputTokens, &l.CacheCreationTokens, &l.CacheReadTokens,
			&l.TTFTMS, &l.TotalLatencyMS, &errMsg, &l.CreatedAt); err != nil {
			return nil, err
		}
		if keyID != nil {
			l.APIKeyID = keyID
		}
		if accountID != nil {
			l.UpstreamAccountID = accountID
		}
		if proxyID != nil {
			l.ProxyID = proxyID
		}
		if errMsg != nil {
			l.ErrorMessage = *errMsg
		}
		if ip != nil {
			l.EgressIPSnapshot = *ip
		}
		if reqModel != nil {
			l.RequestedModel = *reqModel
		}
		if actModel != nil {
			l.ActualModel = *actModel
		}
		if reqID != nil {
			l.UpstreamRequestID = *reqID
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (r *Repo) ListAllUsage(ctx context.Context, limit int) ([]*model.UsageLog, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id,user_id,api_key_id,upstream_account_id,proxy_id,requested_model,actual_model,
			upstream_request_id,egress_ip_snapshot,status_code,is_stream,input_tokens,output_tokens,
			cache_creation_tokens,cache_read_tokens,ttft_ms,total_latency_ms,error_message,created_at
		FROM usage_logs ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.UsageLog
	for rows.Next() {
		l := &model.UsageLog{}
		var keyID, accountID, proxyID, errMsg, ip, reqModel, actModel, reqID *string
		if err := rows.Scan(&l.ID, &l.UserID, &keyID, &accountID, &proxyID,
			&reqModel, &actModel, &reqID, &ip, &l.StatusCode, &l.IsStream,
			&l.InputTokens, &l.OutputTokens, &l.CacheCreationTokens, &l.CacheReadTokens,
			&l.TTFTMS, &l.TotalLatencyMS, &errMsg, &l.CreatedAt); err != nil {
			return nil, err
		}
		if keyID != nil {
			l.APIKeyID = keyID
		}
		if accountID != nil {
			l.UpstreamAccountID = accountID
		}
		if proxyID != nil {
			l.ProxyID = proxyID
		}
		if errMsg != nil {
			l.ErrorMessage = *errMsg
		}
		if ip != nil {
			l.EgressIPSnapshot = *ip
		}
		if reqModel != nil {
			l.RequestedModel = *reqModel
		}
		if actModel != nil {
			l.ActualModel = *actModel
		}
		if reqID != nil {
			l.UpstreamRequestID = *reqID
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (r *Repo) StatsOverview(ctx context.Context, userID string) (map[string]any, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT COUNT(*), COALESCE(SUM(input_tokens+output_tokens),0),
		       COALESCE(AVG(ttft_ms) FILTER (WHERE ttft_ms>0),0),
		       COUNT(*) FILTER (WHERE actual_model<>requested_model AND actual_model<>'')
		FROM usage_logs WHERE user_id=$1`, userID)
	var requests, tokens int64
	var ttft float64
	var mismatch int64
	if err := row.Scan(&requests, &tokens, &ttft, &mismatch); err != nil {
		return nil, err
	}
	return map[string]any{
		"requests": requests, "tokens": tokens,
		"avg_ttft_ms": ttft, "model_mismatch": mismatch,
	}, nil
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nullableUUID(s *string) *string {
	if s != nil && *s == "" {
		return nil
	}
	return s
}

// NewID 便于 handler 生成 UUID。
func NewID() string { return uuid.NewString() }
