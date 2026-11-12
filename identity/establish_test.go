package identity_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/identity"
)

var support = identity.Ref{Type: "staff", ID: "support"}

var (
	errDown      = errorx.Define(errorx.Unavailable, "test.down")
	errDisabled  = errorx.Define(errorx.Unauthenticated, "test.disabled")
	errNotMember = errorx.Define(errorx.NotFound, "test.not_member")
)

func TestEstablish(t *testing.T) {
	cred := identity.Subject{ID: "alice", Method: "bearer", Credential: "tok1", Scopes: []string{"articles:*"}, Impersonator: support}
	as := func(sub identity.Subject, err error) identity.Authenticator {
		return func(context.Context) (identity.Subject, error) { return sub, err }
	}
	grant := func(perms ...string) identity.Resolver {
		return func(ctx context.Context, sub identity.Subject) (identity.Subject, error) {
			sub.Tenant, sub.Permissions = identity.TenantFromContext(ctx), perms
			return sub, nil
		}
	}
	fail := func(err error) identity.Resolver {
		return func(context.Context, identity.Subject) (identity.Subject, error) { return identity.Subject{}, err }
	}
	plain := errors.New("connection refused")
	for name, tc := range map[string]struct {
		tenant  string
		auth    identity.Authenticator
		resolve identity.Resolver
		perms   []string
		want    error // nil 表示通过
		failed  bool
		public  map[string]any
	}{
		"credential only":            {auth: as(cred, nil)},
		"no credential":              {auth: as(identity.Subject{}, nil), want: identity.ErrUnauthenticated},
		"invalid credential":         {auth: as(identity.Subject{}, errDisabled.New()), want: identity.ErrUnauthenticated},
		"invalid with other kind":    {auth: as(identity.Subject{}, errNotMember.New()), want: identity.ErrUnauthenticated},
		"backend down":               {auth: as(identity.Subject{}, errDown.New()), want: errDown, failed: true},
		"backend plain error":        {auth: as(identity.Subject{}, plain), want: plain, failed: true},
		"bound to another tenant":    {tenant: "globex", auth: as(identity.Subject{ID: "alice", Tenant: "acme"}, nil), resolve: grant("*"), want: identity.ErrForbidden},
		"bound to this tenant":       {tenant: "acme", auth: as(identity.Subject{ID: "alice", Tenant: "acme"}, nil)},
		"resolver down":              {auth: as(cred, nil), resolve: fail(errDown.New()), want: errDown, failed: true},
		"resolver plain error":       {auth: as(cred, nil), resolve: fail(plain), want: plain, failed: true},
		"resolver disables":          {auth: as(cred, nil), resolve: fail(errDisabled.New()), want: identity.ErrUnauthenticated},
		"resolver returns nobody":    {auth: as(cred, nil), resolve: fail(nil), want: identity.ErrUnauthenticated},
		"resolver rejects":           {auth: as(cred, nil), resolve: fail(errNotMember.New()), want: errNotMember},
		"permission granted":         {auth: as(cred, nil), resolve: grant("articles:write"), perms: []string{"articles:write"}},
		"permission missing":         {auth: as(cred, nil), resolve: grant("articles:read"), perms: []string{"articles:read", "articles:write"}, want: identity.ErrForbidden, public: map[string]any{"permission": "articles:write"}},
		"permission outside scopes":  {auth: as(cred, nil), resolve: grant("*"), perms: []string{"users:read"}, want: identity.ErrForbidden, public: map[string]any{"permission": "users:read"}},
		"permission without resolve": {auth: as(cred, nil), perms: []string{"articles:read"}, want: identity.ErrForbidden},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := identity.WithTenant(context.Background(), tc.tenant)
			sub, err := identity.Establish(ctx, tc.auth, tc.resolve, tc.perms...)
			if identity.Failed(err) != tc.failed {
				t.Fatalf("Failed(%v) = %v", err, !tc.failed)
			}
			if tc.want == nil {
				if err != nil || sub.ID != "alice" {
					t.Fatalf("sub %+v err %v", sub, err)
				}
				return
			}
			if !errors.Is(err, tc.want) || sub.ID != "" {
				t.Fatalf("sub %+v err %v, want %v", sub, err, tc.want)
			}
			if tc.public != nil && !reflect.DeepEqual(errorx.PublicOf(err), tc.public) {
				t.Fatalf("public %v", errorx.PublicOf(err))
			}
		})
	}
}

