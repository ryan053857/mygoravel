package command_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/danielgtaylor/huma/v2"
	"github.com/spf13/cobra"

	"github.com/ryan053857/mygoravel/command"
	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/httpx/rest"
	"github.com/ryan053857/mygoravel/identity"
)

type settings struct {
	Name     string          `yaml:"name"`
	Database database.Config `yaml:"database"`
}

func (s settings) Validate() error {
	if s.Name == "" {
		return fmt.Errorf("name is required")
	}
	return nil
}

// writeConfig 在临时目录中写入配置文件并返回路径。
func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func execute(root *cobra.Command, args ...string) (stdout, stderr string, err error) {
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)
	err = root.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}

const minimalConfig = "name: blog\ndatabase:\n  dsn: postgres://localhost/blog\n"

// newRoot 返回装配了 cmds 的根命令。
func newRoot(cmds ...*cobra.Command) *cobra.Command {
	root := command.Root("api", "test")
	root.AddCommand(cmds...)
	return root
}

func dbConfig(s settings) database.Config { return s.Database }

type input struct{}
type output struct{ Body string }

// registerRoutes 模拟应用的统一路由注册；任何运行时处理器或认证被调用都会使测试失败。
func registerRoutes(t *testing.T) func(*rest.API) {
	return func(api *rest.API) {
		if api.Mode() != rest.ModeOpenAPI {
			t.Fatal("routes must be registered in OpenAPI mode")
		}
		factory := func() func(context.Context, *input) (*output, error) {
			t.Fatal("handler factory must not run")
			return nil
		}
		create := huma.Operation{OperationID: "article-create", Method: http.MethodPost, Path: "/articles", Summary: "创建文章"}
		rest.RequireBearer(api, func(huma.Context, string) (identity.Subject, error) {
			t.Fatal("token validator must not run")
			return identity.Subject{}, nil
		}, "articles")(&create)
		rest.Register(api, create, factory)
		list := huma.Operation{OperationID: "article-list", Method: http.MethodGet, Path: "/articles", Summary: "获取文章\t\n列表"}
		rest.Public()(&list)
		rest.Register(api, list, factory)
		api.Router().Get("/livez", func(http.ResponseWriter, *http.Request) {
			t.Fatal("native handler must not run")
		})
	}
}

func TestRootHasNoCommandsByDefault(t *testing.T) {
	root := command.Root("api", "test")
	if len(root.Commands()) != 0 {
		t.Fatalf("commands = %v", root.Commands())
	}
	out, _, err := execute(root)
	if err != nil || !strings.Contains(out, "--config") {
		t.Fatalf("help out=%q err=%v", out, err)
	}
}

func TestRunReceivesConfigAndContext(t *testing.T) {
	path := writeConfig(t, minimalConfig)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var got settings
	root := newRoot(command.Run("work", "worker", func(runCtx context.Context, cfg settings) error {
		if runCtx != ctx {
			t.Fatal("run did not receive the command context")
		}
		got = cfg
		return nil
	}))
	root.SetArgs([]string{"work", "--config", path})
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatal(err)
	}
	if got.Name != "blog" {
		t.Fatalf("cfg = %+v", got)
	}
}

func TestConfigErrorsExitWithConfigCode(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	invalid := writeConfig(t, "name: ''\n")
	for _, args := range [][]string{
		{"config", "check", "--config", missing},
		{"config", "check", "--config", invalid},
		{"config", "show", "--config", missing},
		{"serve", "--config", missing},
		{"migrate", "status", "--config", missing},
	} {
		root := newRoot(
			command.Config[settings](),
			command.Run("serve", "serve", func(context.Context, settings) error {
				t.Fatal("serve ran with invalid config")
				return nil
			}),
			command.Migrate(fstest.MapFS{}, dbConfig),
		)
		if _, _, err := execute(root, args...); command.ExitCode(err) != command.ExitConfig {
			t.Errorf("args=%v exit=%d err=%v", args, command.ExitCode(err), err)
		}
	}
}

func TestConfigCheckAndShow(t *testing.T) {
	path := writeConfig(t, "name: blog\ndatabase:\n  dsn: postgres://localhost/blog\n")
	root := func() *cobra.Command { return newRoot(command.Config[settings]()) }

	if out, _, err := execute(root(), "config", "check", "--config", path); err != nil || out != "" {
		t.Fatalf("check out=%q err=%v", out, err)
	}
	out, _, err := execute(root(), "config", "show", "--config", path)
	if err != nil || !strings.Contains(out, "name: blog\n") || !strings.Contains(out, "dsn: postgres://localhost/blog") {
		t.Fatalf("yaml out=%q err=%v", out, err)
	}
	out, _, err = execute(root(), "config", "show", "--format", "json", "--config", path)
	if err != nil || !strings.Contains(out, `"Name": "blog"`) {
		t.Fatalf("json out=%q err=%v", out, err)
	}
	if _, _, err := execute(root(), "config", "show", "--format", "toml", "--config", path); command.ExitCode(err) != command.ExitUsage {
		t.Fatalf("invalid format exit=%d err=%v", command.ExitCode(err), err)
	}
}

