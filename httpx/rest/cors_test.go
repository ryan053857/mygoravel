package rest_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"

	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/httpx/rest"
	"github.com/ryan053857/mygoravel/identity"
)

// 预检请求在路由之前应答：只注册了 GET 和 POST 的路径不会返回 405，认证也不会拦住它。
func TestCORSPreflightBeforeRoutes(t *testing.T) {
	cfg := httpx.DefaultCORSConfig()
	cfg.AllowedOrigins = []string{"https://app.example.com"}
	r := chi.NewRouter()
	r.Use(httpx.CORS(cfg))
	api := rest.New(rest.Options{Accounts: identity.Accounts{"user": nil}, Router: r})
	op := huma.Operation{OperationID: "create", Method: http.MethodPost, Path: "/articles"}
	rest.RequireBearer(api, func(context.Context, string) (identity.Subject, error) { return identity.Subject{}, nil })(&op)
	rest.Register(api, op, func(context.Context, *struct{}) (*struct{}, error) { return nil, nil })

	req := httptest.NewRequest(http.MethodOptions, "/articles", nil)
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", "authorization, content-type")
	rr := httptest.NewRecorder()
	api.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent || rr.Header().Get("Access-Control-Allow-Origin") != "https://app.example.com" {
		t.Fatalf("preflight %d %v", rr.Code, rr.Header())
	}

	// 实际请求没有 token：401 响应同样带 CORS 头，前端能读到错误。
	req = httptest.NewRequest(http.MethodPost, "/articles", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rr = httptest.NewRecorder()
	api.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized || rr.Header().Get("Access-Control-Allow-Origin") != "https://app.example.com" {
		t.Fatalf("unauthenticated %d %v", rr.Code, rr.Header())
	}
}
