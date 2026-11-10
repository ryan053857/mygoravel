package httpx_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ryan053857/mygoravel/httpx"
)

func send(h http.Handler, remote string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = remote
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestRateLimitBurstThenRejectsPerKey(t *testing.T) {
	h := httpx.RateLimit(limit(1, time.Hour, 2, 10), httpx.KeyByIP)(&marker{})
	for i, want := range []int{200, 200, 429} {
		if rr := send(h, "192.0.2.1:1000"); rr.Code != want {
			t.Fatalf("request %d: %d", i, rr.Code)
		}
	}
	rr := send(h, "192.0.2.1:2000")
	retry, _ := strconv.Atoi(rr.Header().Get("Retry-After"))
	if rr.Code != 429 || retry < 3500 || rr.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatal("same host on a new port must share the bucket", rr.Code, rr.Header())
	}
	if rr := send(h, "192.0.2.2:1000"); rr.Code != 200 {
		t.Fatal("other clients must have their own bucket", rr.Code)
	}
}

func TestRateLimitRefillsAndRejectedRequestsDoNotConsume(t *testing.T) {
	h := httpx.RateLimit(limit(1, 50*time.Millisecond, 1, 10), httpx.KeyByIP)(&marker{})
	if send(h, "192.0.2.1:1").Code != 200 {
		t.Fatal("first request rejected")
	}
	for range 5 {
		if send(h, "192.0.2.1:1").Code != 429 {
			t.Fatal("burst exceeded")
		}
	}
	time.Sleep(60 * time.Millisecond)
	if rr := send(h, "192.0.2.1:1"); rr.Code != 200 {
		t.Fatal("rejected requests drained the bucket", rr.Code)
	}
}

func TestRateLimitEvictsLeastRecentKey(t *testing.T) {
	h := httpx.RateLimit(limit(1, time.Hour, 1, 2), httpx.KeyByIP)(&marker{})
	send(h, "192.0.2.1:1")
	send(h, "192.0.2.2:1")
	send(h, "192.0.2.1:1") // .1 最近访问，.2 最久
	send(h, "192.0.2.3:1") // 淘汰 .2
	if send(h, "192.0.2.1:1").Code != 429 {
		t.Fatal("recently used key was evicted")
	}
	if send(h, "192.0.2.2:1").Code != 200 {
		t.Fatal("least recent key was kept")
	}
}

func TestRateLimitCustomKey(t *testing.T) {
	byToken := func(r *http.Request) string { return r.Header.Get("Authorization") }
	h := httpx.RateLimit(limit(1, time.Hour, 1, 10), byToken)(&marker{})
	req := func(token string) int {
		r := httptest.NewRequest("GET", "/", nil)
		if token != "" {
			r.Header.Set("Authorization", token)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, r)
		return rr.Code
	}
	if req("a") != 200 || req("a") != 429 || req("b") != 200 {
		t.Fatal("tokens must be limited separately")
	}
	if req("") != 200 || req("") != 200 {
		t.Fatal("empty key must not be limited")
	}
}

func TestRateLimitConcurrentSafe(t *testing.T) {
	h := httpx.RateLimit(limit(50, time.Hour, 50, 8), httpx.KeyByIP)(&marker{})
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			for j := range 50 {
				send(h, "192.0.2."+strconv.Itoa((i+j)%16)+":1")
			}
		})
	}
	wg.Wait()
}

func TestRateLimitAfterDefaultsUsesProxyResolvedClientAndLogs(t *testing.T) {
	var logs bytes.Buffer
	cfg := httpx.DefaultConfig()
	cfg.ClientIP.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	router := chi.NewRouter()
	router.Use(httpx.Defaults(cfg, slog.New(slog.NewJSONHandler(&logs, nil)))...)
	router.Use(httpx.RateLimit(limit(1, time.Hour, 1, 10), httpx.KeyByIP))
	router.Get("/", func(http.ResponseWriter, *http.Request) {})
	viaProxy := func(client string) int {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "10.0.0.1:5000"
		req.Header.Set("X-Forwarded-For", client)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr.Code
	}
	if viaProxy("192.0.2.1") != 200 || viaProxy("192.0.2.1") != 429 || viaProxy("192.0.2.2") != 200 {
		t.Fatal("limiter keyed on the proxy instead of the client")
	}
	if !strings.Contains(logs.String(), `"status":429`) {
		t.Fatal("rejection missing from access log", logs.String())
	}
}

func limit(rate int, interval time.Duration, burst, maxEntries int) httpx.RateLimitConfig {
	return httpx.RateLimitConfig{Rate: rate, Interval: interval, Burst: burst, MaxEntries: maxEntries}
}
