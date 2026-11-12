package doctest_test

import (
	"context"
	"crypto/rand"
	"os"
	"testing"
	"time"

	"github.com/eko/gocache/lib/v4/cache"
	"github.com/eko/gocache/lib/v4/store"
	redisstore "github.com/eko/gocache/store/redis/v4"
	"github.com/redis/go-redis/v9"
)

func TestRedisCache(t *testing.T) {
	testCache(t, cache.New[string](redisstore.NewRedis(openRedis(t), store.WithExpiration(time.Minute))))
}

// 默认 TTL 通过 NewRedis 的选项设置，单次 Set 的选项会覆盖它，WithExpiration(0) 表示不过期。
func TestRedisCacheExpiration(t *testing.T) {
	ctx := context.Background()
	rdb := openRedis(t)
	c := cache.New[string](redisstore.NewRedis(rdb, store.WithExpiration(10*time.Minute)))
	prefix := "test:cache:" + rand.Text() + ":"
	for _, tc := range []struct {
		name     string
		opts     []store.Option
		min, max time.Duration
	}{
		{"default", nil, 9 * time.Minute, 10 * time.Minute},
		{"override", []store.Option{store.WithExpiration(time.Minute)}, 59 * time.Second, time.Minute},
		{"no expiration", []store.Option{store.WithExpiration(0)}, -1, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := prefix + tc.name
			if err := c.Set(ctx, k, "v", tc.opts...); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { rdb.Del(context.Background(), k) })
			if ttl := rdb.PTTL(ctx, k).Val(); ttl < tc.min || ttl > tc.max {
				t.Fatalf("ttl=%v, want [%v, %v]", ttl, tc.min, tc.max)
			}
		})
	}
}

// Redis store 读出的总是字符串：声明为结构体类型时，Get 返回零值和 nil 错误，而不是报错。
func TestRedisCacheReadsStrings(t *testing.T) {
	type article struct{ Title string }
	ctx := context.Background()
	rdb := openRedis(t)
	k := "test:cache:" + rand.Text()
	if err := cache.New[string](redisstore.NewRedis(rdb)).Set(ctx, k, `{"Title":"hello"}`, store.WithExpiration(time.Minute)); err != nil {
		t.Fatal(err)
	}
	v, err := cache.New[article](redisstore.NewRedis(rdb)).Get(ctx, k)
	if err != nil || v != (article{}) {
		t.Fatalf("get=%+v err=%v", v, err)
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
	opts.ContextTimeoutEnabled = true
	rdb := redis.NewClient(opts)
	t.Cleanup(func() { rdb.Close() })
	return rdb
}
