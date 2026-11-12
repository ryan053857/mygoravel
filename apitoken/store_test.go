package apitoken_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ryan053857/mygoravel/apitoken"
	"github.com/ryan053857/mygoravel/database/dbtest"
	"github.com/ryan053857/mygoravel/httpx/rest"
	"github.com/ryan053857/mygoravel/identity"
)

// testConfig：最长 10 小时，空闲 1 小时，“记住我”空闲 3 小时。
var testConfig = apitoken.Config{Lifetime: 10 * time.Hour, IdleTimeout: time.Hour, RememberIdleTimeout: 3 * time.Hour}

// read 是测试中个人访问令牌常用的 scope。
var read = []string{"articles:read"}

var (
	alice = identity.Subject{ID: "alice", Type: "user"}
	bob   = identity.Subject{ID: "bob", Type: "user"}
)

func newStore(t *testing.T) *apitoken.Store {
	t.Helper()
	return apitoken.NewStore(dbtest.Open(t, apitoken.Migrations()), testConfig)
}

func login(t *testing.T, s *apitoken.Store, sub identity.Subject, d apitoken.Device) string {
	t.Helper()
	plain, err := s.Login(context.Background(), sub, d)
	if err != nil {
		t.Fatal(err)
	}
	return plain
}

func create(t *testing.T, s *apitoken.Store, sub identity.Subject, p apitoken.Personal) string {
	t.Helper()
	plain, err := s.Create(context.Background(), sub, p)
	if err != nil {
		t.Fatal(err)
	}
	return plain
}

// valid 校验 token，返回主体；无效时返回 ok 为 false。
func valid(t *testing.T, s *apitoken.Store, token string) (identity.Subject, bool) {
	t.Helper()
	sub, err := s.Validate(context.Background(), token)
	if errors.Is(err, apitoken.ErrInvalid) {
		return sub, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return sub, true
}

func TestConfig(t *testing.T) {
	t.Parallel()
	var cfg apitoken.Config
	cfg.SetDefaults()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}
	for name, c := range map[string]apitoken.Config{
		"zero lifetime":          {IdleTimeout: time.Hour},
		"negative idle":          {Lifetime: time.Hour, IdleTimeout: -time.Second},
		"negative remember idle": {Lifetime: time.Hour, RememberIdleTimeout: -time.Second},
	} {
		if c.Validate() == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	for name, build := range map[string]func(){
		"nil db":         func() { apitoken.NewStore(nil, testConfig) },
		"invalid config": func() { apitoken.NewStore(dbtest.Open(t, apitoken.Migrations()), apitoken.Config{}) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: expected panic", name)
				}
			}()
			build()
		}()
	}
}

func TestLoginAndValidate(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		s := newStore(t)
		ua := strings.Repeat("浏", 300)
		plain := login(t, s, alice, apitoken.Device{IP: "203.0.113.7", UserAgent: ua})

		sub, ok := valid(t, s, plain)
		// 登录 token 不限制权限，也不携带权限。
		if !ok || sub.ID != "alice" || sub.Type != "user" || sub.Method != "bearer" ||
			sub.Scopes != nil || sub.Permissions != nil || sub.Tenant != "" || sub.Credential == "" {
			t.Fatalf("subject=%+v ok=%v", sub, ok)
		}
		for _, bad := range []string{"", "bogus", plain + "x"} {
			if _, ok := valid(t, s, bad); ok {
				t.Fatalf("token %q accepted", bad)
			}
		}

		list, err := s.List(ctx, sub)
		if err != nil || len(list) != 1 {
			t.Fatalf("list=%+v err=%v", list, err)
		}
		tok := list[0]
		if tok.ID != sub.Credential || !tok.Current || tok.Kind != apitoken.KindLogin || tok.IP != "203.0.113.7" ||
			len([]rune(tok.UserAgent)) != 255 || !tok.ExpiresAt.Equal(time.Now().Add(time.Hour)) {
			t.Fatalf("token=%+v", tok)
		}
		if others, _ := s.List(ctx, bob); len(others) != 0 {
			t.Fatalf("bob sees %d tokens", len(others))
		}
		if _, err := s.Login(ctx, identity.Subject{}, apitoken.Device{}); err == nil {
			t.Fatal("expected error for empty subject")
		}
	})
}

