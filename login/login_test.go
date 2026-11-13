package login_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/crypt"
	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/database/dbtest"
	"github.com/ryan053857/mygoravel/hashing"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/login"
	"github.com/ryan053857/mygoravel/ratelimit"
)

const customer identity.SubjectType = "customer"

var appKey = crypt.GenerateKey()

// hasher 用 bcrypt 的最低成本，让测试保持快速。
func hasher(cost int) *hashing.Hasher {
	var cfg hashing.Config
	cfg.SetDefaults()
	cfg.Driver, cfg.Bcrypt.Cost = "bcrypt", cost
	return hashing.New(cfg)
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

// mailbox 记录 Options.Send 发出的验证码。
type mailbox struct {
	mu   sync.Mutex
	sent []login.Code
	fail error
}

func (m *mailbox) send(_ context.Context, c login.Code) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return m.fail
	}
	m.sent = append(m.sent, c)
	return nil
}

// last 返回最近发给 to 的验证码。
func (m *mailbox) last(t *testing.T, to login.Identifier) login.Code {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.sent) - 1; i >= 0; i-- {
		if m.sent[i].To == to {
			return m.sent[i]
		}
	}
	t.Fatalf("no code sent to %s", to)
	return login.Code{}
}

func (m *mailbox) count() int { m.mu.Lock(); defer m.mu.Unlock(); return len(m.sent) }

type env struct {
	db    *bun.DB
	realm *login.Realm
	mail  *mailbox
	clock *clock
}

// newRealm 创建测试用的 Realm：限流器在内存中，宽松到不影响其他测试，限流本身在 TestAttemptsLimit 中检查。
// realmLimiter 返回一个宽松的内存限流器，供测试中另外创建的 Realm 使用。
func (e *env) realmLimiter() ratelimit.Limiter {
	return ratelimit.NewMemory(ratelimit.Config{Rate: 1000, Interval: time.Minute, Burst: 1000})
}

func newRealm(t *testing.T, opts login.Options) *env {
	t.Helper()
	e := &env{db: dbtest.Open(t, login.Migrations()), mail: &mailbox{}, clock: &clock{t: time.Now().UTC().Truncate(time.Second)}}
	opts.Type = customer
	if opts.Hasher == nil {
		opts.Hasher = hasher(4)
	}
	opts.Crypt = crypt.New(crypt.Config{Key: appKey})
	if opts.Send == nil {
		opts.Send = e.mail.send
	}
	loose := ratelimit.Config{Rate: 1000, Interval: time.Minute, Burst: 1000}
	if opts.Attempts == nil {
		opts.Attempts = ratelimit.NewMemory(loose)
	}
	if opts.Sends == nil {
		opts.Sends = ratelimit.NewMemory(loose)
	}
	e.realm = login.New(e.db, opts)
	login.SetClock(e.realm, e.clock.now)
	return e
}

func ref(id string) identity.Ref { return identity.Ref{Type: customer, ID: id} }

// account 创建一个有邮箱和密码的客户。
func (e *env) account(t *testing.T, ctx context.Context, id, email, password string) {
	t.Helper()
	if err := e.realm.Attach(ctx, e.db, ref(id), login.Email(email), true); err != nil {
		t.Fatal(err)
	}
	if password != "" {
		if err := e.realm.SetPassword(ctx, e.db, ref(id), password); err != nil {
			t.Fatal(err)
		}
	}
}

// code 发送并返回验证码。
func (e *env) code(t *testing.T, ctx context.Context, purpose login.Purpose, id login.Identifier) (string, login.Code) {
	t.Helper()
	requestID, err := e.realm.SendCode(ctx, purpose, id)
	if err != nil {
		t.Fatal(err)
	}
	c := e.mail.last(t, id)
	if c.RequestID != requestID || c.Purpose != purpose {
		t.Fatalf("sent %+v for request %s", c, requestID)
	}
	return requestID, c
}

// proof 发送、验证并返回完成凭证。
func (e *env) proof(t *testing.T, ctx context.Context, purpose login.Purpose, id login.Identifier) string {
	t.Helper()
	requestID, c := e.code(t, ctx, purpose, id)
	out, err := e.realm.VerifyCode(ctx, requestID, c.Code)
	if err != nil || out.Proof == "" {
		t.Fatalf("verify: %+v %v", out, err)
	}
	return out.Proof
}

func is(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("got %v, want %v", err, want)
	}
}

