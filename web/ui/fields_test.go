package ui_test

import (
	"bytes"
	"html"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/a-h/templ"

	"github.com/ryan053857/mygoravel/page"
	"github.com/ryan053857/mygoravel/web"
	"github.com/ryan053857/mygoravel/web/ui"
)

type articleForm struct {
	Title      string   `json:"title" minLength:"1"`
	Slug       string   `json:"slug,omitempty"`
	Password   string   `json:"password,omitempty"`
	Body       string   `json:"body,omitempty"`
	Status     string   `json:"status,omitempty" enum:"draft,published"`
	Pinned     bool     `json:"pinned"`
	Notify     bool     `json:"notify"`
	Tags       []string `json:"tags,omitempty"`
	Visibility string   `json:"visibility,omitempty"`
}

// submitted 返回提交后的表单：title 为空（验证失败），其余字段有值。
func submitted(t *testing.T) *web.Form[articleForm] {
	t.Helper()
	v := url.Values{
		"title": {""}, "slug": {"hello-world"}, "password": {"secret"}, "body": {"<b>正文</b>"},
		"status": {"published"}, "pinned": {"on"}, "tags": {"go", "htmx"}, "visibility": {"team"},
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader(v.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	f, err := web.ParseForm[articleForm](r)
	if err != nil {
		t.Fatal(err)
	}
	if f.Error("title") == "" {
		t.Fatal("title should be invalid")
	}
	return f
}

func render(t *testing.T, c templ.Component) string {
	t.Helper()
	var b bytes.Buffer
	if err := c.Render(t.Context(), &b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// tag 返回 html 中 id 为 id 的元素的开始标签。
func tag(t *testing.T, html, id string) string {
	t.Helper()
	m := regexp.MustCompile(`<[a-z]+[^>]*\sid="` + regexp.QuoteMeta(id) + `"[^>]*>`).FindString(html)
	if m == "" {
		t.Fatalf("no element #%s in\n%s", id, html)
	}
	return m
}

func contains(t *testing.T, s string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(s, w) {
			t.Errorf("missing %s in\n%s", w, s)
		}
	}
}

func lacks(t *testing.T, s string, unwanted ...string) {
	t.Helper()
	for _, w := range unwanted {
		if strings.Contains(s, w) {
			t.Errorf("unexpected %s in\n%s", w, s)
		}
	}
}

func TestTextField(t *testing.T) {
	f := submitted(t)

	// 有错误：字段与输入框标记为无效，显示错误提示而不是说明。
	out := render(t, ui.TextField(f, "title", "标题", ui.FieldProps{Description: "最多 200 字"}))
	contains(t, out, `data-invalid="true"`, `for="title"`, `data-slot="field-error"`, html.EscapeString(f.Error("title")))
	contains(t, tag(t, out, "title"), `name="title"`, `type="text"`, `aria-invalid="true"`)
	lacks(t, out, "最多 200 字")

	// 没有错误：回填输入，显示说明，没有 invalid 属性。
	out = render(t, ui.TextField(f, "slug", "Slug", ui.FieldProps{Description: "小写字母", Required: true, Attributes: templ.Attributes{"autocomplete": "off"}}))
	contains(t, tag(t, out, "slug"), `value="hello-world"`, `required`, `autocomplete="off"`)
	contains(t, out, "小写字母")
	lacks(t, out, "invalid")

	// 密码不回填；ID 可以与字段名不同。
	out = render(t, ui.TextField(f, "password", "密码", ui.FieldProps{Type: "password", ID: "pw"}))
	contains(t, tag(t, out, "pw"), `name="password"`, `type="password"`)
	lacks(t, out, "secret")
}

func TestTextareaField(t *testing.T) {
	html := render(t, ui.TextareaField(submitted(t), "body", "正文", ui.FieldProps{Rows: 8, Required: true}))
	contains(t, tag(t, html, "body"), `name="body"`, `rows="8"`, `required`)
	contains(t, html, "&lt;b&gt;正文&lt;/b&gt;</textarea>")
}

func TestSelectField(t *testing.T) {
	options := []ui.Option{{Value: "draft", Label: "草稿"}, {Value: "published", Label: "已发布"}, {Value: "archived", Label: "已归档", Disabled: true}}
	html := render(t, ui.SelectField(submitted(t), "status", "状态", options, ui.FieldProps{Placeholder: "请选择"}))
	contains(t, tag(t, html, "status"), `name="status"`)
	if !regexp.MustCompile(`<option[^>]*value=""[^>]*>\s*请选择`).MatchString(html) {
		t.Errorf("placeholder option missing:\n%s", html)
	}
	if !regexp.MustCompile(`<option[^>]*value="published"[^>]*selected`).MatchString(html) || regexp.MustCompile(`<option[^>]*value="draft"[^>]*selected`).MatchString(html) {
		t.Errorf("selected option wrong:\n%s", html)
	}
	if !regexp.MustCompile(`<option[^>]*value="archived"[^>]*disabled`).MatchString(html) {
		t.Errorf("disabled option:\n%s", html)
	}
}

func TestCheckboxAndSwitchFields(t *testing.T) {
	f := submitted(t)
	contains(t, tag(t, render(t, ui.CheckboxField(f, "pinned", "置顶")), "pinned"), `type="checkbox"`, `name="pinned"`, "checked")
	lacks(t, tag(t, render(t, ui.CheckboxField(f, "notify", "通知")), "notify"), "checked")
	html := render(t, ui.SwitchField(f, "pinned", "置顶", ui.FieldProps{Description: "显示在首页顶部"}))
	contains(t, tag(t, html, "pinned"), `name="pinned"`, "checked")
	contains(t, html, `data-slot="switch"`, "显示在首页顶部")
}

func TestRadioGroupField(t *testing.T) {
	options := []ui.Option{{Value: "private", Label: "仅自己"}, {Value: "team", Label: "团队"}}
	html := render(t, ui.RadioGroupField(submitted(t), "visibility", "可见范围", options))
	lacks(t, tag(t, html, "visibility-0"), "checked")
	contains(t, tag(t, html, "visibility-1"), `type="radio"`, `name="visibility"`, `value="team"`, "checked")
	contains(t, html, `for="visibility-1"`, "团队", "<legend")
}

func TestCheckboxGroupField(t *testing.T) {
	f := submitted(t)
	options := []ui.Option{{Value: "go", Label: "Go"}, {Value: "rust", Label: "Rust"}, {Value: "htmx", Label: "htmx"}}
	html := render(t, ui.CheckboxGroupField(f, "tags", "标签", options))
	for i, want := range []bool{true, false, true} {
		el := tag(t, html, "tags-"+string(rune('0'+i)))
		contains(t, el, `name="tags"`, `value="`+options[i].Value+`"`)
		if strings.Contains(el, " checked") != want {
			t.Errorf("tags-%d checked = %v: %s", i, !want, el)
		}
	}
}

// 首次渲染（web.NewForm）时字段回填已有数据。
func TestFieldsWithNewForm(t *testing.T) {
	f := web.NewForm(articleForm{Title: "Hello", Pinned: true, Tags: []string{"rust"}, Visibility: "private"})
	contains(t, tag(t, render(t, ui.TextField(f, "title", "标题")), "title"), `value="Hello"`)
	contains(t, tag(t, render(t, ui.CheckboxField(f, "pinned", "置顶")), "pinned"), "checked")
	contains(t, tag(t, render(t, ui.CheckboxGroupField(f, "tags", "标签", []ui.Option{{Value: "go"}, {Value: "rust"}})), "tags-1"), "checked")
	contains(t, tag(t, render(t, ui.RadioGroupField(f, "visibility", "可见范围", []ui.Option{{Value: "private"}, {Value: "team"}})), "visibility-0"), "checked")
}

func TestPagination(t *testing.T) {
	url := func(n int) string { return "/admin/articles?page=" + string(rune('0'+n)) }
	list := page.NewOffset([]int{}, page.OffsetParams{Page: 2, PerPage: 20}, 57) // 共 3 页

	html := render(t, ui.Pagination(list, ui.PaginationProps{URL: url, HTMX: true}))
	contains(t, html, `aria-current="page"`, `hx-get="/admin/articles?page=3"`, "上一页", "下一页")
	if m := regexp.MustCompile(`<a [^>]*aria-current="page"[^>]*>`).FindString(html); !strings.Contains(m, `href="/admin/articles?page=2"`) {
		t.Errorf("current page link: %s", m)
	}
	if n := strings.Count(html, `data-slot="pagination-link"`); n != 5 { // 上一页、1、2、3、下一页
		t.Errorf("%d links:\n%s", n, html)
	}

	// 没有 HTMX：普通链接；第一页时「上一页」不可用。
	first := page.NewOffset([]int{}, page.OffsetParams{Page: 1, PerPage: 20}, 57)
	html = render(t, ui.Pagination(first, ui.PaginationProps{URL: url, Previous: "Prev"}))
	lacks(t, html, "hx-get")
	if !regexp.MustCompile(`<button[^>]*disabled[^>]*>[\s\S]*?Prev`).MatchString(html) {
		t.Errorf("previous should be disabled on the first page:\n%s", html)
	}

	// 只有一页时不显示。
	if html := render(t, ui.Pagination(page.NewOffset([]int{1}, page.OffsetParams{Page: 1, PerPage: 20}, 1), ui.PaginationProps{URL: url})); html != "" {
		t.Errorf("single page rendered %q", html)
	}
}
