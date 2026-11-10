// Package app 是 PoC 的演示应用：登录、文章表单、聊天室和流式任务。
package app

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/a-h/templ"
	"github.com/alexedwards/scs/v2"
	"github.com/alexedwards/scs/v2/memstore"
	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/httpx/rest"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/poc/htmx4/app/views"
	"github.com/ryan053857/mygoravel/poc/htmx4/web"
	"github.com/ryan053857/mygoravel/session"
)

var ErrNotFound = errorx.Define(errorx.NotFound, "article.not_found")

// Options 控制演示应用中需要在测试里调整的部分。
type Options struct {
	HTTP   httpx.Config
	Logger *slog.Logger
	// StreamsUseTimeout 为 true 时，流式路由也挂在 httpx.Timeout 之后（复现问题）。
	StreamsUseTimeout bool
}

type App struct {
	Handler  http.Handler
	Drain    *web.Drain
	Hub      *Hub
	Sessions *scs.SessionManager

	mu       sync.Mutex
	articles map[string]views.Article
}

func New(opts Options) *App {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	a := &App{
		Drain: web.NewDrain(),
		Hub:   NewHub(),
		articles: map[string]views.Article{
			"1": {ID: "1", Title: "Hello htmx", Slug: "hello-htmx", Priority: 1},
		},
	}
	var sc session.Config
	sc.SetDefaults()
	sc.Cookie.Secure = false
	a.Sessions = session.New(sc, memstore.New(), logger)

	errs := web.Errors{
		Page: func(status int, err error) templ.Component { return views.ErrorPage(status, http.StatusText(status)) },
		Fragment: func(status int, err error) templ.Component {
			return views.ErrorFragment(status, http.StatusText(status))
		},
		Target: "#flash",
		Logger: logger,
	}

	cfg := opts.HTTP
	r := chi.NewRouter()
	// Defaults 去掉 Timeout：超时按路由组安装，流式路由不装。
	r.Use(
		httpx.AssignRequestID(cfg.RequestID), httpx.NoSniff(), httpx.ClientIP(cfg.ClientIP),
		httpx.Locale(nil), httpx.AccessLog(cfg.AccessLog, logger), httpx.Recover(logger), httpx.BodyLimit(cfg.BodyLimit),
	)
	r.Use(http.NewCrossOriginProtection().Handler)

	// JSON API 挂在 /api 子路由，rest.New 的 JSON 404 只作用于子路由。
	r.Route("/api", func(sub chi.Router) {
		sub.Use(httpx.Timeout(cfg.Timeout))
		api := rest.New(rest.Options{Router: sub, Logger: logger})
		op := huma.Operation{OperationID: "ping", Method: http.MethodGet, Path: "/ping"}
		rest.Public()(&op)
		rest.Register(api, op, func() func(context.Context, *struct{}) (*PingOutput, error) {
			return func(context.Context, *struct{}) (*PingOutput, error) {
				out := &PingOutput{}
				out.Body.Pong = true
				return out, nil
			}
		})
	})

	r.Handle(web.AssetsPrefix+"*", web.Assets())
	r.NotFound(errs.Handle(func(http.ResponseWriter, *http.Request) error { return ErrNotFound.New() }).ServeHTTP)

	r.Group(func(pages chi.Router) {
		pages.Use(a.Sessions.LoadAndSave)
		requireUser := web.RequirePage(a.Sessions, "/login", errs)

		pages.Group(func(p chi.Router) {
			p.Use(httpx.Timeout(cfg.Timeout))
			p.Method(http.MethodGet, "/login", errs.Handle(a.loginPage))
			p.Method(http.MethodPost, "/login", errs.Handle(a.login))
			p.Method(http.MethodPost, "/logout", errs.Handle(a.logout))
			p.Group(func(p chi.Router) {
				p.Use(requireUser)
				p.Method(http.MethodGet, "/", errs.Handle(a.home))
				p.Method(http.MethodGet, "/admin/articles", errs.Handle(a.listArticles))
				p.Method(http.MethodGet, "/admin/articles/{id}/edit", errs.Handle(a.editArticle))
				p.Method(http.MethodPut, "/admin/articles/{id}", errs.Handle(a.updateArticle))
				p.Method(http.MethodGet, "/admin/only", web.RequirePage(a.Sessions, "/login", errs, "admin")(errs.Handle(a.home)))
				p.Method(http.MethodGet, "/rooms/{room}", errs.Handle(a.room))
				p.Method(http.MethodPost, "/rooms/{room}/messages", errs.Handle(a.postMessage))
				p.Method(http.MethodPost, "/rooms/{room}/typing", errs.Handle(a.typing))
				p.Method(http.MethodGet, "/tasks", errs.Handle(a.tasksPage))
				// 不区分 htmx 的普通 303，观察 fetch 跟随跳转后的行为。
				p.Post("/redirect-test", func(w http.ResponseWriter, r *http.Request) {
					http.Redirect(w, r, "/admin/articles", http.StatusSeeOther)
				})
			})
		})

		// 流式路由：不装 Timeout。
		pages.Group(func(p chi.Router) {
			if opts.StreamsUseTimeout {
				p.Use(httpx.Timeout(cfg.Timeout))
			}
			p.Use(requireUser)
			p.Method(http.MethodGet, "/rooms/{room}/events", errs.Handle(a.events))
			p.Method(http.MethodPost, "/tasks", errs.Handle(a.runTask))
		})
	})
	a.Handler = r
	return a
}

