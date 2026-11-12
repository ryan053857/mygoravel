package docs_test

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/alexedwards/scs/v2/memstore"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ryan053857/mygoravel/i18n"
	"github.com/ryan053857/mygoravel/session"
	"github.com/ryan053857/mygoravel/web"
	"github.com/ryan053857/mygoravel/web/docs"
	"github.com/ryan053857/mygoravel/web/ui"
	"github.com/ryan053857/mygoravel/web/webtest"
)

// help 是测试用的帮助中心：首页、顶层页面、有正文的目录、只有标题的目录和其中的子目录。
func help() fstest.MapFS {
	return fstest.MapFS{
		"_index.md":                 {Data: []byte("---\ntitle: 欢迎\n---\n\n从[安装](getting-started/install.md)开始。\n")},
		"faq.md":                    {Data: []byte("# 常见问题\n\n## 怎么开票？\n\n见[发票](billing/invoices.md#申请发票)。\n")},
		"intro.md":                  {Data: []byte("---\ntitle: 简介\norder: 1\ndescription: 产品是什么\n---\n\n正文\n")},
		"getting-started/_index.md": {Data: []byte("---\ntitle: 入门\norder: 2\n---\n\n入门的概览。\n")},
		"getting-started/install.md": {Data: []byte("---\ntitle: 安装\n---\n\n# 安装说明\n\n" +
			"## 下载 App\n\n![截图](images/shot.png)\n\n" +
			"> [!WARNING]\n> 先备份**数据**。\n\n" +
			"```go\nfmt.Println(\"<hi>\")\n```\n\n" +
			"```\nplain <text>\n```\n\n" +
			"### 第二步 {#step-2}\n\n## 下载 App\n\n> 普通引用\n\n" +
			"[外部](https://example.com) [站内](/pricing) [本页](#step-2) [上级](../faq.md) [目录](../billing/)\n")},
		"getting-started/images/shot.png": {Data: []byte("PNG")},
		"billing/_index.md":               {Data: []byte("---\ntitle: 账单\norder: 3\n---\n")},
		"billing/invoices.md":             {Data: []byte("---\ntitle: 发票\ndescription: 申请和下载发票\n---\n\n## 申请发票\n\n正文\n")},
		"billing/advanced/_index.md":      {Data: []byte("# 高级\n\n高级用法。\n")},
		"billing/advanced/refunds.md":     {Data: []byte("# 退款\n\n正文\n")},
		".drafts/secret.md":               {Data: []byte("# 草稿\n")},
	}
}

func newClient(t *testing.T, opts docs.Options) *webtest.Client {
	t.Helper()
	site, err := docs.New(opts)
	require.NoError(t, err)
	var cfg session.Config
	cfg.SetDefaults()
	pages := web.New(web.Options{Sessions: session.New(cfg, memstore.New()), Login: "/login", ErrorPage: ui.ErrorPage})
	r := chi.NewRouter()
	b, err := i18n.New("zh-CN")
	require.NoError(t, err)
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(i18n.WithLocale(r.Context(), b, "zh-CN")))
		})
	})
	site.Register(pages.Routes(r, web.Public))
	return webtest.New(t, r)
}

func get(t *testing.T, c *webtest.Client, path string) string {
	t.Helper()
	res := c.Get(path)
	require.Equal(t, http.StatusOK, res.Code, res.Text())
	return res.Text()
}

// section 返回 html 中 id 为 PageID 的正文区域。
func section(html string) string {
	_, s, _ := strings.Cut(html, `id="`+docs.PageID+`"`)
	return s
}

