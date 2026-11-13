// Package cachex 在 Memcached 之上提供 cache-aside 读取。Remember 先读缓存，没有时加载并写回，并处理三个常见问题：
//
//   - 击穿：热点键过期的瞬间，同一个进程中同一个键的并发加载合并为一次；
//   - 雪崩：写入时过期时间加上随机抖动，同一批写入的键不会在同一时刻一起过期；
//   - 穿透：用 Missing 声明表示「不存在」的错误后，不存在的数据也会缓存约一分钟。
//
// 一次修改影响很多键时，例如修改角色影响所有拥有它的用户，读取时给值加上 tag，修改后用 Invalidate 让带有这个 tag 的值一起失效。
//
// 值编码为 JSON 存进 Memcached，客户端是应用共用的 *memcache.Client（github.com/bradfitz/gomemcache）。
// 缓存读写失败或缓存中的内容无法解码时直接加载，结果照常返回：查询缓存随时可以从数据库重建，不应让请求失败。
//
//	articles := cachex.New[*article.Article](mc, "blog:article:v1:", 10*time.Minute, cachex.Missing(article.ErrNotFound.New))
//	item, err := articles.Remember(ctx, id.String(), func(ctx context.Context) (*article.Article, error) {
//		return find(ctx, db, id)
//	})
package cachex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"math/rand/v2"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bradfitz/gomemcache/memcache"
	"github.com/cockroachdb/errors"
	"golang.org/x/sync/singleflight"

	"github.com/go-tenon/tenon/logging"
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
	// maxRelative 是 Memcached 按相对秒数理解的最长过期时间，更长的要写成 Unix 时间。
	maxRelative = 30 * 24 * time.Hour
	// tagPrefix 是 tag 版本在 Memcached 中的键的前缀。tag 不属于某个 Cache，不加 Cache 的前缀。
	tagPrefix = "cachex:tag:"
)

// Client 是 Cache 用到的 Memcached 操作，*memcache.Client 实现了它。
type Client interface {
	Get(key string) (*memcache.Item, error)
	GetMulti(keys []string) (map[string]*memcache.Item, error)
	Set(item *memcache.Item) error
	Add(item *memcache.Item) error
	Increment(key string, delta uint64) (uint64, error)
	Delete(key string) error
}

