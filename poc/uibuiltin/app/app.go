// Package app 是演示应用：同一个二进制中的运营后台（/ops）和租户端（/app），
// 各自有登录页、会话、主题、提示样式和错误呈现。
package app

import (
	"net/http"

	"github.com/alexedwards/scs/v2/memstore"
	"github.com/go-chi/chi/v5"

	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/poc/uibuiltin/app/article"
	"github.com/ryan053857/mygoravel/poc/uibuiltin/app/views"
	"github.com/ryan053857/mygoravel/poc/uibuiltin/webui"
	"github.com/ryan053857/mygoravel/session"
	"github.com/ryan053857/mygoravel/web"
)

// Config 是两个区域的配置，来自 config/app.yaml。
type Config struct {
	Ops    Area `yaml:"ops"`
	Tenant Area `yaml:"tenant"`
}

// Area 是一个区域的配置。
type Area struct {
	Theme webui.Theme `yaml:"theme"`
}

func sessions(cookie string) *session.Config {
	var cfg session.Config
	cfg.SetDefaults()
	cfg.Cookie.Name = cookie
	cfg.Cookie.Secure = false
	return &cfg
}

// New 组装演示应用。
func New(cfg Config) (http.Handler, error) {
	static, err := web.NewStatic(Static, web.StaticOptions{})
	if err != nil {
		return nil, err
	}
	store := article.NewStore()

	// 两个区域各一个 web.Pages：不同的会话 cookie、登录页和错误呈现。
	ops := web.New(web.Options{
		Sessions:      session.New(*sessions("ops_session"), memstore.New()),
		Login:         "/ops/login",
		ErrorPage:     webui.ErrorPage,
		ErrorFragment: webui.ErrorFragment,
		ErrorTarget:   webui.ErrorTarget,
	})
	tenant := web.New(web.Options{
		Sessions:      session.New(*sessions("app_session"), memstore.New()),
		Login:         "/app/login",
		ErrorPage:     webui.ErrorPage,
		ErrorFragment: views.TenantErrorFragment,
		ErrorTarget:   "#app-errors",
		Static:        static,
	})

	r := chi.NewRouter()
	r.Use(httpx.Locale(nil))
	r.Handle(web.AssetsPath+"*", web.Assets())
	r.Handle(webui.AssetsPath+"*", webui.Assets())
	r.Handle(web.StaticPath+"*", static)

	r.Route("/ops", func(r chi.Router) {
		r.Use(webui.Use(cfg.Ops.Theme))
		r.NotFound(ops.NotFound)
		h := &opsHandlers{pages: ops, store: store}
		public := ops.Routes(r, web.Public)
		public.Get("/login", h.loginPage)
		public.Post("/login", h.login)
		admin := ops.Routes(r, web.Require())
		admin.Post("/logout", h.logout)
		admin.Get("/articles", h.list)
		admin.Get("/articles/new", h.newDialog)
		admin.Post("/articles", h.create)
		admin.Get("/articles/{id}/delete", h.deleteDialog)
		admin.Delete("/articles/{id}", h.delete)
	})
	r.Route("/app", func(r chi.Router) {
		r.Use(webui.Use(cfg.Tenant.Theme))
		r.NotFound(tenant.NotFound)
		h := &tenantHandlers{pages: tenant, store: store}
		public := tenant.Routes(r, web.Public)
		public.Get("/login", h.loginPage)
		public.Post("/login", h.login)
		member := tenant.Routes(r, web.Require())
		member.Get("/", h.home)
		member.Post("/logout", h.logout)
		member.Post("/articles/{id}/star", h.star)
	})
	return r, nil
}
