package web_test

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/web"
)

func TestRoutesRequiresDeclaredAccess(t *testing.T) {
	for name, register := range map[string]func(){
		"zero access": func() { pages().Routes(chi.NewRouter(), web.Access{}) },
		"nil handler": func() { pages().Routes(chi.NewRouter(), web.Public).Get("/", nil) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected panic")
				}
			}()
			register()
		})
	}
}

func registerPages(r chi.Router, p *web.Pages, h web.HandlerFunc) {
	public := p.Routes(r, web.Public)
	public.Get("/login", h)
	public.Post("/login", h)
	admin := p.Routes(r, web.Require("articles"))
	admin.Get("/admin/articles/{id}/edit", h)
	admin.Put("/admin/articles/{id}", h)
	p.Routes(r, web.Require()).Get("/me", h)
	r.Handle(web.AssetsPath+"*", web.Assets())
	r.Get("/healthz", func(http.ResponseWriter, *http.Request) {})
}

func TestRoutesDescribeAccess(t *testing.T) {
	for _, mode := range []web.Mode{web.ModeServe, web.ModeRoutes} {
		p := pages()
		if mode == web.ModeRoutes {
			p = web.New(web.Options{Mode: web.ModeRoutes})
		}
		router := chi.NewRouter()
		registerPages(router, p, func(http.ResponseWriter, *http.Request) error {
			t.Fatal("route inspection invoked a page handler")
			return nil
		})
		want := []httpx.Route{
			{Method: "GET", Path: "/_web/*", Access: "public"},
			{Method: "PUT", Path: "/admin/articles/{id}", Access: "session", Permissions: []string{"articles"}},
			{Method: "GET", Path: "/admin/articles/{id}/edit", Access: "session", Permissions: []string{"articles"}},
			{Method: "GET", Path: "/healthz", Access: "unknown"},
			{Method: "GET", Path: "/login", Access: "public"},
			{Method: "POST", Path: "/login", Access: "public"},
			{Method: "GET", Path: "/me", Access: "session"},
		}
		var got []httpx.Route
		for _, r := range httpx.Routes(router) {
			if r.Path != "/_web/*" || r.Method == "GET" {
				got = append(got, r)
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("mode %d: routes = %#v", mode, got)
		}
	}
}

func TestModeRoutesHandlersAreNotCalled(t *testing.T) {
	router := chi.NewRouter()
	web.New(web.Options{Mode: web.ModeRoutes}).Routes(router, web.Public).Get("/", func(http.ResponseWriter, *http.Request) error {
		t.Fatal("handler called in ModeRoutes")
		return nil
	})
	if rr := serve(router, httptest.NewRequest("GET", "/", nil)); rr.Code != http.StatusInternalServerError {
		t.Fatalf("status %d", rr.Code)
	}
}
