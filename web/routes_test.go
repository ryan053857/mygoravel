package web_test

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/go-tenon/tenon/httpx"
	"github.com/go-tenon/tenon/web"
)

func TestRoutesRejectNilHandler(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	pages().Routes(chi.NewRouter()).Get("/", nil)
}

func registerPages(r chi.Router, p *web.Pages, h web.HandlerFunc) {
	pages := p.Routes(r)
	pages.Get("/", h)
	pages.Get("/articles/{slug}", h)
	pages.Post("/subscribe", h)
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
			{Method: "GET", Path: "/", Access: "public"},
			{Method: "GET", Path: "/_web/*", Access: "public"},
			{Method: "GET", Path: "/articles/{slug}", Access: "public"},
			{Method: "GET", Path: "/healthz", Access: "unknown"},
			{Method: "POST", Path: "/subscribe", Access: "public"},
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
	web.New(web.Options{Mode: web.ModeRoutes}).Routes(router).Get("/", func(http.ResponseWriter, *http.Request) error {
		t.Fatal("handler called in ModeRoutes")
		return nil
	})
	if rr := serve(router, httptest.NewRequest("GET", "/", nil)); rr.Code != http.StatusInternalServerError {
		t.Fatalf("status %d", rr.Code)
	}
}
