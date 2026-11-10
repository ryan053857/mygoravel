package httpx_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ryan053857/mygoravel/httpx"
)

func TestUnexpectedListenerExitDrainsRequests(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	draining := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		_, _ = io.WriteString(w, "done")
	})
	done := make(chan error, 1)
	go func() {
		done <- httpx.Serve(context.Background(), shutdownIn(time.Second), ln, handler, httpx.ServeOptions{OnDraining: func() { close(draining) }})
	}()
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		resp, err := http.Get("http://" + ln.Addr().String())
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("no request")
	}
	ln.Close()
	<-draining
	select {
	case err := <-done:
		t.Fatalf("returned before request drained: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("lost listener failure")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hang")
	}
	<-requestDone
}
func TestShutdownBudgetCancelsActiveRequest(t *testing.T) {
	for _, budget := range []time.Duration{time.Nanosecond, 20 * time.Millisecond} {
		t.Run(budget.String(), func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entered := make(chan struct{})
			canceled := make(chan struct{})
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done(); close(canceled) })
			done := make(chan error, 1)
			go func() {
				done <- httpx.Serve(ctx, shutdownIn(budget), ln, handler, httpx.ServeOptions{})
			}()
			go func() {
				resp, err := http.Get("http://" + ln.Addr().String())
				if err == nil {
					resp.Body.Close()
				}
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("request missing")
			}
			cancel()
			select {
			case err := <-done:
				if budget > 0 && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("unbounded shutdown")
			}
			select {
			case <-canceled:
			case <-time.After(time.Second):
				t.Fatal("request context remained alive")
			}
		})
	}
}
func TestBoundaryRecoveryLimitsAndProxyTrust(t *testing.T) {
	for _, trusted := range []bool{false, true} {
		cfg := httpx.DefaultConfig()
		cfg.BodyLimit.MaxBytes = 3
		if trusted {
			cfg.ClientIP.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
		}
		handler := defaults(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if trusted && r.RemoteAddr != "192.0.2.1" {
				t.Fatal(r.RemoteAddr)
			}
			if !trusted && r.RemoteAddr != "10.0.0.1:5000" {
				t.Fatal(r.RemoteAddr)
			}
			panic("private-secret")
		}))
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "10.0.0.1:5000"
		req.Header.Set("X-Forwarded-For", "spoofed, 192.0.2.1, 10.0.0.2")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != 500 || strings.Contains(rr.Body.String(), "private-secret") || rr.Header().Get("X-Request-ID") == "" {
			t.Fatal(rr)
		}
		rr = httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest("POST", "/", strings.NewReader("four")))
		if rr.Code != 413 {
			t.Fatal(rr.Code)
		}
	}
}

func TestPanicAfterResponseStartedAbortsConnection(t *testing.T) {
	handler := defaults(httpx.DefaultConfig())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "partial")
		panic("failure")
	}))
	defer func() {
		if got := recover(); got != http.ErrAbortHandler {
			t.Fatalf("expected connection abort, got %v", got)
		}
	}()
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
}
func TestRequestTimeoutReachesHandler(t *testing.T) {
	cfg := httpx.DefaultConfig()
	cfg.Timeout.Duration = 10 * time.Millisecond
	handler := defaults(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		if !errors.Is(r.Context().Err(), context.DeadlineExceeded) {
			t.Fatal(r.Context().Err())
		}
		w.WriteHeader(408)
	}))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	if rr.Code != 408 {
		t.Fatal(rr.Code)
	}
}

func defaults(cfg httpx.Config) func(http.Handler) http.Handler {
	return chi.Chain(httpx.Defaults(cfg, slog.New(slog.DiscardHandler))...).Handler
}

func shutdownIn(d time.Duration) httpx.Config {
	cfg := httpx.DefaultConfig()
	cfg.ShutdownTimeout = d
	return cfg
}
