# 测试：快速入门

- [简介](#introduction)
- [运行测试](#running-tests)
- [HTTP 测试](#http-tests)
    - [测试认证与错误响应](#testing-auth-and-errors)
- [测试业务服务](#testing-services)
- [测试配置](#testing-configuration)
- [数据库测试](#database-tests)
    - [创建测试数据库](#creating-test-databases)
    - [在本地运行数据库测试](#running-database-tests-locally)
- [命令测试](#command-tests)
- [测试平滑停止](#testing-shutdown)
- [测试 OpenAPI 文档](#testing-openapi)

<a id="introduction"></a>
## 简介

mygoravel 在设计时就考虑了测试。由于所有依赖都是显式传入的，你可以在测试中轻松构造 API、服务和命令，而不需要启动整个应用。

mygoravel 不引入额外的测试框架，直接使用 Go 标准库的 `testing`、`net/http/httptest` 和 `go test` 命令。需要断言日志时，使用 `logging/logtest` 捕获，见[测试日志](logging.md#testing-logs)。

<a id="running-tests"></a>
## 运行测试

在应用根目录中执行：

```bash
go test ./...
```

建议同时开启竞态检测，它可以帮你发现并发访问共享数据的问题：

```bash
go test -race ./...
```

需要真实数据库的测试通过 `integration` 构建标签与普通测试区分开，见[数据库测试](#database-tests)。

<a id="http-tests"></a>
## HTTP 测试

测试 HTTP 接口时，不需要真正监听端口。用 `httptest` 创建请求和响应记录器，然后直接调用 `api.Handler()` 即可。

请调用应用自己的路由注册函数，而不是在测试中重新声明路由，这样测试覆盖的就是真实的路由配置、认证和输入验证。测试文件也可以按业务动作命名。下面是博客应用完整的 `internal/article/articlehttp/actions/create_article_test.go`，它测试[创建文章](routing.md#http-actions)的 `CreateArticle` action：

这里使用外部测试包 `actions_test`，通过父包 `articlehttp.Register` 注册真实路由。生产代码的依赖方向仍然是 `articlehttp → actions → article`，`actions` 包不需要反向导入父包。

```go
package actions_test

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"

    "example.com/blog/internal/article"
    "example.com/blog/internal/article/articlehttp"
    "example.com/blog/internal/auth"
    "github.com/ryan053857/mygoravel/httpx/rest"
)

func newTestAPI() *rest.API {
    api := rest.New(rest.Options{})
    validate := auth.Validator(auth.Config{Credentials: []auth.Credential{
        {Token: "alice-token", SubjectID: "alice", Scopes: []string{"articles"}},
        {Token: "guest-token", SubjectID: "guest"},
    }})
    // 这些请求在调用服务之前就会被拒绝，所以不需要数据库
    articlehttp.Register(api, rest.RequireBearer(api, validate, "articles"), article.NewService(nil))
    return api
}

func TestCreateArticleRejectsBadRequests(t *testing.T) {
    const valid = `{"title":"Hello World","slug":"hello-world","content":"……"}`
    tests := []struct {
        name     string
        token    string
        body     string
        status   int
        code     string
        location string
    }{
        {"没有 token", "", valid, http.StatusUnauthorized, "auth.unauthenticated", ""},
        {"缺少 scope", "guest-token", valid, http.StatusForbidden, "auth.forbidden", ""},
        {"slug 格式不对", "alice-token", `{"title":"Hello World","slug":"Hello World","content":""}`, http.StatusUnprocessableEntity, "", "body.slug"},
    }
    api := newTestAPI()
    for _, tc := range tests {
        t.Run(tc.name, func(t *testing.T) {
            req := httptest.NewRequest(http.MethodPost, "/articles", strings.NewReader(tc.body))
            req.Header.Set("Content-Type", "application/json")
            if tc.token != "" {
                req.Header.Set("Authorization", "Bearer "+tc.token)
            }
            rr := httptest.NewRecorder()
            api.Handler().ServeHTTP(rr, req)

            var problem struct {
                Code   string `json:"code"`
                Errors []struct {
                    Location string `json:"location"`
                } `json:"errors"`
            }
            if err := json.Unmarshal(rr.Body.Bytes(), &problem); err != nil {
                t.Fatal(err)
            }
            if rr.Code != tc.status || problem.Code != tc.code {
                t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
            }
            if tc.location != "" && (len(problem.Errors) == 0 || problem.Errors[0].Location != tc.location) {
                t.Fatalf("errors=%s", rr.Body.String())
            }
        })
    }
}
```

这个测试用表格列出了三种在调用业务服务之前就会被拒绝的请求：没有 token、缺少 scope、输入格式不对。它们不会触及数据库，所以可以传入 `article.NewService(nil)`，测试运行得很快。

<a id="testing-auth-and-errors"></a>
### 测试认证与错误响应

请通过完整的 `api.Handler()` 来测试认证、验证错误和越权访问，而不是直接调用处理器函数。这样测试才能覆盖认证中间件、输入验证和错误转换的完整过程。一组典型的接口测试包括：

- 不带 `Authorization` 头的请求返回 `401`；
- 缺少 scope 的 token 返回 `403`；
- 不合法的请求体返回 `422`，并检查 `errors[].location`；
- 访问他人的资源返回 `403` 或 `404`；
- 错误响应中的 `code` 与预期的错误码一致。

前三种由上面的测试覆盖。后两种取决于数据库中的数据，更适合直接测试业务服务，见下一节。

<a id="testing-services"></a>
## 测试业务服务

业务规则（尤其是资源所有权检查）应当通过直接调用业务服务来测试。由于主体是作为参数传入的，你可以很方便地模拟不同的调用者。下面是 `internal/article/service_test.go`，它在一个真实的测试数据库上验证草稿的可见性和发布规则：

```go
//go:build integration

package article_test

import (
    "context"
    "errors"
    "testing"

    "example.com/blog/internal/article"
    "example.com/blog/migrations"
    "github.com/ryan053857/mygoravel/database"
    "github.com/ryan053857/mygoravel/database/dbtest"
    "github.com/ryan053857/mygoravel/identity"
)

func TestDraftVisibilityAndPublishing(t *testing.T) {
    ctx := context.Background()
    svc := article.NewService(dbtest.Open(t, database.Postgres, migrations.FS))
    alice := identity.Subject{ID: "alice", Type: identity.SubjectUser}
    bob := identity.Subject{ID: "bob", Type: identity.SubjectUser}

    item, err := svc.Create(ctx, alice, article.Changes{Title: "Hello World", Slug: "hello-world"})
    if err != nil {
        t.Fatal(err)
    }

    // 草稿只有作者能看到
    if _, err := svc.Get(ctx, bob, item.ID); !errors.Is(err, article.ErrNotFound) {
        t.Fatalf("bob read a draft: %v", err)
    }
    // 只有作者能发布，而且不能重复发布
    if _, err := svc.Publish(ctx, bob, item.ID); !errors.Is(err, article.ErrForbidden) {
        t.Fatalf("bob published: %v", err)
    }
    if _, err := svc.Publish(ctx, alice, item.ID); err != nil {
        t.Fatal(err)
    }
    if _, err := svc.Publish(ctx, alice, item.ID); !errors.Is(err, article.ErrAlreadyPublished) {
        t.Fatalf("published twice: %v", err)
    }
    // 发布之后所有人都能看到
    if _, err := svc.Get(ctx, bob, item.ID); err != nil {
        t.Fatal(err)
    }
    // slug 不能重复
    _, err = svc.Create(ctx, bob, article.Changes{Title: "Another", Slug: "hello-world"})
    if !errors.Is(err, article.ErrSlugTaken) {
        t.Fatalf("duplicate slug: %v", err)
    }
}
```

`dbtest.Open` 为这个测试创建一个独立的数据库并执行 `migrations.FS` 中的迁移，详见下面的[数据库测试](#database-tests)。

优先使用真实的依赖。只有在确实需要隔离某项外部能力时（例如对象存储），才在业务包中定义一个小接口，并在测试中提供简单的实现，见[依赖装配](dependencies.md#replacing-dependencies-in-tests)。

<a id="testing-configuration"></a>
## 测试配置

把配置内容写进临时文件，用 `t.Setenv` 提供环境变量，再调用 `config.Load`：

```go
func TestLoadConfig(t *testing.T) {
    t.Setenv("HTTP_ADDR", ":9000")
    path := filepath.Join(t.TempDir(), "config.yaml")
    data := "http:\n  addr: ${HTTP_ADDR}\ndatabase:\n  driver: postgres\n  dsn: postgres://localhost/test\n"
    if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
        t.Fatal(err)
    }

    cfg, err := config.Load[settings.Config](path)
    if err != nil {
        t.Fatal(err)
    }
    if cfg.HTTP.Addr != ":9000" {
        t.Fatalf("addr = %q", cfg.HTTP.Addr)
    }
}
```

`t.Setenv` 会在测试结束时恢复环境变量，但使用它的测试不能调用 `t.Parallel`。

建议覆盖以下情况：默认值、显式的零值、未知字段和校验失败。

<a id="database-tests"></a>
## 数据库测试

<a id="creating-test-databases"></a>
### 创建测试数据库

测试数据库代码时，最可靠的方式是使用与生产相同的真实数据库。`dbtest.Open` 会为**每个测试**创建一个独立的临时数据库，执行你的迁移，并在测试结束时删除它。测试之间互不干扰，可以放心地并行运行。

上一节的 `service_test.go` 就是这样使用它的：

```go
svc := article.NewService(dbtest.Open(t, database.Postgres, migrations.FS))
```

`dbtest.Open` 返回一个已执行迁移的 `*bun.DB`，你可以把它直接传给业务服务进行测试。迁移参数传 `nil` 时会创建一个空数据库。

使用 SQLite 的应用传入 `database.SQLite`，`dbtest` 在 `t.TempDir()` 中创建数据库文件。它不需要任何外部服务，也不需要环境变量，可以和普通测试一样直接用 `go test ./...` 运行，不必加 `integration` 构建标签。

PostgreSQL 的 `dbtest` 通过环境变量 `DATABASE_TEST_DSN` 获取一个管理员连接，该账户需要有创建和删除数据库的权限。没有设置这个变量时，测试会直接失败，而不是被跳过——这样可以避免数据库测试在 CI 中被悄悄忽略。

> [!WARNING]
> 请为 `DATABASE_TEST_DSN` 使用专门的测试数据库实例，不要指向任何存放真实数据的数据库。

PostgreSQL 测试文件开头的 `//go:build integration` 让这些测试只在显式指定 `-tags=integration` 时运行，普通的 `go test ./...` 不会执行它们。

<a id="running-database-tests-locally"></a>
### 在本地运行数据库测试

使用 SQLite 时不需要准备任何东西。使用 PostgreSQL 时，如果你安装了 Docker，可以用下面的命令启动一个临时的 PostgreSQL：

```bash
docker run --name app-test-postgres --rm -d \
  -e POSTGRES_PASSWORD=postgres \
  -p 127.0.0.1:55432:5432 postgres:18
```

等待数据库就绪：

```bash
docker exec app-test-postgres pg_isready -U postgres
```

然后设置环境变量并运行测试：

```bash
export DATABASE_TEST_DSN='postgres://postgres:postgres@127.0.0.1:55432/postgres?sslmode=disable'
go test -race -count=1 -tags=integration ./...
```

测试完成后，执行 `docker stop app-test-postgres` 即可清理。Redis 的集成测试见 [Redis](redis.md#testing)。

<a id="command-tests"></a>
## 命令测试

测试命令行时，不需要启动新的进程。`cli.New` 返回的是原生的 `*cobra.Command`，用 Cobra 的 `SetOut`、`SetArgs` 把输出重定向到 `bytes.Buffer` 并指定参数，然后执行命令并检查输出和退出码。下面是博客应用完整的 `internal/cli/cli_test.go`，它测试的是[命令行](commands.md#declaring-the-cli)一章中真实的 `cli.New`：

```go
package cli_test

import (
    "bytes"
    "context"
    "path/filepath"
    "strings"
    "testing"

    "example.com/blog/internal/cli"
    "github.com/ryan053857/mygoravel/command"
)

func run(args ...string) (string, error) {
    var out bytes.Buffer
    root := cli.New()
    root.SetOut(&out)
    root.SetErr(&out)
    root.SetArgs(args)
    err := root.ExecuteContext(context.Background())
    return out.String(), err
}

func TestOpenAPIDoesNotNeedConfig(t *testing.T) {
    out, err := run("openapi")
    if err != nil {
        t.Fatal(err)
    }
    if !strings.Contains(out, `"/articles/{id}/publish"`) {
        t.Fatalf("missing route in %s", out)
    }
}

func TestRoutesFilters(t *testing.T) {
    out, err := run("routes", "--method", "post", "--path", "/articles")
    if err != nil {
        t.Fatal(err)
    }
    if !strings.Contains(out, "article-create") || !strings.Contains(out, "article-publish") {
        t.Fatalf("missing route in %s", out)
    }
    if strings.Contains(out, "article-get") || strings.Contains(out, "/docs") {
        t.Fatalf("route filters did not apply: %s", out)
    }
}

func TestConfigCheckReportsMissingFile(t *testing.T) {
    _, err := run("config", "check", "--config", filepath.Join(t.TempDir(), "missing.yaml"))
    if command.ExitCode(err) != command.ExitConfig {
        t.Fatalf("exit code = %d, err = %v", command.ExitCode(err), err)
    }
}
```

前两个测试确认 `openapi`、`routes` 命令可以离线查看真实的接口声明，不需要配置文件；最后一个测试确认配置文件缺失时，`config check` 以 `ExitConfig` 退出。

<a id="testing-shutdown"></a>
## 测试平滑停止

如果你的应用对停机行为有要求，可以为 `httpx.Serve` 编写测试，覆盖以下场景：

- 取消 context 后，服务正常退出；
- 停机时正在处理的请求能够完成；
- 排空超时后，未完成的请求被取消；
- 监听发生错误时，`Serve` 返回该错误。

如果入口自己处理系统信号，还可以在测试中以子进程方式启动应用，发送 `SIGTERM`，验证资源被正确关闭以及进程的退出码。

<a id="testing-openapi"></a>
## 测试 OpenAPI 文档

在文档模式下注册路由时，处理器工厂不应被调用。你可以让测试中的工厂一旦被调用就使测试失败，以此确保生成文档时不会意外地访问数据库等资源：

```go
api := rest.New(rest.Options{Mode: rest.ModeOpenAPI})
op := huma.Operation{OperationID: "ping", Method: http.MethodGet, Path: "/ping"}
rest.Public()(&op)
rest.Register(api, op, func() func(context.Context, *PingInput) (*PingOutput, error) {
    t.Fatal("handler factory must not run in OpenAPI mode")
    return nil
})

data, err := api.OpenAPIJSON()
```

你还可以把生成的文档与仓库中保存的 `openapi.json` 进行比较，这样每次接口变更都会在代码审查中清晰可见。