func TestLoginSlidingExpiry(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		s := newStore(t)

		// 一直在用：每次使用都延长一小时，越过最初的截止时间仍然有效。
		plain := login(t, s, alice, apitoken.Device{})
		start := time.Now()
		for time.Since(start) < 10*time.Hour-50*time.Minute {
			time.Sleep(50 * time.Minute)
			if _, ok := valid(t, s, plain); !ok {
				t.Fatalf("active token expired after %v", time.Since(start))
			}
		}
		// 但不超过 Lifetime。
		time.Sleep(50 * time.Minute)
		if _, ok := valid(t, s, plain); ok {
			t.Fatalf("token valid after lifetime: %v", time.Since(start))
		}

		// 空闲超过一小时失效。
		idle := login(t, s, alice, apitoken.Device{})
		time.Sleep(time.Hour)
		if _, ok := valid(t, s, idle); ok {
			t.Fatal("idle token still valid")
		}

		// “记住我”空闲三小时内有效。
		remembered := login(t, s, alice, apitoken.Device{Remember: true})
		time.Sleep(2 * time.Hour)
		if _, ok := valid(t, s, remembered); !ok {
			t.Fatal("remembered token expired within remember idle timeout")
		}

		// 一分钟内的使用不写库，因此不延长有效期。
		quick := login(t, s, alice, apitoken.Device{})
		time.Sleep(30 * time.Second)
		if _, ok := valid(t, s, quick); !ok {
			t.Fatal("quick token invalid")
		}
		time.Sleep(time.Hour - 30*time.Second)
		if _, ok := valid(t, s, quick); ok {
			t.Fatal("use within a minute extended the token")
		}
	})
}

// 有效期在签发时写入 token：校验用的 Store 配置不同、或配置之后被修改，都不改变已签发 token 的有效期。
// 不同账号体系可以各用自己的 Config 签发，共用一个校验器。
func TestLifetimeFixedAtIssue(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		db := dbtest.Open(t, apitoken.Migrations())
		staff := apitoken.NewStore(db, apitoken.Config{Lifetime: 2 * time.Hour, IdleTimeout: 10 * time.Minute})
		mobile := apitoken.NewStore(db, apitoken.Config{Lifetime: 90 * 24 * time.Hour, IdleTimeout: 30 * 24 * time.Hour})
		staffToken := login(t, staff, identity.Subject{Type: "staff", ID: "s1"}, apitoken.Device{})
		customerToken := login(t, mobile, identity.Subject{Type: "customer", ID: "c1"}, apitoken.Device{})

		// 用 staff 的 Store 校验移动端的 token：仍然按签发时的 30 天空闲滑动。
		time.Sleep(9 * time.Minute)
		for _, token := range []string{staffToken, customerToken} {
			if _, ok := valid(t, staff, token); !ok {
				t.Fatal("token expired early")
			}
		}
		time.Sleep(20 * time.Minute)
		if _, ok := valid(t, mobile, staffToken); ok {
			t.Fatal("staff token outlived its 10 minute idle timeout")
		}
		if _, ok := valid(t, staff, customerToken); !ok {
			t.Fatal("customer token used the validating store's idle timeout")
		}
		// 每天使用一次，滑动延长不超过签发时的 Lifetime（90 天）。
		var lasted time.Duration
		for lasted = 0; lasted < 100*24*time.Hour; lasted += 24 * time.Hour {
			time.Sleep(24 * time.Hour)
			if _, ok := valid(t, staff, customerToken); !ok {
				break
			}
		}
		if lasted < 89*24*time.Hour || lasted > 90*24*time.Hour {
			t.Fatalf("customer token stopped after %v", lasted)
		}
	})
}

