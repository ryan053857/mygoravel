package doctest_test

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/eko/gocache/lib/v4/cache"
	"github.com/eko/gocache/lib/v4/store"
	gocachestore "github.com/eko/gocache/store/go_cache/v4"
	memory "github.com/patrickmn/go-cache"
)

// 以下行为与 docs/cache.md 一致，内存和 Redis 两种后端都必须满足。
func testCache(t *testing.T, c cache.CacheInterface[string]) {
	ctx := context.Background()
	key := func() string { return "test:cache:" + rand.Text() }

	t.Run("miss is store.NotFound", func(t *testing.T) {
		if _, err := c.Get(ctx, key()); !errors.Is(err, store.NotFound{}) {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("set get delete", func(t *testing.T) {
		k := key()
		if err := c.Set(ctx, k, `{"title":"hello"}`); err != nil {
			t.Fatal(err)
		}
		if v, err := c.Get(ctx, k); err != nil || v != `{"title":"hello"}` {
			t.Fatalf("get=%q err=%v", v, err)
		}
		if err := c.Delete(ctx, k); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Get(ctx, k); !errors.Is(err, store.NotFound{}) {
			t.Fatalf("after delete err=%v", err)
		}
		// 删除不存在的键也返回 nil。
		if err := c.Delete(ctx, k); err != nil {
			t.Fatalf("delete missing: %v", err)
		}
	})

	t.Run("empty string is a hit", func(t *testing.T) {
		k := key()
		if err := c.Set(ctx, k, ""); err != nil {
			t.Fatal(err)
		}
		if v, err := c.Get(ctx, k); err != nil || v != "" {
			t.Fatalf("get=%q err=%v", v, err)
		}
	})

	t.Run("per call expiration", func(t *testing.T) {
		k := key()
		if err := c.Set(ctx, k, "v", store.WithExpiration(50*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
		if _, err := c.Get(ctx, k); !errors.Is(err, store.NotFound{}) {
			t.Fatalf("expired key err=%v", err)
		}
	})
}

func TestMemoryCache(t *testing.T) {
	testCache(t, cache.New[string](gocachestore.NewGoCache(memory.New(time.Minute, 0))))
}

// 默认 TTL 要设置在 memory.New 上，NewGoCache 的 WithExpiration 选项会被忽略。
func TestMemoryCacheDefaultTTL(t *testing.T) {
	ctx := context.Background()
	client := memory.New(time.Hour, 0)
	c := cache.New[string](gocachestore.NewGoCache(client, store.WithExpiration(time.Millisecond)))
	if err := c.Set(ctx, "k", "v"); err != nil {
		t.Fatal(err)
	}
	_, expires, ok := client.GetWithExpiration("k")
	if !ok || time.Until(expires) < 59*time.Minute {
		t.Fatalf("ok=%v expires in %v, want memory.New default", ok, time.Until(expires))
	}
}