func TestIdentifierNormalize(t *testing.T) {
	t.Parallel()
	for in, want := range map[login.Identifier]login.Identifier{
		login.Email("  Alice@Example.COM "): login.Email("alice@example.com"),
		login.Phone("+8613800138000"):       login.Phone("+8613800138000"),
		login.Username(" Alice_01 "):        login.Username("alice_01"),
		{Kind: "wechat", Value: " oX_AbC "}: {Kind: "wechat", Value: "oX_AbC"},
	} {
		got, err := in.Normalize()
		if err != nil || got != want {
			t.Errorf("%v: got %v, %v", in, got, err)
		}
	}
	for _, bad := range []login.Identifier{
		login.Email("not an email"), login.Email("Alice <alice@example.com>"), login.Email(""),
		login.Phone("13800138000"), login.Phone("+0123456789"), login.Username("a b"), login.Username(" "),
		{Kind: "Google", Value: "x"}, {Kind: "", Value: "x"}, {Kind: "wechat", Value: strings.Repeat("x", 192)},
		{Kind: "wechat", Value: "a\x00b"},
	} {
		if _, err := bad.Normalize(); !errors.Is(err, login.ErrInvalidIdentifier) {
			t.Errorf("%v: %v", bad, err)
		}
	}
}

func TestPassword(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var resolved []identity.Subject
	e := newRealm(t, login.Options{
		Resolve: func(_ context.Context, sub identity.Subject) (identity.Subject, error) {
			resolved = append(resolved, sub)
			if sub.ID == "blocked" {
				return identity.Subject{}, errors.New("blocked")
			}
			// Resolver 不能改变是谁、在哪个租户。
			return identity.Subject{Type: "staff", ID: "mallory", Tenant: "other", Permissions: []string{"orders:read"}}, nil
		},
	})
	e.account(t, ctx, "c1", "alice@example.com", "correct horse")
	if err := e.realm.Attach(ctx, e.db, ref("c1"), login.Username("alice"), false); err != nil {
		t.Fatal(err)
	}

	for _, id := range []login.Identifier{login.Email(" ALICE@example.com"), login.Username("Alice")} {
		out, err := e.realm.Password(ctx, id, "correct horse")
		if err != nil || out.Proof != "" || out.Subject.Type != customer || out.Subject.ID != "c1" ||
			out.Subject.Tenant != "" || len(out.Subject.Permissions) != 1 {
			t.Fatalf("%v: %+v %v", id, out, err)
		}
	}
	if len(resolved) != 2 || resolved[0].Ref() != ref("c1") {
		t.Fatalf("resolved=%+v", resolved)
	}

	// 密码错误、标识不存在、没有密码、标识类型不能用于密码登录，都是同一个错误。
	e.account(t, ctx, "c2", "nopass@example.com", "")
	for id, password := range map[login.Identifier]string{
		login.Email("alice@example.com"):  "wrong",
		login.Email("nobody@example.com"): "correct horse",
		login.Email("nopass@example.com"): "",
		login.Phone("+8613800138000"):     "correct horse",
		{Kind: "wechat", Value: "o1"}:     "correct horse",
		login.Email("bad"):                "correct horse",
	} {
		_, err := e.realm.Password(ctx, id, password)
		is(t, err, login.ErrInvalidCredentials)
	}

	// Resolve 拒绝时登录失败。
	e.account(t, ctx, "blocked", "blocked@example.com", "correct horse")
	if _, err := e.realm.Password(ctx, login.Email("blocked@example.com"), "correct horse"); err == nil || err.Error() != "blocked" {
		t.Fatalf("blocked: %v", err)
	}
}

// 每次尝试消耗配额，成功后清零：只有连续的失败受到限制。
func TestAttemptsLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newRealm(t, login.Options{Attempts: ratelimit.NewMemory(ratelimit.Config{Rate: 3, Interval: time.Hour, Burst: 3})})
	e.account(t, ctx, "c1", "alice@example.com", "correct horse")
	alice := login.Email("alice@example.com")
	for range 2 {
		_, err := e.realm.Password(ctx, alice, "wrong")
		is(t, err, login.ErrInvalidCredentials)
	}
	if _, err := e.realm.Password(ctx, alice, "correct horse"); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		_, err := e.realm.Password(ctx, alice, "wrong")
		is(t, err, login.ErrInvalidCredentials)
	}
	_, err := e.realm.Password(ctx, alice, "correct horse")
	is(t, err, ratelimit.ErrExceeded)
	// 计数按标识，其他标识不受影响；大小写不同仍是同一个标识。
	_, err = e.realm.Password(ctx, login.Email("ALICE@example.com"), "correct horse")
	is(t, err, ratelimit.ErrExceeded)
	_, err = e.realm.Password(ctx, login.Email("bob@example.com"), "x")
	is(t, err, login.ErrInvalidCredentials)
}

