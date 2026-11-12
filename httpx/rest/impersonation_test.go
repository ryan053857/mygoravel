package rest_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ryan053857/mygoravel/httpx/rest"
	"github.com/ryan053857/mygoravel/identity"
)

type impersonationOut struct {
	Body struct {
		ID, Method, Credential, Impersonator string
		Scopes                               []string
	}
}

// Resolve 新建主体返回时，凭据决定的字段（认证方式、凭据 ID、限制、代为登录者）仍然取自凭据。
func TestResolveKeepsCredentialFields(t *testing.T) {
	api := rest.New(rest.Options{
		Resolve: func(_ context.Context, sub identity.Subject) (identity.Subject, error) {
			return identity.Subject{ID: sub.ID, Permissions: []string{"*"}, Impersonator: identity.Ref{Type: "staff", ID: "forged"}, Scopes: nil}, nil
		},
	})
	validate := func(_ context.Context, token string) (identity.Subject, error) {
		return identity.Subject{ID: "alice", Method: "bearer", Credential: "t1", Impersonator: identity.Ref{Type: "staff", ID: "s1"}, Scopes: []string{"articles:read"}}, nil
	}
	op := huma.Operation{OperationID: "me", Method: http.MethodGet, Path: "/me"}
	rest.RequireBearer(api, validate, "articles:read")(&op)
	rest.Register(api, op, func(ctx context.Context, _ *struct{}) (*impersonationOut, error) {
		sub, _ := identity.FromContext(ctx)
		out := &impersonationOut{}
		out.Body.ID, out.Body.Method, out.Body.Credential, out.Body.Impersonator, out.Body.Scopes = sub.ID, sub.Method, sub.Credential, sub.Impersonator.String(), sub.Scopes
		return out, nil
	})
	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req.Header.Set("Authorization", "Bearer x")
	rr := httptest.NewRecorder()
	api.Handler().ServeHTTP(rr, req)
	want := `{"ID":"alice","Method":"bearer","Credential":"t1","Impersonator":"staff:s1","Scopes":["articles:read"]}`
	if rr.Code != http.StatusOK || rr.Body.String() != want+"\n" {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
}
