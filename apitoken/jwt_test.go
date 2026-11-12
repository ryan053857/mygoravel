package apitoken_test

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/ryan053857/mygoravel/apitoken"
	"github.com/ryan053857/mygoravel/crypt"
	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/database/sqlite"
	"github.com/ryan053857/mygoravel/httpx/rest"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/internal/dbtesting"
)

var appKey = crypt.GenerateKey()

func hmacKeys(key string, previous ...string) apitoken.Keys {
	return apitoken.HMACKeys(crypt.New(crypt.Config{Key: key, PreviousKeys: previous}))
}

// newJWT 用 testConfig 的 Store 和 appKey 派生的 HMAC 密钥创建 JWT。
func newJWT(t *testing.T, driver database.Driver, cfg apitoken.JWTConfig) (*apitoken.JWT, *apitoken.Store) {
	t.Helper()
	s := newStore(t, driver)
	cfg.Keys = hmacKeys(appKey)
	return apitoken.NewJWT(s, cfg), s
}

func jwtLogin(t *testing.T, j *apitoken.JWT, sub identity.Subject, d apitoken.Device) apitoken.Pair {
	t.Helper()
	p, err := j.Login(context.Background(), sub, d)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func access(t *testing.T, j *apitoken.JWT, token string) (identity.Subject, bool) {
	t.Helper()
	sub, err := j.Validate(context.Background(), token)
	if errors.Is(err, apitoken.ErrInvalid) {
		return sub, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return sub, true
}

func TestJWTLoginAndValidate(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		synctest.Test(t, func(t *testing.T) {
			ctx := context.Background()
			j, s := newJWT(t, driver, apitoken.JWTConfig{})
			staff := identity.Ref{Type: "staff", ID: "s1"}
			sub := identity.Subject{Type: "customer", ID: "c:42", Tenant: "acme", Impersonator: staff}
			p := jwtLogin(t, j, sub, apitoken.Device{IP: "203.0.113.7"})
			if p.Access == "" || p.Refresh == "" || !p.ExpiresAt.Equal(time.Now().Add(10*time.Minute)) {
				t.Fatalf("pair=%+v", p)
			}

			got, ok := access(t, j, p.Access)
			if !ok || got.Type != "customer" || got.ID != "c:42" || got.Tenant != "acme" || got.Impersonator != staff ||
				got.Method != "bearer" || got.Credential == "" || got.Scopes != nil || got.Permissions != nil {
				t.Fatalf("subject=%+v ok=%v", got, ok)
			}
			list, err := s.List(ctx, got)
			if err != nil || len(list) != 1 || list[0].ID != got.Credential || !list[0].Current ||
				list[0].Kind != apitoken.KindRefresh || list[0].IP != "203.0.113.7" {
				t.Fatalf("list=%+v err=%v", list, err)
			}

			// refresh token 不能作为 Bearer 凭据；不是 JWT 的 token 交给 Store.Validate，同一个 API 也接受个人访问令牌。
			if _, ok := valid(t, s, p.Refresh); ok {
				t.Fatal("refresh token accepted as an opaque bearer token")
			}
			opaque := login(t, s, alice, apitoken.Device{})
			personal := create(t, s, alice, apitoken.Personal{Name: "CI", Scopes: read})
			for token, want := range map[string]identity.Subject{opaque: {Type: "user", ID: "alice"}, personal: {Type: "user", ID: "alice", Scopes: read}} {
				if got, ok := access(t, j, token); !ok || got.Ref() != want.Ref() || len(got.Scopes) != len(want.Scopes) {
					t.Fatalf("opaque token: %+v ok=%v", got, ok)
				}
			}
			for _, bad := range []string{"", "bogus", "a.b.c", p.Refresh, p.Access + "x", p.Access[:len(p.Access)-2]} {
				if _, ok := access(t, j, bad); ok {
					t.Fatalf("token %q accepted as access token", bad)
				}
			}
			if _, err := j.Refresh(ctx, opaque); !errors.Is(err, apitoken.ErrInvalid) {
				t.Fatalf("opaque token refreshed: %v", err)
			}

			// access token 过期后失效（允许 5 秒时钟偏差）。
			time.Sleep(10*time.Minute + 6*time.Second)
			if _, ok := access(t, j, p.Access); ok {
				t.Fatal("expired access token accepted")
			}
		})
	})
}