func TestLoadConfigInCustomCommand(t *testing.T) {
	path := writeConfig(t, minimalConfig)
	root := newRoot(&cobra.Command{Use: "hello", RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := command.LoadConfig[settings](cmd)
		if err != nil {
			return err
		}
		cmd.Println("hello", cfg.Name)
		return nil
	}})
	out, _, err := execute(root, "hello", "--config", path)
	if err != nil || out != "hello blog\n" {
		t.Fatalf("out=%q err=%v", out, err)
	}

	orphan := &cobra.Command{Use: "orphan"}
	if _, err := command.LoadConfig[settings](orphan); err == nil {
		t.Fatal("LoadConfig without --config flag succeeded")
	}
}

func TestOpenAPI(t *testing.T) {
	root := newRoot(command.OpenAPI(rest.Options{Title: "Blog API", Version: "1.0.0"}, registerRoutes(t)))
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

func TestRoutes(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want []string
		omit []string
	}{
		{"default", nil, []string{"GET /articles article-list public 获取文章 列表", "POST /articles article-create bearer: articles 创建文章", "GET /livez - unknown -"}, []string{"/docs", "/openapi", "/schemas"}},
		{"all", []string{"--all"}, []string{"GET /articles article-list public 获取文章 列表", "GET /docs - docs -", "GET /openapi.json - docs -", "GET /schemas/{schema} - docs -"}, nil},
		{"method", []string{"--method", "post"}, []string{"POST /articles article-create bearer: articles 创建文章"}, []string{"article-list", "/livez", "/docs"}},
		{"path", []string{"--path", "live"}, []string{"GET /livez - unknown -"}, []string{"article-create", "article-list", "/docs"}},
		{"combined", []string{"--path", "articles", "--method", "get"}, []string{"GET /articles article-list public 获取文章 列表"}, []string{"article-create", "/docs"}},
		{"empty", []string{"--path", "missing"}, nil, []string{"article-create", "article-list", "/docs", "/livez"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := newRoot(command.Routes(rest.Options{}, registerRoutes(t)))
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
		{"unknown"},
		{"--unknown"},
		{"routes", "extra"},
		{"routes", "--unknown"},
		{"routes", "--path"},
		{"openapi", "extra"},
		{"serve", "extra"},
		{"config", "extra"},
		{"config", "check", "extra"},
		{"migrate", "up", "extra"},
	} {
		mustNotRegister := func(*rest.API) { t.Fatal("invalid arguments built the API") }
		root := newRoot(
			command.OpenAPI(rest.Options{}, mustNotRegister),
			command.Routes(rest.Options{}, mustNotRegister),
			command.Run("serve", "serve", func(context.Context, settings) error {
				t.Fatal("invalid arguments ran serve")
				return nil
			}),
			command.Migrate(fstest.MapFS{}, dbConfig),
			command.Config[settings](),
		)
		if _, _, err := execute(root, args...); command.ExitCode(err) != command.ExitUsage {
			t.Errorf("args=%v exit=%d err=%v", args, command.ExitCode(err), err)
		}
	}
}

func TestMigrateReportsUnavailableDatabase(t *testing.T) {
	// 端口 1 上没有 PostgreSQL，连接会立即被拒绝
	path := writeConfig(t, "name: blog\ndatabase:\n  dsn: postgres://postgres@127.0.0.1:1/blog?sslmode=disable&connect_timeout=2\n")
	root := newRoot(command.Migrate(fstest.MapFS{}, dbConfig))
	if _, _, err := execute(root, "migrate", "status", "--config", path); command.ExitCode(err) != command.ExitUnavailable {
		t.Fatalf("exit=%d err=%v", command.ExitCode(err), err)
	}
}

func TestExitCode(t *testing.T) {
	unavailable := errorx.Define(errorx.Unavailable, "demo.db")
	for _, tc := range []struct {
		err  error
		want int
	}{
		{nil, 0},
		{command.AsExitError(command.ExitConfig, unavailable.New()), command.ExitConfig},
		{fmt.Errorf("sync: %w", unavailable.Wrap(context.DeadlineExceeded)), command.ExitUnavailable},
		{errorx.Define(errorx.NotFound, "demo.missing").New(), command.ExitFailure},
		{errorx.Define(errorx.Internal, "demo.bug").New(), command.ExitInternal},
		{context.Canceled, command.ExitInternal},
	} {
		if got := command.ExitCode(tc.err); got != tc.want {
			t.Fatalf("ExitCode(%v) = %d, want %d", tc.err, got, tc.want)
		}
	}
	if command.AsExitError(command.ExitConfig, nil) != nil {
		t.Fatal("AsExitError(nil) must be nil")
	}
}
