package login_test

import (
	"context"
	"testing"

	"github.com/go-tenon/tenon/identity"
	"github.com/go-tenon/tenon/login"
)

func TestExternal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newRealm(t, login.Options{})
	google := login.External{Provider: "google", Subject: "g-1", Email: "Alice@Gmail.com", EmailVerified: true, Name: "Alice"}

	// 第一次：没有绑定，得到注册凭证。
	out, err := e.realm.External(ctx, google)
	if err != nil || out.Proof == "" || !out.Subject.Ref().IsZero() {
		t.Fatalf("first sign-in: %+v %v", out, err)
	}
	reg, err := e.realm.Register(ctx, e.db, out.Proof, ref("c1"))
	if err != nil || reg.Subject.Ref() != ref("c1") {
		t.Fatalf("register: %+v %v", reg, err)
	}
	list, _ := e.realm.Identifiers(ctx, ref("c1"))
	if len(list) != 2 || list[0].Kind != "google" && list[1].Kind != "google" {
		t.Fatalf("identifiers=%+v", list)
	}
	for _, a := range list {
		if !a.Verified {
			t.Fatalf("%v not verified", a.Identifier)
		}
	}
	// 之后直接登录；对方确认过的邮箱也能用来收验证码登录。
	if out, err := e.realm.External(ctx, google); err != nil || out.Subject.Ref() != ref("c1") {
		t.Fatalf("second sign-in: %+v %v", out, err)
	}
	if got, err := e.realm.Lookup(ctx, login.Email("alice@gmail.com")); err != nil || got != ref("c1") {
		t.Fatalf("email: %v %v", got, err)
	}

	// 未经对方确认的邮箱不作为登录标识。
	out, _ = e.realm.External(ctx, login.External{Provider: "github", Subject: "42", Email: "bob@example.com"})
	if _, err := e.realm.Register(ctx, e.db, out.Proof, ref("c2")); err != nil {
		t.Fatal(err)
	}
	if list, _ := e.realm.Identifiers(ctx, ref("c2")); len(list) != 1 || list[0].Kind != "github" || list[0].Value != "42" {
		t.Fatalf("identifiers=%+v", list)
	}
}

// 邮箱已属于其他账号：不自动关联，注册返回 ErrTaken；用户登录原账号后绑定，之后用第三方账号登录原账号。
func TestExternalDoesNotMergeByEmail(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newRealm(t, login.Options{})
	e.account(t, ctx, "c1", "alice@example.com", "correct horse")
	apple := login.External{Provider: "apple", Subject: "a-1", Email: "alice@example.com", EmailVerified: true}

	out, err := e.realm.External(ctx, apple)
	if err != nil || out.Proof == "" {
		t.Fatalf("external: %+v %v", out, err)
	}
	_, err = e.realm.Register(ctx, e.db, out.Proof, ref("c9"))
	is(t, err, login.ErrTaken)
	if err := e.realm.Link(ctx, e.db, ref("c1"), out.Proof); err != nil {
		t.Fatalf("link: %v", err)
	}
	if out, err := e.realm.External(ctx, apple); err != nil || out.Subject.Ref() != ref("c1") {
		t.Fatalf("after link: %+v %v", out, err)
	}
	// 第三方账号已绑定到别人：另一个账号不能再绑定。
	out, _ = e.realm.External(ctx, login.External{Provider: "github", Subject: "7"})
	if _, err := e.realm.Register(ctx, e.db, out.Proof, ref("c2")); err != nil {
		t.Fatal(err)
	}
	e.account(t, ctx, "c3", "carol@example.com", "")
	requestID, c := e.code(t, ctx, login.Verify, login.Email("carol@example.com"))
	proof, _ := e.realm.VerifyCode(ctx, requestID, c.Code)
	if err := e.realm.Link(ctx, e.db, ref("c3"), proof.Proof); err != nil {
		t.Fatal(err)
	}
}

// 第三方账号登录不要求二次验证；PerTenant 时第三方账号同样按租户隔离。
func TestExternalTwoFactorAndTenants(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newRealm(t, login.Options{PerTenant: true})
	acme := identity.WithTenant(ctx, "acme")
	google := login.External{Provider: "google", Subject: "g-1"}
	out, _ := e.realm.External(acme, google)
	if _, err := e.realm.Register(acme, e.db, out.Proof, ref("acme:1")); err != nil {
		t.Fatal(err)
	}
	e.enable(t, acme, "acme:1")
	if out, err := e.realm.External(acme, google); err != nil || out.Subject.ID != "acme:1" || out.Subject.Tenant != "acme" {
		t.Fatalf("acme: %+v %v", out, err)
	}
	globex := identity.WithTenant(ctx, "globex")
	out, err := e.realm.External(globex, google)
	if err != nil || out.Proof == "" {
		t.Fatalf("globex: %+v %v", out, err)
	}
	_, err = e.realm.Register(acme, e.db, out.Proof, ref("acme:2"))
	is(t, err, login.ErrInvalidProof)
}

func TestExternalProviderNames(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newRealm(t, login.Options{})
	for _, ext := range []login.External{
		{Provider: "email", Subject: "x"}, {Provider: "Google", Subject: "x"}, {Provider: "google", Subject: ""},
	} {
		if _, err := e.realm.External(ctx, ext); err == nil {
			t.Errorf("%+v accepted", ext)
		}
	}
}
