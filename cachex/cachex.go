// Package cachex 在 gocache 缓存之上提供 cache-aside 读取。Remember 先读缓存，没有时加载并写回，并处理三个常见问题：
//
//   - 击穿：热点键过期的瞬间，同一个进程中同一个键的并发加载合并为一次；
//   - 雪崩：写入时过期时间加上随机抖动，同一批写入的键不会在同一时刻一起过期；
//   - 穿透：用 Missing 声明表示「不存在」的错误后，不存在的数据也会缓存约一分钟。
//
// 值编码为 JSON 存进 cache.CacheInterface[string]，内存和 Redis 等后端可以互相替换。
// 缓存读写失败或缓存中的内容无法解码时直接加载，结果照常返回：查询缓存随时可以从数据库重建，不应让请求失败。
//
//	articles := cachex.New[*article.Article](c, "blog:article:v1:", 10*time.Minute, cachex.Missing(article.ErrNotFound.New))
//	item, err := articles.Remember(ctx, id.String(), func(ctx context.Context) (*article.Article, error) {
//		return find(ctx, db, id)
//	})
package cachex

import (
	"context"
	"encoding/json"
	"log/slog"
	"math/rand/v2"
	"runtime/debug"
	"sync"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/eko/gocache/lib/v4/cache"
	"github.com/eko/gocache/lib/v4/store"
	"golang.org/x/sync/singleflight"

	"github.com/ryan053857/mygoravel/logging"
)

const (
	// missingTTL 是「不存在」在缓存中保留的最长时间，写入时同样加上抖动。
	missingTTL = time.Minute
	// jitter 是过期时间随机增加的最大比例。
	jitter = 0.1
	// missingMarker 是缓存中表示「不存在」的内容。它不是合法的 JSON，不会与值混淆。
	missingMarker = "!missing"
	// logInterval 是每个 Cache 记录缓存故障日志的最小间隔。
	logInterval = time.Minute
)

// Cache 缓存一种值，例如文章详情。它是并发安全的，每种值创建一个，在应用中共用。
type Cache[T any] struct {
	cache   cache.CacheInterface[string]
	prefix  string
	ttl     time.Duration
	missing func() error
	group   singleflight.Group
	logs    throttle
}

// Option 是 New 的选项。
type Option func(*options)

type options struct {
	missing func() error
}

// Missing 声明加载时表示「不存在」的错误：加载返回的错误与 newErr() 匹配（errors.Is）时，缓存约一分钟，
// 期间 Remember 不再加载，直接返回 newErr() 创建的新错误，不存在的 key 就不会每次都查数据库。
// 其他错误不缓存。errorx 的错误按错误码匹配，传入模板的 New 方法即可，例如 article.ErrNotFound.New。
// newErr 为 nil 时 panic。
func Missing(newErr func() error) Option {
	if newErr == nil {
		panic("cachex: nil missing error")
	}
	return func(o *options) { o.missing = newErr }
}

// New 创建 Cache。c 是保存值的缓存；prefix 加在每个 key 前面，由命名空间、值的种类和格式版本组成，
// 例如 "blog:article:v1:"，修改值的结构时升级版本；ttl 是值的过期时间，实际写入时随机增加至多 10%。
// c 为 nil 或 ttl 不为正时 panic。
func New[T any](c cache.CacheInterface[string], prefix string, ttl time.Duration, opts ...Option) *Cache[T] {
	switch {
	case c == nil:
		panic("cachex: nil cache")
	case ttl <= 0:
		panic("cachex: ttl must be positive")
	}
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return &Cache[T]{cache: c, prefix: prefix, ttl: ttl, missing: o.missing}
}

// Remember 返回 key 的值：缓存中有时直接返回，没有时调用 load 加载并写入缓存。
//
// 同一个进程中同一个 key 同时只有一次加载，其他调用等它的结果，各自得到独立的一份。
// 加载使用第一个调用者的 ctx 中的值和截止时间，但不随它取消：一个调用者取消时只有它返回 ctx 的错误，
// 加载继续，其他调用者照常得到结果。load 的错误原样返回给所有等待的调用者，
// 除了 Missing 声明的错误都不缓存；load 中的 panic 转为错误。
func (c *Cache[T]) Remember(ctx context.Context, key string, load func(ctx context.Context) (T, error)) (T, error) {
	key = c.prefix + key
	var zero T
	if raw, ok := c.cached(ctx, key); ok {
		if raw == missingMarker && c.missing != nil {
			return zero, c.missing()
		}
		v, err := decode[T](raw)
		if err == nil {
			return v, nil
		}
		c.logs.warn(ctx, "decode cached value failed", "key", key, "error", err)
	}
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
	key = c.prefix + key
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
		c.logs.warn(ctx, "read cache failed", "key", key, "error", err)
		return "", false
	}
	return raw, true
}

// fill 加载 key 的值，编码后写入缓存，返回编码后的内容。Missing 声明的错误写入「不存在」的标记后原样返回。
func (c *Cache[T]) fill(ctx context.Context, key string, load func(ctx context.Context) (T, error)) (string, error) {
	ctx = context.WithoutCancel(ctx)
	if deadline, ok := ctx.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}
	v, err := call(ctx, load)
	if err != nil {
		if c.missing != nil && errors.Is(err, c.missing()) {
			c.write(ctx, key, missingMarker, min(c.ttl, missingTTL))
		}
		return "", err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return "", errors.Wrapf(err, "cachex: encode %q", key)
	}
	c.write(ctx, key, string(data), c.ttl)
	return string(data), nil
}

// write 以加上抖动的 ttl 写入缓存，失败只记录日志。
func (c *Cache[T]) write(ctx context.Context, key, raw string, ttl time.Duration) {
	if err := c.cache.Set(ctx, key, raw, store.WithExpiration(withJitter(ttl))); err != nil {
		c.logs.warn(ctx, "write cache failed", "key", key, "error", err)
	}
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

// throttle 限制缓存故障日志的频率：缓存后端不可用时每个请求都会出错，每分钟只记录一条，附上期间省略的条数。
type throttle struct {
	mu         sync.Mutex
	next       time.Time
	suppressed int
}

func (t *throttle) warn(ctx context.Context, msg string, args ...any) {
	t.mu.Lock()
	now := time.Now()
	if now.Before(t.next) {
		t.suppressed++
		t.mu.Unlock()
		return
	}
	suppressed := t.suppressed
	t.next, t.suppressed = now.Add(logInterval), 0
	t.mu.Unlock()
	args = append(args, logging.AttrComponent, "cache")
	if suppressed > 0 {
		args = append(args, "suppressed", suppressed)
	}
	slog.WarnContext(ctx, msg, args...)
}