func TestJWTRefresh(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		synctest.Test(t, func(t *testing.T) {
			ctx := context.Background()
			var reused []apitoken.Token
			j, s := newJWT(t, driver, apitoken.JWTConfig{
				OnReuse: func(_ context.Context, tok apitoken.Token) { reused = append(reused, tok) },
			})
			first := jwtLogin(t, j, alice, apitoken.Device{})
			sub, _ := access(t, j, first.Access)

			// 一直在用：每 50 分钟刷新一次，越过最初的空闲截止时间仍然有效，刷新时轮换 refresh token。
			cur := first
			for range 3 {
				time.Sleep(50 * time.Minute)
				next, err := j.Refresh(ctx, cur.Refresh)
				if err != nil || next.Refresh == "" || next.Refresh == cur.Refresh {
					t.Fatalf("refresh: %+v %v", next, err)
				}
				got, ok := access(t, j, next.Access)
				if !ok || got.Credential != sub.Credential {
					t.Fatalf("refreshed subject=%+v", got)
				}
				cur = next
			}
			list, _ := s.List(ctx, sub)
			if len(list) != 1 || !list[0].LastUsedAt.Equal(time.Now()) || !list[0].ExpiresAt.Equal(time.Now().Add(time.Hour)) {
				t.Fatalf("list=%+v", list)
			}

			// 宽限期内重复使用上一个 token：只得到新的 access token。
			time.Sleep(20 * time.Second)
			prev := cur
			cur, _ = j.Refresh(ctx, prev.Refresh)
			again, err := j.Refresh(ctx, prev.Refresh)
			if err != nil || again.Refresh != "" || again.Access == "" {
				t.Fatalf("grace refresh: %+v %v", again, err)
			}
			// 超过宽限期再用上一个 token：视为被盗，撤销整个登录，当前的 refresh token 也失效。
			time.Sleep(31 * time.Second)
			if _, err := j.Refresh(ctx, prev.Refresh); !errors.Is(err, apitoken.ErrInvalid) {
				t.Fatalf("reused token: %v", err)
			}
			if len(reused) != 1 || reused[0].ID != sub.Credential {
				t.Fatalf("OnReuse=%+v", reused)
			}
			if _, err := j.Refresh(ctx, cur.Refresh); !errors.Is(err, apitoken.ErrInvalid) {
				t.Fatalf("login survived reuse: %v", err)
			}
			// 更早的 token 只是无效，不再触发 OnReuse。
			if _, err := j.Refresh(ctx, first.Refresh); !errors.Is(err, apitoken.ErrInvalid) || len(reused) != 1 {
				t.Fatalf("old token: %v reused=%d", err, len(reused))
			}

			// 空闲超时后 refresh token 失效。
			idle := jwtLogin(t, j, alice, apitoken.Device{})
			time.Sleep(time.Hour)
			if _, err := j.Refresh(ctx, idle.Refresh); !errors.Is(err, apitoken.ErrInvalid) {
				t.Fatalf("idle refresh: %v", err)
			}
		})
	})
}

// access token 的过期时间不晚于登录本身。
func TestJWTAccessCappedByLogin(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		short := apitoken.NewStore(dbtesting.Open(t, sqlite.Driver, apitoken.Migrations()), apitoken.Config{Lifetime: 15 * time.Minute})
		j := apitoken.NewJWT(short, apitoken.JWTConfig{Keys: hmacKeys(appKey)})
		p := jwtLogin(t, j, alice, apitoken.Device{})
		time.Sleep(10 * time.Minute)
		next, err := j.Refresh(context.Background(), p.Refresh)
		if err != nil || !next.ExpiresAt.Equal(time.Now().Add(5*time.Minute)) {
			t.Fatalf("pair=%+v err=%v", next, err)
		}
	})
}

