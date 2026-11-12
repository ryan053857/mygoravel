// Package cachemon 记录 gocache 缓存的命中、未命中和出错次数，保存在 monitor 的运行统计中。
//
//	articles := cachemon.Wrap("articles", cache.New[string](store), rec)
//
// 包装后仍是 cache.CacheInterface，业务代码不用修改，内存、Redis 和多级缓存都适用。
package cachemon

import (
	"context"
	"errors"
	"time"

	"github.com/eko/gocache/lib/v4/cache"
	"github.com/eko/gocache/lib/v4/store"

	"github.com/ryan053857/mygoravel/monitor"
)

// Wrap 记录的运行统计，label 是缓存名，值是操作的耗时（秒）。
const (
	// MetricHit 是命中的 Get。
	MetricHit = "cache.hit"
	// MetricMiss 是键不存在或已过期的 Get。
	MetricMiss = "cache.miss"
	// MetricError 是出错的操作，包括 Get、Set、Delete、Invalidate 和 Clear。
	MetricError = "cache.error"
)

// Wrap 返回记录命中率的 c，name 是缓存名，例如 "articles"。
// 未命中（store.NotFound）和出错分开记录：Redis 断线表现为出错，而不是命中率下降。
// name 超过 monitor.MaxLabelLength 字节或 c、rec 为 nil 时 panic。
func Wrap[T any](name string, c cache.CacheInterface[T], rec *monitor.Recorder) cache.CacheInterface[T] {
	switch {
	case c == nil || rec == nil:
		panic("cachemon: nil cache or recorder")
	case len(name) > monitor.MaxLabelLength:
		panic("cachemon: cache name is too long")
	}
	return &recorded[T]{CacheInterface: c, name: name, rec: rec}
}

type recorded[T any] struct {
	cache.CacheInterface[T]
	name string
	rec  *monitor.Recorder
}

func (c *recorded[T]) Get(ctx context.Context, key any) (T, error) {
	started := time.Now()
	v, err := c.CacheInterface.Get(ctx, key)
	metric := MetricHit
	switch {
	case errors.Is(err, store.NotFound{}):
		metric = MetricMiss
	case err != nil:
		metric = MetricError
	}
	c.rec.Record(metric, c.name, time.Since(started).Seconds())
	return v, err
}

func (c *recorded[T]) Set(ctx context.Context, key any, object T, options ...store.Option) error {
	return c.track(func() error { return c.CacheInterface.Set(ctx, key, object, options...) })
}

func (c *recorded[T]) Delete(ctx context.Context, key any) error {
	return c.track(func() error { return c.CacheInterface.Delete(ctx, key) })
}

func (c *recorded[T]) Invalidate(ctx context.Context, options ...store.InvalidateOption) error {
	return c.track(func() error { return c.CacheInterface.Invalidate(ctx, options...) })
}

func (c *recorded[T]) Clear(ctx context.Context) error {
	return c.track(func() error { return c.CacheInterface.Clear(ctx) })
}

// track 执行一个写操作，出错时记录。
func (c *recorded[T]) track(op func() error) error {
	started := time.Now()
	err := op()
	if err != nil {
		c.rec.Record(MetricError, c.name, time.Since(started).Seconds())
	}
	return err
}

// CacheActivity 是一个缓存在一段时间内的使用情况。
type CacheActivity struct {
	// Hits、Misses 是命中和未命中的 Get，Errors 是出错的操作。
	Hits, Misses, Errors int64
}

// HitRate 返回命中占命中与未命中之和的比例（0 到 1），没有 Get 时为 0。出错的操作不计入。
func (a CacheActivity) HitRate() float64 {
	if a.Hits+a.Misses == 0 {
		return 0
	}
	return float64(a.Hits) / float64(a.Hits+a.Misses)
}

// Activity 返回 Wrap 记录的每个缓存在 from 到 to 之间的使用情况，键是缓存名。
// 时间范围的含义见 monitor.Store.Totals。
func Activity(ctx context.Context, stats *monitor.Store, from, to time.Time) (map[string]CacheActivity, error) {
	out := map[string]CacheActivity{}
	for _, metric := range []string{MetricHit, MetricMiss, MetricError} {
		totals, err := stats.Totals(ctx, metric, from, to)
		if err != nil {
			return nil, err
		}
		for _, t := range totals {
			a := out[t.Label]
			switch metric {
			case MetricHit:
				a.Hits = t.Count
			case MetricMiss:
				a.Misses = t.Count
			case MetricError:
				a.Errors = t.Count
			}
			out[t.Label] = a
		}
	}
	return out, nil
}
