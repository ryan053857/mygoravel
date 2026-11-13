package login_test

import (
	"context"
	"encoding/base32"
	"errors"
	"strings"
	"testing"

	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/login"
)

// 必须使用二次验证的账号还没有启用时，登录先引导启用，启用后才得到主体。
func TestEnroll(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var asked []identity.Subject
	e := newRealm(t, login.Options{Register: true, RequireTwoFactor: func(_ context.Context, sub identity.Subject) (bool, error) {
		asked = append(asked, sub)
		return sub.ID != "free", nil
	}})
	e.account(t, ctx, "c1", "c1@example.com", "correct horse")
	e.account(t, ctx, "free", "free@example.com", "correct horse")

	// 不要求的账号照常登录。
	if out, err := e.realm.Password(ctx, login.Email("free@example.com"), "correct horse"); err != nil || out.Subject.ID != "free" {
		t.Fatalf("not required: %+v %v", out, err)
	}

	out, err := e.realm.Password(ctx, login.Email("c1@example.com"), "correct horse")
	if err != nil || out.Enroll == "" || out.Subject.ID != "" || out.Pending != "" {
		t.Fatalf("password: %+v %v", out, err)
	}
	if last := asked[len(asked)-1]; last.Ref() != ref("c1") || last.Tenant != "" {
		t.Fatalf("RequireTwoFactor got %+v", asked[len(asked)-1])
	}
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
	e := newRealm(t, login.Options{Register: true, RequireTwoFactor: func(context.Context, identity.Subject) (bool, error) { return true, nil }})
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

	// RequireTwoFactor 的错误原样返回。
	down := errors.New("settings unavailable")
	broken := newRealm(t, login.Options{RequireTwoFactor: func(context.Context, identity.Subject) (bool, error) { return false, down }})
	broken.account(t, ctx, "c1", "c1@example.com", "correct horse")
	if _, err := broken.realm.Password(ctx, login.Email("c1@example.com"), "correct horse"); !errors.Is(err, down) {
		t.Fatalf("RequireTwoFactor error: %v", err)
	}
}

// 一次待完成的启用最多输错 5 次。
func TestEnrollAttempts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newRealm(t, login.Options{RequireTwoFactor: func(context.Context, identity.Subject) (bool, error) { return true, nil }})
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
