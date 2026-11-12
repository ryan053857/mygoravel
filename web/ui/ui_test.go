package ui_test

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/a-h/templ"
	"github.com/alexedwards/scs/v2/memstore"
	"github.com/go-chi/chi/v5"

	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/session"
	"github.com/ryan053857/mygoravel/web"
	"github.com/ryan053857/mygoravel/web/ui"
)

// 资源必须按 go.mod 中的 shadcn-templ 生成：升级版本后要运行 go generate ./web/ui。
func TestAssetsMatchGoMod(t *testing.T) {
	b, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`github\.com/axadrn/shadcn-templ/v2 (v\S+)`).FindSubmatch(b)
	if m == nil || string(m[1]) != ui.ShadcnTemplVersion {
		t.Fatalf("go.mod requires shadcn-templ %s, assets were generated for %s: run go generate ./web/ui", m[1], ui.ShadcnTemplVersion)
	}
}

// pages 返回使用 theme 的 Pages 和它的路由，GET / 渲染 c。
func pages(t *testing.T, theme web.Theme, static *web.Static, c func() templ.Component) http.Handler {
	t.Helper()
	var cfg session.Config
	cfg.SetDefaults()
	p := web.New(web.Options{Sessions: session.New(cfg, memstore.New()), Login: "/login", ErrorPage: ui.ErrorPage, Theme: theme, Static: static})
	r := chi.NewRouter()
	r.NotFound(p.NotFound)
	r.Handle(ui.AssetsPath+"*", ui.Assets())
	public := p.Routes(r, web.Public)
	public.Get("/", func(w http.ResponseWriter, r *http.Request) error {
		return web.Render(w, r, http.StatusOK, c())
	})
	public.Post("/flash", func(w http.ResponseWriter, r *http.Request) error {
		web.Flash(r.Context(), web.Message{Type: "success", Title: "已保存"})
		web.Redirect(w, r, "/")
		return nil
	})
	return r
}

func body(t *testing.T, h http.Handler, r *http.Request) (*httptest.ResponseRecorder, string) {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	return rr, rr.Body.String()
}

var linkHref = regexp.MustCompile(`<link rel="stylesheet" href="([^"]+)">`)
var scriptSrc = regexp.MustCompile(`<script defer(?: nonce="[^"]+")? src="(/_web/ui/[^"]+)"`)

func TestPageWithDefaultTheme(t *testing.T) {
	h := pages(t, web.Theme{}, nil, func() templ.Component { return ui.Page(ui.PageProps{Title: "文章"}) })
	_, html := body(t, h, httptest.NewRequest("GET", "/", nil))
	for _, want := range []string{
		`<html lang="zh-CN" class="style-nova">`,
		`<title>文章</title>`,
		`localStorage.getItem("theme")`, // Mode 默认跟随系统
		`/_web/htmx-`,                   // web.Scripts
	} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %s in\n%s", want, html)
		}
	}
	if strings.Contains(html, "<style") {
		t.Error("default theme wrote overrides")
	}

	// 页面引用的样式表和脚本都能取到。
	links := linkHref.FindAllStringSubmatch(html, -1)
	if len(links) != 1 || links[0][1] != ui.StylesheetURL("nova") {
		t.Fatalf("stylesheets = %v", links)
	}
	urls := []string{links[0][1]}
	for _, m := range scriptSrc.FindAllStringSubmatch(html, -1) {
		urls = append(urls, m[1])
	}
	if len(urls) != 3 {
		t.Fatalf("assets = %v", urls)
	}
	for _, u := range urls {
		req := httptest.NewRequest("GET", u, nil)
		req.Header.Set("Accept-Encoding", "gzip, deflate, br")
		rr, _ := body(t, h, req)
		if rr.Code != 200 || rr.Header().Get("Content-Encoding") != "gzip" || rr.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
			t.Errorf("%s: %d %v", u, rr.Code, rr.Header())
		}
	}
}

func TestPageWithTheme(t *testing.T) {
	theme := web.Theme{
		Style:    "maia",
		Mode:     "dark",
		Radius:   "1rem",
		Vars:     map[string]string{"primary": "oklch(0.55 0.22 275)", "ring": "oklch(0.6 0.2 275)"},
		DarkVars: map[string]string{"primary": "oklch(0.7 0.16 275)"},
	}
	h := pages(t, theme, nil, func() templ.Component { return ui.Page(ui.PageProps{Title: "x", Lang: "en"}) })
	_, html := body(t, h, httptest.NewRequest("GET", "/", nil))
	for _, want := range []string{
		`<html lang="en" class="style-maia dark">`,
		`href="` + ui.StylesheetURL("maia") + `"`,
		`">:root{--primary:oklch(0.55 0.22 275);--radius:1rem;--ring:oklch(0.6 0.2 275);}.dark{--primary:oklch(0.7 0.16 275);}</style>`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %s in\n%s", want, html)
		}
	}
	if strings.Contains(html, "localStorage") {
		t.Error("fixed dark mode still runs the mode script")
	}
	// 错误页（这里是 404）同样使用主题。
	_, html = body(t, h, httptest.NewRequest("GET", "/nope", nil))
	if !strings.Contains(html, `class="style-maia dark"`) || !strings.Contains(html, "404") {
		t.Errorf("error page:\n%s", html)
	}
}

