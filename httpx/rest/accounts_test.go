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

type accountOut struct {
	Body struct {
		Subject     string   `json:"subject"`
		Permissions []string `json:"permissions"`
	}
}

// Options.Accounts 决定 API 接受哪些账号体系：每种账号由自己的 Resolver 加载，其他账号体系的凭据与无效凭据相同。
// token 就是主体的 Ref，例如 member:alice；同一个校验器交给只接受客户的 API 也不会让成员通过。
func TestAccounts(t *testing.T) {
	validate := func(_ context.Context, token string) (identity.Subject, error) {
		ref, err := identity.ParseRef(token)
		if err != nil {
			return identity.Subject{}, nil
		}
		return identity.Subject{Type: ref.Type, ID: ref.ID}, nil
	}
	grant := func(perms ...string) identity.Resolver {
		return func(_ context.Context, sub identity.Subject) (identity.Subject, error) {
			sub.Permissions = perms
			return sub, nil
		}
	}
	console := rest.New(rest.Options{Accounts: identity.Accounts{"member": grant("articles:*"), "staff": nil}})
	shop := rest.New(rest.Options{Accounts: identity.Accounts{"customer": grant("orders:read")}})
	for _, api := range []*rest.API{console, shop} {
		op := huma.Operation{OperationID: "who", Method: http.MethodGet, Path: "/who"}
		rest.RequireBearer(api, validate)(&op)
		rest.Register(api, op, func(ctx context.Context, _ *struct{}) (*accountOut, error) {
			sub, _ := identity.FromContext(ctx)
			out := &accountOut{}
			out.Body.Subject, out.Body.Permissions = sub.Ref().String(), sub.Permissions
			return out, nil
		})
	}
	call := func(api *rest.API, token string) (int, string) {
		req := httptest.NewRequest(http.MethodGet, "/who", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		api.Handler().ServeHTTP(rr, req)
		return rr.Code, rr.Body.String()
	}
	for _, tc := range []struct {
		api    *rest.API
		token  string
		status int
		body   string
	}{
		{console, "member:alice", http.StatusOK, `{"subject":"member:alice","permissions":["articles:*"]}`},
		{console, "staff:alice", http.StatusOK, `{"subject":"staff:alice","permissions":null}`}, // Resolver 为 nil
		{console, "customer:alice", http.StatusUnauthorized, ""},
		{shop, "customer:alice", http.StatusOK, `{"subject":"customer:alice","permissions":["orders:read"]}`},
		{shop, "member:alice", http.StatusUnauthorized, ""},
	} {
		status, body := call(tc.api, tc.token)
		if status != tc.status || tc.body != "" && body != tc.body+"\n" {
			t.Errorf("%s: %d %s", tc.token, status, body)
		}
	}
}

func TestAccountsRequired(t *testing.T) {
	validate := func(context.Context, string) (identity.Subject, error) { return identity.Subject{}, nil }
	panics := func(name string, f func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s: expected panic", name)
			}
		}()
		f()
	}
	panics("authenticated operation without Accounts", func() {
		rest.RequireBearer(rest.New(rest.Options{}), validate)
	})
	panics("invalid account type", func() {
		rest.New(rest.Options{Accounts: identity.Accounts{"Member": nil}})
	})
	// 文档模式不运行处理器，不需要 Accounts。
	op := huma.Operation{OperationID: "who", Method: http.MethodGet, Path: "/who"}
	rest.RequireBearer(rest.New(rest.Options{Mode: rest.ModeOpenAPI}), validate)(&op)
}
