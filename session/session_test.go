package session_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/alexedwards/scs/v2/memstore"
	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"

	"github.com/ryan053857/mygoravel/httpx/rest"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/session"
)

func defaults() session.Config {
	var cfg session.Config
	cfg.SetDefaults()
	return cfg
}

func TestConfigValidate(t *testing.T) {
	if err := defaults().Validate(); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}
	for name, mutate := range map[string]func(*session.Config){
		"zero lifetime":      func(c *session.Config) { c.Lifetime = 0 },
		"negative idle":      func(c *session.Config) { c.IdleTimeout = -time.Second },
		"empty name":         func(c *session.Config) { c.Cookie.Name = "" },
		"invalid name":       func(c *session.Config) { c.Cookie.Name = "my session" },
		"unknown same_site":  func(c *session.Config) { c.Cookie.SameSite = "Lax" },
		"none without https": func(c *session.Config) { c.Cookie.SameSite = "none"; c.Cookie.Secure = false },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := defaults()
			mutate(&cfg)
			if cfg.Validate() == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestNewPanicsOnInvalidInput(t *testing.T) {
	for name, build := range map[string]func(){
		"invalid config": func() { session.New(session.Config{}, memstore.New(), nil) },
		"nil store":      func() { session.New(defaults(), nil, nil) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected panic")
				}
			}()
			build()
		})
	}
}

func TestNewAppliesCookieConfig(t *testing.T) {
	cfg := defaults()
	cfg.Cookie.Name = "sid"
	cfg.Cookie.SameSite = "strict"
	sm := session.New(cfg, memstore.New(), nil)
	if sm.Cookie.Name != "sid" || !sm.Cookie.HttpOnly || !sm.Cookie.Secure || sm.Cookie.SameSite != http.SameSiteStrictMode || sm.Lifetime != 24*time.Hour {
		t.Fatalf("cookie=%+v lifetime=%v", sm.Cookie, sm.Lifetime)
	}
}

type loginIn struct {
	Body struct {
		User string `json:"user"`
	}
}

type meOut struct {
	Body struct {
		ID     string `json:"id"`
		Method string `json:"method"`
	}
}

// newApp 装配一个最小应用：login 公开，me 需要登录，admin 还需要 admin scope；
// store 为 *session.Store 时另有 logout-others 撤销其他会话。
func newApp(t *testing.T, store scs.Store) (*httptest.Server, *rest.API) {
	t.Helper()
	cfg := defaults()
	cfg.Cookie.Secure = false // httptest 使用 http://127.0.0.1
	sm := session.New(cfg, store, nil)
	router := chi.NewRouter()
	router.Use(sm.LoadAndSave)
	api := rest.New(rest.Options{Router: router})

	login := huma.Operation{OperationID: "login", Method: http.MethodPost, Path: "/login"}
	rest.Public()(&login)
	rest.Register(api, login, func() func(context.Context, *loginIn) (*struct{}, error) {
		return func(ctx context.Context, in *loginIn) (*struct{}, error) {
			sub := identity.Subject{ID: in.Body.User, Type: identity.SubjectUser}
			if in.Body.User == "root" {
				sub.Scopes = []string{"admin"}
			}
			return nil, session.Login(ctx, sm, sub)
		}
	})

	logout := huma.Operation{OperationID: "logout", Method: http.MethodPost, Path: "/logout"}
	session.Require(api, sm)(&logout)
	rest.Register(api, logout, func() func(context.Context, *struct{}) (*struct{}, error) {
		return func(ctx context.Context, _ *struct{}) (*struct{}, error) {
			return nil, session.Logout(ctx, sm)
		}
	})

	me := func(ctx context.Context, _ *struct{}) (*meOut, error) {
		sub, _ := identity.FromContext(ctx)
		out := &meOut{}
		out.Body.ID, out.Body.Method = sub.ID, sub.Method
		return out, nil
	}
	meOp := huma.Operation{OperationID: "me", Method: http.MethodGet, Path: "/me"}
	session.Require(api, sm)(&meOp)
	rest.Register(api, meOp, func() func(context.Context, *struct{}) (*meOut, error) { return me })

	adminOp := huma.Operation{OperationID: "admin", Method: http.MethodGet, Path: "/admin"}
	session.Require(api, sm, "admin")(&adminOp)
	rest.Register(api, adminOp, func() func(context.Context, *struct{}) (*meOut, error) { return me })

	if st, ok := store.(*session.Store); ok {
		others := huma.Operation{OperationID: "logout-others", Method: http.MethodPost, Path: "/logout-others"}
		session.Require(api, sm)(&others)
		rest.Register(api, others, func() func(context.Context, *struct{}) (*revokedOut, error) {
			return func(ctx context.Context, _ *struct{}) (*revokedOut, error) {
				n, err := st.RevokeOthers(ctx, sm)
				out := &revokedOut{}
				out.Body.Revoked = n
				return out, err
			}
		})
	}

	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return srv, api
}