// Theme.Stylesheet 换成应用用 Tailwind 生成的样式表。
func TestThemeStylesheet(t *testing.T) {
	static, err := web.NewStatic(fstest.MapFS{"css/app.css": {Data: []byte("body{}")}}, web.StaticOptions{})
	if err != nil {
		t.Fatal(err)
	}
	h := pages(t, web.Theme{Style: "maia", Stylesheet: "css/app.css"}, static, func() templ.Component { return ui.Page(ui.PageProps{}) })
	_, html := body(t, h, httptest.NewRequest("GET", "/", nil))
	links := linkHref.FindAllStringSubmatch(html, -1)
	if len(links) != 1 || !regexp.MustCompile(`^/static/css/app\.[0-9a-f]{8}\.css$`).MatchString(links[0][1]) {
		t.Fatalf("stylesheets = %v", links)
	}
}

func TestHeadNonce(t *testing.T) {
	ctx := templ.WithNonce(t.Context(), `n"1`)
	var buf bytes.Buffer
	if err := ui.Head().Render(ctx, &buf); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	if n := strings.Count(html, "<script"); n != strings.Count(html, `nonce="n&#34;1"`) || n < 6 {
		t.Errorf("scripts without nonce:\n%s", html)
	}
}

// 衔接脚本带上扩展组件的地址，页面出现对应元素时加载。
func TestHeadExtras(t *testing.T) {
	var buf bytes.Buffer
	if err := ui.Head().Render(t.Context(), &buf); err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`data-extras="([^"]+)"`).FindStringSubmatch(buf.String())
	if m == nil || !strings.Contains(m[1], "&#34;[data-slot=\\&#34;calendar\\&#34;]&#34;:&#34;/_web/ui/shadcn-templ-calendar-") {
		t.Fatalf("extras: %v", m)
	}
}

func TestToasterShowsFlashes(t *testing.T) {
	h := pages(t, web.Theme{}, nil, func() templ.Component { return ui.Toaster() })
	rr, _ := body(t, h, httptest.NewRequest("POST", "/flash", nil))
	req := httptest.NewRequest("GET", "/", nil)
	for _, c := range rr.Result().Cookies() {
		req.AddCookie(c)
	}
	_, html := body(t, h, req)
	if !strings.Contains(html, "data-web-ui-toaster") || !strings.Contains(html, "已保存") {
		t.Fatalf("toaster:\n%s", html)
	}
}

func TestAssetsHandler(t *testing.T) {
	h := ui.Assets()
	u := ui.StylesheetURL("vega")

	rr, css := body(t, h, httptest.NewRequest("GET", u, nil)) // 不接受 gzip：解压后发送
	if rr.Code != 200 || rr.Header().Get("Content-Encoding") != "" || !strings.HasPrefix(rr.Header().Get("Content-Type"), "text/css") || !strings.Contains(css, ".style-vega") {
		t.Fatalf("plain: %d %v %.60q", rr.Code, rr.Header(), css)
	}
	req := httptest.NewRequest("GET", u, nil)
	req.Header.Set("Accept-Encoding", "gzip;q=0, br")
	if rr, _ := body(t, h, req); rr.Header().Get("Content-Encoding") != "" {
		t.Error("gzip;q=0 got gzip")
	}
	req = httptest.NewRequest("GET", u, nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rr, gz := body(t, h, req)
	zr, err := gzip.NewReader(strings.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	if plain, _ := io.ReadAll(zr); string(plain) != css || rr.Header().Get("Vary") != "Accept-Encoding" {
		t.Fatal("gzip body differs")
	}
	for path, code := range map[string]int{ui.AssetsPath + "nope.css": 404, ui.AssetsPath + "a/b.css": 404} {
		if rr, _ := body(t, h, httptest.NewRequest("GET", path, nil)); rr.Code != code {
			t.Errorf("%s: %d", path, rr.Code)
		}
	}
	if rr, _ := body(t, h, httptest.NewRequest("POST", u, nil)); rr.Code != 405 {
		t.Errorf("POST: %d", rr.Code)
	}
}

// web/ui 组件（全部 .templ）中的类名都在每套风格的预编译样式表中；常用工具类也在。
func TestStylesheetsContainClasses(t *testing.T) {
	files, _ := filepath.Glob("*.templ")
	var classes []string
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range regexp.MustCompile(`(?:class="|Class: ")([^"]+)"`).FindAllStringSubmatch(string(src), -1) {
			classes = append(classes, strings.Fields(m[1])...)
		}
	}
	classes = append(classes, "px-6", "md:grid-cols-3", "max-w-5xl", "text-right", "bg-primary", "sm:flex-row")
	h := ui.Assets()
	for _, style := range web.Styles {
		_, css := body(t, h, httptest.NewRequest("GET", ui.StylesheetURL(style), nil))
		if !strings.Contains(css, ".style-"+style) {
			t.Errorf("%s: style rules missing", style)
		}
		for _, c := range classes {
			if !strings.Contains(css, "."+cssEscape(c)) {
				t.Errorf("%s: class %s missing", style, c)
			}
		}
	}
}

func cssEscape(class string) string {
	var b strings.Builder
	for _, r := range class {
		if strings.ContainsRune(`:/[]().%#,!&>*=@+`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// 5xx 的错误页显示请求编号，4xx 不显示。
func TestErrorPageShowsRequestID(t *testing.T) {
	t.Parallel()
	render := func(status int) string {
		var buf bytes.Buffer
		h := httpx.AssignRequestID(httpx.DefaultRequestIDConfig())(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			if err := ui.ErrorPage(status, "x").Render(r.Context(), &buf); err != nil {
				t.Error(err)
			}
		}))
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("X-Request-ID", "9F2K")
		h.ServeHTTP(httptest.NewRecorder(), r)
		return buf.String()
	}
	if html := render(500); !strings.Contains(html, "Request ID: 9F2K") {
		t.Errorf("500 page:\n%s", html)
	}
	if html := render(404); strings.Contains(html, "9F2K") {
		t.Errorf("404 page shows request ID:\n%s", html)
	}
}
