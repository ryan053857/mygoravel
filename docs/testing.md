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

mygoravel 不引入额外的测试框架，直接使用 Go 标准库的 `testing`、`net/http/httptest` 和 `go test` 命令。

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

测试 HTTP 接口时，不需要真正监听端口。用 `httptest` 创建请求和响应记录器，然后直接调用 `api.Handler()` 即可。下面是一个完整的 `api_test.go`：

```go
package api_test

import (
    "context"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/danielgtaylor/huma/v2"
    "github.com/ryan053857/mygoravel/httpx/rest"
)

func TestPing(t *testing.T) {
    type Input struct{}
    type Output struct {
        Body struct {
            Message string `json:"message"`
        }
    }
    api := rest.New(rest.Options{})
    op := huma.Operation{OperationID: "ping", Method: http.MethodGet, Path: "/ping"}
    rest.Public()(&op)
    rest.Register(api, op, func() func(context.Context, *Input) (*Output, error) {
        return func(context.Context, *Input) (*Output, error) {
            out := &Output{}
            out.Body.Message = "pong"
            return out, nil
        }
    })

    response := httptest.NewRecorder()
    api.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/ping", nil))
    if response.Code != http.StatusOK {
        t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
    }
    var body struct {
        Message string `json:"message"`
    }
    if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
        t.Fatal(err)
    }
    if body.Message != "pong" || response.Header().Get("X-Request-ID") == "" {
        t.Fatalf("unexpected response: %s", response.Body.String())
    }
}
```

在实际项目中，你通常会调用应用自己的路由注册函数（例如 `notehttp.Register`），而不是在测试中重新声明路由。这样测试覆盖的就是真实的路由配置。

<a id="testing-auth-and-errors"></a>
### 测试认证与错误响应

请通过完整的 `api.Handler()` 来测试认证、验证错误和越权访问，而不是直接调用处理器函数。这样测试才能覆盖认证中间件、输入验证和错误转换的完整过程。

一组典型的接口测试包括：

- 不带 `Authorization` 头的请求返回 `401`；
- 缺少 scope 的 token 返回 `403`；
- 不合法的请求体返回 `422`，并检查 `errors[].location`；
- 访问他人的资源返回 `403` 或 `404`；
- 错误响应中的 `code` 与预期的错误码一致。

<a id="testing-services"></a>
## 测试业务服务

业务规则（尤其是资源所有权检查）应当通过直接调用业务服务来测试。由于主体是作为参数传入的，你可以很方便地模拟不同的调用者：

```go
alice := identity.Subject{ID: "alice", Type: identity.SubjectUser}
bob := identity.Subject{ID: "bob", Type: identity.SubjectUser}

item, err := svc.Create(ctx, alice, "准备发布")
if err != nil {
    t.Fatal(err)
}
if _, err := svc.Get(ctx, bob, item.ID); !errors.Is(err, note.ErrForbidden) {
    t.Fatalf("expected forbidden, got %v", err)
}
```