type revokedOut struct {
	Body struct {
		Revoked int `json:"revoked"`
	}
}

func newClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar}
}

func do(t *testing.T, c *http.Client, method, url, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func sessionCookie(resp *http.Response) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == "session" {
			return c
		}
	}
	return nil
}

func TestLoginRequireLogout(t *testing.T) {
	srv, _ := newApp(t, memstore.New())
	c := newClient(t)

	if resp := do(t, c, http.MethodGet, srv.URL+"/me", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous /me status=%d", resp.StatusCode)
	} else if resp.Header.Get("WWW-Authenticate") != "" {
		t.Fatalf("cookie scheme must not send a bearer challenge: %q", resp.Header.Get("WWW-Authenticate"))
	}

	resp := do(t, c, http.MethodPost, srv.URL+"/login", `{"user":"alice"}`)
	cookie := sessionCookie(resp)
	if resp.StatusCode != http.StatusNoContent || cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("login status=%d cookie=%+v", resp.StatusCode, cookie)
	}

	resp = do(t, c, http.MethodGet, srv.URL+"/me", "")
	var out meOut
	if err := json.NewDecoder(resp.Body).Decode(&out.Body); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("me status=%d err=%v", resp.StatusCode, err)
	}
	if out.Body.ID != "alice" || out.Body.Method != "session" {
		t.Fatalf("subject=%+v", out.Body)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control=%q", resp.Header.Get("Cache-Control"))
	}

	if resp := do(t, c, http.MethodGet, srv.URL+"/admin", ""); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("admin without scope status=%d", resp.StatusCode)
	}

	resp = do(t, c, http.MethodPost, srv.URL+"/logout", "")
	if cleared := sessionCookie(resp); resp.StatusCode != http.StatusNoContent || cleared == nil || cleared.MaxAge >= 0 {
		t.Fatalf("logout status=%d cookie=%+v", resp.StatusCode, cleared)
	}
	if resp := do(t, c, http.MethodGet, srv.URL+"/me", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/me after logout status=%d", resp.StatusCode)
	}

	// 登出后旧 token 也失效。
	stale := &http.Client{}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/me", nil)
	req.AddCookie(cookie)
	staleResp, err := stale.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = staleResp.Body.Close()
	if staleResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("stale cookie status=%d", staleResp.StatusCode)
	}
}

func TestLoginRenewsToken(t *testing.T) {
	srv, _ := newApp(t, memstore.New())
	c := newClient(t)
	first := sessionCookie(do(t, c, http.MethodPost, srv.URL+"/login", `{"user":"alice"}`))
	second := sessionCookie(do(t, c, http.MethodPost, srv.URL+"/login", `{"user":"root"}`))
	if first == nil || second == nil || first.Value == second.Value {
		t.Fatalf("token not renewed: %v %v", first, second)
	}
	if resp := do(t, c, http.MethodGet, srv.URL+"/admin", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("admin status=%d", resp.StatusCode)
	}
}

func TestLoginRequiresSubjectID(t *testing.T) {
	srv, _ := newApp(t, memstore.New())
	if resp := do(t, newClient(t), http.MethodPost, srv.URL+"/login", `{"user":""}`); resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

type failingStore struct{ scs.Store }

func (failingStore) Find(string) ([]byte, bool, error) { return nil, false, context.DeadlineExceeded }

func TestStoreFailureReturnsProblem(t *testing.T) {
	srv, _ := newApp(t, failingStore{memstore.New()})
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/me", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: "token"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/problem+json") {
		t.Fatalf("status=%d content-type=%q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
}

func TestOpenAPIAndRoutes(t *testing.T) {
	_, api := newApp(t, memstore.New())
	scheme := api.OpenAPI().Components.SecuritySchemes["session"]
	if scheme == nil || scheme.Type != "apiKey" || scheme.In != "cookie" || scheme.Name != "session" {
		t.Fatalf("scheme=%+v", scheme)
	}
	access := map[string]rest.Route{}
	for _, r := range api.Routes() {
		access[r.Path] = r
	}
	if access["/login"].Access != rest.AccessPublic || access["/me"].Access != "session" {
		t.Fatalf("routes=%+v", access)
	}
	if got := access["/admin"].Scopes; len(got) != 1 || got[0] != "admin" {
		t.Fatalf("admin scopes=%v", got)
	}
}

func TestRequireCannotCombineWithBearer(t *testing.T) {
	_, api := newApp(t, memstore.New())
	sm := session.New(defaults(), memstore.New(), nil)
	op := huma.Operation{OperationID: "x", Method: http.MethodGet, Path: "/x"}
	rest.RequireBearer(api, func(huma.Context, string) (identity.Subject, error) { return identity.Subject{}, nil })(&op)
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	session.Require(api, sm)(&op)
}