// 并发刷新同一个 refresh token：只有一个得到新的 refresh token，其余在宽限期内只得到 access token。
func TestJWTConcurrentRefresh(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		j, _ := newJWT(t, driver, apitoken.JWTConfig{})
		p := jwtLogin(t, j, alice, apitoken.Device{})
		const n = 8
		var (
			wg      sync.WaitGroup
			mu      sync.Mutex
			rotated int
			errs    []error
		)
		for range n {
			wg.Go(func() {
				next, err := j.Refresh(context.Background(), p.Refresh)
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err != nil:
					errs = append(errs, err)
				case next.Access == "":
					errs = append(errs, errors.New("no access token"))
				case next.Refresh != "":
					rotated++
				}
			})
		}
		wg.Wait()
		if rotated != 1 || len(errs) != 0 {
			t.Fatalf("rotated=%d errs=%v", rotated, errs)
		}
	})
}

func TestJWTRevoke(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		ctx := context.Background()
		j, s := newJWT(t, driver, apitoken.JWTConfig{})
		a := jwtLogin(t, j, alice, apitoken.Device{})
		b := jwtLogin(t, j, alice, apitoken.Device{})
		opaque := login(t, s, alice, apitoken.Device{})
		sub, _ := access(t, j, a.Access)

		// 退出其他设备：JWT 模式的登录和不透明 token 一起撤销。
		if n, err := s.RevokeOthers(ctx, sub); err != nil || n != 2 {
			t.Fatalf("revoke others: %d %v", n, err)
		}
		if _, err := j.Refresh(ctx, b.Refresh); !errors.Is(err, apitoken.ErrInvalid) {
			t.Fatalf("other device refreshed: %v", err)
		}
		if _, ok := valid(t, s, opaque); ok {
			t.Fatal("opaque token survived")
		}
		// 登出当前设备：refresh token 立即失效，已签发的 access token 在有效期内仍可用。
		if err := s.Revoke(ctx, sub, sub.Credential); err != nil {
			t.Fatal(err)
		}
		if _, err := j.Refresh(ctx, a.Refresh); !errors.Is(err, apitoken.ErrInvalid) {
			t.Fatalf("revoked refresh: %v", err)
		}
		if _, ok := access(t, j, a.Access); !ok {
			t.Fatal("access token is expected to stay valid until it expires")
		}
	})
}

