package rest_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/cockroachdb/errors"
	"github.com/danielgtaylor/huma/v2"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/httpx/rest"
	"github.com/ryan053857/mygoravel/i18n"
	"github.com/ryan053857/mygoravel/identity"
)

func TestMissingSecurityRejectedBeforeFactory(t *testing.T) {
	for _, mode := range []rest.Mode{rest.ModeServe, rest.ModeOpenAPI} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatal("unmarked endpoint accepted")
				}
			}()
			rest.Register(rest.New(rest.Options{Mode: mode}), huma.Operation{OperationID: "missing", Method: "GET", Path: "/missing"}, func() func(context.Context, *pingIn) (*pingOut, error) { t.Fatal("factory ran"); return nil })
		}()
	}
}
func TestWrappedErrorsAndMapping(t *testing.T) {
	if got := rest.FromError(fmt.Errorf("outer: %w", errors.WithStack(huma.Error404NotFound("safe")))); got.GetStatus() != 404 {
		t.Fatal(got)
	}
	for kind, status := range map[errorx.Kind]int{errorx.InvalidArgument: 422, errorx.Unauthenticated: 401, errorx.PermissionDenied: 403, errorx.NotFound: 404, errorx.Conflict: 409, errorx.Unavailable: 503, errorx.Internal: 500} {
		err := fmt.Errorf("context: %w", errorx.Define(kind, "demo.code").Wrap(huma.Error400BadRequest("secret")))
		if got := rest.FromError(err); got.GetStatus() != status {
			t.Fatalf("kind %v: %v", kind, got)
		}
	}
}
func TestErrorResponsesMatchDocumentedSchema(t *testing.T) {
	api := rest.New(rest.Options{})
	register := func(path string, err error) {
		op := huma.Operation{OperationID: strings.TrimPrefix(path, "/"), Method: "POST", Path: path}
		rest.Public()(&op)
		rest.Register(api, op, func() func(context.Context, *echoIn) (*echoOut, error) {
			return func(context.Context, *echoIn) (*echoOut, error) { return nil, err }
		})
	}
	register("/business", errorx.Define(errorx.NotFound, "example.missing").New())
	register("/native", errors.WithStack(huma.Error409Conflict("already exists")))
	register("/unknown", errors.New("private-secret-diagnostics"))
	schema := api.OpenAPI().Paths["/business"].Post.Responses["default"].Content["application/problem+json"].Schema

	for _, tc := range []struct {
		path, body string
		status     int
		code       string
		detail     string
	}{
		{"/business", `{"secret":"valid"}`, 404, "example.missing", "Not Found"},
		{"/native", `{"secret":"valid"}`, 409, "", "already exists"},
		{"/unknown", `{"secret":"valid"}`, 500, "", "unexpected error occurred"},
		{"/business", `{"secret":"x"}`, 422, "", "validation failed"},
		{"/missing-route", `{}`, 404, "", "route not found"},
	} {
		req := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		api.Handler().ServeHTTP(rr, req)
		var obj map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &obj); err != nil {
			t.Fatal(err)
		}
		result := &huma.ValidateResult{}
		huma.Validate(api.OpenAPI().Components.Schemas, schema, &huma.PathBuffer{}, huma.ModeReadFromServer, obj, result)
		if len(result.Errors) > 0 {
			t.Fatalf("%s: response violates documented schema: %v; %s", tc.path, result.Errors, rr.Body.String())
		}
		code, _ := obj["code"].(string)
		if rr.Code != tc.status || code != tc.code || obj["detail"] != tc.detail || rr.Header().Get("Content-Type") != "application/problem+json" {
			t.Fatalf("%s: %d %s", tc.path, rr.Code, rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), "private-secret-diagnostics") {
			t.Fatal("internal error leaked")
		}
		if tc.status == 422 {
			errs, _ := obj["errors"].([]any)
			if len(errs) == 0 || errs[0].(map[string]any)["location"] != "body.secret" {
				t.Fatal(rr.Body.String())
			}
		}
	}
}

