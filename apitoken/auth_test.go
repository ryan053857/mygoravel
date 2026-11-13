package apitoken_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ryan053857/mygoravel/apitoken"
	"github.com/ryan053857/mygoravel/httpx/rest"
	"github.com/ryan053857/mygoravel/identity"
)

// signedIn 返回用 methods 刚登录的 alice。
func signedIn(methods ...string) identity.Subject {
	sub := alice
	sub.Auth = identity.Auth{Methods: methods, Time: time.Now().UTC()}
	return sub
}

// 登录 token 保存登录时的验证情况；确认身份补充验证方式、更新时间。
func TestConfirm(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		s := newStore(t)
		plain := login(t, s, signedIn("password"), apitoken.Device{})
		sub, ok := valid(t, s, plain)
		if !ok || !slices.Equal(sub.Auth.Methods, []string{"password"}) || !sub.Auth.Time.Equal(time.Now()) || sub.Auth.TwoFactor() {
			t.Fatalf("auth %+v", sub.Auth)
		}

		time.Sleep(20 * time.Minute)
		if err := s.Confirm(ctx, sub, "totp", "password"); err != nil {
			t.Fatal(err)
		}
		sub, _ = valid(t, s, plain)
		if !slices.Equal(sub.Auth.Methods, []string{"password", "totp"}) || !sub.Auth.Time.Equal(time.Now()) || !sub.Auth.TwoFactor() {
			t.Fatalf("confirmed auth %+v", sub.Auth)
		}

		// 不经过本人登录的凭据没有验证情况，也不能确认身份。
		personal := create(t, s, signedIn("password"), apitoken.Personal{Name: "脚本", Scopes: read})
		psub, _ := valid(t, s, personal)
		if !psub.Auth.Time.IsZero() || psub.Auth.Methods != nil {
			t.Fatalf("personal token auth %+v", psub.Auth)
		}
		if err := s.Confirm(ctx, psub, "password"); !errors.Is(err, apitoken.ErrNotFound) {
			t.Fatalf("confirm personal token: %v", err)
		}
		impersonated := alice
		impersonated.Impersonator = identity.Ref{Type: "staff", ID: "s1"}
		isub, _ := valid(t, s, login(t, s, impersonated, apitoken.Device{}))
		if err := s.Confirm(ctx, isub, "password"); !errors.Is(err, identity.ErrForbidden) {
			t.Fatalf("confirm impersonated: %v", err)
		}
		// 别人的登录、没有方式、方式含空白都不行。
		if err := s.Confirm(ctx, identity.Subject{Type: "user", ID: "bob", Credential: sub.Credential}, "password"); !errors.Is(err, apitoken.ErrNotFound) {
			t.Fatalf("confirm another subject's login: %v", err)
		}
		if err := s.Confirm(ctx, sub); err == nil {
			t.Fatal("confirm without methods")
		}
		if err := s.Confirm(ctx, sub, "pass word"); err == nil {
			t.Fatal("confirm with an invalid method")
		}
	})
}

// JWT 模式的 access token 带有 amr、auth_time；Confirm 返回带有新验证情况的 access token，之后的刷新同样带有它。
func TestJWTConfirm(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		j, s := newJWT(t, apitoken.JWTConfig{})
		pair := jwtLogin(t, j, signedIn("email"), apitoken.Device{})
		sub, ok := access(t, j, pair.Access)
		if !ok || !slices.Equal(sub.Auth.Methods, []string{"email"}) || !sub.Auth.Time.Equal(time.Now()) {
			t.Fatalf("auth %+v", sub.Auth)
		}

		time.Sleep(5 * time.Minute)
		confirmed, err := j.Confirm(ctx, sub, "totp")
		if err != nil || confirmed.Refresh != "" {
			t.Fatalf("confirm %+v %v", confirmed, err)
		}
		csub, _ := access(t, j, confirmed.Access)
		if !slices.Equal(csub.Auth.Methods, []string{"email", "totp"}) || !csub.Auth.Time.Equal(time.Now()) {
			t.Fatalf("confirmed auth %+v", csub.Auth)
		}
		refreshed, err := j.Refresh(ctx, pair.Refresh)
		if err != nil {
			t.Fatal(err)
		}
		if rsub, _ := access(t, j, refreshed.Access); !rsub.Auth.TwoFactor() || !rsub.Auth.Time.Equal(csub.Auth.Time) {
			t.Fatalf("refreshed auth %+v", rsub.Auth)
		}

		// 不透明的登录 token 不是 JWT 模式的登录。
		opaque, _ := valid(t, s, login(t, s, signedIn("password"), apitoken.Device{}))
		if _, err := j.Confirm(ctx, opaque, "password"); !errors.Is(err, apitoken.ErrNotFound) {
			t.Fatalf("JWT.Confirm with an opaque login: %v", err)
		}
	})
}

// rest.Confirmed 按凭据的验证时间放行；确认身份后重新放行。
func TestRequireConfirmed(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		s := newStore(t)
		api := rest.New(rest.Options{Accounts: identity.Accounts{"user": nil}})
		op := huma.Operation{OperationID: "account-delete", Method: http.MethodDelete, Path: "/account"}
		rest.Confirmed(15 * time.Minute)(&op)
		rest.RequireBearer(api, s.Validate)(&op)
		rest.Register(api, op, func(context.Context, *struct{}) (*struct{}, error) { return nil, nil })
		call := func(token string) *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodDelete, "/account", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			rr := httptest.NewRecorder()
			api.Handler().ServeHTTP(rr, req)
			return rr
		}

		plain := login(t, s, signedIn("password"), apitoken.Device{})
		if rr := call(plain); rr.Code != http.StatusNoContent {
			t.Fatalf("fresh login: %d %s", rr.Code, rr.Body)
		}
		time.Sleep(16 * time.Minute)
		if rr := call(plain); rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), `"code":"auth.confirm_required"`) {
			t.Fatalf("stale login: %d %s", rr.Code, rr.Body)
		}
		sub, _ := valid(t, s, plain)
		if err := s.Confirm(context.Background(), sub, "password"); err != nil {
			t.Fatal(err)
		}
		if rr := call(plain); rr.Code != http.StatusNoContent {
			t.Fatalf("after confirm: %d %s", rr.Code, rr.Body)
		}
		if rr := call(create(t, s, alice, apitoken.Personal{Name: "脚本", Scopes: []string{"*"}})); rr.Code != http.StatusForbidden {
			t.Fatalf("personal token: %d %s", rr.Code, rr.Body)
		}
	})
}
