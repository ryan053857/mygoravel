package cachemon_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/eko/gocache/lib/v4/cache"
	"github.com/eko/gocache/lib/v4/store"
	gocachestore "github.com/eko/gocache/store/go_cache/v4"
	memory "github.com/patrickmn/go-cache"

	"github.com/ryan053857/mygoravel/database/sqlite"
	"github.com/ryan053857/mygoravel/internal/dbtesting"
	"github.com/ryan053857/mygoravel/monitor"
	"github.com/ryan053857/mygoravel/monitor/cachemon"
)

// broken 是每个操作都出错的缓存，例如 Redis 断线。
type broken struct{ cache.CacheInterface[string] }

var errDown = errors.New("connection refused")

func (broken) Get(context.Context, any) (string, error)                    { return "", errDown }
func (broken) Set(context.Context, any, string, ...store.Option) error     { return errDown }
func (broken) Delete(context.Context, any) error                           { return errDown }
func (broken) Invalidate(context.Context, ...store.InvalidateOption) error { return errDown }
func (broken) Clear(context.Context) error                                 { return errDown }

func TestWrap(t *testing.T) {
	ctx := context.Background()
	stats := monitor.NewStore(dbtesting.Open(t, sqlite.Driver, monitor.Migrations()))
	rec := monitor.NewRecorder(stats, nil)
	t.Cleanup(func() { _ = rec.Close(ctx) })

	articles := cachemon.Wrap("articles", cache.New[string](gocachestore.NewGoCache(memory.New(time.Minute, 0))), rec)
	if _, err := articles.Get(ctx, "a"); !errors.Is(err, store.NotFound{}) {
		t.Fatalf("miss: %v", err)
	}
	if err := articles.Set(ctx, "a", "hello"); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if v, err := articles.Get(ctx, "a"); err != nil || v != "hello" {
			t.Fatalf("hit %q %v", v, err)
		}
	}
	if err := articles.Delete(ctx, "a"); err != nil {
		t.Fatal(err)
	}

	down := cachemon.Wrap("sessions", cache.CacheInterface[string](broken{}), rec)
	if _, err := down.Get(ctx, "a"); !errors.Is(err, errDown) {
		t.Fatalf("get: %v", err)
	}
	for _, err := range []error{down.Set(ctx, "a", "x"), down.Delete(ctx, "a"), down.Invalidate(ctx), down.Clear(ctx)} {
		if !errors.Is(err, errDown) {
			t.Fatalf("write: %v", err)
		}
	}
	if err := rec.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	caches, err := cachemon.Activity(ctx, stats, now.Add(-time.Hour), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	a, s := caches["articles"], caches["sessions"]
	if a.Hits != 3 || a.Misses != 1 || a.Errors != 0 || a.HitRate() != 0.75 {
		t.Fatalf("articles %+v", a)
	}
	if s.Hits != 0 || s.Misses != 0 || s.Errors != 5 || s.HitRate() != 0 {
		t.Fatalf("sessions %+v", s)
	}
}
