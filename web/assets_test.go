package web_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

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