func TestCreatePersonal(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		s := newStore(t)
		for name, call := range map[string]func() error{
			"empty name": func() error { _, err := s.Create(ctx, alice, apitoken.Personal{Name: " ", Scopes: read}); return err },
			"long name": func() error {
				_, err := s.Create(ctx, alice, apitoken.Personal{Name: strings.Repeat("名", 256), Scopes: read})
				return err
			},
			"no scopes": func() error { _, err := s.Create(ctx, alice, apitoken.Personal{Name: "CI"}); return err },
			"past expiry": func() error {
				_, err := s.Create(ctx, alice, apitoken.Personal{Name: "CI", Scopes: read, ExpiresAt: time.Now()})
				return err
			},
			"spaced scope": func() error {
				_, err := s.Create(ctx, alice, apitoken.Personal{Name: "CI", Scopes: []string{"a b"}})
				return err
			},
			"empty scope": func() error {
				_, err := s.Create(ctx, alice, apitoken.Personal{Name: "CI", Scopes: []string{""}})
				return err
			},
			"empty subject": func() error {
				_, err := s.Create(ctx, identity.Subject{}, apitoken.Personal{Name: "CI", Scopes: read})
				return err
			},
		} {
			if call() == nil {
				t.Errorf("%s: expected error", name)
			}
		}

		forever := create(t, s, alice, apitoken.Personal{Name: "CI 部署", Scopes: []string{"articles:read", "comments:*"}})
		expiring := create(t, s, alice, apitoken.Personal{Name: "报表", Scopes: read, ExpiresAt: time.Now().Add(time.Hour)})
		sub, ok := valid(t, s, forever)
		if !ok || strings.Join(sub.Scopes, ",") != "articles:read,comments:*" || sub.Permissions != nil {
			t.Fatalf("subject=%+v ok=%v", sub, ok)
		}
		// 个人访问令牌不按空闲时间过期。
		time.Sleep(1000 * time.Hour)
		if _, ok := valid(t, s, forever); !ok {
			t.Fatal("non-expiring token expired")
		}
		if _, ok := valid(t, s, expiring); ok {
			t.Fatal("token valid after expiresAt")
		}
		list, err := s.List(ctx, alice)
		if err != nil || len(list) != 1 || list[0].Name != "CI 部署" || list[0].Kind != apitoken.KindPersonal ||
			!list[0].ExpiresAt.IsZero() || list[0].Current {
			t.Fatalf("list=%+v err=%v", list, err)
		}
	})
}

func TestRevoke(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)
	laptop := login(t, s, alice, apitoken.Device{})
	phone := login(t, s, alice, apitoken.Device{})
	ci := create(t, s, alice, apitoken.Personal{Name: "CI", Scopes: read})
	bobs := login(t, s, bob, apitoken.Device{})
	state := func(want map[string]bool) {
		t.Helper()
		for name, token := range map[string]string{"laptop": laptop, "phone": phone, "ci": ci, "bob": bobs} {
			if _, ok := valid(t, s, token); ok != want[name] {
				t.Fatalf("%s valid=%v, want %v", name, ok, want[name])
			}
		}
	}

	// 在笔记本上退出其他设备：只撤销其他登录 token，保留个人访问令牌。
	onLaptop, _ := valid(t, s, laptop)
	if n, err := s.RevokeOthers(ctx, onLaptop); n != 1 || err != nil {
		t.Fatalf("revoke others n=%d err=%v", n, err)
	}
	state(map[string]bool{"laptop": true, "ci": true, "bob": true})

	// 从页面（会话，没有 token 凭据）发起时撤销全部登录 token。
	if n, err := s.RevokeOthers(ctx, alice); n != 1 || err != nil {
		t.Fatalf("revoke others from session n=%d err=%v", n, err)
	}
	state(map[string]bool{"ci": true, "bob": true})

	ciSub, _ := valid(t, s, ci)
	for name, call := range map[string]func() error{
		"other subject": func() error { return s.Revoke(ctx, bob, ciSub.Credential) },
		"malformed id":  func() error { return s.Revoke(ctx, alice, "not-a-uuid") },
	} {
		if err := call(); !errors.Is(err, apitoken.ErrNotFound) {
			t.Fatalf("%s: err=%v", name, err)
		}
	}
	// 传入当前凭据即登出当前设备。
	if err := s.Revoke(ctx, ciSub, ciSub.Credential); err != nil {
		t.Fatal(err)
	}
	if err := s.Revoke(ctx, ciSub, ciSub.Credential); !errors.Is(err, apitoken.ErrNotFound) {
		t.Fatalf("revoke twice: %v", err)
	}
	state(map[string]bool{"bob": true})

	laptop = login(t, s, alice, apitoken.Device{})
	ci = create(t, s, alice, apitoken.Personal{Name: "CI", Scopes: read})
	if n, err := s.RevokeSubject(ctx, alice.Ref()); n != 2 || err != nil {
		t.Fatalf("revoke subject n=%d err=%v", n, err)
	}
	state(map[string]bool{"bob": true})
	if _, err := s.RevokeSubject(ctx, identity.Ref{}); err == nil {
		t.Fatal("expected error for empty subject")
	}
	if _, err := s.RevokeOthers(ctx, identity.Subject{}); err == nil {
		t.Fatal("expected error for empty subject")
	}
}

