package uibuiltin_test

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

	"github.com/ryan053857/mygoravel/config"
	"github.com/ryan053857/mygoravel/poc/uibuiltin/app"
)

type settings struct {
	Addr       string `yaml:"addr"`
	app.Config `yaml:",inline"`
}

// start 用 config/app.yaml 启动演示应用。
func start(t *testing.T) string {
	t.Helper()
	cfg, err := config.Load[settings]("config/app.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Ops.Theme.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Tenant.Theme.Validate(); err != nil {
		t.Fatal(err)
	}
	h, err := app.New(cfg.Config)
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s.URL
}

func client(t *testing.T) *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func get(t *testing.T, c *http.Client, u string) (*http.Response, string) {
	t.Helper()
	res, err := c.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

func postForm(t *testing.T, c *http.Client, u string, v url.Values) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, u, strings.NewReader(v.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", u[:strings.Index(u[8:], "/")+8])
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res
}

var stylesheetLink = regexp.MustCompile(`<link rel="stylesheet" href="([^"]+)"`)

// S1：两个区域各自的登录跳转、风格、样式表和主题变量；每页只有一份样式表。
func TestAreas(t *testing.T) {
	base := start(t)
	c := client(t)

	res, _ := get(t, c, base+"/ops/articles")
	if res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(res.Header.Get("Location"), "/ops/login?next=") {
		t.Fatalf("ops redirect: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	res, _ = get(t, c, base+"/app/")
	if res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(res.Header.Get("Location"), "/app/login?next=") {
		t.Fatalf("app redirect: %d %s", res.StatusCode, res.Header.Get("Location"))
	}

	_, ops := get(t, c, base+"/ops/login")
	if !strings.Contains(ops, `class="style-nova"`) {
		t.Error("ops: no style-nova class")
	}
	links := stylesheetLink.FindAllStringSubmatch(ops, -1)
	if len(links) != 1 || !strings.Contains(links[0][1], "/_web/ui/style-nova-") {
		t.Errorf("ops stylesheets = %v", links)
	}
	if strings.Contains(ops, "<style") {
		t.Error("ops: unexpected theme overrides")
	}

	_, tenant := get(t, c, base+"/app/login")
	if !strings.Contains(tenant, `class="style-maia"`) {
		t.Error("tenant: no style-maia class")
	}
	links = stylesheetLink.FindAllStringSubmatch(tenant, -1)
	if len(links) != 1 || !regexp.MustCompile(`^/static/css/app\.[0-9a-f]{8}\.css$`).MatchString(links[0][1]) {
		t.Errorf("tenant stylesheets = %v", links)
	}
	if !strings.Contains(tenant, `<style>:root{--primary:oklch(0.55 0.22 275);--primary-foreground:oklch(0.98 0 0);--radius:1rem;--ring:oklch(0.62 0.2 275);}.dark{--primary:oklch(0.7 0.16 275);}</style>`) {
		t.Errorf("tenant theme overrides missing:\n%s", tenant[:min(len(tenant), 2000)])
	}

	// 登录运营后台不会登录租户端：两个区域的会话互相独立。
	res = postForm(t, c, base+"/ops/login", url.Values{"email": {"ops@example.com"}, "password": {"secret"}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("ops login: %d", res.StatusCode)
	}
	if res, _ := get(t, c, base+"/ops/articles"); res.StatusCode != http.StatusOK {
		t.Fatalf("ops articles after login: %d", res.StatusCode)
	}
	if res, _ := get(t, c, base+"/app/"); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("app after ops login: %d", res.StatusCode)
	}

	// 404 按区域呈现：运营后台是 nova，租户端是 maia。
	res, body := get(t, c, base+"/ops/nope")
	if res.StatusCode != http.StatusNotFound || !strings.Contains(body, "style-nova") {
		t.Errorf("ops 404: %d", res.StatusCode)
	}
	res, body = get(t, c, base+"/app/nope")
	if res.StatusCode != http.StatusNotFound || !strings.Contains(body, "style-maia") || !strings.Contains(body, "/static/css/app.") {
		t.Errorf("app 404: %d", res.StatusCode)
	}
}

// S2：不装 Tailwind 的区域只能用预编译样式表中已有的类名。列出运营后台视图中不存在的类名。
func TestOpsClassesInPrecompiledCSS(t *testing.T) {
	src, err := os.ReadFile("app/views/ops.templ")
	if err != nil {
		t.Fatal(err)
	}
	css := readStylesheet(t, "nova")
	var missing []string
	for _, m := range regexp.MustCompile(`(?:class=|Class: )"([^"]+)"`).FindAllStringSubmatch(string(src), -1) {
		for _, c := range strings.Fields(m[1]) {
			if !strings.Contains(css, "."+cssEscape(c)+"{") && !strings.Contains(css, "."+cssEscape(c)+":") && !strings.Contains(css, "."+cssEscape(c)+",") {
				missing = append(missing, c)
			}
		}
	}
	if len(missing) > 0 {
		t.Errorf("ops classes not in the precompiled stylesheet: %v", missing)
	}
}

func readStylesheet(t *testing.T, style string) string {
	t.Helper()
	files, _ := os.ReadDir("webui/assets")
	for _, f := range files {
		if strings.HasPrefix(f.Name(), "style-"+style+"-") {
			b, err := os.ReadFile("webui/assets/" + f.Name())
			if err != nil {
				t.Fatal(err)
			}
			return string(b)
		}
	}
	t.Fatalf("no stylesheet for %s", style)
	return ""
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
