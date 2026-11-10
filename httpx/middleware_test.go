package httpx_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/ryan053857/mygoravel/httpx"
)

func TestAssignRequestIDReusesOnlyValidInbound(t *testing.T) {
	var seen string
	handler := httpx.AssignRequestID()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = httpx.RequestID(r.Context())
	}))
	for inbound, reuse := range map[string]bool{
		"gw-1234.abc:9_x":        true,
		"":                       false,
		"has space":              false,
		"inject\nlog":            false,
		strings.Repeat("a", 129): false,
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

func TestZeroValuedMiddlewaresPassThrough(t *testing.T) {
	next := http.Handler(&marker{})
	for name, mw := range map[string]func(http.Handler) http.Handler{
		"ClientIP":  httpx.ClientIP(nil),
		"AccessLog": httpx.AccessLog(nil),
		"BodyLimit": httpx.BodyLimit(0),
		"Timeout":   httpx.Timeout(0),
		"Locale":    httpx.Locale(nil),
	} {
		if got := mw(next); got != next {
			t.Fatalf("%s wrapped the handler", name)
		}
	}
}

func TestDefaultsLogPanicAsServerError(t *testing.T) {
	var logs bytes.Buffer
	router := chi.NewRouter()
	router.Use(httpx.Defaults(httpx.Config{}, httpx.DefaultsOptions{Logger: slog.New(slog.NewJSONHandler(&logs, nil))})...)
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
	var access map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &access); err != nil {
		t.Fatal(err)
	}
	if access["msg"] != "http request" || access["status"] != float64(500) || access["request_id"] != rr.Header().Get("X-Request-ID") {
		t.Fatal(access)
	}
}

func TestBodyLimitRejectsStreamedOverflow(t *testing.T) {
	handler := httpx.BodyLimit(3)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
