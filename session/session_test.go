package session_test

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/alexedwards/scs/v2/memstore"
	"github.com/go-chi/chi/v5"

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
		"invalid config": func() { session.New(session.Config{}, memstore.New()) },
		"nil store":      func() { session.New(defaults(), nil) },
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
	sm := session.New(cfg, memstore.New())
	if sm.Cookie.Name != "sid" || !sm.Cookie.HttpOnly || !sm.Cookie.Secure || sm.Cookie.SameSite != http.SameSiteStrictMode || sm.Lifetime != 24*time.Hour {
		t.Fatalf("cookie=%+v lifetime=%v", sm.Cookie, sm.Lifetime)
	}
}

// newApp 装配一个最小应用：login 接收 {"user": ...} 登录，me 返回当前主体，
// admin 还需要 admin scope，logout 登出；store 为 *session.Store 时另有 logout-others 撤销其他会话。
func newApp(t *testing.T, store scs.Store) *httptest.Server {
	t.Helper()
	cfg := defaults()
	cfg.Cookie.Secure = false
	sm := session.New(cfg, store)
	router := chi.NewRouter()
	router.Use(sm.LoadAndSave)
	router.Post("/login", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ User string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		sub := identity.Subject{ID: in.User, Type: "user"}
		if in.User == "root" {
			sub.Permissions = []string{"admin"}
		}
		if err := session.Login(r.Context(), sm, sub, session.Device{IP: "10.0.0.1", UserAgent: r.UserAgent()}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	router.Post("/logout", func(w http.ResponseWriter, r *http.Request) {
		if err := session.Logout(r.Context(), sm); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	me := func(scope string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			sub, ok := session.Subject(r.Context(), sm)
			switch {
			case !ok:
				w.WriteHeader(http.StatusUnauthorized)
			case scope != "" && !sub.Can(scope):
				w.WriteHeader(http.StatusForbidden)
			default:
				_ = json.NewEncoder(w).Encode(map[string]string{"id": sub.ID, "method": sub.Method})
			}
		}
	}
	router.Get("/me", me(""))
	router.Get("/admin", me("admin"))
	if st, ok := store.(*session.Store); ok {
		router.Post("/logout-others", func(w http.ResponseWriter, r *http.Request) {
			sub, _ := session.Subject(r.Context(), sm)
			n, err := st.RevokeOthers(r.Context(), sub)
			if err != nil {
				http.Error(w, err.Error(), http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]int{"revoked": n})
		})
		router.Get("/devices", func(w http.ResponseWriter, r *http.Request) {
			sub, _ := session.Subject(r.Context(), sm)
			list, err := st.List(r.Context(), sub)
			if err != nil {
				http.Error(w, err.Error(), http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(list)
		})
		router.Post("/devices/revoke", func(w http.ResponseWriter, r *http.Request) {
			sub, _ := session.Subject(r.Context(), sm)
			if err := st.Revoke(r.Context(), sub, r.URL.Query().Get("id")); err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})
	}
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return srv
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

func TestLoginSubjectLogout(t *testing.T) {
	srv := newApp(t, memstore.New())
	c := newClient(t)

	if resp := do(t, c, http.MethodGet, srv.URL+"/me", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous /me status=%d", resp.StatusCode)
	}

	resp := do(t, c, http.MethodPost, srv.URL+"/login", `{"user":"alice"}`)
	cookie := sessionCookie(resp)
	if resp.StatusCode != http.StatusNoContent || cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("login status=%d cookie=%+v", resp.StatusCode, cookie)
	}

	resp = do(t, c, http.MethodGet, srv.URL+"/me", "")
	var out map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("me status=%d err=%v", resp.StatusCode, err)
	}
	if out["id"] != "alice" || out["method"] != "session" {
		t.Fatalf("subject=%+v", out)
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
	srv := newApp(t, memstore.New())
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
	srv := newApp(t, memstore.New())
	if resp := do(t, newClient(t), http.MethodPost, srv.URL+"/login", `{"user":""}`); resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}
