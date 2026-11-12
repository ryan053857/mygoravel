package httpx_test

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/identity"
)

var (
	errRevoked     = errorx.Define(errorx.Unauthenticated, "auth.unauthenticated")
	errUnavailable = errorx.Define(errorx.Unavailable, "users.unavailable")
)

func TestCheckCredential(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if err := httpx.CheckCredential(ctx); err != nil {
		t.Fatalf("no checks: %v", err)
	}
	ok := httpx.WithCredentialCheck(ctx, func(context.Context) error { return nil })
	if err := httpx.CheckCredential(ok); err != nil {
		t.Fatalf("passing check: %v", err)
	}
	// 暂时无法判断（5xx 类、普通错误）不算失效。
	for _, err := range []error{errUnavailable.New(), errors.New("db down")} {
		c := httpx.WithCredentialCheck(ok, func(context.Context) error { return err })
		if got := httpx.CheckCredential(c); got != nil {
			t.Fatalf("%v treated as revoked", err)
		}
	}
	// 多个校验依次运行，返回第一个失效的错误；派生出的 context 互不影响。
	revoked := httpx.WithCredentialCheck(ok, func(context.Context) error { return errRevoked.New() })
	if err := httpx.CheckCredential(revoked); !errors.Is(err, errRevoked) {
		t.Fatalf("revoked: %v", err)
	}
	if err := httpx.CheckCredential(ok); err != nil {
		t.Fatalf("parent context changed: %v", err)
	}
}

func TestSSEEndsWhenCredentialRevoked(t *testing.T) {
	defer httpx.SetCredentialInterval(50 * time.Millisecond)()
	var revoked, unavailable atomic.Bool
	ended := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := httpx.WithCredentialCheck(r.Context(), func(context.Context) error {
			switch {
			case unavailable.Load():
				return errUnavailable.New()
			case revoked.Load():
				return errRevoked.New()
			}
			return nil
		})
		_ = httpx.SSE(w, r.WithContext(ctx), func(s *httpx.Stream) error {
			<-s.Done()
			close(ended)
			return nil
		})
	})
	url, _ := startServer(t, httpx.DefaultConfig(), handler)
	lines, _ := readLines(t, url, nil)
	_ = lines

	unavailable.Store(true) // 校验暂时失败时连接保持
	select {
	case <-ended:
		t.Fatal("stream ended while the credential check was unavailable")
	case <-time.After(300 * time.Millisecond):
	}
	unavailable.Store(false)
	revoked.Store(true)
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("stream not ended after the credential was revoked")
	}
}

// fakeWatcher 记录 Watch 收到的主体，Signal 发出复查信号。
type fakeWatcher struct {
	ch      chan struct{}
	watched chan identity.Subject
}

func (w *fakeWatcher) Watch(_ context.Context, sub identity.Subject) <-chan struct{} {
	w.watched <- sub
	return w.ch
}

// 安装了 CredentialWatcher 时，收到信号立即复查，不等定期校验。
func TestSSERechecksOnSignal(t *testing.T) {
	t.Parallel()
	var revoked atomic.Bool
	var checks atomic.Int32
	w := &fakeWatcher{ch: make(chan struct{}, 1), watched: make(chan identity.Subject, 1)}
	ended := make(chan struct{})
	handler := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		ctx := identity.WithSubject(r.Context(), identity.Subject{ID: "alice", Credential: "t1"})
		ctx = httpx.WithCredentialCheck(ctx, func(context.Context) error {
			checks.Add(1)
			if revoked.Load() {
				return errRevoked.New()
			}
			return nil
		})
		ctx = httpx.WithCredentialWatcher(ctx, w)
		_ = httpx.SSE(rw, r.WithContext(ctx), func(s *httpx.Stream) error {
			<-s.Done()
			close(ended)
			return nil
		})
	})
	url, _ := startServer(t, httpx.DefaultConfig(), handler)
	readLines(t, url, nil)
	if sub := <-w.watched; sub.ID != "alice" || sub.Credential != "t1" {
		t.Fatalf("watched %+v", sub)
	}

	// 凭据仍然有效：复查通过，连接保持。
	w.ch <- struct{}{}
	deadline := time.Now().Add(2 * time.Second)
	for checks.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if checks.Load() == 0 {
		t.Fatal("signal did not trigger a recheck")
	}
	select {
	case <-ended:
		t.Fatal("stream ended although the credential is valid")
	case <-time.After(200 * time.Millisecond):
	}

	// 撤销后发出信号，连接随即结束；定期校验间隔仍是 30 秒。
	revoked.Store(true)
	w.ch <- struct{}{}
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("stream not ended after the recheck signal")
	}
}

// Watch 返回的通道被关闭时不会空转，连接照常保持。
func TestSSEIgnoresClosedWatchChannel(t *testing.T) {
	t.Parallel()
	var checks atomic.Int32
	w := &fakeWatcher{ch: make(chan struct{}), watched: make(chan identity.Subject, 1)}
	close(w.ch)
	ended := make(chan struct{})
	handler := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		ctx := identity.WithSubject(r.Context(), identity.Subject{ID: "alice"})
		ctx = httpx.WithCredentialCheck(ctx, func(context.Context) error { checks.Add(1); return nil })
		ctx = httpx.WithCredentialWatcher(ctx, w)
		_ = httpx.SSE(rw, r.WithContext(ctx), func(s *httpx.Stream) error {
			<-s.Done()
			close(ended)
			return nil
		})
	})
	url, _ := startServer(t, httpx.DefaultConfig(), handler)
	readLines(t, url, nil)
	time.Sleep(200 * time.Millisecond)
	if n := checks.Load(); n > 1 {
		t.Fatalf("%d rechecks from a closed channel", n)
	}
	select {
	case <-ended:
		t.Fatal("stream ended")
	default:
	}
}
