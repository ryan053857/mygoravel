package web_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/a-h/templ"

	"github.com/ryan053857/mygoravel/web"
)

func TestScriptsAreServed(t *testing.T) {
	var buf bytes.Buffer
	if err := web.Scripts().Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	srcs := regexp.MustCompile(`src="([^"]+)"`).FindAllStringSubmatch(buf.String(), -1)
	if len(srcs) != 4 {
		t.Fatalf("scripts: %s", buf.String())
	}
	assets := web.Assets()
	for _, m := range srcs {
		rr := serve(assets, httptest.NewRequest("GET", m[1], nil))
		if rr.Code != 200 || rr.Body.Len() < 1000 || rr.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
			t.Errorf("%s: %d len=%d %v", m[1], rr.Code, rr.Body.Len(), rr.Header())
		}
	}
	if rr := serve(assets, httptest.NewRequest("GET", web.AssetsPath+"nope.js", nil)); rr.Code != http.StatusNotFound {
		t.Errorf("missing asset: %d", rr.Code)
	}
}

func TestScriptsNonce(t *testing.T) {
	var plain, withNonce bytes.Buffer
	if err := web.Scripts().Render(context.Background(), &plain); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain.String(), "nonce") || strings.Count(plain.String(), "<script") != 4 {
		t.Errorf("nonce without templ.WithNonce: %s", plain.String())
	}
	ctx := templ.WithNonce(context.Background(), `abc"123`)
	if err := web.Scripts().Render(ctx, &withNonce); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(withNonce.String(), `nonce="abc&#34;123"`); n != 5 {
		t.Errorf("%d tags with the nonce:\n%s", n, withNonce.String())
	}
	if !strings.Contains(withNonce.String(), `<script defer nonce="abc&#34;123" src="/_web/alpine-`) {
		t.Errorf("Alpine tag: %s", withNonce.String())
	}
	if !strings.Contains(withNonce.String(), `<script nonce="abc&#34;123">htmx.config.inlineScriptNonce = "abc\"123"</script>`) {
		t.Errorf("inlineScriptNonce: %s", withNonce.String())
	}
}
