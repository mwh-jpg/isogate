// Package ratelimit 提供 Redis 并发槽位锁与 RPM 滑动窗口限流。
// 所有判定用 Lua 脚本保证原子性（并发下不会出现竞态超限）。
package ratelimit

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

var (
	ErrConcurrencyFull = errors.New("账号并发已满")
	ErrRateLimited     = errors.New("请求频率超限")
)

type Limiter struct {
	rdb *redis.Client
}

func New(rdb *redis.Client) *Limiter { return &Limiter{rdb: rdb} }

// acquireScript: 清理过期 -> 检查数量 -> 加槽位，原子执行。
var acquireScript = redis.NewScript(`
local key = KEYS[1]
local seqKey = KEYS[2]
local max = tonumber(ARGV[1])
local cutoff = tonumber(ARGV[2])
local expireAt = tonumber(ARGV[3])
local ttl = tonumber(ARGV[4])
local seq = redis.call('INCR', seqKey)
local member = tostring(seq)
redis.call('ZREMRANGEBYSCORE', key, '0', cutoff)
local n = redis.call('ZCARD', key)
if n >= max then
  return 0
end
redis.call('ZADD', key, expireAt, member)
redis.call('PEXPIRE', key, ttl)
redis.call('PEXPIRE', seqKey, ttl)
return member
`)

// Acquire 账号级并发锁：SET NX + TTL 自动释放（防客户端断连死锁）。
func (l *Limiter) Acquire(ctx context.Context, accountID string, max int, ttl time.Duration) (func(context.Context), error) {
	key := "conc:" + accountID
	now := time.Now()
	cutoff := now.Add(-ttl).UnixMilli()

	member, err := acquireScript.Run(ctx, l.rdb, []string{key, key + ":seq"},
		max, cutoff, now.Add(ttl).UnixMilli(), (ttl + time.Minute).Milliseconds()).Text()
	if err != nil {
		return nil, err
	}
	if member == "" {
		return nil, ErrConcurrencyFull
	}
	released := false
	return func(c context.Context) {
		if released {
			return
		}
		released = true
		_ = l.rdb.ZRem(c, key, member)
	}, nil
}

// rpmScript: 60秒滑动窗口，原子清理+计数+加入。member 用 Redis 自增序号保证唯一。
var rpmScript = redis.NewScript(`
local key = KEYS[1]
local seqKey = KEYS[2]
local limit = tonumber(ARGV[1])
local now = tonumber(ARGV[2])
local windowStart = now - 60000
local seq = redis.call('INCR', seqKey)
local member = tostring(seq)
redis.call('ZREMRANGEBYSCORE', key, '0', windowStart)
redis.call('ZADD', key, now, member)
redis.call('PEXPIRE', key, 120000)
redis.call('PEXPIRE', seqKey, 120000)
local n = redis.call('ZCARD', key)
if n > limit then
  return 0
end
return 1
`)

// AllowRPM API Key 级 RPM 滑动窗口限流。
func (l *Limiter) AllowRPM(ctx context.Context, keyID string, limit int) error {
	if limit <= 0 {
		return nil
	}
	now := time.Now()
	key := "rpm:" + keyID
	ok, err := rpmScript.Run(ctx, l.rdb, []string{key, key + ":seq"},
		limit, now.UnixMilli()).Int()
	if err != nil {
		return err
	}
	if ok != 1 {
		return ErrRateLimited
	}
	return nil
}