// 哈希参数过时时，登录成功后按当前配置重新哈希。
func TestRehash(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newRealm(t, login.Options{Hasher: hasher(4)})
	e.account(t, ctx, "c1", "alice@example.com", "correct horse")
	stored := func() string {
		var h string
		if err := e.db.NewSelect().Table("auth_passwords").Column("hash").Scan(ctx, &h); err != nil {
			t.Fatal(err)
		}
		return h
	}
	before := stored()
	stronger := login.New(e.db, login.Options{Type: customer, Hasher: hasher(5), Crypt: crypt.New(crypt.Config{Key: appKey}),
		Attempts: ratelimit.NewMemory(ratelimit.Config{Rate: 9, Interval: time.Minute, Burst: 9}),
		Sends:    ratelimit.NewMemory(ratelimit.Config{Rate: 9, Interval: time.Minute, Burst: 9})})
	if _, err := stronger.Password(ctx, login.Email("alice@example.com"), "correct horse"); err != nil {
		t.Fatal(err)
	}
	if after := stored(); after == before || !strings.HasPrefix(after, "$2a$05$") {
		t.Fatalf("hash not upgraded: %s", after)
	}
}

func TestSignInWithCode(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newRealm(t, login.Options{})
	if err := e.realm.Attach(ctx, e.db, ref("c1"), login.Phone("+8613800138000"), false); err != nil {
		t.Fatal(err)
	}
	phone := login.Phone("+8613800138000")

	requestID, c := e.code(t, ctx, login.SignIn, phone)
	if !c.Registered || len(c.Code) != 6 || !c.ExpiresAt.Equal(e.clock.now().Add(15*time.Minute)) {
		t.Fatalf("code=%+v", c)
	}
	out, err := e.realm.VerifyCode(ctx, requestID, c.Code)
	if err != nil || out.Subject.Ref() != ref("c1") || out.Proof != "" {
		t.Fatalf("verify: %+v %v", out, err)
	}
	// 登录后标识标为已验证，验证码不能再用。
	list, _ := e.realm.Identifiers(ctx, ref("c1"))
	if len(list) != 1 || !list[0].Verified {
		t.Fatalf("identifiers=%+v", list)
	}
	_, err = e.realm.VerifyCode(ctx, requestID, c.Code)
	is(t, err, login.ErrInvalidCode)

	// 每个验证码最多试 3 次，之后正确的也不行。
	requestID, c = e.code(t, ctx, login.SignIn, phone)
	for range 3 {
		_, err := e.realm.VerifyCode(ctx, requestID, "000000x")
		is(t, err, login.ErrInvalidCode)
	}
	_, err = e.realm.VerifyCode(ctx, requestID, c.Code)
	is(t, err, login.ErrInvalidCode)

	// 过期后无效。
	requestID, c = e.code(t, ctx, login.SignIn, phone)
	e.clock.add(15 * time.Minute)
	_, err = e.realm.VerifyCode(ctx, requestID, c.Code)
	is(t, err, login.ErrInvalidCode)

	// 发送之后标识被移除：不能登录。
	requestID, c = e.code(t, ctx, login.SignIn, phone)
	if err := e.realm.Attach(ctx, e.db, ref("c1"), login.Email("c1@example.com"), true); err != nil {
		t.Fatal(err)
	}
	if err := e.realm.Detach(ctx, e.db, ref("c1"), phone); err != nil {
		t.Fatal(err)
	}
	_, err = e.realm.VerifyCode(ctx, requestID, c.Code)
	is(t, err, login.ErrInvalidCode)

	for _, bad := range []string{"", "not-a-uuid", "0190a6a1-0000-7000-8000-000000000000"} {
		_, err := e.realm.VerifyCode(ctx, bad, "123456")
		is(t, err, login.ErrInvalidCode)
	}
}

