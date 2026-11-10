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
)

var (
	errRevoked     = errorx.Define(errorx.Unauthenticated, "auth.unauthenticated")
	errUnavailable = errorx.Define(errorx.Unavailable, "users.unavailable")
)

func TestCheckCredential(t *testing.T) {
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
