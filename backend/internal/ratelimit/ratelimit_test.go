package ratelimit

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func testLimiter(t *testing.T) (*Limiter, func()) {
	t.Helper()
	s := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	return New(rdb), func() { rdb.Close(); s.Close() }
}

func TestConcurrencyLimit(t *testing.T) {
	l, cleanup := testLimiter(t)
	defer cleanup()
	ctx := context.Background()

	rel1, err := l.Acquire(ctx, "acct1", 2, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	rel2, err := l.Acquire(ctx, "acct1", 2, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Acquire(ctx, "acct1", 2, time.Minute); err == nil {
		t.Fatal("超过并发上限应拒绝")
	}
	rel1(ctx) // 释放一个槽位
	if rel3, err := l.Acquire(ctx, "acct1", 2, time.Minute); err != nil {
		t.Fatalf("释放后应可再获取: %v", err)
	} else {
		rel3(ctx)
	}
	rel2(ctx)
}

func TestConcurrencyIsolationPerAccount(t *testing.T) {
	l, cleanup := testLimiter(t)
	defer cleanup()
	ctx := context.Background()

	relA, err := l.Acquire(ctx, "acctA", 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Acquire(ctx, "acctA", 1, time.Minute); err == nil {
		t.Fatal("同账号第二个应拒绝")
	}
	// 不同账号互不干扰
	relB, err := l.Acquire(ctx, "acctB", 1, time.Minute)
	if err != nil {
		t.Fatalf("不同账号应独立: %v", err)
	}
	relA(ctx)
	relB(ctx)
}

func TestRPMLimit(t *testing.T) {
	l, cleanup := testLimiter(t)
	defer cleanup()
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if err := l.AllowRPM(ctx, "key1", 5); err != nil {
			t.Fatalf("前5次应通过: %v", err)
		}
	}
	if err := l.AllowRPM(ctx, "key1", 5); err == nil {
		t.Fatal("第6次应限流")
	}
	// 不同 key 独立
	if err := l.AllowRPM(ctx, "key2", 5); err != nil {
		t.Fatalf("不同key应独立: %v", err)
	}
}

func TestRPMDisabledWhenZero(t *testing.T) {
	l, cleanup := testLimiter(t)
	defer cleanup()
	if err := l.AllowRPM(context.Background(), "key1", 0); err != nil {
		t.Fatal("limit=0 应不限制")
	}
}
