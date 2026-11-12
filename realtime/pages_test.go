package realtime_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/alexedwards/scs/v2/memstore"
	"github.com/go-chi/chi/v5"

	"github.com/ryan053857/mygoravel/broadcast"
	"github.com/ryan053857/mygoravel/broadcast/broadcasttest"
	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/realtime"
	"github.com/ryan053857/mygoravel/session"
	"github.com/ryan053857/mygoravel/web"
)

// 同源页面的连接放在页面路由之后：经过框架的基础中间件、请求超时和会话，使用登录的主体；
// 会话被撤销并发出提醒后连接立即断开。
func TestBehindPageRoutes(t *testing.T) {
	t.Parallel()
	hub := broadcast.New(broadcasttest.NewBackend(), nil)
	defer hub.Close()
	creds := broadcast.NewCredentials(hub)
	rt, err := realtime.New(hub, realtime.Options{Credentials: creds})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Shutdown(context.Background())
	rt.Channel("inbox:{user}", realtime.Private(func(_ context.Context, sub identity.Subject, p realtime.Params) (bool, error) {
		return p["user"] == sub.ID, nil
	}))

	var cfg session.Config
	cfg.SetDefaults()
	cfg.Cookie.Secure = false
	store := memstore.New()
	text := func(format string, args ...any) templ.Component {
		return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
			_, err := fmt.Fprintf(w, format, args...)
			return err
		})
	}
	pages := web.New(web.Options{Accounts: identity.Accounts{"user": nil},
		Sessions:      session.New(cfg, store),
		Login:         "/login",
		ErrorPage:     func(status int, msg string) templ.Component { return text("page %d %s", status, msg) },
		ErrorFragment: func(status int, msg string) templ.Component { return text("fragment %d %s", status, msg) },
		ErrorTarget:   "#flash",
		Credentials:   creds,
	})
	router := chi.NewRouter()
	router.Use(httpx.Defaults(httpx.DefaultConfig(), nil, nil)...)
	router.Use(httpx.Timeout(httpx.TimeoutConfig{Duration: 100 * time.Millisecond}))
	pages.Routes(router, web.Public).Post("/login", func(w http.ResponseWriter, r *http.Request) error {
		return pages.Login(r, identity.Subject{ID: "alice", Type: "user"})
	})
	pages.Routes(router, web.Require()).Get("/realtime", func(w http.ResponseWriter, r *http.Request) error {
		rt.Handler().ServeHTTP(w, r)
		return nil
	})
	srv := httptest.NewServer(router)
	defer srv.Close()

	// 没有登录时页面路由把请求重定向到登录页，连接建立不起来。
	if _, _, err := dial(t, srv.URL+"/realtime", nil); err == nil {
		t.Fatal("connected without a session")
	}

	jar, _ := cookiejar.New(nil)
	if resp, err := (&http.Client{Jar: jar}).Post(srv.URL+"/login", "", nil); err != nil {
		t.Fatal(err)
	} else {
		resp.Body.Close()
	}
	u, _ := url.Parse(srv.URL)
	cookie := jar.Cookies(u)[0]
	c := mustConnect(t, srv.URL+"/realtime", "", http.Header{"Cookie": {cookie.String()}, "Origin": {srv.URL}})
	time.Sleep(250 * time.Millisecond) // 超过请求超时，连接仍然可用
	c.mustSubscribe("inbox:alice")
	if reply := c.subscribe("inbox:bob"); reply.Error == nil || reply.Error.Code != 103 {
		t.Fatalf("subscribe to another user's inbox: %+v", reply.Error)
	}
	if err := hub.PublishJSON(context.Background(), "inbox:alice", map[string]int{"unread": 1}); err != nil {
		t.Fatal(err)
	}
	c.push()

	// 会话仍然有效时的提醒不会断开连接。
	if err := creds.RecheckSubject(context.Background(), identity.Ref{Type: "user", ID: "alice"}); err != nil {
		t.Fatal(err)
	}
	c.noPush(150 * time.Millisecond)

	if err := store.Delete(cookie.Value); err != nil {
		t.Fatal(err)
	}
	if err := creds.RecheckSubject(context.Background(), identity.Ref{Type: "user", ID: "alice"}); err != nil {
		t.Fatal(err)
	}
	if code := c.closed(); code != 3500 {
		t.Fatalf("closed with %d after the session was revoked", code)
	}
}
