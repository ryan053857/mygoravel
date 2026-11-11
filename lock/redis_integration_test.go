//go:build integration

package lock_test

import (
	"context"
	"crypto/rand"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/ryan053857/mygoravel/lock"
	"github.com/ryan053857/mygoravel/lock/redislock"
)

func TestRedisStore(t *testing.T) {
	rdb := openRedis(t)
	testStore(t, func() lock.Store {
		return prefixed{redislock.New(rdb), "test:lock:" + rand.Text() + ":"}
	})
}

func TestRedisStoreKeyExpires(t *testing.T) {
	ctx := context.Background()
	rdb := openRedis(t)
	store := redislock.New(rdb)
	name := "test:lock:" + rand.Text()
	if ok, err := store.Acquire(ctx, name, "o", time.Minute); !ok || err != nil {
		t.Fatalf("acquire ok=%v err=%v", ok, err)
	}
	if ok, err := store.Refresh(ctx, name, "o", 30*time.Second); !ok || err != nil {
		t.Fatalf("refresh ok=%v err=%v", ok, err)
	}
	if ttl := rdb.PTTL(ctx, name).Val(); ttl <= 0 || ttl > 30*time.Second {
		t.Fatalf("ttl after refresh = %v", ttl)
	}
	if ok, err := store.Release(ctx, name, "o"); !ok || err != nil {
		t.Fatalf("release ok=%v err=%v", ok, err)
	}
	if n := rdb.Exists(ctx, name).Val(); n != 0 {
		t.Fatalf("key still exists after release")
	}
}

func openRedis(t *testing.T) *redis.Client {
	t.Helper()
	url := os.Getenv("REDIS_TEST_URL")
	if url == "" {
		t.Fatal("REDIS_TEST_URL is required for this test")
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(opts)
	t.Cleanup(func() { rdb.Close() })
	return rdb
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
