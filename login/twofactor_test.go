package login_test

import (
	"context"
	"encoding/base32"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/login"
)

// RFC 6238 附录 B 中 SHA1 的测试向量，取后 6 位。
func TestTOTPVectors(t *testing.T) {
	t.Parallel()
	key := []byte("12345678901234567890")
	for unix, want := range map[int64]string{59: "287082", 1111111109: "081804", 1234567890: "005924", 2000000000: "279037"} {
		if got := login.TOTP(key, unix/30); got != want {
			t.Errorf("T=%d: got %s, want %s", unix, got, want)
		}
	}
}

// authenticator 模拟用户手机上的验证器。
type authenticator struct {
	key   []byte
	clock *clock
}

func (a authenticator) code() string { return login.TOTP(a.key, a.clock.now().Unix()/30) }

// enable 为 c1 启用 TOTP，返回验证器和恢复码。
func (e *env) enable(t *testing.T, ctx context.Context, id string) (authenticator, []string) {
	t.Helper()
	setup, err := e.realm.SetupTOTP(ctx, e.db, ref(id), id+"@example.com")
	if err != nil {
		t.Fatal(err)
	}
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(setup.Secret)
	if err != nil {
		t.Fatal(err)
	}
	a := authenticator{key: key, clock: e.clock}
	codes, err := e.realm.ConfirmTOTP(ctx, e.db, ref(id), a.code())
	if err != nil {
		t.Fatal(err)
	}
	// 同一个时间步的验证码已经用过，让时钟走到下一步。
	e.clock.add(30 * time.Second)
	return a, codes
}

// pending 用密码完成第一步，返回待完成的登录。
func (e *env) pending(t *testing.T, ctx context.Context, email, password string) string {
	t.Helper()
	out, err := e.realm.Password(ctx, login.Email(email), password)
	if err != nil || out.Pending == "" || !out.Subject.Ref().IsZero() {
		t.Fatalf("password: %+v %v", out, err)
	}
	return out.Pending
}

func TestTOTPSetup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newRealm(t, login.Options{Issuer: "Blog"})
	e.account(t, ctx, "c1", "c1@example.com", "correct horse")

	_, err := e.realm.ConfirmTOTP(ctx, e.db, ref("c1"), "123456")
	is(t, err, login.ErrTwoFactorDisabled)
	setup, err := e.realm.SetupTOTP(ctx, e.db, ref("c1"), "c1@example.com")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(setup.URI)
	if err != nil || u.Scheme != "otpauth" || u.Host != "totp" || u.Path != "/Blog:c1@example.com" ||
		u.Query().Get("secret") != setup.Secret || u.Query().Get("issuer") != "Blog" || len(setup.Secret) != 32 {
		t.Fatalf("setup=%+v", setup)
	}
	// 尚未确认：登录不要求二次验证。
	if out, err := e.realm.Password(ctx, login.Email("c1@example.com"), "correct horse"); err != nil || out.Pending != "" {
		t.Fatalf("login before confirm: %+v %v", out, err)
	}
	_, err = e.realm.ConfirmTOTP(ctx, e.db, ref("c1"), "000000")
	is(t, err, login.ErrInvalidCode)

	// 重新设置会替换尚未确认的密钥。
	again, _ := e.realm.SetupTOTP(ctx, e.db, ref("c1"), "c1@example.com")
	if again.Secret == setup.Secret {
		t.Fatal("secret not replaced")
	}
	a, codes := e.enable(t, ctx, "c1")
	if len(codes) != 8 || len(codes[0]) != 11 || codes[0][5] != '-' {
		t.Fatalf("recovery codes=%v", codes)
	}
	if on, _ := e.realm.TwoFactorEnabled(ctx, ref("c1")); !on {
		t.Fatal("two-factor not enabled")
	}
	_, err = e.realm.SetupTOTP(ctx, e.db, ref("c1"), "c1@example.com")
	is(t, err, login.ErrTwoFactorEnabled)
	_, err = e.realm.ConfirmTOTP(ctx, e.db, ref("c1"), a.code())
	is(t, err, login.ErrTwoFactorEnabled)
}

