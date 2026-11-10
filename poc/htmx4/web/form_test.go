package web_test

import (
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/ryan053857/mygoravel/poc/htmx4/web"
)

type article struct {
	Title     string   `json:"title" minLength:"1" maxLength:"20"`
	Slug      string   `json:"slug" pattern:"^[a-z0-9-]+$" patternDescription:"小写字母、数字或连字符"`
	Email     string   `json:"email" format:"email"`
	Priority  int      `json:"priority" minimum:"0" maximum:"10"`
	Rating    float64  `json:"rating,omitempty" minimum:"0"`
	Published bool     `json:"published"`
	Tags      []string `json:"tags,omitempty" maxItems:"3"`
	Note      string   `json:"note,omitempty"`
}

func parse(t *testing.T, form url.Values) (article, web.FieldErrors) {
	t.Helper()
	r := httptest.NewRequest("POST", "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	got, errs, err := web.Form[article](r)
	if err != nil {
		t.Fatal(err)
	}
	return got, errs
}

func TestFormValid(t *testing.T) {
	got, errs := parse(t, url.Values{
		"title": {"Hello"}, "slug": {"hello-world"}, "email": {"a@b.co"}, "priority": {" 3 "},
		"published": {"on"}, "tags": {"go", "web"}, "unknown": {"ignored"},
	})
	if errs != nil {
		t.Fatalf("errs=%v", errs)
	}
	want := article{Title: "Hello", Slug: "hello-world", Email: "a@b.co", Priority: 3, Published: true, Tags: []string{"go", "web"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}

func TestFormErrors(t *testing.T) {
	got, errs := parse(t, url.Values{
		"title": {""}, "slug": {"Hello World"}, "email": {"nope"}, "priority": {"abc"},
		"rating": {""}, "tags": {"a", "b", "c", "d"},
	})
	want := web.FieldErrors{
		"title":    "不能为空",
		"slug":     "必须是小写字母、数字或连字符",
		"email":    "邮箱格式不正确",
		"priority": "必须是整数",
		"tags":     "最多选择 3 项",
	}
	if !reflect.DeepEqual(errs, want) {
		t.Fatalf("errs=%#v", errs)
	}
	// 能转换的值保留下来，用于重新渲染表单。
	if got.Slug != "Hello World" || got.Email != "nope" || got.Published {
		t.Fatalf("got %+v", got)
	}
}

func TestFormRequired(t *testing.T) {
	_, errs := parse(t, url.Values{"slug": {"x"}, "email": {"a@b.co"}, "priority": {""}})
	if errs["title"] != "必填" || errs["priority"] != "必填" || len(errs) != 2 {
		t.Fatalf("errs=%#v", errs)
	}
}

func TestFormRange(t *testing.T) {
	_, errs := parse(t, url.Values{"title": {strings.Repeat("长", 21)}, "slug": {"x"}, "email": {"a@b.co"}, "priority": {"11"}})
	if errs["title"] != "最多 20 个字符" || errs["priority"] != "不能大于 10" {
		t.Fatalf("errs=%#v", errs)
	}
}
