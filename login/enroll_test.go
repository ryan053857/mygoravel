package login_test

import (
	"context"
	"encoding/base32"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-tenon/tenon/identity"
	"github.com/go-tenon/tenon/login"
)

// 必须使用二次验证的账号还没有启用时，登录先引导启用，启用后才得到主体。策略按租户不同。
func TestEnroll(t *testing.T) {
	t.Parallel()
	ctx := identity.WithTenant(context.Background(), "acme")
	free := identity.WithTenant(context.Background(), "free")
	e := newRealm(t, login.Options{PerTenant: true, PolicyFor: func(ctx context.Context) (login.Policy, error) {
		return login.Policy{RequireTwoFactor: identity.TenantFromContext(ctx) == "acme"}, nil
	}})
	e.account(t, ctx, "c1", "c1@example.com", "correct horse")
	e.account(t, free, "c2", "c2@example.com", "correct horse")

	// 不要求的租户照常登录。
	if out, err := e.realm.Password(free, login.Email("c2@example.com"), "correct horse"); err != nil || out.Subject.ID != "c2" {
		t.Fatalf("not required: %+v %v", out, err)
	}

	out, err := e.realm.Password(ctx, login.Email("c1@example.com"), "correct horse")
	if err != nil || out.Enroll == "" || out.Subject.ID != "" || out.Pending != "" {
		t.Fatalf("password: %+v %v", out, err)
	}
	// 待完成的启用只能在发起登录的租户中继续。
	_, err = e.realm.EnrollTOTP(free, out.Enroll, "c1")
	is(t, err, login.ErrInvalidProof)
	_, _, err = e.realm.CompleteEnroll(ctx, out.Enroll, "000000")
	is(t, err, login.ErrTwoFactorDisabled) // 还没有生成密钥
	setup, err := e.realm.EnrollTOTP(ctx, out.Enroll, "c1@example.com")
	if err != nil || !strings.HasPrefix(setup.URI, "otpauth://totp/") {
		t.Fatalf("enroll totp: %+v %v", setup, err)
	}
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(setup.Secret)
	if err != nil {
		t.Fatal(err)
	}
	a := authenticator{key: key, clock: e.clock}
	_, _, err = e.realm.CompleteEnroll(ctx, out.Enroll, "000000")
	is(t, err, login.ErrInvalidCode)
	done, codes, err := e.realm.CompleteEnroll(ctx, out.Enroll, a.code())
	if err != nil || done.Subject.ID != "c1" || strings.Join(done.Subject.Auth.Methods, " ") != "password totp" || len(codes) != 8 {
		t.Fatalf("complete: %+v %v %v", done, codes, err)
	}
	_, _, err = e.realm.CompleteEnroll(ctx, out.Enroll, a.code())
	is(t, err, login.ErrInvalidProof)
	_, err = e.realm.EnrollTOTP(ctx, out.Enroll, "c1@example.com")
	is(t, err, login.ErrInvalidProof)

	// 启用后与平常一样要求第二步。
	if out, err := e.realm.Password(ctx, login.Email("c1@example.com"), "correct horse"); err != nil || out.Pending == "" {
		t.Fatalf("after enroll: %+v %v", out, err)
	}
}

// 要求二次验证时，第三方登录同样要求第二步或先启用；passkey 登录本身就算二次验证；注册也要先启用。
func TestRequireTwoFactorExternal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newRealm(t, login.Options{Policy: login.Policy{Register: true, RequireTwoFactor: true}})
	github := login.Identifier{Kind: "github", Value: "42"}
	if err := e.realm.Attach(ctx, e.db, ref("c1"), github, true); err != nil {
		t.Fatal(err)
	}
	if out, err := e.realm.External(ctx, login.External{Provider: "github", Subject: "42"}); err != nil || out.Enroll == "" {
		t.Fatalf("external without two-factor: %+v %v", out, err)
	}
	e.enable(t, ctx, "c1")
	if out, err := e.realm.External(ctx, login.External{Provider: "github", Subject: "42"}); err != nil || out.Pending == "" {
		t.Fatalf("external with two-factor: %+v %v", out, err)
	}
	if err := e.realm.Attach(ctx, e.db, ref("c1"), login.Identifier{Kind: "passkey", Value: "cred1"}, true); err != nil {
		t.Fatal(err)
	}
	if out, err := e.realm.External(ctx, login.External{Provider: "passkey", Subject: "cred1"}); err != nil || out.Subject.ID != "c1" {
		t.Fatalf("passkey: %+v %v", out, err)
	}
	// 提供方做过多因素认证（应用按 amr 设置 TwoFactor）时算作二次验证，直接成功。
	mfa, err := e.realm.External(ctx, login.External{Provider: "github", Subject: "42", TwoFactor: true})
	if err != nil || mfa.Subject.ID != "c1" || strings.Join(mfa.Subject.Auth.Methods, " ") != "github idp_mfa" || !mfa.Subject.Auth.TwoFactor() {
		t.Fatalf("idp mfa: %+v %v", mfa, err)
	}

	reg, err := e.realm.Register(ctx, e.db, e.proof(t, ctx, login.SignIn, login.Email("new@example.com")), ref("c2"))
	if err != nil || reg.Enroll == "" || reg.Subject.ID != "" {
		t.Fatalf("register: %+v %v", reg, err)
	}

	// 不要求时，第三方登录照旧不要求第二步。
	free := newRealm(t, login.Options{})
	if err := free.realm.Attach(ctx, free.db, ref("c1"), github, true); err != nil {
		t.Fatal(err)
	}
	free.enable(t, ctx, "c1")
	if out, err := free.realm.External(ctx, login.External{Provider: "github", Subject: "42"}); err != nil || out.Subject.ID != "c1" {
		t.Fatalf("external not required: %+v %v", out, err)
	}

	// PolicyFor 的错误原样返回。
	down := errors.New("settings unavailable")
	var failing atomic.Bool
	broken := newRealm(t, login.Options{PolicyFor: func(context.Context) (login.Policy, error) {
		if failing.Load() {
			return login.Policy{}, down
		}
		return login.Policy{}, nil
	}})
	broken.account(t, ctx, "c1", "c1@example.com", "correct horse")
	failing.Store(true)
	if _, err := broken.realm.Password(ctx, login.Email("c1@example.com"), "correct horse"); !errors.Is(err, down) {
		t.Fatalf("PolicyFor error: %v", err)
	}
}

