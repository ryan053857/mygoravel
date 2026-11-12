package lock_test

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/redis/rueidis"

	"github.com/ryan053857/mygoravel/internal/redistest"
	"github.com/ryan053857/mygoravel/lock"
	"github.com/ryan053857/mygoravel/lock/locktest"
	"github.com/ryan053857/mygoravel/lock/redislock"
)

func TestRedisStore(t *testing.T) {
	for _, srv := range redistest.Servers {
		t.Run(srv.Name, func(t *testing.T) {
			rdb := srv.Open(t)
			locktest.TestStore(t, func() lock.Store {
				return prefixed{redislock.New(rdb), "test:lock:" + rand.Text() + ":"}
			})
		})
	}
}

func TestRedisStoreKeyExpires(t *testing.T) {
	for _, srv := range redistest.Servers {
		t.Run(srv.Name, func(t *testing.T) { testRedisStoreKeyExpires(t, srv.Open(t)) })
	}
}

func testRedisStoreKeyExpires(t *testing.T, rdb rueidis.Client) {
	ctx := context.Background()
	store := redislock.New(rdb)
	name := "test:lock:" + rand.Text()
	if ok, err := store.Acquire(ctx, name, "o", time.Minute); !ok || err != nil {
		t.Fatalf("acquire ok=%v err=%v", ok, err)
	}
	if ok, err := store.Refresh(ctx, name, "o", 30*time.Second); !ok || err != nil {
		t.Fatalf("refresh ok=%v err=%v", ok, err)
	}
	if ms, _ := rdb.Do(ctx, rdb.B().Pttl().Key(name).Build()).AsInt64(); ms <= 0 || ms > 30_000 {
		t.Fatalf("ttl after refresh = %dms", ms)
	}
	if ok, err := store.Release(ctx, name, "o"); !ok || err != nil {
		t.Fatalf("release ok=%v err=%v", ok, err)
	}
	if n, _ := rdb.Do(ctx, rdb.B().Exists().Key(name).Build()).AsInt64(); n != 0 {
		t.Fatalf("key still exists after release")
	}
}

// prefixed 给锁名加上随机前缀，让每次契约测试使用独立的键，不需要清空 Redis。
type prefixed struct {
	lock.Store
	prefix string
}

func (p prefixed) Acquire(ctx context.Context, name, owner string, ttl time.Duration) (bool, error) {
	return p.Store.Acquire(ctx, p.prefix+name, owner, ttl)
}

func (p prefixed) Refresh(ctx context.Context, name, owner string, ttl time.Duration) (bool, error) {
	return p.Store.Refresh(ctx, p.prefix+name, owner, ttl)
}

func (p prefixed) Release(ctx context.Context, name, owner string) (bool, error) {
	return p.Store.Release(ctx, p.prefix+name, owner)
}

func (p prefixed) Held(ctx context.Context, name string) (bool, error) {
	return p.Store.Held(ctx, p.prefix+name)
}
