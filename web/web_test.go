package web_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-tenon/tenon/web"
)

func TestRender(t *testing.T) {
	rr := httptest.NewRecorder()
	if err := web.Render(rr, httptest.NewRequest("GET", "/", nil), http.StatusCreated, text("<b>")); err != nil {
		t.Fatal(err)
	}
	if rr.Code != 201 || rr.Body.String() != "&lt;b&gt;" || rr.Header().Get("Content-Type") != "text/html; charset=utf-8" || rr.Header().Get("Vary") != "HX-Request" {
		t.Fatalf("%d %q %v", rr.Code, rr.Body.String(), rr.Header())
	}

	rr = httptest.NewRecorder()
	if err := web.Render(rr, httptest.NewRequest("GET", "/", nil), 200, failing()); err == nil {
		t.Fatal("expected render error")
	}
	if rr.Body.Len() != 0 || rr.Header().Get("Content-Type") != "" {
		t.Fatal("partial output written on render error")
	}
}

func TestRequestKinds(t *testing.T) {
	plain := httptest.NewRequest("GET", "/", nil)
	full := htmx(httptest.NewRequest("GET", "/", nil), false)
	partial := htmx(httptest.NewRequest("GET", "/", nil), true)
	if web.IsHTMX(plain) || !web.IsHTMX(full) || !web.IsHTMX(partial) {
		t.Fatal("IsHTMX")
	}
	if web.IsPartial(plain) || web.IsPartial(full) || !web.IsPartial(partial) {
		t.Fatal("IsPartial")
	}
}

func TestRedirect(t *testing.T) {
	rr := httptest.NewRecorder()
	web.Redirect(rr, httptest.NewRequest("POST", "/", nil), "/done")
	if rr.Code != 303 || rr.Header().Get("Location") != "/done" {
		t.Fatalf("plain: %d %v", rr.Code, rr.Header())
	}
	rr = httptest.NewRecorder()
	web.Redirect(rr, htmx(httptest.NewRequest("POST", "/", nil), true), "/done")
	if rr.Code != 204 || rr.Header().Get("HX-Redirect") != "/done" || rr.Header().Get("Location") != "" {
		t.Fatalf("htmx: %d %v", rr.Code, rr.Header())
	}
}

func TestLocalPath(t *testing.T) {
	for in, want := range map[string]string{
		"/admin?x=1":           "/admin?x=1",
		"/":                    "/",
		"":                     "/home",
		"//evil.example":       "/home",
		`/\evil.example`:       "/home",
		"https://evil.example": "/home",
		"admin":                "/home",
	} {
		if got := web.LocalPath(in, "/home"); got != want {
			t.Errorf("LocalPath(%q) = %q, want %q", in, got, want)
		}
	}
}