func TestCleanup(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		s := newStore(t)
		for range 3 {
			login(t, s, alice, apitoken.Device{})
		}
		live := login(t, s, alice, apitoken.Device{Remember: true})
		forever := create(t, s, alice, apitoken.Personal{Name: "CI", Scopes: read})
		time.Sleep(2 * time.Hour)

		if _, err := s.Cleanup(ctx, 0); err == nil {
			t.Fatal("expected error for non-positive limit")
		}
		if n, err := s.Cleanup(ctx, 2); n != 2 || err != nil {
			t.Fatalf("first cleanup n=%d err=%v", n, err)
		}
		if n, err := s.Cleanup(ctx, 10); n != 1 || err != nil {
			t.Fatalf("second cleanup n=%d err=%v", n, err)
		}
		for _, token := range []string{live, forever} {
			if _, ok := valid(t, s, token); !ok {
				t.Fatal("live token removed")
			}
		}
	})
}

func TestTenantBinding(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)
	customer := identity.Subject{ID: "carol", Type: "user", Tenant: "acme"}
	loginToken := login(t, s, customer, apitoken.Device{})
	personal := create(t, s, alice, apitoken.Personal{Name: "acme 报表", Scopes: read, Tenant: "globex"})
	if sub, ok := valid(t, s, loginToken); !ok || sub.Tenant != "acme" {
		t.Fatalf("login token tenant=%q ok=%v", sub.Tenant, ok)
	}
	sub, ok := valid(t, s, personal)
	if !ok || sub.Tenant != "globex" {
		t.Fatalf("personal token tenant=%q ok=%v", sub.Tenant, ok)
	}
	if list, err := s.List(ctx, sub); err != nil || len(list) != 1 || list[0].Tenant != "globex" {
		t.Fatalf("list=%+v err=%v", list, err)
	}
}

// 不同账号体系中 ID 相同的主体互不影响：列表、撤销都按类型和 ID 一起区分。
func TestAccountSystems(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)
	staffAlice := identity.Subject{ID: "alice", Type: "staff"}
	userToken := login(t, s, alice, apitoken.Device{})
	staffToken := login(t, s, staffAlice, apitoken.Device{})
	if sub, ok := valid(t, s, staffToken); !ok || sub.Ref() != staffAlice.Ref() {
		t.Fatalf("staff token sub=%+v ok=%v", sub, ok)
	}
	if list, err := s.List(ctx, staffAlice); err != nil || len(list) != 1 {
		t.Fatalf("staff list=%+v err=%v", list, err)
	}
	if n, err := s.RevokeOthers(ctx, staffAlice); n != 1 || err != nil {
		t.Fatalf("revoke others n=%d err=%v", n, err)
	}
	if _, ok := valid(t, s, userToken); !ok {
		t.Fatal("user token revoked by staff RevokeOthers")
	}
	if n, err := s.RevokeSubject(ctx, alice.Ref()); n != 1 || err != nil {
		t.Fatalf("revoke subject n=%d err=%v", n, err)
	}
	if _, err := s.Login(ctx, identity.Subject{ID: "alice", Type: "Bad Type"}, apitoken.Device{}); err == nil {
		t.Fatal("expected error for invalid subject type")
	}
}

