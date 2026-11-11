package web_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/page"
	"github.com/ryan053857/mygoravel/web"
)

type articleQuery struct {
	Q      string   `json:"q,omitempty"`
	Status string   `json:"status,omitempty" enum:"draft,published"`
	Sort   string   `json:"sort,omitempty" default:"-created" enum:"-created,title,-title"`
	Tags   []string `json:"tag,omitempty"`
	Mine   bool     `json:"mine,omitempty"`
	page.OffsetParams
}

func (q articleQuery) URL() string { return web.QueryURL("/admin/articles", q) }

func parseQuery(t *testing.T, target string) (articleQuery, error) {
	t.Helper()
	return web.ParseQuery[articleQuery](zh(t, httptest.NewRequest("GET", target, nil)))
}

func TestParseQueryDefaults(t *testing.T) {
	q, err := parseQuery(t, "/admin/articles?q=&status=")
	if err != nil {
		t.Fatal(err)
	}
	want := articleQuery{Sort: "-created", OffsetParams: page.OffsetParams{Page: 1, PerPage: 20}}
	if q.Q != "" || q.Status != "" || q.Sort != want.Sort || q.OffsetParams != want.OffsetParams || len(q.Tags) != 0 || q.Mine {
		t.Errorf("empty query = %+v", q)
	}
	if q.Limit() != 20 || q.Offset() != 0 {
		t.Errorf("limit/offset = %d/%d", q.Limit(), q.Offset())
	}
}

func TestParseQueryValues(t *testing.T) {
	q, err := parseQuery(t, "/admin/articles?q=htmx+4&status=draft&sort=title&tag=go&tag=web&mine=on&page=3&per_page=50")
	if err != nil {
		t.Fatal(err)
	}
	if q.Q != "htmx 4" || q.Status != "draft" || q.Sort != "title" || len(q.Tags) != 2 || !q.Mine || q.Page != 3 || q.PerPage != 50 {
		t.Errorf("query = %+v", q)
	}
}

func TestParseQueryInvalid(t *testing.T) {
	for target, field := range map[string]string{
		"/?page=0":        "page",
		"/?page=abc":      "page",
		"/?per_page=1000": "per_page",
		"/?status=bogus":  "status",
	} {
		_, err := parseQuery(t, target)
		if !errors.Is(err, web.ErrBadQuery) {
			t.Errorf("%s: err = %v", target, err)
			continue
		}
		if got := errorx.PublicOf(err)["field"]; got != field {
			t.Errorf("%s: field = %v", target, got)
		}
		if httpx.StatusOf(err) != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d", target, httpx.StatusOf(err))
		}
	}
	// 错误提示按请求语言翻译。
	_, err := parseQuery(t, "/?page=0")
	detail := httpx.ErrorDetail(zh(t, httptest.NewRequest("GET", "/", nil)).Context(), err)
	if detail != "查询参数 page 不正确：不能小于 1" {
		t.Errorf("detail = %q", detail)
	}
}

func TestQueryURL(t *testing.T) {
	q, _ := parseQuery(t, "/")
	cases := []struct {
		q    articleQuery
		want string
	}{
		{q, "/admin/articles"},
		{func() articleQuery { q := q; q.Q, q.Status = "htmx 4", "draft"; return q }(), "/admin/articles?q=htmx+4&status=draft"},
		{func() articleQuery { q := q; q.Sort = "title"; q.Page = 2; return q }(), "/admin/articles?sort=title&page=2"},
		{func() articleQuery { q := q; q.Tags = []string{"a&b", "c"}; q.Mine = true; q.PerPage = 50; return q }(), "/admin/articles?tag=a%26b&tag=c&mine=true&per_page=50"},
	}
	for _, c := range cases {
		if got := c.q.URL(); got != c.want {
			t.Errorf("URL() = %s, want %s", got, c.want)
		}
		// 往返：解析生成的地址得到同一个状态。
		back, err := parseQuery(t, c.want)
		if err != nil || back.URL() != c.want {
			t.Errorf("round trip %s → %s (%v)", c.want, back.URL(), err)
		}
	}
	if got := web.QueryURL("/x", &struct {
		A string `json:"a"`
		b string
		C string `json:"-"`
		D int
	}{A: "1", b: "2", C: "3", D: 4}); got != "/x?a=1&D=4" {
		t.Errorf("QueryURL = %s", got)
	}
	defer func() {
		if recover() == nil {
			t.Error("QueryURL accepted a non-struct")
		}
	}()
	web.QueryURL("/x", 1)
}

func TestPushURL(t *testing.T) {
	w := httptest.NewRecorder()
	web.PushURL(w, "/admin/articles?page=2")
	if w.Header().Get("HX-Push-Url") != "/admin/articles?page=2" {
		t.Errorf("header = %v", w.Header())
	}
}
