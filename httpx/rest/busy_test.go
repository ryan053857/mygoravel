package rest_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"
	"github.com/uptrace/bun"

	"github.com/go-tenon/tenon/database"
	"github.com/go-tenon/tenon/database/dbtest"
	"github.com/go-tenon/tenon/httpx"
	"github.com/go-tenon/tenon/httpx/rest"
)

// 数据库连接用满时，请求在 http.acquire_timeout 之后以 503 和 Retry-After 快速失败。
func TestBusyDatabase(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, database.Config{DSN: dbtest.URL(t), MaxConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	cfg := httpx.DefaultConfig()
	cfg.AcquireTimeout = 200 * time.Millisecond
	api := rest.New(rest.Options{Title: "busy"})
	rest.Register(api, huma.Operation{Security: []map[string][]string{}, OperationID: "ping", Method: http.MethodGet, Path: "/ping"},
		func(ctx context.Context, _ *pingIn) (*pingOut, error) {
			if _, err := db.NewRaw("SELECT 1").Exec(ctx); err != nil {
				return nil, err
			}
			out := &pingOut{}
			out.Body.Message = "pong"
			return out, nil
		})
	router := chi.NewRouter()
	router.Use(httpx.Defaults(cfg, slog.New(slog.DiscardHandler), nil)...)
	router.Mount("/", api.Handler())

	get := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ping", nil))
		return rec
	}
	if rec := get(); rec.Code != http.StatusOK {
		t.Fatalf("idle pool: %d %s", rec.Code, rec.Body)
	}

	tx, err := db.BeginTx(ctx, nil) // 占住唯一的连接
	if err != nil {
		t.Fatal(err)
	}
	defer func(tx bun.Tx) { _ = tx.Rollback() }(tx)
	start := time.Now()
	rec := get()
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "1" {
		t.Fatalf("busy pool: %d %q %s", rec.Code, rec.Header().Get("Retry-After"), rec.Body)
	}
	if d := time.Since(start); d < 200*time.Millisecond || d > 2*time.Second {
		t.Fatalf("failed after %v", d)
	}
}
