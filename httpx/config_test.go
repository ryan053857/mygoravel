package httpx_test

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"
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
		func(c *httpx.Config) { c.Addr = "8080" },
		func(c *httpx.Config) { c.Addr = "not an addr" },
		func(c *httpx.Config) { c.Addr = ":99999" },
		func(c *httpx.Config) { c.Addr = ":http" },
		func(c *httpx.Config) { c.ReadTimeout = 0 },
		func(c *httpx.Config) { c.ShutdownTimeout = -time.Second },
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
		done <- httpx.ListenAndServe(ctx, addr("127.0.0.1:0"), http.NotFoundHandler(), httpx.ServeOptions{})
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
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	err = httpx.ListenAndServe(context.Background(), addr(busy.Addr().String()), http.NotFoundHandler(), httpx.ServeOptions{})
	if err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatal("expected listen error", err)
	}
}

func TestServeRejectsInvalidConfig(t *testing.T) {
	err := httpx.ListenAndServe(context.Background(), httpx.Config{Addr: "127.0.0.1:0"}, http.NotFoundHandler(), httpx.ServeOptions{})
	if err == nil || !strings.Contains(err.Error(), "must be positive") {
		t.Fatal("zero timeouts must be rejected, not defaulted", err)
	}
}

func TestServeClosesListenerOnInvalidConfig(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if httpx.Serve(context.Background(), httpx.Config{}, ln, http.NotFoundHandler(), httpx.ServeOptions{}) == nil {
		t.Fatal("invalid config accepted")
	}
	if _, err := ln.Accept(); err == nil {
		t.Fatal("listener left open; clients would hang")
	}
}

func TestMiddlewareConfigsValidate(t *testing.T) {
	if err := httpx.DefaultRateLimitConfig().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"empty prefix":  {httpx.ClientIPConfig{TrustedProxies: []netip.Prefix{{}}}.Validate(), "trusted_proxies[0] is not a valid network"},
		"host bits":     {httpx.ClientIPConfig{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("10.0.0.5/8")}}.Validate(), "trusted_proxies[1]: 10.0.0.5/8 has host bits set; use 10.0.0.0/8 or 10.0.0.5/32"},
		"ipv6 host":     {httpx.ClientIPConfig{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("fd00::1/64")}}.Validate(), "use fd00::/64 or fd00::1/128"},
		"timeout":       {httpx.TimeoutConfig{}.Validate(), "duration must be positive"},
		"body limit":    {httpx.BodyLimitConfig{MaxBytes: -1}.Validate(), "max_bytes must be positive"},
		"rate":          {httpx.RateLimitConfig{Interval: time.Second, Burst: 1, MaxEntries: 1}.Validate(), "rate must be positive"},
		"rate interval": {httpx.RateLimitConfig{Rate: 1, Burst: 1, MaxEntries: 1}.Validate(), "interval must be positive"},
	} {
		if tc.err == nil || !strings.Contains(tc.err.Error(), tc.want) {
			t.Fatalf("%s: got %v, want %q", name, tc.err, tc.want)
		}
	}
	for _, ok := range []string{"10.0.0.0/8", "10.0.0.5/32", "0.0.0.0/0", "::/0", "fd00::/64"} {
		if err := (httpx.ClientIPConfig{TrustedProxies: []netip.Prefix{netip.MustParsePrefix(ok)}}).Validate(); err != nil {
			t.Fatal(ok, err)
		}
	}
}

func addr(a string) httpx.Config {
	cfg := httpx.DefaultConfig()
	cfg.Addr = a
	return cfg
}
