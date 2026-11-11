package ui_test

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/ryan053857/mygoravel/poc/ui/app"
)

func get(t *testing.T, c *http.Client, url string, hdr ...string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	for i := 0; i < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	return res, string(b)
}

// S1：生产模式的静态资源：带指纹的 CSS 永久缓存；已带哈希的组件脚本永久缓存；
// 原始文件名可访问但需要协商（ETag → 304）；不存在的文件 404。
func TestStaticProduction(t *testing.T) {
	base := start(t)
	c := http.DefaultClient
	_, page := get(t, c, base+"/admin/articles")
	css := regexp.MustCompile(`href="(/static/css/app\.[0-9a-f]{8}\.css)"`).FindStringSubmatch(page)
	js := regexp.MustCompile(`src="(/static/js/shadcn-templ-[0-9a-f]{16}\.js)"`).FindStringSubmatch(page)
	if css == nil || js == nil {
		t.Fatalf("asset URLs not found: css=%v js=%v", css, js)
	}
	for _, u := range []string{css[1], js[1]} {
		res, body := get(t, c, base+u)
		if res.StatusCode != 200 || res.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" || len(body) == 0 {
			t.Errorf("%s: %d %q", u, res.StatusCode, res.Header.Get("Cache-Control"))
		}
	}
	res, _ := get(t, c, base+"/static/css/app.css")
	etag := res.Header.Get("ETag")
	if res.StatusCode != 200 || res.Header.Get("Cache-Control") != "no-cache" || etag == "" {
		t.Errorf("logical name: %d %q etag=%q", res.StatusCode, res.Header.Get("Cache-Control"), etag)
	}
	if res, _ := get(t, c, base+"/static/css/app.css", "If-None-Match", etag); res.StatusCode != 304 {
		t.Errorf("If-None-Match: %d, want 304", res.StatusCode)
	}
	if res, _ := get(t, c, base+"/static/css/nope.css"); res.StatusCode != 404 {
		t.Errorf("missing file: %d", res.StatusCode)
	}
}

// S2：开发模式读磁盘、不加指纹、不缓存。
func TestStaticDev(t *testing.T) {
	h, err := app.New(os.DirFS("assets"), true)
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(h)
	defer s.Close()
	_, page := get(t, http.DefaultClient, s.URL+"/")
	if !strings.Contains(page, `href="/static/css/app.css"`) {
		t.Error("dev mode should not fingerprint")
	}
	res, _ := get(t, http.DefaultClient, s.URL+"/static/css/app.css")
	if res.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("dev Cache-Control = %q", res.Header.Get("Cache-Control"))
	}
}

// S3：公开页面不创建会话：没有 Set-Cookie。会话中间件仍然加上 Vary: Cookie。
// 有了 flash 之后才有 cookie，显示一次后会话里不再有它。
func TestPublicPageSession(t *testing.T) {
	base := start(t)
	res, _ := get(t, http.DefaultClient, base+"/")
	if sc := res.Header.Values("Set-Cookie"); len(sc) > 0 {
		t.Errorf("anonymous GET / sets cookies: %v", sc)
	}
	t.Logf("Vary on public page: %v, Cache-Control: %q", res.Header.Values("Vary"), res.Header.Get("Cache-Control"))

	jar, _ := cookiejar.New(nil)
	// 不自动跟随 303，否则跳转后的 GET 已经把 flash 显示掉了。
	c := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := c.PostForm(base+"/subscribe", url.Values{"email": {"a@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	_, page := get(t, c, base+"/")
	if !strings.Contains(page, "订阅成功") {
		t.Error("flash not rendered after redirect")
	}
	_, page = get(t, c, base+"/")
	if strings.Contains(page, "订阅成功") {
		t.Error("flash rendered twice")
	}
}