func TestLocalizedBusinessErrorUsesPublicParams(t *testing.T) {
	app := fstest.MapFS{
		"en.yaml":    {Data: []byte("demo.busy: \"Busy, retry in {{.retry}}s\"\n")},
		"zh-CN.yaml": {Data: []byte("demo.busy: \"繁忙，请 {{.retry}} 秒后重试\"\n")},
	}
	bundle, err := i18n.New(i18n.Options{Default: "en", Layers: []i18n.Layer{i18n.FrameworkLayer(), {Priority: i18n.PriorityProduct, FS: app}}})
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	api := rest.New(rest.Options{Logger: slog.New(slog.NewJSONHandler(&logs, nil)), Languages: bundle})
	busy := errorx.Define(errorx.Unavailable, "demo.busy")
	untranslated := errorx.Define(errorx.Conflict, "demo.untranslated")
	for path, err := range map[string]error{"/busy": busy.New(errorx.Public("retry", 3)), "/untranslated": untranslated.New()} {
		op := huma.Operation{OperationID: strings.TrimPrefix(path, "/"), Method: "GET", Path: path}
		rest.Public()(&op)
		rest.Register(api, op, func() func(context.Context, *pingIn) (*pingOut, error) {
			return func(context.Context, *pingIn) (*pingOut, error) { return nil, err }
		})
	}
	for lang, want := range map[string]string{"en": "Busy, retry in 3s", "zh-CN": "繁忙，请 3 秒后重试"} {
		req := httptest.NewRequest("GET", "/busy", nil)
		req.Header.Set("Accept-Language", lang)
		rr := httptest.NewRecorder()
		api.Handler().ServeHTTP(rr, req)
		if rr.Code != 503 || !strings.Contains(rr.Body.String(), `"detail":"`+want+`"`) || !strings.Contains(rr.Body.String(), `"code":"demo.busy"`) {
			t.Fatal(rr.Body.String())
		}
	}
	rr := httptest.NewRecorder()
	api.Handler().ServeHTTP(rr, httptest.NewRequest("GET", "/untranslated", nil))
	if rr.Code != 409 || !strings.Contains(rr.Body.String(), `"detail":"Conflict"`) || !strings.Contains(logs.String(), "missing error translation") {
		t.Fatal(rr.Body.String(), logs.String())
	}
}

func TestBearerRejectsEmptySubjectAndDocumentsNoOAuthScopes(t *testing.T) {
	api := rest.New(rest.Options{})
	op := huma.Operation{OperationID: "auth", Method: http.MethodGet, Path: "/auth"}
	rest.RequireBearer(api, func(huma.Context, string) (identity.Subject, error) { return identity.Subject{}, nil }, "orders")(&op)
	if len(op.Security[0]["bearer"]) != 0 {
		t.Fatal("HTTP bearer cannot carry OAuth scopes")
	}
	rest.Register(api, op, func() func(context.Context, *pingIn) (*pingOut, error) {
		return func(context.Context, *pingIn) (*pingOut, error) {
			t.Fatal("anonymous subject reached handler")
			return nil, nil
		}
	})
	req := httptest.NewRequest("GET", "/auth", nil)
	req.Header.Set("Authorization", "Bearer token")
	rr := httptest.NewRecorder()
	api.Handler().ServeHTTP(rr, req)
	if rr.Code != 401 || !strings.Contains(rr.Body.String(), `"code":"auth.unauthenticated"`) {
		t.Fatal(rr.Code, rr.Body.String())
	}
}

