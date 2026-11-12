package httpx_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/i18n"
	"github.com/ryan053857/mygoravel/ratelimit"
)

func TestAssignRequestIDReusesOnlyValidInbound(t *testing.T) {
	t.Parallel()
	var seen string
	handler := httpx.AssignRequestID(httpx.DefaultRequestIDConfig())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = httpx.RequestID(r.Context())
		if middleware.GetReqID(r.Context()) != seen {
			t.Fatal("chi request id differs")
		}
	}))
	for inbound, reuse := range map[string]bool{
		"gw-1234.abc:9_x":           true,
		"Root=1-6789abcd-0123;o=1/": true,
		"":                          false,
		"has space":                 false,
		"inject\nlog":               false,
		"中文":                        false,
		strings.Repeat("a", 129):    false,
	} {
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("X-Request-ID", inbound)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if seen == "" || rr.Header().Get("X-Request-ID") != seen || (seen == inbound) != reuse {
			t.Fatalf("inbound %q: got %q", inbound, seen)
		}
	}
}

func TestAssignRequestIDReadsConfiguredHeader(t *testing.T) {
	t.Parallel()
	var seen string
	handler := httpx.AssignRequestID(httpx.RequestIDConfig{Header: "X-Amzn-Trace-Id"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = httpx.RequestID(r.Context())
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Amzn-Trace-Id", "Root=1-abc")
	req.Header.Set("X-Request-ID", "ignored")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if seen != "Root=1-abc" || rr.Header().Get("X-Request-ID") != seen {
		t.Fatal(seen, rr.Header())
	}
}

func TestInvalidParametersPanicInsteadOfDisabling(t *testing.T) {
	t.Parallel()
	for name, build := range map[string]func(){
		"RequestID": func() { httpx.AssignRequestID(httpx.RequestIDConfig{Header: "bad header"}) },
		"AccessLog": func() { httpx.AccessLog(httpx.AccessLogConfig{IgnorePaths: []string{"healthz"}}, nil) },
		"ClientIP":  func() { httpx.ClientIP(httpx.ClientIPConfig{TrustedProxies: []netip.Prefix{{}}}) },
		"BodyLimit": func() { httpx.BodyLimit(httpx.BodyLimitConfig{}) },
		"Timeout":   func() { httpx.Timeout(httpx.TimeoutConfig{}) },
		"RateLimit": func() { httpx.RateLimit(nil, httpx.KeyByIP) },
		"RateKey":   func() { httpx.RateLimit(ratelimit.NewMemory(httpx.DefaultConfig().RateLimit), nil) },
		"Defaults":  func() { httpx.Defaults(httpx.Config{}, slog.New(slog.DiscardHandler), nil) },
	} {
		func() {
			defer func() {
				r := recover()
				if r == nil || !strings.Contains(fmt.Sprint(r), "httpx: ") {
					t.Fatalf("%s: expected panic, got %v", name, r)
				}
			}()
			build()
		}()
	}
}

func TestDefaultsLogPanicAsServerError(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	router := chi.NewRouter()
	router.Use(httpx.Defaults(httpx.DefaultConfig(), slog.New(slog.NewJSONHandler(&logs, nil)), nil)...)
	router.Get("/", func(http.ResponseWriter, *http.Request) { panic("boom") })
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	if rr.Code != 500 || rr.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal(rr.Code, rr.Header())
	}
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 2 {
		t.Fatal(logs.String())
	}
	var access struct {
		Level     string
		Msg       string
		RequestID string `json:"request_id"`
		Response  struct{ Status int }
	}
	if err := json.Unmarshal([]byte(lines[1]), &access); err != nil {
		t.Fatal(err)
	}
	if access.Msg != "http request" || access.Level != "ERROR" || access.Response.Status != 500 || access.RequestID != rr.Header().Get("X-Request-ID") {
		t.Fatal(lines[1])
	}
}

func TestBodyLimitRejectsStreamedOverflow(t *testing.T) {
	t.Parallel()
	handler := httpx.BodyLimit(httpx.BodyLimitConfig{MaxBytes: 3})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err == nil {
			t.Fatal("overflow accepted")
		}
	}))
	req := httptest.NewRequest("POST", "/", strings.NewReader("four"))
	req.ContentLength = -1
	handler.ServeHTTP(httptest.NewRecorder(), req)
}

type marker struct{}

func (*marker) ServeHTTP(http.ResponseWriter, *http.Request) {}

func TestNilLoggerFallsBackToSlogDefault(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	router := chi.NewRouter()
	router.Use(httpx.Defaults(httpx.DefaultConfig(), nil, nil)...)
	router.Get("/", func(http.ResponseWriter, *http.Request) { panic("boom") })
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	id := rr.Header().Get("X-Request-ID")
	if !strings.Contains(logs.String(), `"msg":"request panic"`) || !strings.Contains(logs.String(), `"msg":"http request"`) || strings.Count(logs.String(), id) != 2 {
		t.Fatalf("logs=%q", logs.String())
	}
}

func TestDefaultsNegotiateBuiltinLocale(t *testing.T) {
	t.Parallel()
	var got string
	router := chi.NewRouter()
	router.Use(httpx.Defaults(httpx.DefaultConfig(), slog.New(slog.DiscardHandler), nil)...)
	router.Get("/", func(w http.ResponseWriter, r *http.Request) {
		got = i18n.T(r.Context(), "auth.forbidden", nil)
	})
	for header, want := range map[string]string{"en-US": "Permission denied", "fr": "没有访问权限"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Accept-Language", header)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if got != want || rr.Header().Get("Content-Language") == "" {
			t.Fatalf("%s: got %q, Content-Language=%q", header, got, rr.Header().Get("Content-Language"))
		}
	}
}

// Defaults 不设置请求超时：Timeout 按路由组安装，流式路由不能有超时。
func TestDefaultsSetNoDeadline(t *testing.T) {
	t.Parallel()
	handler := chi.Chain(httpx.Defaults(httpx.DefaultConfig(), slog.New(slog.DiscardHandler), nil)...).
		HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := r.Context().Deadline(); ok {
				t.Error("Defaults set a request deadline")
			}
		})
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
}