func TestPage(t *testing.T) {
	c := newClient(t, docs.Options{Content: help(), Prefix: "/help", Title: "帮助中心"})
	html := get(t, c, "/help/getting-started/install")
	body := section(html)

	assert.Contains(t, html, "<title>安装 · 帮助中心</title>")
	assert.Contains(t, body, `<a href="/help/getting-started" hx-boost="true" class="hover:text-foreground">入门</a>`, "面包屑")
	assert.Contains(t, body, `<h1 class="text-3xl font-bold tracking-tight">安装</h1>`, "front matter 的标题优先")
	assert.NotContains(t, body, "安装说明", "开头的一级标题不重复显示")

	assert.Contains(t, body, `<h2 id="下载-app">下载 App<a href="#%E4%B8%8B%E8%BD%BD-app" class="docs-anchor">#</a></h2>`)
	assert.Contains(t, body, `<h3 id="step-2">`)
	assert.Contains(t, body, `<h2 id="下载-app-1">`, "重复的标题 ID 加序号")

	assert.Contains(t, body, `<div class="docs-alert" data-kind="warning"><p class="docs-alert-title">警告</p>`+"\n<p>先备份<strong>数据</strong>。</p>\n</div>")
	assert.Contains(t, body, "<blockquote>\n<p>普通引用</p>")

	assert.Contains(t, body, `aria-label="复制"`)
	assert.Regexp(t, `<pre class="chroma"><code><span class="line"><span class="cl"><span class="nx">fmt</span>.*<span class="s">&#34;&lt;hi&gt;&#34;</span>`, body)
	assert.Contains(t, body, `<pre class="chroma"><code>plain &lt;text&gt;`+"\n</code></pre>")

	assert.Contains(t, body, `<img src="/help/getting-started/images/shot.png" alt="截图">`)
	assert.Contains(t, body, `<a href="https://example.com">外部</a> <a href="/pricing">站内</a> <a href="#step-2">本页</a> `+
		`<a href="/help/faq" data-hx-boost="true">上级</a> <a href="/help/billing" data-hx-boost="true">目录</a>`)

	toc := regexp.MustCompile(`<a href="(#[^"]+)" class="[^"]*?(pl-3)?">([^<]+)</a>`).FindAllStringSubmatch(body[strings.Index(body, "data-docs-toc"):], -1)
	require.Len(t, toc, 3)
	assert.Equal(t, []string{"#下载-app", "", "下载 App"}, toc[0][1:])
	assert.Equal(t, []string{"#step-2", "pl-3", "第二步"}, toc[1][1:])

	assert.Contains(t, body, `<a href="/help/getting-started" hx-boost="true" rel="prev"`)
	assert.Contains(t, body, `<a href="/help/billing/advanced" hx-boost="true" rel="next"`, "下一页跳过没有正文的目录首页")
}

var activeAttr = regexp.MustCompile(`\sdata-active[\s=>]|\sdata-active$`)

// navLinks 返回侧边栏中的链接，当前页面的前面加上 *。
func navLinks(html string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`<a href="([^"]*)"([^>]*)>(?:<span[^>]*>)?([^<]*)`).FindAllStringSubmatch(html, -1) {
		if !strings.Contains(m[2], "data-docs-nav") {
			continue
		}
		mark := ""
		if activeAttr.MatchString(m[2]) {
			mark = "*"
		}
		out = append(out, mark+m[3]+" "+m[1])
	}
	return out
}

func TestSidebar(t *testing.T) {
	c := newClient(t, docs.Options{Content: help(), Prefix: "/help", Title: "帮助中心"})
	html := get(t, c, "/help/billing/advanced/refunds")
	assert.Equal(t, []string{
		"简介 /help/intro",
		"入门 /help/getting-started",
		"安装 /help/getting-started/install",
		"概览 /help/billing/advanced",
		"*退款 /help/billing/advanced/refunds",
		"发票 /help/billing/invoices",
		"常见问题 /help/faq",
	}, navLinks(html))
	assert.Contains(t, html, ">账单</div>", "没有正文的目录只显示标题")
	assert.NotContains(t, html, "images", "只有图片的目录不在侧边栏中")
	assert.NotContains(t, html, "草稿", "以 . 开头的目录被忽略")
}

func TestHomeAndListing(t *testing.T) {
	c := newClient(t, docs.Options{Content: help(), Prefix: "/help", Title: "帮助中心"})
	home := get(t, c, "/help")
	assert.Contains(t, home, "<title>欢迎 · 帮助中心</title>")
	assert.Contains(t, section(home), `<a href="/help/getting-started/install" data-hx-boost="true">安装</a>`)
	assert.NotContains(t, section(home), `rel="prev"`)
	assert.Contains(t, section(home), `<a href="/help/intro" hx-boost="true" rel="next"`)

	billing := section(get(t, c, "/help/billing"))
	assert.NotContains(t, billing, "docs-prose")
	assert.Regexp(t, `<a href="/help/billing/advanced"[^>]*><p class="font-medium">高级</p></a>`, billing)
	assert.Regexp(t, `<a href="/help/billing/invoices"[^>]*><p class="font-medium">发票</p><p class="mt-1 text-sm text-muted-foreground">申请和下载发票</p></a>`, billing)

	res := c.Get("/help/billing/")
	assert.Equal(t, http.StatusMovedPermanently, res.Code)
	assert.Equal(t, "/help/billing", res.Header().Get("Location"))
	assert.Equal(t, http.StatusOK, c.Get("/help/").Code)
}

func TestFiles(t *testing.T) {
	c := newClient(t, docs.Options{Content: help(), Prefix: "/help"})
	res := c.Get("/help/getting-started/images/shot.png")
	require.Equal(t, http.StatusOK, res.Code)
	assert.Equal(t, "PNG", res.Text())
	assert.Equal(t, "image/png", res.Header().Get("Content-Type"))
	etag := res.Header().Get("ETag")
	assert.NotEmpty(t, etag)

	res = c.Get("/help/getting-started/images/shot.png", webtest.Header("If-None-Match", etag))
	assert.Equal(t, http.StatusNotModified, res.Code)

	for _, path := range []string{"/help/faq.md", "/help/.drafts/secret", "/help/.drafts/secret.md", "/help/missing"} {
		assert.Equal(t, http.StatusNotFound, c.Get(path).Code, path)
	}
}

