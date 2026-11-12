package logintest_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ryan053857/mygoravel/crypt"
	"github.com/ryan053857/mygoravel/database/sqlite"
	"github.com/ryan053857/mygoravel/hashing"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/internal/dbtesting"
	"github.com/ryan053857/mygoravel/login"
	"github.com/ryan053857/mygoravel/login/logintest"
	"github.com/ryan053857/mygoravel/ratelimit"
)

func TestOutbox(t *testing.T) {
	ctx := context.Background()
	o := logintest.NewOutbox()
	email := login.Email("alice@example.com")
	for _, code := range []string{"111111", "222222"} {
		if err := o.Send(ctx, login.Code{To: email, Code: code}); err != nil {
			t.Fatal(err)
		}
	}
	if err := o.Send(ctx, login.Code{To: login.Email("bob@example.com"), Code: "333333"}); err != nil {
		t.Fatal(err)
	}
	// 按规范化后的标识查找。
	if c, ok := o.Last(login.Email(" Alice@Example.com ")); !ok || c.Code != "222222" {
		t.Fatalf("last=%+v ok=%v", c, ok)
	}
	if _, ok := o.Last(login.Phone("+8613800138000")); ok {
		t.Fatal("found a code that was never sent")
	}

	failed := errors.New("smtp down")
	o.FailWith(failed)
	if err := o.Send(ctx, login.Code{To: email}); !errors.Is(err, failed) || len(o.Sent()) != 3 {
		t.Fatalf("err=%v sent=%d", err, len(o.Sent()))
	}
	o.Reset()
	if err := o.Send(ctx, login.Code{To: email}); err != nil || len(o.Sent()) != 1 {
		t.Fatalf("after reset: err=%v sent=%d", err, len(o.Sent()))
	}
}

// 用 Outbox 和 TOTP 走一遍验证码登录和二次验证。
func TestWithRealm(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		outbox := logintest.NewOutbox()
		loose := ratelimit.Config{Rate: 100, Interval: time.Minute, Burst: 100}
		db := dbtesting.Open(t, sqlite.Driver, login.Migrations())
		var hc hashing.Config
		hc.SetDefaults()
		hc.Driver, hc.Bcrypt.Cost = "bcrypt", 4 // 测试中用最低成本，哈希密码不拖慢测试
		users := login.New(db, login.Options{
			Type:     "user",
			Hasher:   hashing.New(hc),
			Crypt:    crypt.New(crypt.Config{Key: crypt.GenerateKey()}),
			Send:     outbox.Send,
			Attempts: ratelimit.NewMemory(loose),
			Sends:    ratelimit.NewMemory(loose),
		})
		alice := identity.Ref{Type: "user", ID: "alice"}
		email := login.Email("alice@example.com")
		if err := users.Attach(ctx, db, alice, email, true); err != nil {
			t.Fatal(err)
		}

		requestID, err := users.SendCode(ctx, login.SignIn, email)
		if err != nil {
			t.Fatal(err)
		}
		c, _ := outbox.Last(email)
		if out, err := users.VerifyCode(ctx, requestID, c.Code); err != nil || out.Subject.Ref() != alice {
			t.Fatalf("sign in: %+v %v", out, err)
		}

		setup, err := users.SetupTOTP(ctx, db, alice, "alice@example.com")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := users.ConfirmTOTP(ctx, db, alice, logintest.TOTP(setup.Secret)); err != nil {
			t.Fatal(err)
		}
		// 同一个 30 秒内的验证码已经用过，时间前进后得到新的。
		time.Sleep(30 * time.Second)
		if err := users.CheckTwoFactor(ctx, alice, logintest.TOTP(setup.Secret)); err != nil {
			t.Fatal(err)
		}
	})
}