func TestJWTKeys(t *testing.T) {
	oldKey, newKey := crypt.GenerateKey(), crypt.GenerateKey()
	s := newStore(t, sqlite.Driver)
	old := apitoken.NewJWT(s, apitoken.JWTConfig{Keys: hmacKeys(oldKey)})
	token := jwtLogin(t, old, alice, apitoken.Device{}).Access

	// 轮换应用密钥后，旧密钥签发的 access token 仍然有效；不认识的密钥签发的无效。
	rotated := apitoken.NewJWT(s, apitoken.JWTConfig{Keys: hmacKeys(newKey, oldKey)})
	if _, ok := access(t, rotated, token); !ok {
		t.Fatal("token signed with the previous key rejected")
	}
	other := apitoken.NewJWT(s, apitoken.JWTConfig{Keys: hmacKeys(newKey)})
	if _, ok := access(t, other, token); ok {
		t.Fatal("token signed with an unknown key accepted")
	}

	// Issuer、Audience 不一致的无效。
	scoped := apitoken.NewJWT(s, apitoken.JWTConfig{Keys: hmacKeys(oldKey), Issuer: "shop", Audience: "app"})
	if _, ok := access(t, scoped, token); ok {
		t.Fatal("token without issuer and audience accepted")
	}
	if _, ok := access(t, scoped, jwtLogin(t, scoped, alice, apitoken.Device{}).Access); !ok {
		t.Fatal("scoped token rejected")
	}

	// Ed25519：用 JWKS 公开的公钥即可在其他服务中校验。
	pub, priv, _ := ed25519.GenerateKey(nil)
	_, oldPriv, _ := ed25519.GenerateKey(nil)
	ed := apitoken.NewJWT(s, apitoken.JWTConfig{Keys: apitoken.Ed25519Keys(priv, oldPriv.Public().(ed25519.PublicKey))})
	edToken := jwtLogin(t, ed, alice, apitoken.Device{}).Access
	if _, ok := access(t, ed, edToken); !ok {
		t.Fatal("Ed25519 token rejected")
	}
	if _, ok := access(t, old, edToken); ok {
		t.Fatal("Ed25519 token accepted by HMAC keys")
	}
	rr := httptest.NewRecorder()
	ed.JWKS().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil))
	var set jose.JSONWebKeySet
	if err := json.Unmarshal(rr.Body.Bytes(), &set); err != nil || len(set.Keys) != 2 || rr.Header().Get("Content-Type") != "application/jwk-set+json" {
		t.Fatalf("jwks=%s err=%v", rr.Body, err)
	}
	parsed, err := jwt.ParseSigned(edToken, []jose.SignatureAlgorithm{jose.EdDSA})
	if err != nil {
		t.Fatal(err)
	}
	keys := set.Key(parsed.Headers[0].KeyID)
	var claims jwt.Claims
	if len(keys) != 1 || !keys[0].Key.(ed25519.PublicKey).Equal(pub) || parsed.Claims(keys[0].Key, &claims) != nil || claims.Subject != "user:alice" {
		t.Fatalf("keys=%+v claims=%+v", keys, claims)
	}
	rr = httptest.NewRecorder()
	old.JWKS().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if strings.TrimSpace(rr.Body.String()) != `{"keys":[]}` {
		t.Fatalf("HMAC jwks=%s", rr.Body)
	}
}

func TestNewJWTPanics(t *testing.T) {
	s := newStore(t, sqlite.Driver)
	keys := hmacKeys(appKey)
	for name, build := range map[string]func(){
		"nil store":    func() { apitoken.NewJWT(nil, apitoken.JWTConfig{Keys: keys}) },
		"no keys":      func() { apitoken.NewJWT(s, apitoken.JWTConfig{}) },
		"negative ttl": func() { apitoken.NewJWT(s, apitoken.JWTConfig{Keys: keys, TTL: -time.Second}) },
		// testConfig 空闲 1 小时，不能短于 access token 的有效期。
		"ttl beyond idle": func() { apitoken.NewJWT(s, apitoken.JWTConfig{Keys: keys, TTL: time.Hour}) },
		"bad ed25519":     func() { apitoken.Ed25519Keys(ed25519.PrivateKey("short")) },
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

// TestJWTRequireBearer 确认 JWT.Validate 可以直接作为 rest.RequireBearer 的校验器，权限仍由 Resolver 加载。
func TestJWTRequireBearer(t *testing.T) {
	j, _ := newJWT(t, sqlite.Driver, apitoken.JWTConfig{})
	api := rest.New(rest.Options{Accounts: identity.Accounts{"user": func(_ context.Context, sub identity.Subject) (identity.Subject, error) {
		if sub.ID == "alice" {
			sub.Permissions = []string{"articles:read"}
		}
		return sub, nil
	}}})
	op := huma.Operation{OperationID: "me", Method: http.MethodGet, Path: "/me"}
	rest.RequireBearer(api, j.Validate, "articles:read")(&op)
	rest.Register(api, op, func(context.Context, *struct{}) (*struct{}, error) { return nil, nil })
	for token, status := range map[string]int{
		"bogus": http.StatusUnauthorized,
		jwtLogin(t, j, bob, apitoken.Device{}).Access:   http.StatusForbidden,
		jwtLogin(t, j, alice, apitoken.Device{}).Access: http.StatusNoContent,
	} {
		req := httptest.NewRequest(http.MethodGet, "/me", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		api.Handler().ServeHTTP(rr, req)
		if rr.Code != status {
			t.Errorf("%.20s: %d %s", token, rr.Code, rr.Body)
		}
	}
}
