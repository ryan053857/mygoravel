package web_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/web"
)

// TestServerErrorToastCarriesRequestID 检查局部请求的 5xx 提示以请求编号为说明，4xx 没有。
func TestServerErrorToastCarriesRequestID(t *testing.T) {
	t.Parallel()
	o := options()
	o.ErrorFragment, o.ErrorTarget = nil, ""
	p := web.New(o)
	router := chi.NewRouter()
	router.Use(httpx.AssignRequestID(httpx.DefaultRequestIDConfig()))
	routes := p.Routes(router)
	routes.Post("/internal", func(http.ResponseWriter, *http.Request) error { return errorx.Define(errorx.Internal, "x.y").New() })
	routes.Post("/conflict", func(http.ResponseWriter, *http.Request) error { return errLocked.New() })

	rr := serve(router, htmx(httptest.NewRequest("POST", "/internal", nil), true))
	msgs := flashTrigger(t, rr)
	if want := "Request ID: " + rr.Header().Get("X-Request-ID"); len(msgs) != 1 || msgs[0].Description != want {
		t.Fatalf("5xx toast %+v, want description %q", msgs, want)
	}
	rr = serve(router, htmx(httptest.NewRequest("POST", "/conflict", nil), true))
	if msgs := flashTrigger(t, rr); len(msgs) != 1 || msgs[0].Description != "" {
		t.Fatalf("4xx toast %+v", msgs)
	}
}

func TestErrorReference(t *testing.T) {
	t.Parallel()
	if got := web.ErrorReference(t.Context()); got != "" {
		t.Errorf("without request ID: %q", got)
	}
	var en, zhRef string
	h := httpx.AssignRequestID(httpx.DefaultRequestIDConfig())(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		en = web.ErrorReference(r.Context())
		zhRef = web.ErrorReference(zh(t, r).Context())
	}))
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Request-ID", "9F2K")
	serve(h, r)
	if en != "Request ID: 9F2K" || zhRef != "请求编号：9F2K" {
		t.Errorf("en %q, zh %q", en, zhRef)
	}
}