// Resolver 加载权限和租户；主体本身、认证方式、凭据、凭据的限制和代为登录者总是取自凭据。
func TestEstablishKeepsCredentialFields(t *testing.T) {
	cred := identity.Subject{ID: "alice", Type: "user", Method: "bearer", Credential: "tok1", Scopes: []string{"articles:read"}, Impersonator: support}
	rebuilt := func(context.Context, identity.Subject) (identity.Subject, error) {
		return identity.Subject{Type: "staff", ID: "mallory", Tenant: "acme", Permissions: []string{"*"}, Method: "forged", Credential: "other", Impersonator: identity.Ref{Type: "staff", ID: "nobody"}}, nil
	}
	auth := func(context.Context) (identity.Subject, error) { return cred, nil }
	sub, err := identity.Establish(context.Background(), auth, rebuilt, "articles:read")
	if err != nil {
		t.Fatal(err)
	}
	want := identity.Subject{Type: "user", ID: "alice", Tenant: "acme", Permissions: []string{"*"}, Method: "bearer", Credential: "tok1", Scopes: []string{"articles:read"}, Impersonator: support}
	if !reflect.DeepEqual(sub, want) {
		t.Fatalf("%+v", sub)
	}
	// 凭据的限制仍然生效：Resolver 丢掉 Scopes 不会放宽权限。
	if _, err := identity.Establish(context.Background(), auth, rebuilt, "articles:write"); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("scopes ignored: %v", err)
	}
}

func TestReestablish(t *testing.T) {
	was := identity.Subject{Type: "user", ID: "alice", Impersonator: support}
	now := was
	var authErr, resolveErr error
	perms := []string{"events"}
	auth := func(ctx context.Context) (identity.Subject, error) {
		if !identity.Rechecking(ctx) {
			t.Error("authenticator: not rechecking")
		}
		return now, authErr
	}
	resolve := func(ctx context.Context, sub identity.Subject) (identity.Subject, error) {
		if !identity.Rechecking(ctx) {
			t.Error("resolver: not rechecking")
		}
		sub.Permissions = perms
		return sub, resolveErr
	}
	check := func() error { return identity.Reestablish(context.Background(), was, auth, resolve, "events") }
	if identity.Rechecking(context.Background()) {
		t.Fatal("plain context is rechecking")
	}

	if err := check(); err != nil {
		t.Fatalf("unchanged: %v", err)
	}
	// 暂时无法判断时不是失效。
	authErr = errDown.New()
	if err := check(); !identity.Failed(err) {
		t.Fatalf("backend down: %v", err)
	}
	authErr, resolveErr = nil, errDown.New()
	if err := check(); !identity.Failed(err) {
		t.Fatalf("resolver down: %v", err)
	}
	resolveErr = errDisabled.New()
	if err := check(); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatalf("disabled: %v", err)
	}
	resolveErr, perms = nil, nil
	if err := check(); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("permission removed: %v", err)
	}
	perms = []string{"events"}
	// 凭据换了主体，或不再是代为登录。
	now = identity.Subject{Type: "user", ID: "bob", Impersonator: support}
	if err := check(); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatalf("another subject: %v", err)
	}
	now = identity.Subject{Type: "staff", ID: "alice", Impersonator: support}
	if err := check(); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatalf("same ID in another account system: %v", err)
	}
	now = identity.Subject{Type: "user", ID: "alice"}
	if err := check(); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatalf("another impersonator: %v", err)
	}
	now = identity.Subject{}
	if err := check(); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatalf("revoked: %v", err)
	}
}
