package httpx_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/errors"

	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/logging/logtest"
)

type readyBody struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

func probe(t *testing.T, h http.Handler) (int, readyBody) {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	var body readyBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q: %v", w.Body, err)
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("headers %v", w.Header())
	}
	return w.Code, body
}

func TestReady(t *testing.T) {
	t.Parallel()
	var dbDown, cacheDown atomic.Bool
	check := func(down *atomic.Bool) func(context.Context) error {
		return func(context.Context) error {
			if down.Load() {
				return errors.New("connection refused")
			}
			return nil
		}
	}
	var draining httpx.Draining
	rec := logtest.New()
	h := httpx.Ready(httpx.ReadyOptions{
		Draining: &draining,
		Checks: []httpx.Check{
			{Name: "database", Ping: check(&dbDown)},
			{Name: "cache", Ping: check(&cacheDown), Optional: true},
		},
		Logger: rec.Logger(),
	})

	code, body := probe(t, h)
	if code != http.StatusOK || body.Status != "ready" || body.Checks["database"] != "ok" || body.Checks["cache"] != "ok" {
		t.Fatalf("healthy: %d %+v", code, body)
	}

	// 可降级的依赖失败：仍然就绪，结果中报告。
	cacheDown.Store(true)
	if code, body = probe(t, h); code != http.StatusOK || body.Status != "ready" || body.Checks["cache"] != "failed" {
		t.Fatalf("cache down: %d %+v", code, body)
	}
	// 必需的依赖失败：不就绪，响应中没有失败原因。
	dbDown.Store(true)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if w.Code != http.StatusServiceUnavailable || !json.Valid(w.Body.Bytes()) || strings.Contains(w.Body.String(), "refused") {
		t.Fatalf("db down: %d %s", w.Code, w.Body)
	}
	// 反复探测不重复记录日志。
	probe(t, h)
	probe(t, h)
	dbDown.Store(false)
	cacheDown.Store(false)
	probe(t, h)

	var events []string
	for _, r := range rec.Records() {
		events = append(events, r.Level.String()+" "+r.Message+" "+r.Attr("check").(string))
	}
	want := []string{
		"WARN readiness check failed cache",
		"WARN readiness check failed database",
		"INFO readiness check recovered database",
		"INFO readiness check recovered cache",
	}
	if len(events) != len(want) {
		t.Fatalf("log %v", events)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("log %v, want %v", events, want)
		}
	}
	if r := rec.Records()[1]; r.Attr("component") != "http" || r.Attr("error.message") != "connection refused" {
		t.Fatalf("failure record %+v", r)
	}

	// 排空时不再执行检查，直接不就绪。
	draining.Set()
	if code, body = probe(t, h); code != http.StatusServiceUnavailable || body.Status != "draining" || body.Checks != nil {
		t.Fatalf("draining: %d %+v", code, body)
	}
}

func TestReadyTimeoutAndPanic(t *testing.T) {
	t.Parallel()
	block := make(chan struct{})
	defer close(block)
	h := httpx.Ready(httpx.ReadyOptions{
		Checks: []httpx.Check{
			// 不理会 ctx 的检查，例如 gomemcache 的 Ping。
			{Name: "stuck", Ping: func(context.Context) error { <-block; return nil }},
			{Name: "slow", Ping: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }},
			{Name: "broken", Ping: func(context.Context) error { panic("nil client") }, Optional: true},
			{Name: "fine", Ping: func(context.Context) error { return nil }},
		},
		Timeout: 50 * time.Millisecond,
		Logger:  slog.New(slog.DiscardHandler),
	})
	start := time.Now()
	code, body := probe(t, h)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("took %s", elapsed)
	}
	if code != http.StatusServiceUnavailable || body.Checks["stuck"] != "failed" || body.Checks["slow"] != "failed" ||
		body.Checks["broken"] != "failed" || body.Checks["fine"] != "ok" {
		t.Fatalf("%d %+v", code, body)
	}
}

func TestReadyWithoutChecks(t *testing.T) {
	t.Parallel()
	h := httpx.Ready(httpx.ReadyOptions{})
	if code, body := probe(t, h); code != http.StatusOK || body.Status != "ready" {
		t.Fatalf("%d %+v", code, body)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodHead, "/readyz", nil))
	if w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Fatalf("HEAD %d %q", w.Code, w.Body)
	}
}

func TestReadyPanicsOnBadChecks(t *testing.T) {
	t.Parallel()
	ok := func(context.Context) error { return nil }
	for name, checks := range map[string][]httpx.Check{
		"no name":   {{Ping: ok}},
		"no ping":   {{Name: "database"}},
		"duplicate": {{Name: "database", Ping: ok}, {Name: "database", Ping: ok}},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: no panic", name)
				}
			}()
			httpx.Ready(httpx.ReadyOptions{Checks: checks})
		}()
	}
}