func TestRootPrefix(t *testing.T) {
	c := newClient(t, docs.Options{Content: help(), Title: "帮助中心"})
	assert.Contains(t, get(t, c, "/"), `<a href="/getting-started/install" data-hx-boost="true">安装</a>`)
	assert.Contains(t, get(t, c, "/faq"), `<a href="/billing/invoices#%E7%94%B3%E8%AF%B7%E5%8F%91%E7%A5%A8" data-hx-boost="true">发票</a>`)
}

func TestHTML(t *testing.T) {
	content := fstest.MapFS{"a.md": {Data: []byte("<kbd>Ctrl</kbd>\n\n<div class=\"note\">块</div>\n")}}
	off := section(get(t, newClient(t, docs.Options{Content: content}), "/a"))
	assert.NotContains(t, off, "<kbd>")
	assert.NotContains(t, off, `<div class="note">`)
	on := section(get(t, newClient(t, docs.Options{Content: content, HTML: true}), "/a"))
	assert.Contains(t, on, "<kbd>Ctrl</kbd>")
	assert.Contains(t, on, `<div class="note">块</div>`)
}

func TestReload(t *testing.T) {
	content := fstest.MapFS{"a.md": {Data: []byte("# 旧标题\n")}}
	c := newClient(t, docs.Options{Content: content, Reload: true})
	assert.Contains(t, get(t, c, "/a"), "旧标题")
	content["a.md"] = &fstest.MapFile{Data: []byte("# 新标题\n")}
	assert.Contains(t, get(t, c, "/a"), "新标题")
	content["b.md"] = &fstest.MapFile{Data: []byte("[坏链接](missing.md)\n")}
	assert.Equal(t, http.StatusInternalServerError, c.Get("/a").Code)
}

func TestNewErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		opts docs.Options
		want string
	}{
		"no content":        {docs.Options{}, "Options.Content is required"},
		"relative prefix":   {docs.Options{Content: fstest.MapFS{}, Prefix: "help"}, `Prefix "help" must start with /`},
		"missing page":      {docs.Options{Content: fstest.MapFS{"a/b.md": {Data: []byte("[x](c.md)")}}}, "a/b.md: link c.md points to a missing file"},
		"missing image":     {docs.Options{Content: fstest.MapFS{"a.md": {Data: []byte("![x](x.png)")}}}, "a.md: link x.png points to a missing file"},
		"outside":           {docs.Options{Content: fstest.MapFS{"a.md": {Data: []byte("[x](../go.mod)")}}}, "a.md: link ../go.mod points outside the content"},
		"too deep":          {docs.Options{Content: fstest.MapFS{"a/b/c/d.md": {Data: []byte("x")}}}, "a/b/c/d.md: directories nest at most 2 levels"},
		"same address":      {docs.Options{Content: fstest.MapFS{"a.md": {}, "a/_index.md": {}}}, "a/_index.md and a.md have the same address /a"},
		"same as directory": {docs.Options{Content: fstest.MapFS{"a.md": {}, "a/b.md": {}}}, "a.md and directory a have the same address /a"},
		"unknown field":     {docs.Options{Content: fstest.MapFS{"a.md": {Data: []byte("---\nweight: 1\n---\n")}}}, "a.md: front matter:"},
		"unclosed matter":   {docs.Options{Content: fstest.MapFS{"a.md": {Data: []byte("---\ntitle: x\n")}}}, "a.md: front matter: missing closing ---"},
		"several bad links": {docs.Options{Content: fstest.MapFS{"a.md": {Data: []byte("[x](x.md)")}, "b.md": {Data: []byte("[y](y.md)")}}}, "a.md: link x.md points to a missing file\nb.md: link y.md points to a missing file"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := docs.New(tc.opts)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.True(t, strings.HasPrefix(err.Error(), "web/docs: "), err.Error())
		})
	}
}

func TestFrontMatter(t *testing.T) {
	content := fstest.MapFS{
		"a.md": {Data: []byte("---\r\ntitle: 甲\r\n---\r\n正文\r\n")},
		"b.md": {Data: []byte("---\n---\n# 乙\n")},
		"c.md": {Data: []byte("没有标题\n")},
	}
	c := newClient(t, docs.Options{Content: content})
	assert.Contains(t, get(t, c, "/a"), "<title>甲</title>")
	assert.Contains(t, get(t, c, "/b"), "<title>乙</title>")
	assert.Contains(t, get(t, c, "/c"), "<title>c</title>")
}
