package identity_test

import (
	"testing"

	"github.com/go-tenon/tenon/identity"
)

func TestRef(t *testing.T) {
	sub := identity.Subject{Type: "customer", ID: "42", Tenant: "acme"}
	ref := sub.Ref()
	if ref != (identity.Ref{Type: "customer", ID: "42"}) || ref.IsZero() {
		t.Fatalf("ref = %+v", ref)
	}
	if got := ref.String(); got != "customer:42" {
		t.Fatalf("String = %q", got)
	}
	if !(identity.Ref{}).IsZero() || (identity.Ref{}).String() != "" {
		t.Fatal("zero ref")
	}
	// ID 中的冒号原样保留：类型不含冒号，第一个冒号就是分隔符。
	for _, r := range []identity.Ref{ref, {Type: "member", ID: "t42:1001"}, {Type: identity.System, ID: "queue"}} {
		got, err := identity.ParseRef(r.String())
		if err != nil || got != r {
			t.Fatalf("ParseRef(%q) = %+v, %v", r.String(), got, err)
		}
	}
}

func TestRefValidate(t *testing.T) {
	long := string(make([]byte, 192))
	for _, r := range []identity.Ref{
		{},
		{Type: "user"},
		{ID: "42"},
		{Type: "User", ID: "42"},
		{Type: "1user", ID: "42"},
		{Type: "_user", ID: "42"},
		{Type: "user-x", ID: "42"},
		{Type: "user:x", ID: "42"},
		{Type: identity.SubjectType(long[:65]), ID: "42"},
		{Type: "user", ID: long},
	} {
		if r.Validate() == nil {
			t.Errorf("%+v: expected error", r)
		}
	}
	for _, r := range []identity.Ref{{Type: "user", ID: "42"}, {Type: "tenant_member2", ID: long[:191]}} {
		if err := r.Validate(); err != nil {
			t.Errorf("%+v: %v", r, err)
		}
	}
	for _, s := range []string{"", "user", ":42", "user:", "User:42"} {
		if _, err := identity.ParseRef(s); err == nil {
			t.Errorf("ParseRef(%q): expected error", s)
		}
	}
}