type PingOutput struct {
	Body struct {
		Pong bool `json:"pong"`
	}
}

func (a *App) loginPage(w http.ResponseWriter, r *http.Request) error {
	return web.Render(w, r, http.StatusOK, views.Login(r.URL.Query().Get("next"), "", false))
}

func (a *App) login(w http.ResponseWriter, r *http.Request) error {
	email, password := r.PostFormValue("email"), r.PostFormValue("password")
	if password != "secret" || email == "" {
		return web.Render(w, r, http.StatusUnprocessableEntity, views.Login(r.URL.Query().Get("next"), email, true))
	}
	var scopes []string
	if email == "admin@example.com" {
		scopes = []string{"admin"}
	}
	if err := session.Login(r.Context(), a.Sessions, identity.Subject{ID: email, Type: identity.SubjectUser, Scopes: scopes}); err != nil {
		return err
	}
	next := r.URL.Query().Get("next")
	if next == "" || next[0] != '/' || (len(next) > 1 && next[1] == '/') {
		next = "/"
	}
	web.Redirect(w, r, next)
	return nil
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) error {
	if err := session.Logout(r.Context(), a.Sessions); err != nil {
		return err
	}
	web.Redirect(w, r, "/login")
	return nil
}

func (a *App) home(w http.ResponseWriter, r *http.Request) error {
	sub, _ := identity.FromContext(r.Context())
	return web.Render(w, r, http.StatusOK, views.Home(sub.ID))
}

func (a *App) listArticles(w http.ResponseWriter, r *http.Request) error {
	a.mu.Lock()
	items := make([]views.Article, 0, len(a.articles))
	for i := 1; i <= len(a.articles); i++ {
		items = append(items, a.articles[strconv.Itoa(i)])
	}
	a.mu.Unlock()
	return web.Render(w, r, http.StatusOK, views.ArticleList(items))
}

func (a *App) article(id string) (views.Article, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	art, ok := a.articles[id]
	if !ok {
		return art, ErrNotFound.New()
	}
	return art, nil
}

func (a *App) editArticle(w http.ResponseWriter, r *http.Request) error {
	art, err := a.article(chi.URLParam(r, "id"))
	if err != nil {
		return err
	}
	in := views.ArticleForm{Title: art.Title, Slug: art.Slug, Priority: art.Priority, Published: art.Published}
	return web.Render(w, r, http.StatusOK, views.ArticleEdit(art.ID, in, nil))
}

func (a *App) updateArticle(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if _, err := a.article(id); err != nil {
		return err
	}
	in, errs, err := web.Form[views.ArticleForm](r)
	if err != nil {
		return err
	}
	if errs != nil {
		return web.Render(w, r, http.StatusUnprocessableEntity, views.ArticleFormView(id, in, errs))
	}
	a.mu.Lock()
	a.articles[id] = views.Article{ID: id, Title: in.Title, Slug: in.Slug, Priority: in.Priority, Published: in.Published}
	a.mu.Unlock()
	web.Redirect(w, r, "/admin/articles")
	return nil
}

func (a *App) tasksPage(w http.ResponseWriter, r *http.Request) error {
	timeout, _ := strconv.Atoi(r.URL.Query().Get("timeout"))
	return web.Render(w, r, http.StatusOK, views.Tasks(timeout))
}

// runTask 用流式响应回应一个普通的 hx-post：每 700ms 推一步。
func (a *App) runTask(w http.ResponseWriter, r *http.Request) error {
	s, err := web.NewStream(w, r)
	if err != nil {
		return err
	}
	for i := 1; i <= 3; i++ {
		select {
		case <-r.Context().Done():
			return nil
		case <-time.After(700 * time.Millisecond):
		}
		if err := s.Send(views.Step("第 "+strconv.Itoa(i)+" 步"), web.Event{}); err != nil {
			return nil
		}
	}
	return nil
}
