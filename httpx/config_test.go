package httpx_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/httpx"
)

func TestDefaultConfigIsValid(t *testing.T) {
	if err := httpx.DefaultConfig().Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*httpx.Config){
		func(c *httpx.Config) { c.Addr = "" },
		func(c *httpx.Config) { c.ReadTimeout = 0 },
		func(c *httpx.Config) { c.ShutdownTimeout = -time.Second },
		func(c *httpx.Config) { c.MaxBodyBytes = 0 },
	} {
		cfg := httpx.DefaultConfig()
		mutate(&cfg)
		if cfg.Validate() == nil {
			t.Fatalf("accepted %+v", cfg)
		}
	}
}

func TestListenAndServeStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- httpx.ListenAndServe(ctx, httpx.Config{Addr: "127.0.0.1:0"}, http.NotFoundHandler(), httpx.ServeOptions{})
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not stop")
	}
}

func TestListenAndServeReportsListenError(t *testing.T) {
	err := httpx.ListenAndServe(context.Background(), httpx.Config{Addr: "invalid:address:"}, http.NotFoundHandler(), httpx.ServeOptions{})
	if err == nil {
		t.Fatal("expected listen error")
	}
}
