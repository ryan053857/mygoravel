package httpx_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/ratelimit"
)

func send(h http.Handler, remote string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = remote
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func memory(rate int, interval time.Duration, burst int) ratelimit.Limiter {
	return ratelimit.NewMemory(ratelimit.Config{Rate: rate, Interval: interval, Burst: burst})
}

func TestRateLimitBurstThenRejectsPerKey(t *testing.T) {
	h := httpx.RateLimit(memory(1, time.Hour, 2), httpx.KeyByIP)(&marker{})
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

func TestRateLimitCustomKey(t *testing.T) {
	byTenant := func(r *http.Request) string { return r.Header.Get("X-Tenant") }
	h := httpx.RateLimit(memory(1, time.Hour, 1), byTenant)(&marker{})
	req := func(tenant string) int {
		r := httptest.NewRequest("GET", "/", nil)
		if tenant != "" {
			r.Header.Set("X-Tenant", tenant)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, r)
		return rr.Code
	}
	if req("a") != 200 || req("a") != 429 || req("b") != 200 {
		t.Fatal("tenants must be limited separately")
	}
	for range 2 {
		if req("") != 200 {
			t.Fatal("empty key must not be limited")
		}
	}
}

type failingLimiter struct{}

func (failingLimiter) Allow(context.Context, string) error { return errors.New("db down") }
func (failingLimiter) Reset(context.Context, string) error { return nil }

func TestRateLimitBackendFailureAndProxyResolvedClient(t *testing.T) {
	var logs bytes.Buffer
	cfg := httpx.DefaultConfig()
	cfg.ClientIP.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	router := chi.NewRouter()
	router.Use(httpx.Defaults(cfg, slog.New(slog.NewJSONHandler(&logs, nil)), nil)...)
	router.With(httpx.RateLimit(memory(1, time.Hour, 1), httpx.KeyByIP)).Get("/", func(http.ResponseWriter, *http.Request) {})
	router.With(httpx.RateLimit(failingLimiter{}, httpx.KeyByIP)).Get("/broken", func(http.ResponseWriter, *http.Request) {})
	viaProxy := func(path, client string) int {
		req := httptest.NewRequest("GET", path, nil)
		req.RemoteAddr = "10.0.0.1:5000"
		req.Header.Set("X-Forwarded-For", client)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr.Code
	}
	if viaProxy("/", "192.0.2.1") != 200 || viaProxy("/", "192.0.2.1") != 429 || viaProxy("/", "192.0.2.2") != 200 {
		t.Fatal("limiter keyed on the proxy instead of the client")
	}
	if !strings.Contains(logs.String(), `"status":429`) {
		t.Fatal("rejection missing from access log", logs.String())
	}
	if viaProxy("/broken", "192.0.2.1") != 503 || !strings.Contains(logs.String(), `"error":"db down"`) {
		t.Fatal("backend failure must return 503 and be logged", logs.String())
	}
}
