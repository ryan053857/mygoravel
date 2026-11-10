package httpx_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	slogformatter "github.com/samber/slog-formatter"

	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/logging"
	"github.com/ryan053857/mygoravel/logging/logtest"
)

func serve(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(method, path, nil))
	return rr
}

func TestAccessLogRecordsRouteLevelAndAttrs(t *testing.T) {
	rec := logtest.New()
	cfg := httpx.DefaultConfig()
	cfg.AccessLog.IgnorePaths = []string{"/healthz"}
	router := chi.NewRouter()
	router.Use(httpx.Defaults(cfg, rec.Logger(), nil)...)
	router.Get("/articles/{id}", func(w http.ResponseWriter, r *http.Request) {
		httpx.AddAccessLogAttrs(r.Context(), slog.String("code", "article.missing"))
		w.WriteHeader(http.StatusNotFound)
	})
	router.Get("/healthz", func(http.ResponseWriter, *http.Request) {})

	rr := serve(router, "GET", "/articles/42")
	serve(router, "GET", "/healthz")

	records := rec.Records()
	if len(records) != 1 {
		t.Fatalf("ignored path was logged: %v", records)
	}
	got := records[0]
	if got.Message != "http request" || got.Level != slog.LevelWarn {
		t.Fatal(got)
	}
	for key, want := range map[string]any{
		"request.method":  "GET",
		"request.path":    "/articles/42",
		"request.route":   "/articles/{id}",
		"response.status": int64(404),
		"code":            "article.missing",
		"request_id":      rr.Header().Get("X-Request-ID"),
	} {
		if got.Attr(key) != want {
			t.Fatalf("%s=%v, want %v; attrs=%v", key, got.Attr(key), want, got.Attrs)
		}
	}
	if _, ok := got.Attrs["id"]; ok {
		t.Fatal("slog-chi id field must be disabled")
	}
}

func TestAccessLogOutsideChiAndImplicitOK(t *testing.T) {
	rec := logtest.New()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(http.ResponseWriter, *http.Request) {}) // 不写任何响应
	h := httpx.AccessLog(httpx.DefaultAccessLogConfig(), rec.Logger())(mux)

	if rr := serve(h, "GET", "/"); rr.Code != 200 {
		t.Fatal(rr.Code)
	}
	got, ok := rec.Find("http request")
	if !ok || got.Attr("response.status") != int64(200) || got.Level != slog.LevelInfo {
		t.Fatal(rec.Records())
	}
}

func TestAccessLogHeadersCanBeRedactedByKey(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(logging.Config{Writer: &buf}, logging.Format(
		slogformatter.FormatByKey("X-Api-Key", func(slog.Value) slog.Value { return slog.StringValue("hidden") }),
	))
	h := httpx.AccessLog(httpx.AccessLogConfig{RequestHeader: true}, logger)(http.NotFoundHandler())
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Api-Key", "secret-key")
	req.Header.Set("Authorization", "Bearer secret-token")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if strings.Contains(buf.String(), "secret") || !strings.Contains(buf.String(), `"X-Api-Key":"hidden"`) {
		t.Fatal(buf.String())
	}
}

func TestHTTPComponentLevel(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(logging.Config{Writer: &buf, Levels: map[string]slog.Level{"http": slog.LevelWarn}})
	router := chi.NewRouter()
	router.Use(httpx.Defaults(httpx.DefaultConfig(), logger, nil)...)
	router.Get("/ok", func(http.ResponseWriter, *http.Request) {})
	router.Get("/missing", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	serve(router, "GET", "/ok")
	serve(router, "GET", "/missing")
	if strings.Count(buf.String(), "\n") != 1 || !strings.Contains(buf.String(), `"component":"http"`) || !strings.Contains(buf.String(), `"status":404`) {
		t.Fatal(buf.String())
	}
}
