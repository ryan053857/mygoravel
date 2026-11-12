package docs_test

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ryan053857/mygoravel/web/docs"
	"github.com/ryan053857/mygoravel/web/webtest"
)

// partial 模拟顶栏搜索框发出的 htmx 请求：目标是结果弹层。
func partial(r *http.Request) {
	r.Header.Set("HX-Request", "true")
	r.Header.Set("HX-Request-Type", "partial")
	r.Header.Set("HX-Target", "#"+docs.SearchResultsID)
}

// hits 返回搜索结果的链接和标题。
func hits(html string) []string {
	var out []string
	re := regexp.MustCompile(`<a href="([^"]+)" hx-boost="true" class="block rounded-md[^"]*"><p class="text-sm font-medium">([^<]*)(?:<span class="text-muted-foreground">› ([^<]*)</span>)?`)
	for _, m := range re.FindAllStringSubmatch(html, -1) {
		hit := m[1] + " " + strings.TrimSpace(m[2])
		if m[3] != "" {
			hit += " › " + m[3]
		}
		out = append(out, hit)
	}
	return out
}

func searchSite() fstest.MapFS {
	content := help()
	content["billing/invoices.md"] = &fstest.MapFile{Data: []byte("---\ntitle: 发票\ndescription: 申请和下载发票\n---\n\n" +
		"发票在每月月底开具。\n\n## 申请发票\n\n在账单页点击申请。\n\n## 下载\n\n| 格式 | 说明 |\n| --- | --- |\n| PDF | 电子发票 |\n\n" +
		"```sh\ncurl https://example.com/invoices.zip\n```\n")}
	return content
}

func TestSearchPartial(t *testing.T) {
	c := newClient(t, docs.Options{Content: searchSite(), Prefix: "/help", Title: "帮助中心"})
	res := c.Get("/help/_search?q=发票", partial)
	require.Equal(t, http.StatusOK, res.Code)
	assert.NotContains(t, res.Text(), "<html", "htmx 请求只返回结果列表")
	assert.Equal(t, []string{
		"/help/billing/invoices 发票",
		"/help/billing/invoices#申请发票 发票 › 申请发票",
		"/help/billing/invoices#下载 发票 › 下载",
		"/help/faq#怎么开票 常见问题 › 怎么开票？",
	}, hits(res.Text()), "页面标题、小节标题、正文依次排列，同分的按侧边栏的顺序")
	assert.Contains(t, res.Text(), `<mark class="rounded-sm bg-primary/15 text-foreground">发票</mark>在每月月底开具。`)
}

func TestSearchTerms(t *testing.T) {
	c := newClient(t, docs.Options{Content: searchSite(), Prefix: "/help", Title: "帮助中心"})
	for q, want := range map[string][]string{
		"申请 点击":        {"/help/billing/invoices#申请发票 发票 › 申请发票"},
		"发票 点击":        {"/help/billing/invoices#申请发票 发票 › 申请发票"},
		"pdf":          {"/help/billing/invoices#下载 发票 › 下载"},
		"INVOICES.zip": {"/help/billing/invoices#下载 发票 › 下载"},
		"欢迎":           {"/help 欢迎"},
		"不存在的词":        nil,
	} {
		assert.Equal(t, want, hits(c.Get("/help/_search?q="+url.QueryEscape(q), partial).Text()), q)
	}
	assert.Contains(t, c.Get("/help/_search?q=不存在的词", partial).Text(), "没有找到相关内容")
	assert.Empty(t, strings.TrimSpace(c.Get("/help/_search?q=+", partial).Text()), "没有输入时不显示提示")
}

func TestSearchPage(t *testing.T) {
	c := newClient(t, docs.Options{Content: searchSite(), Prefix: "/help", Title: "帮助中心"})
	res := c.Get("/help/_search?q=申请", webtest.HTMXPage)
	require.Equal(t, http.StatusOK, res.Code)
	html := res.Text()
	assert.Contains(t, html, "<title>搜索 · 帮助中心</title>")
	assert.Contains(t, section(html), `value="申请"`)
	assert.Contains(t, section(html), `/help/billing/invoices#申请发票`)
	assert.Contains(t, html, `hx-get="/help/_search"`, "顶栏的搜索框")
}

func TestSearchReserved(t *testing.T) {
	_, err := docs.New(docs.Options{Content: fstest.MapFS{"_search.md": {Data: []byte("x")}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "_search.md: the address /_search is reserved for search")
}