// Cache 缓存一种值，例如文章详情。它是并发安全的，每种值创建一个，在应用中共用。
type Cache[T any] struct {
	mc      Client
	prefix  string
	ttl     time.Duration
	missing func() error
	group   singleflight.Group
	// forgets 是 Forget 的次数，计入合并加载的键：Forget 之前开始的加载不与之后的 Remember 共用。
	forgets atomic.Uint64
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

// New 创建 Cache。mc 通常是应用共用的 *memcache.Client；prefix 加在每个 key 前面，由命名空间、值的种类和格式版本组成，
// 例如 "blog:article:v1:"，修改值的结构时升级版本；ttl 是值的过期时间，实际写入时随机增加至多 10%，按秒向上取整。
// 加上前缀后超过 Memcached 的 250 字节或含有空白、控制字符的 key 以 SHA-256 摘要保存。
// mc 为 nil 或 ttl 不为正时 panic。
func New[T any](mc Client, prefix string, ttl time.Duration, opts ...Option) *Cache[T] {
	switch {
	case mc == nil:
		panic("cachex: nil client")
	case ttl <= 0:
		panic("cachex: ttl must be positive")
	}
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return &Cache[T]{mc: mc, prefix: prefix, ttl: ttl, missing: o.missing}
}

// Remember 返回 key 的值：缓存中有时直接返回，没有时调用 load 加载并写入缓存。
//
// 同一个进程中同一个 key 同时只有一次加载，其他调用等它的结果，各自得到独立的一份。
// 加载使用第一个调用者的 ctx 中的值和截止时间，但不随它取消：一个调用者取消时只有它返回 ctx 的错误，
// 加载继续，其他调用者照常得到结果。load 的错误原样返回给所有等待的调用者，
// 除了 Missing 声明的错误都不缓存；load 中的 panic 转为错误。
//
// tags 给值加上标签，Invalidate 其中任何一个之后，缓存的值不再使用。tag 的版本和值在同一次请求中读取；
// 版本在加载之前读取，所以 Invalidate 返回之后，Remember 不会返回在 Invalidate 之前开始加载的值。
// 读取 tag 的版本失败时直接加载，结果不写入缓存。同一个 key 每次应传入相同的 tags。
func (c *Cache[T]) Remember(ctx context.Context, key string, load func(ctx context.Context) (T, error), tags ...string) (T, error) {
	key = c.storedKey(key)
	var zero T
	raw, header, hit, store := c.lookup(ctx, key, tags)
	if hit {
		if raw == missingMarker && c.missing != nil {
			return zero, c.missing()
		}
		v, err := decode[T](raw)
		if err == nil {
			return v, nil
		}
		c.logs.warn(ctx, "decode cached value failed", "key", key, "error", err)
	}
	// 版本不同的加载不共用：版本变化之后的 Remember 不能等待变化之前开始的加载。
	flight := strconv.FormatUint(c.forgets.Load(), 10) + "\x00" + header + key
	select {
	case r := <-c.group.DoChan(flight, func() (any, error) { return c.fill(ctx, key, header, store, load) }):
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
	key = c.storedKey(key)
	c.forgets.Add(1)
	if err := c.mc.Delete(key); err != nil && !errors.Is(err, memcache.ErrCacheMiss) {
		return errors.Wrapf(err, "cachex: forget %q", key)
	}
	return nil
}

// Invalidate 让带有 tags 中任何一个的值失效：提升这些 tag 的版本，之后的 Remember 发现版本不一致时重新加载。
// tag 在共用同一个 Memcached 的所有 Cache 之间共享，Invalidate 一个 tag 影响所有 Cache 中带有它的值。
// 和 Forget 一样，数据修改之后、事务提交之后调用。提升版本失败时返回错误，其余的 tag 照常处理。
func (c *Cache[T]) Invalidate(ctx context.Context, tags ...string) error {
	var errs []error
	for _, tag := range tags {
		// 版本不存在（从未读取或已被淘汰）时不用处理：之后的 Remember 会创建一个随机的新版本，与缓存中的值都不一致。
		if _, err := c.mc.Increment(tagKey(tag), 1); err != nil && !errors.Is(err, memcache.ErrCacheMiss) {
			errs = append(errs, errors.Wrapf(err, "cachex: invalidate %q", tag))
		}
	}
	return errors.Join(errs...)
}

// storedKey 返回 key 在 Memcached 中的键：加上前缀，不合法时换成摘要。
func (c *Cache[T]) storedKey(key string) string {
	return legalized(c.prefix + key)
}

// tagKey 返回 tag 的版本在 Memcached 中的键。
func tagKey(tag string) string {
	return legalized(tagPrefix + tag)
}

// legalized 返回 key 本身，不能直接作为 Memcached 的键时换成摘要。
func legalized(key string) string {
	if legalKey(key) {
		return key
	}
	sum := sha256.Sum256([]byte(key))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// legalKey 报告 key 是否能直接作为 Memcached 的键：最长 250 字节，不含空白和控制字符。
func legalKey(key string) bool {
	if len(key) > 250 {
		return false
	}
	for i := range len(key) {
		if key[i] <= ' ' || key[i] == 0x7f {
			return false
		}
	}
	return true
}

// lookup 读取 key 在缓存中的内容和 tags 的当前版本，二者在同一次请求中读取。
// hit 表示缓存中有内容，并且它的版本与 tags 当前的版本一致；header 是写回时放在内容之前的版本，没有 tags 时为空；
// store 为 false 表示无法确定版本，加载的结果不写回。
func (c *Cache[T]) lookup(ctx context.Context, key string, tags []string) (raw, header string, hit, store bool) {
	if len(tags) == 0 {
		raw, hit = c.cached(ctx, key)
		return raw, "", hit, true
	}
	keys := make([]string, 0, len(tags)+1)
	keys = append(keys, key)
	for _, tag := range tags {
		keys = append(keys, tagKey(tag))
	}
	items, err := c.mc.GetMulti(keys)
	if err != nil {
		c.logs.warn(ctx, "read cache failed", "key", key, "error", err)
		return "", "", false, false
	}
	versions := make([]string, len(tags))
	for i, k := range keys[1:] {
		if item, ok := items[k]; ok {
			versions[i] = strings.TrimSpace(string(item.Value))
			continue
		}
		if versions[i], err = c.newVersion(k); err != nil {
			c.logs.warn(ctx, "create cache tag failed", "tag", tags[i], "error", err)
			return "", "", false, false
		}
	}
	header = "#" + strings.Join(versions, ",") + "\n"
	if item, ok := items[key]; ok {
		if raw, ok := strings.CutPrefix(string(item.Value), header); ok {
			return raw, header, true, true
		}
	}
	return "", header, false, true
}

// newVersion 为不存在的 tag 创建版本并返回。版本是随机数，与被淘汰之前的版本几乎不可能相同，
// 所以 tag 被淘汰后，带有它的旧值都会失效。另一个调用者先创建时，返回它创建的版本。
func (c *Cache[T]) newVersion(key string) (string, error) {
	v := strconv.FormatUint(rand.Uint64(), 10)
	err := c.mc.Add(&memcache.Item{Key: key, Value: []byte(v)})
	if err == nil {
		return v, nil
	}
	if !errors.Is(err, memcache.ErrNotStored) {
		return "", err
	}
	item, err := c.mc.Get(key)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(item.Value)), nil
}

// cached 读取 key 在缓存中的内容。没有缓存或读取失败时 ok 为 false，失败时记录日志。
func (c *Cache[T]) cached(ctx context.Context, key string) (raw string, ok bool) {
	item, err := c.mc.Get(key)
	switch {
	case errors.Is(err, memcache.ErrCacheMiss):
		return "", false
	case err != nil:
		c.logs.warn(ctx, "read cache failed", "key", key, "error", err)
		return "", false
	}
	return string(item.Value), true
}

// fill 加载 key 的值，编码后写入缓存，返回编码后的内容。Missing 声明的错误写入「不存在」的标记后原样返回。
// 写入的内容以 header 开头；store 为 false 时不写入。
func (c *Cache[T]) fill(ctx context.Context, key, header string, store bool, load func(ctx context.Context) (T, error)) (string, error) {
	ctx = context.WithoutCancel(ctx)
	if deadline, ok := ctx.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}
	v, err := call(ctx, load)
	if err != nil {
		if store && c.missing != nil && errors.Is(err, c.missing()) {
			c.write(ctx, key, header+missingMarker, min(c.ttl, missingTTL))
		}
		return "", err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return "", errors.Wrapf(err, "cachex: encode %q", key)
	}
	if store {
		c.write(ctx, key, header+string(data), c.ttl)
	}
	return string(data), nil
}

// write 以加上抖动的 ttl 写入缓存，失败只记录日志。
func (c *Cache[T]) write(ctx context.Context, key, raw string, ttl time.Duration) {
	if err := c.mc.Set(&memcache.Item{Key: key, Value: []byte(raw), Expiration: expiration(withJitter(ttl))}); err != nil {
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

// expiration 把 ttl 转换为 Memcached 的过期时间：按秒向上取整；超过 30 天时 Memcached 把数值当作 Unix 时间，
// 所以写成到期的 Unix 秒。
func expiration(ttl time.Duration) int32 {
	if ttl > maxRelative {
		return int32(time.Now().Add(ttl).Unix())
	}
	return int32((ttl + time.Second - 1) / time.Second)
}
