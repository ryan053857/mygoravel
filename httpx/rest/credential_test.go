package rest_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/httpx/rest"
	"github.com/ryan053857/mygoravel/identity"
)

// 认证通过后，请求的 context 带有重新校验凭据的函数，长连接期间据此发现 token 被撤销或失去 scope。
func TestRequireBearerInstallsCredentialCheck(t *testing.T) {
	var (
		mu      sync.Mutex
		subject = identity.Subject{ID: "alice", Type: identity.SubjectUser, Permissions: []string{"events"}}
		valid   = true
		down    bool
	)
	validate := func(_ context.Context, token string) (identity.Subject, error) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case down:
			return identity.Subject{}, errorx.Define(errorx.Unavailable, "tokens.unavailable").New()
		case !valid || token != "t":
			return identity.Subject{}, errorx.Define(errorx.Unauthenticated, "tokens.revoked").New()
		}
		return subject, nil
	}
	api := rest.New(rest.Options{})
	captured := make(chan context.Context, 1)
	op := huma.Operation{OperationID: "events", Method: http.MethodGet, Path: "/events"}
	rest.RequireBearer(api, validate, "events")(&op)
	rest.Register(api, op, func() func(context.Context, *struct{}) (*struct{}, error) {
		return func(ctx context.Context, _ *struct{}) (*struct{}, error) {
			captured <- ctx
			return nil, nil
		}
	})
	req := httptest.NewRequest(http.MethodGet, "/events", nil)
	req.Header.Set("Authorization", "Bearer t")
	rr := httptest.NewRecorder()
	api.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status %d", rr.Code)
	}
	ctx := <-captured
	set := func(f func()) { mu.Lock(); f(); mu.Unlock() }

	if err := httpx.CheckCredential(ctx); err != nil {
		t.Fatalf("valid token: %v", err)
	}
	set(func() { down = true })
	if err := httpx.CheckCredential(ctx); err != nil {
		t.Fatalf("validator unavailable treated as revoked: %v", err)
	}
	set(func() { down = false; subject.Permissions = nil })
	if err := httpx.CheckCredential(ctx); httpx.StatusOf(err) != http.StatusForbidden {
		t.Fatalf("scope removed: %v", err)
	}
	set(func() { subject.Permissions = []string{"events"}; subject.ID = "bob" })
	if err := httpx.CheckCredential(ctx); httpx.StatusOf(err) != http.StatusUnauthorized {
		t.Fatalf("subject changed: %v", err)
	}
	set(func() { subject.ID = "alice"; valid = false })
	if err := httpx.CheckCredential(ctx); err == nil || errors.Is(err, context.Canceled) || httpx.StatusOf(err) != http.StatusUnauthorized {
		t.Fatalf("revoked token: %v", err)
	}
}