// 没有密码的账号用发给自己的验证码确认身份。
func TestConfirmCode(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newRealm(t, login.Options{})
	phone := login.Phone("+8613800138000")
	if err := e.realm.Attach(ctx, e.db, ref("c1"), phone, false); err != nil {
		t.Fatal(err)
	}
	e.account(t, ctx, "c2", "c2@example.com", "")

	// 只能发给自己的标识。
	_, err := e.realm.SendConfirmCode(ctx, ref("c2"), phone)
	is(t, err, login.ErrNotFound)
	_, err = e.realm.SendConfirmCode(ctx, ref("c1"), login.Email("nobody@example.com"))
	is(t, err, login.ErrNotFound)
	if _, err := e.realm.SendCode(ctx, login.Confirm, phone); err == nil {
		t.Fatal("SendCode sent a confirm code")
	}

	send := func() (string, login.Code) {
		t.Helper()
		requestID, err := e.realm.SendConfirmCode(ctx, ref("c1"), phone)
		if err != nil {
			t.Fatal(err)
		}
		c := e.mail.last(t, phone)
		if c.RequestID != requestID || c.Purpose != login.Confirm || !c.Registered {
			t.Fatalf("sent %+v for request %s", c, requestID)
		}
		return requestID, c
	}
	requestID, c := send()
	// 确认用的验证码不能用来登录，也不能替别人确认；这些尝试不消耗次数。
	for range 3 {
		_, err = e.realm.VerifyCode(ctx, requestID, c.Code)
		is(t, err, login.ErrInvalidCode)
		is(t, e.realm.ConfirmCode(ctx, ref("c2"), requestID, c.Code), login.ErrInvalidCode)
	}
	if err := e.realm.ConfirmCode(ctx, ref("c1"), requestID, c.Code); err != nil {
		t.Fatal(err)
	}
	list, _ := e.realm.Identifiers(ctx, ref("c1"))
	if len(list) != 1 || !list[0].Verified {
		t.Fatalf("identifiers=%+v", list)
	}
	is(t, e.realm.ConfirmCode(ctx, ref("c1"), requestID, c.Code), login.ErrInvalidCode)

	// 登录用的验证码不能用来确认身份。
	requestID, c = e.code(t, ctx, login.SignIn, phone)
	is(t, e.realm.ConfirmCode(ctx, ref("c1"), requestID, c.Code), login.ErrInvalidCode)

	// 每个验证码最多试 3 次。
	requestID, c = send()
	for range 3 {
		is(t, e.realm.ConfirmCode(ctx, ref("c1"), requestID, "000000x"), login.ErrInvalidCode)
	}
	is(t, e.realm.ConfirmCode(ctx, ref("c1"), requestID, c.Code), login.ErrInvalidCode)

	// 发送之后标识被移除：不能确认。
	requestID, c = send()
	if err := e.realm.Attach(ctx, e.db, ref("c1"), login.Email("c1@example.com"), true); err != nil {
		t.Fatal(err)
	}
	if err := e.realm.Detach(ctx, e.db, ref("c1"), phone); err != nil {
		t.Fatal(err)
	}
	is(t, e.realm.ConfirmCode(ctx, ref("c1"), requestID, c.Code), login.ErrInvalidCode)

	// 过期后无效。
	requestID, err = e.realm.SendConfirmCode(ctx, ref("c1"), login.Email("c1@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	c = e.mail.last(t, login.Email("c1@example.com"))
	e.clock.add(15 * time.Minute)
	is(t, e.realm.ConfirmCode(ctx, ref("c1"), requestID, c.Code), login.ErrInvalidCode)
}

// 并发提交同一个验证码，只有一次成功。
func TestConcurrentVerify(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newRealm(t, login.Options{CodeAttempts: 20})
	e.account(t, ctx, "c1", "alice@example.com", "")
	for _, purpose := range []login.Purpose{login.SignIn, login.Reset} {
		requestID, c := e.code(t, ctx, purpose, login.Email("alice@example.com"))
		var (
			wg sync.WaitGroup
			mu sync.Mutex
			ok int
		)
		for range 8 {
			wg.Go(func() {
				_, err := e.realm.VerifyCode(ctx, requestID, c.Code)
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
			t.Fatalf("%s: %d verifications succeeded", purpose, ok)
		}
	}
}

func TestRegister(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newRealm(t, login.Options{Policy: login.Policy{Register: true}})
	email := login.Email("new@example.com")
	requestID, c := e.code(t, ctx, login.SignIn, email)
	if c.Registered {
		t.Fatal("unregistered identifier reported as registered")
	}
	out, err := e.realm.VerifyCode(ctx, requestID, c.Code)
	if err != nil || out.Proof == "" || !out.Subject.Ref().IsZero() {
		t.Fatalf("verify: %+v %v", out, err)
	}

	// 应用的注册在事务中失败：一起回滚，凭证仍可使用。
	failed := errors.New("insert account failed")
	err = database.RunInTx(ctx, e.db, nil, func(ctx context.Context, tx database.Tx) error {
		if _, err := e.realm.Register(ctx, tx, out.Proof, ref("c9")); err != nil {
			return err
		}
		return failed
	})
	is(t, err, failed)
	// 密码不符合策略时不消耗凭证。
	_, err = e.realm.Register(ctx, e.db, out.Proof, ref("c9"), login.WithPassword("short"))
	is(t, err, login.ErrWeakPassword)

	var reg login.Outcome
	err = database.RunInTx(ctx, e.db, nil, func(ctx context.Context, tx database.Tx) error {
		reg, err = e.realm.Register(ctx, tx, out.Proof, ref("c9"), login.WithPassword("correct horse"))
		return err
	})
	if err != nil || reg.Subject.Ref() != ref("c9") {
		t.Fatalf("register: %+v %v", reg, err)
	}
	list, _ := e.realm.Identifiers(ctx, ref("c9"))
	if len(list) != 1 || list[0].Identifier != email || !list[0].Verified {
		t.Fatalf("identifiers=%+v", list)
	}
	if _, err := e.realm.Password(ctx, email, "correct horse"); err != nil {
		t.Fatal(err)
	}
	_, err = e.realm.Register(ctx, e.db, out.Proof, ref("c10"))
	is(t, err, login.ErrInvalidProof)

	// 两个人同时用同一个邮箱注册：后完成的得到 ErrTaken，凭证不被消耗。
	taken := login.Email("race@example.com")
	first, second := e.proof(t, ctx, login.SignIn, taken), e.proof(t, ctx, login.SignIn, taken)
	if _, err := e.realm.Register(ctx, e.db, first, ref("c11")); err != nil {
		t.Fatal(err)
	}
	_, err = e.realm.Register(ctx, e.db, second, ref("c12"))
	is(t, err, login.ErrTaken)
	if err := e.realm.Link(ctx, e.db, ref("c11"), second); err != nil {
		t.Fatalf("proof consumed by the failed registration: %v", err)
	}

	// Reset 的凭证不能用于注册；凭证过期后无效。
	_, err = e.realm.Register(ctx, e.db, e.proof(t, ctx, login.Reset, email), ref("c13"))
	is(t, err, login.ErrInvalidProof)
	late := e.proof(t, ctx, login.SignIn, login.Email("late@example.com"))
	e.clock.add(15 * time.Minute)
	_, err = e.realm.Register(ctx, e.db, late, ref("c14"))
	is(t, err, login.ErrInvalidProof)
	for _, bad := range []string{"", "x", out.Proof + "x", strings.Split(out.Proof, ".")[0] + ".x"} {
		_, err := e.realm.Register(ctx, e.db, bad, ref("c15"))
		is(t, err, login.ErrInvalidProof)
	}
}

// 不开放注册、或者找回密码时，未注册的标识不发送验证码，但仍返回请求 ID。
func TestUnregisteredGetsNoCode(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newRealm(t, login.Options{})
	for _, purpose := range []login.Purpose{login.SignIn, login.Reset} {
		requestID, err := e.realm.SendCode(ctx, purpose, login.Email("nobody@example.com"))
		if err != nil || requestID == "" {
			t.Fatalf("%s: %q %v", purpose, requestID, err)
		}
		_, err = e.realm.VerifyCode(ctx, requestID, "123456")
		is(t, err, login.ErrInvalidCode)
	}
	if e.mail.count() != 0 {
		t.Fatalf("sent %d codes", e.mail.count())
	}
	// Verify 不论是否注册都发送。
	e.code(t, ctx, login.Verify, login.Email("nobody@example.com"))
	_, err := e.realm.SendCode(ctx, "other", login.Email("nobody@example.com"))
	if err == nil {
		t.Fatal("unknown purpose accepted")
	}
	_, err = e.realm.SendCode(ctx, login.SignIn, login.Phone("138"))
	is(t, err, login.ErrInvalidIdentifier)
}

func TestSendFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newRealm(t, login.Options{Policy: login.Policy{Register: true}})
	e.mail.fail = errors.New("smtp down")
	_, err := e.realm.SendCode(ctx, login.SignIn, login.Email("a@example.com"))
	is(t, err, e.mail.fail)
	if n, _ := e.db.NewSelect().Table("auth_challenges").Count(ctx); n != 0 {
		t.Fatalf("%d challenges left after a failed send", n)
	}
	noSend := login.New(e.db, login.Options{Type: customer, Hasher: hasher(4), Crypt: crypt.New(crypt.Config{Key: appKey}), Attempts: e.realmLimiter(), Sends: e.realmLimiter()})
	if _, err := noSend.SendCode(ctx, login.SignIn, login.Email("a@example.com")); err == nil {
		t.Fatal("SendCode without Options.Send succeeded")
	}
}

func TestSendLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newRealm(t, login.Options{Policy: login.Policy{Register: true}, Sends: ratelimit.NewMemory(ratelimit.Config{Rate: 2, Interval: time.Hour, Burst: 2})})
	for range 2 {
		e.code(t, ctx, login.SignIn, login.Email("a@example.com"))
	}
	_, err := e.realm.SendCode(ctx, login.Reset, login.Email("A@example.com"))
	is(t, err, ratelimit.ErrExceeded)
	e.code(t, ctx, login.SignIn, login.Email("b@example.com"))
}

func TestResetPassword(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newRealm(t, login.Options{Attempts: ratelimit.NewMemory(ratelimit.Config{Rate: 2, Interval: time.Hour, Burst: 2})})
	e.account(t, ctx, "c1", "alice@example.com", "")
	alice := login.Email("alice@example.com")
	if err := e.realm.Attach(ctx, e.db, ref("c1"), login.Username("alice"), false); err != nil {
		t.Fatal(err)
	}
	// 被猜过密码，计数接近上限。
	_, err := e.realm.Password(ctx, alice, "guess")
	is(t, err, login.ErrInvalidCredentials)

	proof := e.proof(t, ctx, login.Reset, alice)
	_, err = e.realm.ResetPassword(ctx, proof, " short ")
	is(t, err, login.ErrWeakPassword)
	who, err := e.realm.ResetPassword(ctx, proof, "new password")
	if err != nil || who != ref("c1") {
		t.Fatalf("reset: %+v %v", who, err)
	}
	// 找回密码后失败计数清零：再失败一次仍能登录（配额为 2）。
	_, err = e.realm.Password(ctx, alice, "guess")
	is(t, err, login.ErrInvalidCredentials)
	if _, err := e.realm.Password(ctx, alice, "new password"); err != nil {
		t.Fatal(err)
	}
	_, err = e.realm.ResetPassword(ctx, proof, "another password")
	is(t, err, login.ErrInvalidProof)

	// 发送之后标识转给了别人：凭证失效。
	proof = e.proof(t, ctx, login.Reset, alice)
	if err := e.realm.Detach(ctx, e.db, ref("c1"), alice); err != nil {
		t.Fatal(err)
	}
	e.account(t, ctx, "c2", "alice@example.com", "")
	_, err = e.realm.ResetPassword(ctx, proof, "hijacked password")
	is(t, err, login.ErrInvalidProof)
	if ok, _ := e.realm.HasPassword(ctx, ref("c2")); ok {
		t.Fatal("password set on the new owner")
	}
}

func TestLink(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newRealm(t, login.Options{})
	e.account(t, ctx, "c1", "alice@example.com", "")
	e.account(t, ctx, "c2", "bob@example.com", "")
	phone := login.Phone("+8613800138000")

	// 标识属于别人：ErrTaken，凭证不被消耗，属于自己后仍可使用。
	bobs := e.proof(t, ctx, login.Verify, login.Email("bob@example.com"))
	is(t, e.realm.Link(ctx, e.db, ref("c1"), bobs), login.ErrTaken)
	if err := e.realm.Link(ctx, e.db, ref("c2"), bobs); err != nil {
		t.Fatal(err)
	}

	if err := e.realm.Link(ctx, e.db, ref("c1"), e.proof(t, ctx, login.Verify, phone)); err != nil {
		t.Fatal(err)
	}
	// 已属于自己的未验证标识，绑定后标为已验证。
	if err := e.realm.Attach(ctx, e.db, ref("c1"), login.Email("alice2@example.com"), false); err != nil {
		t.Fatal(err)
	}
	if err := e.realm.Link(ctx, e.db, ref("c1"), e.proof(t, ctx, login.Verify, login.Email("alice2@example.com"))); err != nil {
		t.Fatal(err)
	}
	list, err := e.realm.Identifiers(ctx, ref("c1"))
	if err != nil || len(list) != 3 {
		t.Fatalf("identifiers=%+v %v", list, err)
	}
	for _, a := range list {
		if !a.Verified {
			t.Fatalf("%v not verified", a.Identifier)
		}
	}
	// 更换邮箱：在同一个事务中绑定新的、移除旧的；只有一个邮箱的账号也可以更换。
	e.account(t, ctx, "c3", "carol@example.com", "")
	newEmail := e.proof(t, ctx, login.Verify, login.Email("carol@new.example.com"))
	err = database.RunInTx(ctx, e.db, nil, func(ctx context.Context, tx database.Tx) error {
		if err := e.realm.Link(ctx, tx, ref("c3"), newEmail); err != nil {
			return err
		}
		return e.realm.Detach(ctx, tx, ref("c3"), login.Email("carol@example.com"))
	})
	if list, _ := e.realm.Identifiers(ctx, ref("c3")); err != nil || len(list) != 1 || list[0].Value != "carol@new.example.com" {
		t.Fatalf("change email: %+v %v", list, err)
	}
	if got, err := e.realm.Lookup(ctx, login.Phone("+8613800138000")); err != nil || got != ref("c1") {
		t.Fatalf("lookup: %v %v", got, err)
	}
	_, err = e.realm.Lookup(ctx, login.Email("nobody@example.com"))
	is(t, err, login.ErrNotFound)
}

// PerTenant：标识在租户内唯一，验证码和凭证只能在发送时的租户中使用，主体带有租户。
func TestPerTenant(t *testing.T) {
	t.Parallel()
	acme := identity.WithTenant(context.Background(), "acme")
	globex := identity.WithTenant(context.Background(), "globex")
	e := newRealm(t, login.Options{PerTenant: true, Policy: login.Policy{Register: true}})
	email := login.Email("alice@example.com")
	e.account(t, acme, "acme:1", "alice@example.com", "acme password")
	e.account(t, globex, "globex:1", "alice@example.com", "globex password")

	out, err := e.realm.Password(acme, email, "acme password")
	if err != nil || out.Subject.ID != "acme:1" || out.Subject.Tenant != "acme" {
		t.Fatalf("acme: %+v %v", out, err)
	}
	_, err = e.realm.Password(globex, email, "acme password")
	is(t, err, login.ErrInvalidCredentials)
	_, err = e.realm.Password(context.Background(), email, "acme password")
	is(t, err, login.ErrInvalidCredentials)

	requestID, c := e.code(t, acme, login.SignIn, email)
	if c.Tenant != "acme" {
		t.Fatalf("code tenant=%q", c.Tenant)
	}
	_, err = e.realm.VerifyCode(globex, requestID, c.Code)
	is(t, err, login.ErrInvalidCode)
	if out, err := e.realm.VerifyCode(acme, requestID, c.Code); err != nil || out.Subject.Tenant != "acme" {
		t.Fatalf("verify: %+v %v", out, err)
	}

	newcomer := e.proof(t, acme, login.SignIn, login.Email("new@example.com"))
	_, err = e.realm.Register(globex, e.db, newcomer, ref("globex:2"))
	is(t, err, login.ErrInvalidProof)
	reg, err := e.realm.Register(acme, e.db, newcomer, ref("acme:2"))
	if err != nil || reg.Subject.Tenant != "acme" {
		t.Fatalf("register: %+v %v", reg, err)
	}
	if _, err := e.realm.Lookup(globex, login.Email("new@example.com")); !errors.Is(err, login.ErrNotFound) {
		t.Fatalf("identifier leaked to another tenant: %v", err)
	}
}

func TestDetachAndForget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newRealm(t, login.Options{Policy: login.Policy{Register: true}})
	e.account(t, ctx, "c1", "alice@example.com", "correct horse")
	phone := login.Phone("+8613800138000")
	if err := e.realm.Attach(ctx, e.db, ref("c1"), phone, true); err != nil {
		t.Fatal(err)
	}
	is(t, e.realm.Attach(ctx, e.db, ref("c2"), phone, true), login.ErrTaken)

	is(t, e.realm.Detach(ctx, e.db, ref("c1"), login.Email("other@example.com")), login.ErrNotFound)
	is(t, e.realm.Detach(ctx, e.db, ref("c2"), phone), login.ErrNotFound)
	if err := e.realm.Detach(ctx, e.db, ref("c1"), login.Email("ALICE@example.com")); err != nil {
		t.Fatal(err)
	}
	is(t, e.realm.Detach(ctx, e.db, ref("c1"), phone), login.ErrLastMethod)

	e.code(t, ctx, login.SignIn, phone)
	if err := e.realm.Forget(ctx, e.db, ref("c1")); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"auth_identifiers", "auth_passwords", "auth_challenges"} {
		if n, _ := e.db.NewSelect().Table(table).Count(ctx); n != 0 {
			t.Fatalf("%s has %d rows after Forget", table, n)
		}
	}
	// 注销后标识可以被别人注册。
	if err := e.realm.Attach(ctx, e.db, ref("c2"), phone, true); err != nil {
		t.Fatal(err)
	}
}

