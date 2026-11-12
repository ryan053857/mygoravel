package web_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/alexedwards/scs/v2/memstore"
	"github.com/go-chi/chi/v5"

	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/web"
)

// 会话中的主体不属于 Options.Accounts 时按未登录处理：例如员工后台不接受用户的会话。
func TestAccountsRejectOtherTypes(t *testing.T) {
	srv := authServer(t, memstore.New(), func(o *web.Options) { o.Accounts = identity.Accounts{"staff": nil} })
	c := noFollow(t)
	resp, err := c.PostForm(srv.URL+"/login", map[string][]string{"user": {"alice"}}) // 登录为 user:alice
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	resp, _ = get(t, c, srv.URL+"/me")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/login") {
		t.Fatalf("other account type: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestAccountsRequired(t *testing.T) {
	o := options()
	o.Accounts = nil
	pages := web.New(o)
	pages.Routes(chi.NewRouter(), web.Public) // 公开路由不需要 Accounts
	func() {
		defer func() {
			if recover() == nil {
				t.Error("web.Require without Accounts: expected panic")
			}
		}()
		pages.Routes(chi.NewRouter(), web.Require())
	}()
	func() {
		defer func() {
			if recover() == nil {
				t.Error("invalid account type: expected panic")
			}
		}()
		o.Accounts = identity.Accounts{"Staff": nil}
		web.New(o)
	}()
	// 只登记路由时不需要 Accounts。
	web.New(web.Options{Mode: web.ModeRoutes}).Routes(chi.NewRouter(), web.Require())
}
