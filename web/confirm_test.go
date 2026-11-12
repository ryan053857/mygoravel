package web_test

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"testing"
	"testing/synctest"
	"time"

	"github.com/alexedwards/scs/v2/memstore"
	"github.com/go-chi/chi/v5"

	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/web"
)

// TestConfirmed：Confirmed 的路由要求最近确认过身份，登录本身算一次确认，超过有效期后跳转到确认页，确认后放行。
func TestConfirmed(t *testing.T) {
	// options 在气泡外调用：其中 memstore.New 启动的清理协程不能属于气泡。会话改用不启动清理协程的存储。
	o := options()
	o.Sessions.Store = memstore.NewWithCleanupInterval(0)
	o.Confirm = "/confirm"
	synctest.Test(t, func(t *testing.T) {
		pages := web.New(o)
		router := chi.NewRouter()
		pages.Routes(router, web.Public).Post("/login", func(w http.ResponseWriter, r *http.Request) error {
			return pages.Login(r, identity.Subject{Type: "user", ID: "alice"})
		})
		pages.Routes(router, web.Require()).Post("/confirm", func(w http.ResponseWriter, r *http.Request) error {
			pages.Confirm(r.Context())
			web.Redirect(w, r, web.LocalPath(r.URL.Query().Get("next"), "/"))
			return nil
		})
		pages.Routes(router, web.Require().Confirmed(10*time.Minute)).Get("/danger", func(w http.ResponseWriter, r *http.Request) error {
			_, err := w.Write([]byte("ok"))
			return err
		})

		jar, _ := cookiejar.New(nil)
		base, _ := url.Parse("http://blog.test/")
		do := func(method, target string) *httptest.ResponseRecorder {
			req := httptest.NewRequest(method, "http://blog.test"+target, nil)
			for _, c := range jar.Cookies(base) {
				req.AddCookie(c)
			}
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)
			jar.SetCookies(base, rr.Result().Cookies())
			return rr
		}

		if rr := do("GET", "/danger"); rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/login?next=%2Fdanger" {
			t.Fatalf("anonymous: %d %s", rr.Code, rr.Header().Get("Location"))
		}
		do("POST", "/login")
		if rr := do("GET", "/danger"); rr.Code != http.StatusOK {
			t.Fatalf("right after login: %d", rr.Code)
		}
		time.Sleep(11 * time.Minute)
		if rr := do("GET", "/danger?x=1"); rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/confirm?next=%2Fdanger%3Fx%3D1" {
			t.Fatalf("stale confirmation: %d %s", rr.Code, rr.Header().Get("Location"))
		}
		if rr := do("POST", "/confirm?next=/danger"); rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/danger" {
			t.Fatalf("confirm: %d %s", rr.Code, rr.Header().Get("Location"))
		}
		if rr := do("GET", "/danger"); rr.Code != http.StatusOK {
			t.Fatalf("after confirm: %d", rr.Code)
		}
	})
}

func TestConfirmedPanics(t *testing.T) {
	for name, fn := range map[string]func(){
		"public":       func() { web.Public.Confirmed(time.Minute) },
		"zero max age": func() { web.Require().Confirmed(0) },
		"no confirm page": func() {
			pages().Routes(chi.NewRouter(), web.Require().Confirmed(time.Minute))
		},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: expected panic", name)
				}
			}()
			fn()
		}()
	}
}