func TestPasswordManagement(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newRealm(t, login.Options{Attempts: ratelimit.NewMemory(ratelimit.Config{Rate: 2, Interval: time.Hour, Burst: 2})})
	e.account(t, ctx, "c1", "alice@example.com", "")
	if ok, err := e.realm.HasPassword(ctx, ref("c1")); ok || err != nil {
		t.Fatalf("has password: %v %v", ok, err)
	}
	is(t, e.realm.Confirm(ctx, ref("c1"), ""), login.ErrInvalidCredentials)
	is(t, e.realm.SetPassword(ctx, e.db, ref("c1"), "1234567"), login.ErrWeakPassword)
	// bcrypt 只能处理 72 字节。
	is(t, e.realm.SetPassword(ctx, e.db, ref("c1"), strings.Repeat("长", 30)), login.ErrWeakPassword)
	if err := e.realm.SetPassword(ctx, e.db, ref("c1"), "correct horse"); err != nil {
		t.Fatal(err)
	}
	if err := e.realm.Confirm(ctx, ref("c1"), "correct horse"); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	is(t, e.realm.Confirm(ctx, ref("c1"), "wrong"), login.ErrInvalidCredentials)
	is(t, e.realm.Confirm(ctx, ref("c1"), "wrong"), login.ErrInvalidCredentials)
	is(t, e.realm.Confirm(ctx, ref("c1"), "correct horse"), ratelimit.ErrExceeded)

	if err := e.realm.RemovePassword(ctx, e.db, ref("c1")); err != nil {
		t.Fatal(err)
	}
	if ok, _ := e.realm.HasPassword(ctx, ref("c1")); ok {
		t.Fatal("password not removed")
	}
	// 其他账号体系的主体不能交给这个 Realm。
	if err := e.realm.SetPassword(ctx, e.db, identity.Ref{Type: "staff", ID: "s1"}, "correct horse"); err == nil {
		t.Fatal("staff subject accepted by the customer realm")
	}
}

