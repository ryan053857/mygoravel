package cachex_test

import (
	"context"
	"crypto/rand"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/cachex"
)

// Invalidate 一个 tag 后，带有它的值重新加载；其他 tag 不受影响，tag 在 Cache 之间共享。
func TestTags(t *testing.T) {
	ctx := context.Background()
	mc := openMemcached(t)
	ns := "test:" + rand.Text() + ":"
	articles := cachex.New[*article](mc, ns+"article:", time.Minute)
	lists := cachex.New[[]int](mc, ns+"list:", time.Minute)
	author, all := ns+"author:7", ns+"articles"

	var loads, listLoads atomic.Int64
	load := counter(&loads, &article{ID: 1, Title: "hello"}, nil)
	loadList := func(context.Context) ([]int, error) { listLoads.Add(1); return []int{1}, nil }
	remember := func() {
		t.Helper()
		if got, err := articles.Remember(ctx, "1", load, author, all); err != nil || got.Title != "hello" {
			t.Fatalf("got %+v err %v", got, err)
		}
		if _, err := lists.Remember(ctx, "page:1", loadList, all); err != nil {
			t.Fatal(err)
		}
	}

	remember()
	remember()
	if loads.Load() != 1 || listLoads.Load() != 1 {
		t.Fatalf("cached: loaded %d/%d times", loads.Load(), listLoads.Load())
	}

	if err := articles.Invalidate(ctx, author); err != nil {
		t.Fatal(err)
	}
	remember()
	if loads.Load() != 2 || listLoads.Load() != 1 {
		t.Fatalf("after invalidating author: loaded %d/%d times", loads.Load(), listLoads.Load())
	}

	// 另一个 Cache 提升的 tag 同样生效。
	if err := lists.Invalidate(ctx, all); err != nil {
		t.Fatal(err)
	}
	remember()
	if loads.Load() != 3 || listLoads.Load() != 2 {
		t.Fatalf("after invalidating all: loaded %d/%d times", loads.Load(), listLoads.Load())
	}

	if err := articles.Invalidate(ctx, ns+"never-read"); err != nil {
		t.Fatalf("invalidate an unknown tag: %v", err)
	}
}

// tag 的版本被淘汰后，之后的读取创建新的随机版本，带有它的旧值全部失效。
func TestTagEvicted(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	c := cachex.New[*article](f, "article:", time.Minute)
	var loads atomic.Int64
	load := counter(&loads, &article{ID: 1}, nil)
	if _, err := c.Remember(ctx, "1", load, "t"); err != nil {
		t.Fatal(err)
	}
	if err := f.Delete("cachex:tag:t"); err != nil {
		t.Fatal(err)
	}
	// 版本不存在时 Invalidate 什么也不用做。
	if err := c.Invalidate(ctx, "t"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := c.Remember(ctx, "1", load, "t"); err != nil {
			t.Fatal(err)
		}
	}
	if loads.Load() != 2 {
		t.Fatalf("loaded %d times", loads.Load())
	}
}

// 加载期间 Invalidate：之后的 Remember 不等待这次加载，它写入的旧值也不会被使用。
func TestInvalidateDuringLoad(t *testing.T) {
	ctx := context.Background()
	c := cachex.New[*article](newFake(), "article:", time.Minute)
	started, release := make(chan struct{}), make(chan struct{})
	var loads atomic.Int64
	stale := func(context.Context) (*article, error) {
		loads.Add(1)
		close(started)
		<-release
		return &article{ID: 1, Title: "old"}, nil
	}
	fresh := counter(&loads, &article{ID: 1, Title: "new"}, nil)

	done := make(chan *article, 1)
	go func() {
		got, _ := c.Remember(ctx, "1", stale, "t")
		done <- got
	}()
	<-started
	if err := c.Invalidate(ctx, "t"); err != nil {
		t.Fatal(err)
	}
	if got, err := c.Remember(ctx, "1", fresh, "t"); err != nil || got.Title != "new" {
		t.Fatalf("after invalidate: got %+v err %v", got, err)
	}
	close(release)
	if got := <-done; got.Title != "old" {
		t.Fatalf("first caller got %+v", got)
	}
	// 第一次加载最后写入，但它带的是旧版本。
	if got, err := c.Remember(ctx, "1", fresh, "t"); err != nil || got.Title != "new" {
		t.Fatalf("stale write was used: got %+v err %v", got, err)
	}
	if loads.Load() != 3 {
		t.Fatalf("loaded %d times", loads.Load())
	}
}

// Forget 之后的 Remember 不等待 Forget 之前开始的加载。
func TestForgetDuringLoad(t *testing.T) {
	ctx := context.Background()
	c := cachex.New[*article](newFake(), "article:", time.Minute)
	started, release := make(chan struct{}), make(chan struct{})
	go func() {
		_, _ = c.Remember(ctx, "1", func(context.Context) (*article, error) {
			close(started)
			<-release
			return &article{Title: "old"}, nil
		})
	}()
	<-started
	defer close(release)
	if err := c.Forget(ctx, "1"); err != nil {
		t.Fatal(err)
	}
	var loads atomic.Int64
	if got, err := c.Remember(ctx, "1", counter(&loads, &article{Title: "new"}, nil)); err != nil || got.Title != "new" {
		t.Fatalf("got %+v err %v", got, err)
	}
}

// 「不存在」同样带有 tag 的版本。
func TestTagsMissing(t *testing.T) {
	ctx := context.Background()
	c := cachex.New[*article](newFake(), "article:", time.Minute, cachex.Missing(errNotFound))
	var loads atomic.Int64
	for range 2 {
		if _, err := c.Remember(ctx, "404", counter(&loads, nil, errNotFound()), "t"); !errors.Is(err, errNotFound()) {
			t.Fatalf("err %v", err)
		}
	}
	if err := c.Invalidate(ctx, "t"); err != nil {
		t.Fatal(err)
	}
	if got, err := c.Remember(ctx, "404", counter(&loads, &article{ID: 404}, nil), "t"); err != nil || got.ID != 404 {
		t.Fatalf("got %+v err %v", got, err)
	}
	if loads.Load() != 2 {
		t.Fatalf("loaded %d times", loads.Load())
	}
}

// 无法确定 tag 的版本时直接加载，结果不写入缓存；提升版本失败时 Invalidate 返回错误。
func TestTagFailures(t *testing.T) {
	ctx := context.Background()
	down := errors.New("connection refused")
	for name, fail := range map[string]func(f *fake){
		"read":   func(f *fake) { f.getErr = down },
		"create": func(f *fake) { f.addErr = down },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake()
			fail(f)
			c := cachex.New[*article](f, "article:", time.Minute)
			var loads atomic.Int64
			for range 2 {
				if got, err := c.Remember(ctx, "1", counter(&loads, &article{ID: 1}, nil), "t"); err != nil || got.ID != 1 {
					t.Fatalf("got %+v err %v", got, err)
				}
			}
			if _, ok := f.values["article:1"]; ok || loads.Load() != 2 {
				t.Fatalf("cached %v, loaded %d times", ok, loads.Load())
			}
		})
	}

	f := newFake()
	c := cachex.New[*article](f, "article:", time.Minute)
	if _, err := c.Remember(ctx, "1", counter(new(atomic.Int64), &article{ID: 1}, nil), "a", "b"); err != nil {
		t.Fatal(err)
	}
	f.incrErr = down
	if err := c.Invalidate(ctx, "a", "b"); !errors.Is(err, down) {
		t.Fatalf("invalidate: %v", err)
	}
}
