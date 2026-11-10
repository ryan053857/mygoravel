package rest_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/httpx/rest"
	"github.com/ryan053857/mygoravel/identity"
)

type whoOut struct {
	Body struct {
		ID      string `json:"id"`
		Tenant  string `json:"tenant"`
		Context string `json:"context"`
	}
}

// tenantAPI 装配一个按路径参数区分租户的 API：token 即用户 ID，Resolve 按租户加载权限。
// alice 是 acme 的编辑（articles:*），在 globex 中没有角色；down 让 Resolve 模拟故障，gone 模拟账号已停用。
// token "bound" 是绑定 globex 的凭据，"reader" 是只允许 articles:read 的个人令牌。
func tenantAPI(t *testing.T) *rest.API {
	t.Helper()
	members := map[string]map[string][]string{"acme": {"alice": {"articles:*"}, "reader": {"articles:*"}}, "globex": {"bound": {"articles:*"}}}
	api := rest.New(rest.Options{
		Tenant: func(ctx huma.Context) string { return ctx.Param("tenant") },
		Resolve: func(ctx context.Context, sub identity.Subject) (identity.Subject, error) {
			switch sub.ID {
			case "down":
				return identity.Subject{}, errorx.Define(errorx.Unavailable, "users.unavailable").New()
			case "gone":
				return identity.Subject{}, errorx.Define(errorx.Unauthenticated, "users.disabled").New()
			}
			tenant := identity.TenantFromContext(ctx)
			perms, ok := members[tenant][sub.ID]
			if !ok {
				return identity.Subject{}, errorx.Define(errorx.NotFound, "tenant.not_found").New()
			}
			sub.Tenant, sub.Permissions = tenant, perms
			return sub, nil
		},
	})
	validate := func(_ context.Context, token string) (identity.Subject, error) {
		sub := identity.Subject{ID: token}
		switch token {
		case "bound":
			sub.Tenant = "globex"
		case "reader":
			sub.Scopes = []string{"articles:read"}
		}
		return sub, nil
	}
	who := func() func(ctx context.Context, _ *struct{}) (*whoOut, error) {
		return func(ctx context.Context, _ *struct{}) (*whoOut, error) {
			sub, _ := identity.FromContext(ctx)
			out := &whoOut{}
			out.Body.ID, out.Body.Tenant, out.Body.Context = sub.ID, sub.Tenant, identity.TenantFromContext(ctx)
			return out, nil
		}
	}
	for _, r := range []struct {
		id, method, path, perm string
	}{
		{"read", http.MethodGet, "/t/{tenant}/articles", "articles:read"},
		{"write", http.MethodPost, "/t/{tenant}/articles", "articles:write"},
	} {
		op := huma.Operation{OperationID: r.id, Method: r.method, Path: r.path}
		rest.RequireBearer(api, validate, r.perm)(&op)
		rest.Register(api, op, who)
	}
	public := huma.Operation{OperationID: "public", Method: http.MethodGet, Path: "/t/{tenant}/about"}
	rest.Public()(&public)
	rest.Register(api, public, who)
	return api
}

func call(api *rest.API, method, path, token string) (*httptest.ResponseRecorder, whoOut) {
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	api.Handler().ServeHTTP(rr, req)
	var out whoOut
	_ = json.Unmarshal(rr.Body.Bytes(), &out.Body)
	return rr, out
}

func TestTenantAndResolve(t *testing.T) {
	api := tenantAPI(t)

	rr, out := call(api, http.MethodPost, "/t/acme/articles", "alice")
	if rr.Code != http.StatusOK || out.Body.ID != "alice" || out.Body.Tenant != "acme" || out.Body.Context != "acme" {
		t.Fatalf("member: %d %s", rr.Code, rr.Body)
	}
	// 公开操作同样拿到租户。
	if rr, out := call(api, http.MethodGet, "/t/acme/about", ""); rr.Code != http.StatusOK || out.Body.Context != "acme" || out.Body.ID != "" {
		t.Fatalf("public: %d %s", rr.Code, rr.Body)
	}

	for name, tc := range map[string]struct {
		method, path, token string
		status              int
		body                string
	}{
		"not a member":          {http.MethodGet, "/t/globex/articles", "alice", http.StatusNotFound, `"code":"tenant.not_found"`},
		"disabled":              {http.MethodGet, "/t/acme/articles", "gone", http.StatusUnauthorized, `"code":"auth.unauthenticated"`},
		"resolver down":         {http.MethodGet, "/t/acme/articles", "down", http.StatusServiceUnavailable, "authentication unavailable"},
		"credential for globex": {http.MethodGet, "/t/acme/articles", "bound", http.StatusForbidden, `"code":"auth.forbidden"`},
		"scope limits token":    {http.MethodPost, "/t/acme/articles", "reader", http.StatusForbidden, `"code":"auth.forbidden"`},
	} {
		rr, _ := call(api, tc.method, tc.path, tc.token)
		if rr.Code != tc.status || !strings.Contains(rr.Body.String(), tc.body) {
			t.Errorf("%s: %d %s", name, rr.Code, rr.Body)
		}
		if tc.status == http.StatusUnauthorized && rr.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Errorf("%s: missing challenge", name)
		}
	}
	// 绑定租户的凭据在自己的租户中可用；只读令牌可以读。
	if rr, _ := call(api, http.MethodGet, "/t/globex/articles", "bound"); rr.Code != http.StatusOK {
		t.Fatalf("bound in own tenant: %d %s", rr.Code, rr.Body)
	}
	if rr, _ := call(api, http.MethodGet, "/t/acme/articles", "reader"); rr.Code != http.StatusOK {
		t.Fatalf("reader read: %d %s", rr.Code, rr.Body)
	}
}

func TestRequiredPermissionsInRoutesAndOpenAPI(t *testing.T) {
	api := tenantAPI(t)
	for _, r := range httpx.Routes(api.Router()) {
		if r.OperationID == "write" && (len(r.Permissions) != 1 || r.Permissions[0] != "articles:write") {
			t.Fatalf("route=%+v", r)
		}
	}
	b, err := api.OpenAPIJSON()
	if err != nil || !strings.Contains(string(b), `"x-required-permissions":["articles:write"]`) {
		t.Fatalf("openapi missing x-required-permissions: %v", err)
	}
}