// 移除标识或密码之后，账号必须还有能用的登录方式。
func TestLastMethod(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newRealm(t, login.Options{})
	name := login.Username("alice")

	// 只有用户名和密码：密码是唯一的登录方式。
	if err := e.realm.Attach(ctx, e.db, ref("c1"), name, false); err != nil {
		t.Fatal(err)
	}
	if err := e.realm.SetPassword(ctx, e.db, ref("c1"), "correct horse"); err != nil {
		t.Fatal(err)
	}
	is(t, e.realm.RemovePassword(ctx, e.db, ref("c1")), login.ErrLastMethod)

	// 有了邮箱，可以用验证码登录，密码可以删除；之后用户名和邮箱都不能移除最后能登录的那个。
	if err := e.realm.Attach(ctx, e.db, ref("c1"), login.Email("alice@example.com"), true); err != nil {
		t.Fatal(err)
	}
	if err := e.realm.RemovePassword(ctx, e.db, ref("c1")); err != nil {
		t.Fatal(err)
	}
	if err := e.realm.RemovePassword(ctx, e.db, ref("c1")); err != nil {
		t.Fatalf("remove password twice: %v", err)
	}
	is(t, e.realm.Detach(ctx, e.db, ref("c1"), login.Email("alice@example.com")), login.ErrLastMethod)

	// 第三方账号单独就能登录。
	ext := login.Identifier{Kind: "github", Value: "42"}
	if err := e.realm.Attach(ctx, e.db, ref("c1"), ext, true); err != nil {
		t.Fatal(err)
	}
	if err := e.realm.Detach(ctx, e.db, ref("c1"), login.Email("alice@example.com")); err != nil {
		t.Fatal(err)
	}
	is(t, e.realm.Detach(ctx, e.db, ref("c1"), ext), login.ErrLastMethod)
	if err := e.realm.Detach(ctx, e.db, ref("c1"), name); err != nil {
		t.Fatal(err)
	}

	// 没有设置 Options.Send 时邮箱不能接收验证码，只能和密码一起使用。
	e.account(t, ctx, "c2", "bob@example.com", "correct horse")
	noSend := login.New(e.db, login.Options{Type: customer, Hasher: hasher(4), Crypt: crypt.New(crypt.Config{Key: appKey}), Attempts: e.realmLimiter(), Sends: e.realmLimiter()})
	is(t, noSend.RemovePassword(ctx, e.db, ref("c2")), login.ErrLastMethod)
}

