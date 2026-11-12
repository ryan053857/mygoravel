// Package cachex 在 gocache 缓存之上提供 cache-aside 读取。Remember 先读缓存，没有时加载并写回，并处理三个常见问题：
//
//   - 击穿：热点键过期的瞬间，同一个进程中同一个键的并发加载合并为一次；
//   - 雪崩：写入时过期时间加上随机抖动，同一批写入的键不会在同一时刻一起过期；
//   - 穿透：加载结果为空（编码为 JSON null，例如 nil 指针）时也写入缓存，但只保留约一分钟。
//
// 值编码为 JSON 存进 cache.CacheInterface[string]，内存和 Redis 等后端可以互相替换。
// 缓存读写失败或缓存中的内容无法解码时直接加载，结果照常返回：查询缓存随时可以从数据库重建，不应让请求失败。
//
//	articles := cachex.New[*article.Article](c, 10*time.Minute)
//	item, err := articles.Remember(ctx, key, func(ctx context.Context) (*article.Article, error) {
//		return find(ctx, db, id)
//	})
package cachex

import (
	"context"
	"encoding/json"
	"log/slog"
	"math/rand/v2"
	"runtime/debug"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/eko/gocache/lib/v4/cache"
	"github.com/eko/gocache/lib/v4/store"
	"golang.org/x/sync/singleflight"

	"github.com/ryan053857/mygoravel/logging"
)

const (
	// missingTTL 是空结果在缓存中保留的最长时间，写入时同样加上抖动。
	missingTTL = time.Minute
	// jitter 是过期时间随机增加的最大比例。
	jitter = 0.1
)

// Cache 缓存一种值，例如文章详情。它是并发安全的，每种值创建一个，在应用中共用。
type Cache[T any] struct {
	cache cache.CacheInterface[string]
	ttl   time.Duration
	group singleflight.Group
}

// New 创建 Cache，c 是保存值的缓存，ttl 是值的过期时间，实际写入时随机增加至多 10%。
// c 为 nil 或 ttl 不为正时 panic。
func New[T any](c cache.CacheInterface[string], ttl time.Duration) *Cache[T] {
	switch {
	case c == nil:
		panic("cachex: nil cache")
	case ttl <= 0:
		panic("cachex: ttl must be positive")
	}
	return &Cache[T]{cache: c, ttl: ttl}
}

// Remember 返回 key 的值：缓存中有时直接返回，没有时调用 load 加载并写入缓存。
//
// 同一个进程中同一个 key 同时只有一次加载，其他调用等它的结果，各自得到独立的一份。
// 加载使用第一个调用者的 ctx 中的值和截止时间，但不随它取消：一个调用者取消时只有它返回 ctx 的错误，
// 加载继续，其他调用者照常得到结果。load 的错误不缓存，原样返回给所有等待的调用者；load 中的 panic 转为错误。
//
// load 没有找到数据时，返回 nil 指针这类编码为 JSON null 的零值和 nil 错误，空结果也会缓存约一分钟，
// 不存在的 key 不会每次都打到数据库；返回错误则不缓存。
func (c *Cache[T]) Remember(ctx context.Context, key string, load func(ctx context.Context) (T, error)) (T, error) {
	if raw, ok := c.cached(ctx, key); ok {
		v, err := decode[T](raw)
		if err == nil {
			return v, nil
		}
		slog.WarnContext(ctx, "decode cached value failed", logging.AttrComponent, "cache", "key", key, "error", err)
	}
	var zero T
	select {
	case r := <-c.group.DoChan(key, func() (any, error) { return c.fill(ctx, key, load) }):
		if r.Err != nil {
			return zero, r.Err
		}
		v, err := decode[T](r.Val.(string))
		return v, errors.Wrap(err, "cachex: decode")
	case <-ctx.Done():
		return zero, context.Cause(ctx)
	}
}

// Forget 删除 key 的值，例如数据修改之后。正在进行的加载不会被之后的 Remember 共用。
// 缓存的删除失败时返回错误。
func (c *Cache[T]) Forget(ctx context.Context, key string) error {
	c.group.Forget(key)
	return errors.Wrapf(c.cache.Delete(ctx, key), "cachex: forget %q", key)
}

// cached 读取 key 在缓存中的内容。没有缓存或读取失败时 ok 为 false，失败时记录日志。
func (c *Cache[T]) cached(ctx context.Context, key string) (raw string, ok bool) {
	raw, err := c.cache.Get(ctx, key)
	switch {
	case errors.Is(err, store.NotFound{}):
		return "", false
	case err != nil:
		slog.WarnContext(ctx, "read cache failed", logging.AttrComponent, "cache", "key", key, "error", err)
		return "", false
	}
	return raw, true
}

// fill 加载 key 的值，编码后写入缓存，返回编码后的内容。写入失败只记录日志。
func (c *Cache[T]) fill(ctx context.Context, key string, load func(ctx context.Context) (T, error)) (string, error) {
	ctx = context.WithoutCancel(ctx)
	if deadline, ok := ctx.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}
	v, err := call(ctx, load)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return "", errors.Wrapf(err, "cachex: encode %q", key)
	}
	ttl := c.ttl
	if string(data) == "null" {
		ttl = min(ttl, missingTTL)
	}
	if err := c.cache.Set(ctx, key, string(data), store.WithExpiration(withJitter(ttl))); err != nil {
		slog.WarnContext(ctx, "write cache failed", logging.AttrComponent, "cache", "key", key, "error", err)
	}
	return string(data), nil
}

// call 调用 load，把 panic 转为错误：并发等待的调用者不能被一次 panic 拖垮。
func call[T any](ctx context.Context, load func(ctx context.Context) (T, error)) (v T, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.Newf("cachex: panic: %v\n%s", r, debug.Stack())
		}
	}()
	return load(ctx)
}

// decode 解码缓存中的内容，每次得到独立的一份。
func decode[T any](raw string) (T, error) {
	var v T
	err := json.Unmarshal([]byte(raw), &v)
	return v, err
}

// withJitter 返回 ttl 加上至多 10% 的随机时长。
func withJitter(ttl time.Duration) time.Duration {
	if n := int64(float64(ttl) * jitter); n > 0 {
		return ttl + time.Duration(rand.Int64N(n))
	}
	return ttl
}
