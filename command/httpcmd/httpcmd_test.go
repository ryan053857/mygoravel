package httpcmd_test

import (
	"bytes"
	"context"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"
	"github.com/spf13/cobra"

	"github.com/ryan053857/mygoravel/command"
	"github.com/ryan053857/mygoravel/command/httpcmd"
	"github.com/ryan053857/mygoravel/httpx/rest"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/web"
)

func newRoot(cmds ...*cobra.Command) *cobra.Command {
	root := command.Root("api", "test")
	root.AddCommand(cmds...)
	return root
}

func execute(root *cobra.Command, args ...string) (stdout, stderr string, err error) {
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)
	err = root.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}

type input struct{}
type output struct{ Body string }

// registerRoutes 模拟应用的统一路由注册；任何运行时处理器或认证被调用都会使测试失败。
func registerRoutes(t *testing.T) func(*rest.API) {
	return func(api *rest.API) {
		if api.Mode() != rest.ModeOpenAPI {
			t.Fatal("routes must be registered in OpenAPI mode")
		}
		handler := func(context.Context, *input) (*output, error) {
			t.Fatal("handler must not run")
			return nil, nil
		}
		create := huma.Operation{OperationID: "article-create", Method: http.MethodPost, Path: "/articles", Summary: "创建文章"}
		rest.RequireBearer(api, func(context.Context, string) (identity.Subject, error) {
			t.Fatal("token validator must not run")
			return identity.Subject{}, nil
		}, "articles")(&create)
		rest.Register(api, create, handler)
		list := huma.Operation{OperationID: "article-list", Method: http.MethodGet, Path: "/articles", Summary: "获取文章\t\n列表"}
		rest.Public()(&list)
		rest.Register(api, list, handler)
		api.Router().Get("/livez", func(http.ResponseWriter, *http.Request) {
			t.Fatal("native handler must not run")
		})
	}
}

// docRouter 模拟应用的 DocRouter：JSON API 以文档模式注册，页面以 ModeRoutes 注册。
func docRouter(t *testing.T) func() chi.Routes {
	return func() chi.Routes {
		router := chi.NewRouter()
		registerRoutes(t)(rest.New(rest.Options{Mode: rest.ModeOpenAPI, Router: router}))
		pages := web.New(web.Options{Mode: web.ModeRoutes})
		page := func(http.ResponseWriter, *http.Request) error {
			t.Fatal("page handler must not run")
			return nil
		}
		pages.Routes(router, web.Public).Get("/login", page)
		pages.Routes(router, web.Require("articles")).Get("/admin/articles/{id}/edit", page)
		return router
	}
}

func TestOpenAPI(t *testing.T) {
	root := newRoot(httpcmd.OpenAPI(rest.Options{Title: "Blog API", Version: "1.0.0"}, registerRoutes(t)))
	// openapi 不读取配置文件
	out, _, err := execute(root, "openapi", "--config", filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"title":"Blog API"`, `"/articles"`, `"article-create"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %s", want, out)
		}
	}
}

func TestOpenAPISpec30(t *testing.T) {
	root := newRoot(httpcmd.OpenAPI(rest.Options{Title: "Blog API", Version: "1.0.0"}, registerRoutes(t)))
	out, _, err := execute(root, "openapi", "--spec", "3.0")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"openapi":"3.0.3"`, `"article-create"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %s", want, out)
		}
	}
}

func TestRoutes(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want []string
		omit []string
	}{
		{"default", nil, []string{"GET /articles article-list public 获取文章 列表", "POST /articles article-create bearer: articles 创建文章", "GET /livez - unknown -", "GET /login - public -"}, []string{"/docs", "/openapi", "/schemas"}},
		{"all", []string{"--all"}, []string{"GET /articles article-list public 获取文章 列表", "GET /docs - docs -", "GET /openapi.json - docs -", "GET /schemas/{schema} - docs -"}, nil},
		{"method", []string{"--method", "post"}, []string{"POST /articles article-create bearer: articles 创建文章"}, []string{"article-list", "/livez", "/docs"}},
		{"path", []string{"--path", "live"}, []string{"GET /livez - unknown -"}, []string{"article-create", "article-list", "/docs"}},
		{"pages", []string{"--path", "/admin"}, []string{"GET /admin/articles/{id}/edit - session: articles -"}, []string{"/login", "article-create"}},
		{"combined", []string{"--path", "articles", "--method", "get"}, []string{"GET /articles article-list public 获取文章 列表"}, []string{"article-create", "/docs"}},
		{"empty", []string{"--path", "missing"}, nil, []string{"article-create", "article-list", "/docs", "/livez"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := newRoot(httpcmd.Routes(docRouter(t)))
			out, stderr, err := execute(root, append([]string{"routes"}, tc.args...)...)
			if err != nil || stderr != "" {
				t.Fatalf("err=%v stderr=%s", err, stderr)
			}
			lines := strings.Split(strings.TrimSpace(out), "\n")
			for i := range lines {
				lines[i] = strings.Join(strings.Fields(lines[i]), " ")
			}
			got := strings.Join(lines, "\n")
			if lines[0] != "METHOD PATH OPERATION ID ACCESS SUMMARY" {
				t.Fatalf("missing header: %s", got)
			}
			for _, want := range tc.want {
				if !slices.Contains(lines, want) {
					t.Errorf("missing %q in %s", want, got)
				}
			}
			for _, omit := range tc.omit {
				if strings.Contains(got, omit) {
					t.Errorf("unexpected %q in %s", omit, got)
				}
			}
		})
	}
}

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"routes", "extra"},
		{"routes", "--unknown"},
		{"routes", "--path"},
		{"openapi", "extra"},
		{"openapi", "--spec", "2.0"},
	} {
		mustNotRegister := func(*rest.API) { t.Fatal("invalid arguments built the API") }
		mustNotBuild := func() chi.Routes { t.Fatal("invalid arguments built the router"); return nil }
		root := newRoot(httpcmd.OpenAPI(rest.Options{}, mustNotRegister), httpcmd.Routes(mustNotBuild))
		if _, _, err := execute(root, args...); command.ExitCode(err) != command.ExitUsage {
			t.Errorf("args=%v exit=%d err=%v", args, command.ExitCode(err), err)
		}
	}
}
