package identity_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/identity"
)

func TestSubjectContext(t *testing.T) {
	sub := identity.Subject{ID: "u1", Type: "user"}
	got, ok := identity.FromContext(identity.WithSubject(context.Background(), sub))
	if !ok || got.ID != "u1" {
		t.Fatalf("%+v ok=%v", got, ok)
	}
	if _, ok := identity.FromContext(context.Background()); ok {
		t.Fatal("subject in empty context")
	}
}

func TestTenantContext(t *testing.T) {
	if got := identity.TenantFromContext(context.Background()); got != "" {
		t.Fatalf("default tenant %q", got)
	}
	if got := identity.TenantFromContext(identity.WithTenant(context.Background(), "acme")); got != "acme" {
		t.Fatalf("tenant %q", got)
	}
}

func TestGrants(t *testing.T) {
	for _, tc := range []struct {
		granted []string
		perm    string
		want    bool
	}{
		{[]string{"articles:write"}, "articles:write", true},
		{[]string{"articles:write"}, "articles:read", false},
		{[]string{"articles:*"}, "articles:read", true},
		{[]string{"articles:*"}, "articles:comments:delete", true},
		{[]string{"articles:*"}, "articles", false},
		{[]string{"articles:*"}, "articlesx:read", false},
		{[]string{"articles:comments:*"}, "articles:read", false},
		{[]string{"*"}, "members:manage", true},
		{[]string{"articles*"}, "articles:read", false},
		{nil, "articles:read", false},
		{[]string{"*"}, "", false},
	} {
		if got := identity.Grants(tc.granted, tc.perm); got != tc.want {
			t.Errorf("Grants(%v, %q)=%v want %v", tc.granted, tc.perm, got, tc.want)
		}
	}
}

func TestCan(t *testing.T) {
	user := identity.Subject{ID: "u1", Permissions: []string{"articles:*", "members:read"}}
	for perm, want := range map[string]bool{"articles:write": true, "members:read": true, "members:manage": false} {
		if got := user.Can(perm); got != want {
			t.Errorf("unrestricted Can(%q)=%v", perm, got)
		}
	}
	// 凭据限制：需要权限和 scope 同时允许。
	token := user
	token.Scopes = []string{"articles:read", "members:manage"}
	for perm, want := range map[string]bool{"articles:read": true, "articles:write": false, "members:manage": false} {
		if got := token.Can(perm); got != want {
			t.Errorf("scoped Can(%q)=%v", perm, got)
		}
	}
	// 空切片什么都不允许。
	token.Scopes = []string{}
	if token.Can("articles:read") {
		t.Error("empty scopes allowed a permission")
	}
}

func TestAuth(t *testing.T) {
	t.Parallel()
	for methods, want := range map[string]bool{
		"":                  false,
		"password":          false,
		"email":             false,
		"google":            false,
		"password totp":     true,
		"password recovery": true,
		"passkey":           true,
	} {
		if got := (identity.Auth{Methods: strings.Fields(methods)}).TwoFactor(); got != want {
			t.Errorf("TwoFactor(%q) = %v", methods, got)
		}
	}
	if (identity.Auth{}).ConfirmedWithin(time.Hour) {
		t.Error("zero Auth confirmed")
	}
	recent := identity.Auth{Time: time.Now().Add(-10 * time.Minute)}
	if !recent.ConfirmedWithin(15*time.Minute) || recent.ConfirmedWithin(5*time.Minute) {
		t.Errorf("ConfirmedWithin %v", recent.Time)
	}
}
