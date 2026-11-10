package web_test

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/ryan053857/mygoravel/httpx/web"
)

type articleInput struct {
	Title     string   `json:"title" minLength:"1" maxLength:"20"`
	Slug      string   `json:"slug" pattern:"^[a-z0-9-]+$" patternDescription:"小写字母、数字或连字符"`
	Email     string   `json:"email" format:"email"`
	Priority  int      `json:"priority" minimum:"0" maximum:"10"`
	Rating    float64  `json:"rating,omitempty" minimum:"0"`
	Published bool     `json:"published"`
	Tags      []string `json:"tags,omitempty" maxItems:"3"`
	Note      string   `json:"note,omitempty"`
}

func postForm(t *testing.T, form url.Values) *http.Request {
	t.Helper()
	r := httptest.NewRequest("POST", "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return zh(t, r)
}

func parse(t *testing.T, r *http.Request) *web.Form[articleInput] {
	t.Helper()
	f, err := web.ParseForm[articleInput](r)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestParseFormValid(t *testing.T) {
	f := parse(t, postForm(t, url.Values{
		"title": {"Hello"}, "slug": {"hello-world"}, "email": {"a@b.co"}, "priority": {" 3 "},
		"published": {"on"}, "tags": {"go", "web"}, "unknown": {"ignored"},
	}))
	if !f.Valid() {
		t.Fatalf("errors: %v", f.Errors())
	}
	want := articleInput{Title: "Hello", Slug: "hello-world", Email: "a@b.co", Priority: 3, Published: true, Tags: []string{"go", "web"}}
	if !reflect.DeepEqual(f.Data, want) {
		t.Fatalf("data: %+v", f.Data)
	}
	if f.Value("priority") != " 3 " || !f.Checked("published") || !reflect.DeepEqual(f.Values("tags"), []string{"go", "web"}) {
		t.Fatal("raw values not kept")
	}
}

func TestParseFormErrors(t *testing.T) {
	f := parse(t, postForm(t, url.Values{
		"title": {""}, "slug": {"Hello World"}, "email": {"nope"}, "priority": {"abc"},
		"rating": {""}, "tags": {"a", "b", "c", "d"},
	}))
	want := map[string]string{
		"title":    "不能为空",
		"slug":     "必须是小写字母、数字或连字符",
		"email":    "邮箱格式不正确",
		"priority": "必须是整数",
		"tags":     "最多选择 3 项",
	}
	if f.Valid() || !reflect.DeepEqual(f.Errors(), want) {
		t.Fatalf("errors: %#v", f.Errors())
	}
	// 类型错误的原始输入保留下来用于回填，能转换的值照常解码。
	if f.Value("priority") != "abc" || f.Data.Priority != 0 || f.Data.Slug != "Hello World" || f.Checked("published") {
		t.Fatalf("value=%q data=%+v", f.Value("priority"), f.Data)
	}
}

func TestParseFormRequiredAndRange(t *testing.T) {
	f := parse(t, postForm(t, url.Values{"slug": {"x"}, "email": {"a@b.co"}, "priority": {""}}))
	if f.Error("title") != "必填" || f.Error("priority") != "必填" || len(f.Errors()) != 2 {
		t.Fatalf("errors: %#v", f.Errors())
	}
	f = parse(t, postForm(t, url.Values{"title": {strings.Repeat("长", 21)}, "slug": {"x"}, "email": {"a@b.co"}, "priority": {"11"}}))
	if f.Error("title") != "最多 20 个字符" || f.Error("priority") != "不能大于 10" {
		t.Fatalf("errors: %#v", f.Errors())
	}
}

func TestParseFormWithoutLocaleKeepsHumaMessages(t *testing.T) {
	r := httptest.NewRequest("POST", "/", strings.NewReader("title=&slug=x&email=a@b.co&priority=1"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	f := parse(t, r)
	if f.Error("title") != "expected length >= 1" {
		t.Fatalf("errors: %#v", f.Errors())
	}
}

func TestParseFormMultipart(t *testing.T) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, v := range map[string]string{"title": "Hi", "slug": "hi", "email": "a@b.co", "priority": "1"} {
		_ = mw.WriteField(k, v)
	}
	fw, _ := mw.CreateFormFile("cover", "cover.png")
	_, _ = fw.Write([]byte("png"))
	_ = mw.Close()
	r := httptest.NewRequest("POST", "/", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	f := parse(t, r)
	if !f.Valid() || f.Data.Title != "Hi" {
		t.Fatalf("errors=%v data=%+v", f.Errors(), f.Data)
	}
	if _, _, err := r.FormFile("cover"); err != nil {
		t.Fatalf("file not available: %v", err)
	}
}

func TestParseFormBadBody(t *testing.T) {
	r := httptest.NewRequest("POST", "/", strings.NewReader("x"))
	r.Header.Set("Content-Type", "multipart/form-data; boundary=missing")
	if _, err := web.ParseForm[articleInput](r); err == nil {
		t.Fatal("expected error")
	}
}

func TestNewFormAndSetError(t *testing.T) {
	f := web.NewForm(articleInput{Title: "Hello", Priority: 3, Rating: 4.5, Published: true, Tags: []string{"a", "b"}})
	if f.Value("title") != "Hello" || f.Value("priority") != "3" || f.Value("rating") != "4.5" || !f.Checked("published") ||
		!reflect.DeepEqual(f.Values("tags"), []string{"a", "b"}) || !f.Valid() {
		t.Fatalf("values: %q %q %q %v", f.Value("title"), f.Value("priority"), f.Value("rating"), f.Values("tags"))
	}
	if web.NewForm(articleInput{}).Checked("published") {
		t.Fatal("false bool should be unchecked")
	}
	f.SetError("slug", "已被占用")
	if f.Valid() || f.Error("slug") != "已被占用" {
		t.Fatal("SetError")
	}
}