func TestTwoFactorLogin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newRealm(t, login.Options{
		Resolve: func(_ context.Context, sub identity.Subject) (identity.Subject, error) {
			sub.Permissions = []string{"orders:read"}
			return sub, nil
		},
	})
	e.account(t, ctx, "c1", "c1@example.com", "correct horse")
	a, codes := e.enable(t, ctx, "c1")

	// 密码通过后需要二次验证。
	pending := e.pending(t, ctx, "c1@example.com", "correct horse")
	_, err := e.realm.TwoFactor(ctx, pending, "000000")
	is(t, err, login.ErrInvalidCode)
	out, err := e.realm.TwoFactor(ctx, pending, a.code())
	if err != nil || out.Subject.Ref() != ref("c1") || len(out.Subject.Permissions) != 1 {
		t.Fatalf("two-factor: %+v %v", out, err)
	}
	_, err = e.realm.TwoFactor(ctx, pending, a.code())
	is(t, err, login.ErrInvalidProof)

	// 同一个验证码不能用两次。
	_, err = e.realm.TwoFactor(ctx, e.pending(t, ctx, "c1@example.com", "correct horse"), a.code())
	is(t, err, login.ErrInvalidCode)

	// 恢复码：不区分大小写，忽略空白和连字符，用过即作废。
	typed := " " + strings.ToUpper(strings.ReplaceAll(codes[0], "-", " ")) + " "
	if out, err := e.realm.TwoFactor(ctx, e.pending(t, ctx, "c1@example.com", "correct horse"), typed); err != nil || out.Subject.ID != "c1" {
		t.Fatalf("recovery code: %+v %v", out, err)
	}
	_, err = e.realm.TwoFactor(ctx, e.pending(t, ctx, "c1@example.com", "correct horse"), codes[0])
	is(t, err, login.ErrInvalidCode)
	if n, _ := e.realm.RecoveryCodesLeft(ctx, ref("c1")); n != 7 {
		t.Fatalf("recovery codes left: %d", n)
	}

	// 验证码登录同样需要二次验证。
	requestID, c := e.code(t, ctx, login.SignIn, login.Email("c1@example.com"))
	out, err = e.realm.VerifyCode(ctx, requestID, c.Code)
	if err != nil || out.Pending == "" {
		t.Fatalf("code login: %+v %v", out, err)
	}

	// 一次待完成的登录最多输错 5 次。
	pending = e.pending(t, ctx, "c1@example.com", "correct horse")
	for range 5 {
		_, err := e.realm.TwoFactor(ctx, pending, "000000")
		is(t, err, login.ErrInvalidCode)
	}
	e.clock.add(30 * time.Second)
	_, err = e.realm.TwoFactor(ctx, pending, a.code())
	is(t, err, login.ErrInvalidProof)

	// 待完成的登录会过期。
	pending = e.pending(t, ctx, "c1@example.com", "correct horse")
	e.clock.add(16 * time.Minute)
	_, err = e.realm.TwoFactor(ctx, pending, a.code())
	is(t, err, login.ErrInvalidProof)

	// 重新生成后旧的恢复码作废。
	fresh, err := e.realm.RegenerateRecoveryCodes(ctx, e.db, ref("c1"))
	if err != nil || len(fresh) != 8 {
		t.Fatalf("regenerate: %v %v", fresh, err)
	}
	is(t, e.realm.CheckTwoFactor(ctx, ref("c1"), codes[1]), login.ErrInvalidCode)
	if err := e.realm.CheckTwoFactor(ctx, ref("c1"), fresh[1]); err != nil {
		t.Fatal(err)
	}

	// 关闭后直接登录。
	if err := e.realm.DisableTwoFactor(ctx, e.db, ref("c1")); err != nil {
		t.Fatal(err)
	}
	if out, err := e.realm.Password(ctx, login.Email("c1@example.com"), "correct horse"); err != nil || out.Subject.ID != "c1" {
		t.Fatalf("after disable: %+v %v", out, err)
	}
	is(t, e.realm.CheckTwoFactor(ctx, ref("c1"), fresh[2]), login.ErrTwoFactorDisabled)
	_, err = e.realm.RegenerateRecoveryCodes(ctx, e.db, ref("c1"))
	is(t, err, login.ErrTwoFactorDisabled)
}

// 找回密码不会绕过二次验证：它不登录，用新密码登录时仍要二次验证。
func TestResetKeepsTwoFactor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newRealm(t, login.Options{})
	e.account(t, ctx, "c1", "c1@example.com", "correct horse")
	e.enable(t, ctx, "c1")
	if _, err := e.realm.ResetPassword(ctx, e.proof(t, ctx, login.Reset, login.Email("c1@example.com")), "new password"); err != nil {
		t.Fatal(err)
	}
	e.pending(t, ctx, "c1@example.com", "new password")

	if err := e.realm.Forget(ctx, e.db, ref("c1")); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"auth_two_factor", "auth_recovery_codes"} {
		if n, _ := e.db.NewSelect().Table(table).Count(ctx); n != 0 {
			t.Fatalf("%s has %d rows after Forget", table, n)
		}
	}
}

// 并发提交同一个 TOTP 验证码，只有一次通过。
func TestTOTPReplay(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newRealm(t, login.Options{})
	e.account(t, ctx, "c1", "c1@example.com", "")
	a, _ := e.enable(t, ctx, "c1")
	code := a.code()
	var (
		wg sync.WaitGroup
		mu sync.Mutex
		ok int
	)
	for range 8 {
		wg.Go(func() {
			err := e.realm.CheckTwoFactor(ctx, ref("c1"), code)
			if err != nil && !errors.Is(err, login.ErrInvalidCode) {
				t.Error(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				ok++
			}
		})
	}
	wg.Wait()
	if ok != 1 {
		t.Fatalf("%d checks passed", ok)
	}
	// 允许前后各一个时间步的时钟误差，但不能早于已用过的那一步。
	e.clock.add(-30 * time.Second)
	is(t, e.realm.CheckTwoFactor(ctx, ref("c1"), a.code()), login.ErrInvalidCode)
	e.clock.add(60 * time.Second)
	if err := e.realm.CheckTwoFactor(ctx, ref("c1"), login.TOTP(a.key, e.clock.now().Unix()/30+1)); err != nil {
		t.Fatalf("next step within skew: %v", err)
	}
}
