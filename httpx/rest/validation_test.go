package rest_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/httpx/rest"
	"github.com/ryan053857/mygoravel/i18n"
)

type articleIn struct {
	Limit int `query:"limit" minimum:"1"`
	Body  struct {
		Title string `json:"title" minLength:"1" maxLength:"5"`
		Slug  string `json:"slug" pattern:"^[a-z-]+$" patternDescription:"小写字母和连字符"`
		Email string `json:"email" format:"email"`
	}
}

type problem struct {
	Detail string `json:"detail"`
	Code   string `json:"code"`
	Errors []struct {
		Code     string `json:"code"`
		Message  string `json:"message"`
		Location string `json:"location"`
	} `json:"errors"`
}

func validationAPI(t *testing.T, lang string) *rest.API {
	t.Helper()
	bundle, err := i18n.New(lang)
	if err != nil {
		t.Fatal(err)
	}
	api := withLanguages(rest.Options{}, bundle)
	op := huma.Operation{OperationID: "article-create", Method: http.MethodPost, Path: "/articles"}
	rest.Public()(&op)
	rest.Register(api, op, func(context.Context, *articleIn) (*pingOut, error) {
		return nil, errorx.Define(errorx.Conflict, "article.slug_taken").New()
	})
	return api
}

func post(api *rest.API, query, body string) (int, problem) {
	req := httptest.NewRequest(http.MethodPost, "/articles"+query, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	api.Handler().ServeHTTP(rr, req)
	var p problem
	_ = json.Unmarshal(rr.Body.Bytes(), &p)
	return rr.Code, p
}

func fieldErrors(p problem) map[string][2]string {
	got := map[string][2]string{}
	for _, e := range p.Errors {
		got[e.Location] = [2]string{e.Code, e.Message}
	}
	return got
}

func TestValidationErrorsTranslated(t *testing.T) {
	api := validationAPI(t, "zh-CN")
	status, p := post(api, "?limit=0", `{"title":"","slug":"Hello World","email":"nope"}`)
	if status != http.StatusUnprocessableEntity || p.Detail != "提交的内容有误" || p.Code != "" {
		t.Fatalf("%d %+v", status, p)
	}
	want := map[string][2]string{
		"query.limit": {"validation.minimum", "不能小于 1"},
		"body.title":  {"validation.filled", "不能为空"},
		"body.slug":   {"validation.pattern_description", "必须是小写字母和连字符"},
		"body.email":  {"validation.email", "邮箱格式不正确"},
	}
	if got := fieldErrors(p); !reflect.DeepEqual(got, want) {
		t.Fatalf("errors: %v", got)
	}

	// 必填错误的位置补上字段名；未知字段和参数格式错误同样翻译。
	_, p = post(api, "?limit=abc", `{"title":"toolong","extra":1}`)
	want = map[string][2]string{
		"query.limit": {"validation.integer", "必须是整数"},
		"body.title":  {"validation.max_length", "最多 5 个字符"},
		"body.slug":   {"validation.required", "必填"},
		"body.email":  {"validation.required", "必填"},
		"body.extra":  {"validation.unexpected", "不支持这个字段"},
	}
	if got := fieldErrors(p); !reflect.DeepEqual(got, want) {
		t.Fatalf("errors: %v", got)
	}

	// 业务错误不受影响。
	status, p = post(api, "?limit=1", `{"title":"ok","slug":"ok","email":"a@example.com"}`)
	if status != http.StatusConflict || p.Code != "article.slug_taken" || p.Errors != nil {
		t.Fatalf("%d %+v", status, p)
	}
}

func TestValidationErrorsEnglish(t *testing.T) {
	_, p := post(validationAPI(t, "en"), "?limit=0", `{"title":"","slug":"ok","email":"a@example.com"}`)
	want := map[string][2]string{
		"query.limit": {"validation.minimum", "Must be at least 1"},
		"body.title":  {"validation.filled", "This field must not be empty"},
	}
	if got := fieldErrors(p); !reflect.DeepEqual(got, want) || p.Detail != "Validation failed" {
		t.Fatalf("%s %v", p.Detail, got)
	}
}

func TestValidationErrorsWithoutLocale(t *testing.T) {
	// 没有安装 httpx.Locale：提示保持 Huma 的原文，仍然带有规则。
	api := rest.New(rest.Options{})
	op := huma.Operation{OperationID: "article-create", Method: http.MethodPost, Path: "/articles"}
	rest.Public()(&op)
	rest.Register(api, op, func(context.Context, *articleIn) (*pingOut, error) { return &pingOut{}, nil })
	_, p := post(api, "", `{"title":"","slug":"ok","email":"a@example.com"}`)
	want := map[string][2]string{"body.title": {"validation.filled", "expected length >= 1"}}
	if got := fieldErrors(p); !reflect.DeepEqual(got, want) || p.Detail != "validation failed" {
		t.Fatalf("%s %v", p.Detail, got)
	}
}

func TestErrorDetailSchema(t *testing.T) {
	api := validationAPI(t, "en")
	doc, err := api.OpenAPIJSON()
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Items struct {
						Ref string `json:"$ref"`
					} `json:"items"`
				} `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(doc, &spec); err != nil {
		t.Fatal(err)
	}
	schemas := spec.Components.Schemas
	if ref := schemas["ErrorModel"].Properties["errors"].Items.Ref; ref != "#/components/schemas/ErrorDetail" {
		t.Fatalf("errors items: %q", ref)
	}
	for _, p := range []string{"code", "message", "location", "value"} {
		if _, ok := schemas["ErrorDetail"].Properties[p]; !ok {
			t.Errorf("ErrorDetail has no %s", p)
		}
	}
}