func TestConflictingRouteRejectedBeforeBinding(t *testing.T) {
	for _, mode := range []rest.Mode{rest.ModeServe, rest.ModeOpenAPI} {
		api := rest.New(rest.Options{Mode: mode})
		first := huma.Operation{OperationID: "first", Method: "GET", Path: "/same"}
		rest.Public()(&first)
		rest.Register(api, first, func() func(context.Context, *pingIn) (*pingOut, error) {
			return func(context.Context, *pingIn) (*pingOut, error) { return &pingOut{}, nil }
		})
		second := huma.Operation{OperationID: "second", Method: "GET", Path: "/same"}
		rest.Public()(&second)
		func() {
			defer func() {
				if recover() == nil {
					t.Fatal("route silently overwritten")
				}
			}()
			rest.Register(api, second, func() func(context.Context, *pingIn) (*pingOut, error) {
				t.Fatal("conflict executed factory")
				return nil
			})
		}()
	}
}
func TestLanguageAndLoggerAreInstanceLocal(t *testing.T) {
	var aLog, bLog bytes.Buffer
	for _, tc := range []struct {
		lang     string
		buf      *bytes.Buffer
		expected string
	}{{"en", &aLog, "Authentication required"}, {"zh-CN", &bLog, "请先认证"}} {
		bundle, err := i18n.New(i18n.Options{Default: tc.lang, Layers: []i18n.Layer{i18n.FrameworkLayer()}})
		if err != nil {
			t.Fatal(err)
		}
		api := rest.New(rest.Options{Logger: slog.New(slog.NewJSONHandler(tc.buf, nil)), Languages: bundle})
		op := huma.Operation{OperationID: "fail", Method: "GET", Path: "/fail"}
		rest.Public()(&op)
		rest.Register(api, op, func() func(context.Context, *pingIn) (*pingOut, error) {
			return func(context.Context, *pingIn) (*pingOut, error) {
				return nil, errorx.Define(errorx.Unauthenticated, "auth.unauthenticated").Wrap(errors.New("private-secret-diagnostics"))
			}
		})
		op = huma.Operation{OperationID: "crash", Method: "GET", Path: "/crash"}
		rest.Public()(&op)
		rest.Register(api, op, func() func(context.Context, *pingIn) (*pingOut, error) {
			return func(context.Context, *pingIn) (*pingOut, error) { return nil, errors.New("private-secret-diagnostics") }
		})
		rr := httptest.NewRecorder()
		api.Handler().ServeHTTP(rr, httptest.NewRequest("GET", "/fail", nil))
		if !strings.Contains(rr.Body.String(), tc.expected) {
			t.Fatal(rr.Body.String())
		}
		tc.buf.Reset()
		rr = httptest.NewRecorder()
		api.Handler().ServeHTTP(rr, httptest.NewRequest("GET", "/crash", nil))
		lines := strings.Split(strings.TrimSpace(tc.buf.String()), "\n")
		if rr.Code != 500 || len(lines) != 2 {
			t.Fatal(rr.Body.String(), tc.buf.String())
		}
		for _, line := range lines {
			var entry map[string]any
			if err := json.Unmarshal([]byte(line), &entry); err != nil {
				t.Fatal(err)
			}
			if entry["request_id"] != rr.Header().Get("X-Request-ID") {
				t.Fatal(entry)
			}
			if entry["msg"] == "request failed" && !strings.Contains(line, "private-secret-diagnostics") {
				t.Fatal("error log must carry the cause for diagnostics")
			}
		}
	}
	if strings.Count(aLog.String(), "http request") != 1 || strings.Count(bLog.String(), "http request") != 1 {
		t.Fatal("loggers crossed instances")
	}
}

func TestAccessLogCarriesBusinessCode(t *testing.T) {
	var logs bytes.Buffer
	api := rest.New(rest.Options{Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
	op := huma.Operation{OperationID: "conflict", Method: "GET", Path: "/conflict"}
	rest.Public()(&op)
	rest.Register(api, op, func() func(context.Context, *pingIn) (*pingOut, error) {
		return func(context.Context, *pingIn) (*pingOut, error) {
			return nil, errorx.Define(errorx.Conflict, "demo.conflict").Wrap(errors.New("pq: duplicate key"))
		}
	})
	secured := huma.Operation{OperationID: "secured", Method: "GET", Path: "/secured"}
	rest.RequireBearer(api, func(huma.Context, string) (identity.Subject, error) { return identity.Subject{ID: "u"}, nil })(&secured)
	rest.Register(api, secured, func() func(context.Context, *pingIn) (*pingOut, error) {
		return func(context.Context, *pingIn) (*pingOut, error) { return &pingOut{}, nil }
	})
	for path, code := range map[string]string{"/conflict": "demo.conflict", "/secured": "auth.unauthenticated"} {
		logs.Reset()
		api.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", path, nil))
		lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
		var entry map[string]any
		if len(lines) != 1 || json.Unmarshal([]byte(lines[0]), &entry) != nil {
			t.Fatalf("%s: expected only the access log line, got:\n%s", path, logs.String())
		}
		if entry["msg"] != "http request" || entry["code"] != code {
			t.Fatalf("%s: %v", path, entry)
		}
	}
}