func TestCleanup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newRealm(t, login.Options{Policy: login.Policy{Register: true}})
	e.account(t, ctx, "c1", "alice@example.com", "")
	used, c := e.code(t, ctx, login.SignIn, login.Email("alice@example.com"))
	if _, err := e.realm.VerifyCode(ctx, used, c.Code); err != nil {
		t.Fatal(err)
	}
	e.code(t, ctx, login.SignIn, login.Email("expired@example.com"))
	e.clock.add(16 * time.Minute)
	pending, _ := e.code(t, ctx, login.SignIn, login.Email("pending@example.com"))
	n, err := e.realm.Cleanup(ctx, 10)
	if err != nil || n != 2 {
		t.Fatalf("cleanup: %d %v", n, err)
	}
	var ids []string
	if err := e.db.NewSelect().Table("auth_challenges").Column("id").Scan(ctx, &ids); err != nil || len(ids) != 1 || ids[0] != pending {
		t.Fatalf("left=%v %v", ids, err)
	}
}

// 轮换应用密钥后，之前发出的验证码仍然有效。
func TestCodeSurvivesKeyRotation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newRealm(t, login.Options{Policy: login.Policy{Register: true}})
	requestID, c := e.code(t, ctx, login.SignIn, login.Email("a@example.com"))
	rotated := login.New(e.db, login.Options{Type: customer, Hasher: hasher(4),
		Crypt:    crypt.New(crypt.Config{Key: crypt.GenerateKey(), PreviousKeys: []string{appKey}}),
		Attempts: e.realmLimiter(), Sends: e.realmLimiter()})
	if out, err := rotated.VerifyCode(ctx, requestID, c.Code); err != nil || out.Proof == "" {
		t.Fatalf("verify after rotation: %+v %v", out, err)
	}
}