优先使用真实的依赖。只有在确实需要隔离某项外部能力时（例如第三方支付），才在业务包中定义一个小接口，并在测试中提供简单的实现，见[依赖装配](dependencies.md#replacing-dependencies-in-tests)。

<a id="testing-configuration"></a>
## 测试配置

把配置内容写进临时文件，用 `t.Setenv` 提供环境变量，再调用 `config.Load`：

```go
func TestLoadConfig(t *testing.T) {
    t.Setenv("HTTP_ADDR", ":9000")
    path := filepath.Join(t.TempDir(), "config.yaml")
    data := "http:\n  addr: ${HTTP_ADDR}\ndatabase:\n  dsn: postgres://localhost/test\n"
    if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
        t.Fatal(err)
    }

    cfg, err := settings.Load(path)
    if err != nil {
        t.Fatal(err)
    }
    if cfg.HTTP.Addr != ":9000" {
        t.Fatalf("addr = %q", cfg.HTTP.Addr)
    }
}
```

`t.Setenv` 会在测试结束时恢复环境变量，但使用它的测试不能调用 `t.Parallel`。

建议覆盖以下情况：默认值、显式的零值、未知字段、校验失败，以及敏感字段在输出中是否被脱敏。

<a id="database-tests"></a>
## 数据库测试

<a id="creating-test-databases"></a>
### 创建测试数据库

测试数据库代码时，最可靠的方式是使用真实的 PostgreSQL。`dbtest.Open` 会为**每个测试**创建一个独立的临时数据库，执行你的迁移，并在测试结束时删除它。测试之间互不干扰，可以放心地并行运行。

下面的测试文件应放在 `migrations` 目录的上一级，`migrations` 中至少要有一个 SQL 文件：

```go
//go:build integration

package storage_test

import (
    "embed"
    "testing"

    "github.com/ryan053857/mygoravel/database/dbtest"
    "github.com/ryan053857/mygoravel/database/migrate"
)

//go:embed migrations/*.sql
var files embed.FS

func TestDatabase(t *testing.T) {
    migrations, err := migrate.Sub(files, "migrations")
    if err != nil {
        t.Fatal(err)
    }
    db := dbtest.Open(t, migrations)

    var name string
    if err := db.QueryRow("select current_database()").Scan(&name); err != nil {
        t.Fatal(err)
    }
    if name == "" {
        t.Fatal("missing isolated database")
    }
}
```

`dbtest.Open` 返回一个已执行迁移的 `*bun.DB`，你可以把它直接传给业务服务进行测试。迁移参数传 `nil` 时会创建一个空数据库。

`dbtest` 通过环境变量 `DATABASE_TEST_DSN` 获取一个管理员连接，该账户需要有创建和删除数据库的权限。没有设置这个变量时，测试会直接失败，而不是被跳过——这样可以避免数据库测试在 CI 中被悄悄忽略。

> [!WARNING]
> 请为 `DATABASE_TEST_DSN` 使用专门的测试数据库实例，不要指向任何存放真实数据的数据库。

文件开头的 `//go:build integration` 让这些测试只在显式指定 `-tags=integration` 时运行，普通的 `go test ./...` 不会执行它们。

<a id="running-database-tests-locally"></a>
### 在本地运行数据库测试

如果你安装了 Docker，可以用下面的命令启动一个临时的 PostgreSQL：

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

测试完成后，执行 `docker stop app-test-postgres` 即可清理。如果你的应用还用到了 Redis 等其他外部服务，请为它们编写各自的集成测试。

<a id="command-tests"></a>
## 命令测试

测试命令行时，不需要启动新的进程。把 `command.IO` 指向 `bytes.Buffer`，通过 `SetArgs` 指定参数，然后执行命令并检查输出。下面是一个完整的测试文件：

```go
package cli_test

import (
    "bytes"
    "context"
    "fmt"
    "testing"

    "github.com/ryan053857/mygoravel/command"
    "github.com/spf13/cobra"
)

func TestOpenAPICommand(t *testing.T) {
    var out, diagnostic bytes.Buffer
    root := command.Root("api", "HTTP API", command.IO{Out: &out, Err: &diagnostic})
    root.AddCommand(command.NewOpenAPI(func(ctx context.Context, cmd *cobra.Command, args []string) error {
        _, err := fmt.Fprintln(cmd.OutOrStdout(), `{"openapi":"3.1.0"}`)
        return err
    }))
    root.SetArgs([]string{"openapi"})
    if err := root.ExecuteContext(context.Background()); err != nil {
        t.Fatal(err)
    }
    if out.String() != "{\"openapi\":\"3.1.0\"}\n" || diagnostic.Len() != 0 {
        t.Fatalf("stdout=%q stderr=%q", out.String(), diagnostic.String())
    }
}
```

这个例子使用固定的输出来演示如何测试命令的接线。在你的应用中，请测试真实的命令回调，并对失败情况（配置错误、依赖不可用、非法参数）断言对应的 `command.ExitCode`。

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