func TestImpersonation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)
	staff := identity.Ref{Type: "staff", ID: "s1"}
	asAlice := identity.Subject{ID: "alice", Type: "user", Impersonator: staff}
	own := login(t, s, alice, apitoken.Device{})
	impersonating := login(t, s, asAlice, apitoken.Device{})
	personal := create(t, s, asAlice, apitoken.Personal{Name: "排查", Scopes: read})
	if sub, ok := valid(t, s, own); !ok || !sub.Impersonator.IsZero() {
		t.Fatalf("own token impersonator=%v ok=%v", sub.Impersonator, ok)
	}
	for _, token := range []string{impersonating, personal} {
		if sub, ok := valid(t, s, token); !ok || sub.ID != "alice" || sub.Impersonator != staff {
			t.Fatalf("impersonation token sub=%+v ok=%v", sub, ok)
		}
	}
	list, err := s.List(ctx, alice)
	if err != nil || len(list) != 3 {
		t.Fatalf("list=%+v err=%v", list, err)
	}
	impersonators := map[identity.Ref]int{}
	for _, tok := range list {
		impersonators[tok.Impersonator]++
	}
	if impersonators[identity.Ref{}] != 1 || impersonators[staff] != 2 {
		t.Fatalf("impersonators=%v", impersonators)
	}
}

// TestRequireBearer 确认 Validate 可以直接作为 rest.RequireBearer 的校验器：
// 权限由 Resolve 按用户加载，个人访问令牌的 scope 再加以限制。
func TestRequireBearer(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	permissions := map[string][]string{"alice": {"articles:*"}}
	api := rest.New(rest.Options{Accounts: identity.Accounts{"user": func(_ context.Context, sub identity.Subject) (identity.Subject, error) {
		sub.Permissions = permissions[sub.ID]
		return sub, nil
	}}})
	op := huma.Operation{OperationID: "me", Method: http.MethodGet, Path: "/me"}
	rest.RequireBearer(api, s.Validate, "articles:read")(&op)
	type meOut struct {
		Body struct {
			ID         string `json:"id"`
			Credential string `json:"credential"`
		}
	}
	rest.Register(api, op, func(ctx context.Context, _ *struct{}) (*meOut, error) {
		sub, _ := identity.FromContext(ctx)
		out := &meOut{}
		out.Body.ID, out.Body.Credential = sub.ID, sub.Credential
		return out, nil
	})
	call := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/me", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		api.Handler().ServeHTTP(rr, req)
		return rr
	}
	for name, tc := range map[string]struct {
		token  string
		status int
	}{
		"bogus token":             {"bogus", http.StatusUnauthorized},
		"no permission":           {login(t, s, bob, apitoken.Device{}), http.StatusForbidden},
		"login token":             {login(t, s, alice, apitoken.Device{}), http.StatusOK},
		"personal within scope":   {create(t, s, alice, apitoken.Personal{Name: "读", Scopes: read}), http.StatusOK},
		"personal outside scope":  {create(t, s, alice, apitoken.Personal{Name: "评论", Scopes: []string{"comments:read"}}), http.StatusForbidden},
		"scope beyond permission": {create(t, s, bob, apitoken.Personal{Name: "越权", Scopes: []string{"*"}}), http.StatusForbidden},
	} {
		if rr := call(tc.token); rr.Code != tc.status {
			t.Errorf("%s: %d %s", name, rr.Code, rr.Body)
		}
	}
}