func TestNewPanics(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t, login.Migrations())
	c := crypt.New(crypt.Config{Key: appKey})
	l := ratelimit.NewMemory(ratelimit.Config{Rate: 5, Interval: time.Minute, Burst: 5})
	for name, opts := range map[string]login.Options{
		"no type":        {Hasher: hasher(4), Crypt: c},
		"bad type":       {Type: "Customer", Hasher: hasher(4), Crypt: c},
		"no hasher":      {Type: customer, Crypt: c},
		"no crypt":       {Type: customer, Hasher: hasher(4)},
		"no attempts":    {Type: customer, Hasher: hasher(4), Crypt: c, Sends: l},
		"no sends":       {Type: customer, Hasher: hasher(4), Crypt: c, Attempts: l},
		"short code":     {Type: customer, Hasher: hasher(4), Crypt: c, CodeLength: 3},
		"negative ttl":   {Type: customer, Hasher: hasher(4), Crypt: c, CodeTTL: -time.Second},
		"negative tries": {Type: customer, Hasher: hasher(4), Crypt: c, CodeAttempts: -1},
	} {
		if name != "no attempts" && name != "no sends" {
			opts.Attempts, opts.Sends = l, l
		}
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: expected panic", name)
				}
			}()
			login.New(db, opts)
		}()
	}
}