// 一次待完成的启用最多输错 5 次。
func TestEnrollAttempts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newRealm(t, login.Options{Policy: login.Policy{RequireTwoFactor: true}})
	e.account(t, ctx, "c1", "c1@example.com", "correct horse")
	out, err := e.realm.Password(ctx, login.Email("c1@example.com"), "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.realm.EnrollTOTP(ctx, out.Enroll, "c1"); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		_, _, err = e.realm.CompleteEnroll(ctx, out.Enroll, "000000")
		is(t, err, login.ErrInvalidCode)
	}
	_, _, err = e.realm.CompleteEnroll(ctx, out.Enroll, "000000")
	is(t, err, login.ErrInvalidProof)
}

// Policy.Methods 限制登录方式：例如租户只允许用自己的企业 IdP 登录。
func TestPolicyMethods(t *testing.T) {
	t.Parallel()
	ctx := identity.WithTenant(context.Background(), "acme")
	open := identity.WithTenant(context.Background(), "open")
	e := newRealm(t, login.Options{PerTenant: true, PolicyFor: func(ctx context.Context) (login.Policy, error) {
		if identity.TenantFromContext(ctx) == "acme" {
			return login.Policy{Register: true, Methods: []string{"acme_sso"}}, nil
		}
		return login.Policy{Register: true}, nil
	}})
	e.account(t, open, "c1", "c1@example.com", "correct horse")
	e.account(t, ctx, "c2", "c2@example.com", "")
	if err := e.realm.Attach(ctx, e.db, ref("c2"), login.Identifier{Kind: "acme_sso", Value: "u2"}, true); err != nil {
		t.Fatal(err)
	}

	_, err := e.realm.Password(ctx, login.Email("c2@example.com"), "whatever")
	is(t, err, login.ErrMethodNotAllowed)
	_, err = e.realm.SendCode(ctx, login.SignIn, login.Email("c2@example.com"))
	is(t, err, login.ErrMethodNotAllowed)
	_, err = e.realm.External(ctx, login.External{Provider: "github", Subject: "9"})
	is(t, err, login.ErrMethodNotAllowed)
	if out, err := e.realm.External(ctx, login.External{Provider: "acme_sso", Subject: "u2"}); err != nil || out.Subject.ID != "c2" {
		t.Fatalf("allowed provider: %+v %v", out, err)
	}
	// 找回密码不是登录，不受限制；其他租户不受影响。
	if _, err := e.realm.SendCode(ctx, login.Reset, login.Email("c2@example.com")); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if out, err := e.realm.Password(open, login.Email("c1@example.com"), "correct horse"); err != nil || out.Subject.ID != "c1" {
		t.Fatalf("other tenant: %+v %v", out, err)
	}
}

// TrustEmail：租户验证过域名的企业 IdP 断言的邮箱，直接关联到这个邮箱已有的账号。
func TestTrustEmail(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newRealm(t, login.Options{Policy: login.Policy{Register: true}})
	e.account(t, ctx, "c1", "alice@acme.com", "correct horse")
	sso := login.External{Provider: "acme_sso", Subject: "u1", Email: "Alice@acme.com", EmailVerified: true}

	// 没有 TrustEmail 时照旧不按邮箱关联。
	if out, err := e.realm.External(ctx, sso); err != nil || out.Proof == "" {
		t.Fatalf("untrusted: %+v %v", out, err)
	}
	// 没有确认过的邮箱同样不关联。
	unverified := sso
	unverified.TrustEmail, unverified.EmailVerified = true, false
	if out, err := e.realm.External(ctx, unverified); err != nil || out.Proof == "" {
		t.Fatalf("unverified: %+v %v", out, err)
	}

	sso.TrustEmail = true
	out, err := e.realm.External(ctx, sso)
	if err != nil || out.Subject.ID != "c1" || strings.Join(out.Subject.Auth.Methods, " ") != "acme_sso" {
		t.Fatalf("trusted: %+v %v", out, err)
	}
	ids, _ := e.realm.Identifiers(ctx, ref("c1"))
	if len(ids) != 2 {
		t.Fatalf("identifiers %+v", ids)
	}
	// 邮箱没有注册时仍然返回注册凭证。
	newcomer := login.External{Provider: "acme_sso", Subject: "u9", Email: "bob@acme.com", EmailVerified: true, TrustEmail: true}
	if out, err := e.realm.External(ctx, newcomer); err != nil || out.Proof == "" {
		t.Fatalf("newcomer: %+v %v", out, err)
	}
}
