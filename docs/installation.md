# 安装

- [认识 mygoravel](#meet-mygoravel)
    - [为什么选择 mygoravel](#why-mygoravel)
- [创建应用](#creating-an-application)
    - [环境要求](#requirements)
    - [安装 mygoravel](#installing-mygoravel)
    - [编写入口文件](#entrypoint)
    - [启动服务](#running)
- [下一步](#next-steps)

<a id="meet-mygoravel"></a>
## 认识 mygoravel

mygoravel 是一个用于构建 HTTP API 的 Go 应用框架。它为配置加载、路由、请求验证、错误响应、日志、认证、数据库访问、迁移和命令行工具提供了一套统一的约定，让你把精力放在业务本身，而不是反复拼装基础设施。

和很多“大而全”的框架不同，mygoravel 不会把社区组件藏在自己的抽象后面。路由使用 [chi](https://github.com/go-chi/chi) 和 [Huma](https://huma.rocks)，日志使用标准库 `log/slog`，并通过 [slog-multi](https://github.com/samber/slog-multi)、[slog-formatter](https://github.com/samber/slog-formatter) 和 [slog-chi](https://github.com/samber/slog-chi) 扩展，数据库使用 [Bun](https://bun.uptrace.dev) 与 pgx，迁移使用 [goose](https://github.com/pressly/goose)，命令行使用 [Cobra](https://github.com/spf13/cobra)。mygoravel 负责把它们以一致的方式连接起来，你学到的每一项技能都是这些组件本身的技能。

<a id="why-mygoravel"></a>
### 为什么选择 mygoravel

**依赖一目了然。** mygoravel 没有服务容器，也没有全局注册表。数据库连接、缓存都通过普通的 Go 构造函数参数传递，日志直接使用标准库的 `slog.Default()`，阅读入口文件就能知道应用由哪些部分组成。

**约定开箱即用。** 每个请求都会自动获得请求 ID；所有错误都使用统一的 [Problem Details](https://www.rfc-editor.org/rfc/rfc9457) 格式返回；每条路由都必须显式声明是公开的还是需要认证的——忘记声明会在启动时就报错，而不是上线后才发现接口没有受保护。

**按需引入。** 每个包都可以单独使用。一个最小的 API 只需要路由相关的包；需要数据库、缓存或本地化时，再逐步加入。

**接口文档自动生成。** 路由的输入输出由 Go 类型描述，OpenAPI 文档随代码自动生成，并且可以在不连接数据库的情况下离线导出。

<a id="creating-an-application"></a>
## 创建应用

<a id="requirements"></a>
### 环境要求

- Go 1.27.3 或更高版本（精确的工具链要求以框架的 [go.mod](../go.mod) 为准）
- PostgreSQL（仅在使用数据库功能时需要）

本章创建的最小 API 只需要 Go 工具链。

<a id="installing-mygoravel"></a>
### 安装 mygoravel

mygoravel 是一个普通的 Go module。首先为你的应用创建一个目录并初始化 module：

```bash
mkdir hello-api
cd hello-api
go mod init example.com/hello-api
```

目录名和 module 路径可以按你的项目来取。接下来添加框架依赖：

```bash
go get github.com/ryan053857/mygoravel@main
```

你也可以把 `@main` 换成某个具体的提交哈希或版本标签，让依赖版本固定下来。

<a id="entrypoint"></a>
### 编写入口文件

在应用目录中创建 `main.go`，内容如下。这是一个完整的程序：

```go
package main

import (
    "context"
    "log/slog"
    "net/http"
    "os"
    "os/signal"
    "syscall"

    "github.com/danielgtaylor/huma/v2"
    "github.com/go-chi/chi/v5"
    "github.com/ryan053857/mygoravel/httpx"
    "github.com/ryan053857/mygoravel/httpx/rest"
    "github.com/ryan053857/mygoravel/logging"
)

type PingInput struct{}

type PingOutput struct {
    Body struct {
        Message string `json:"message"`
    }
}

func registerPing(api *rest.API) {
    op := huma.Operation{
        OperationID: "ping",
        Method:      http.MethodGet,
        Path:        "/ping",
        Summary:     "检查 API 是否可响应",
    }
    rest.Public()(&op)
    rest.Register(api, op, func() func(context.Context, *PingInput) (*PingOutput, error) {
        return func(ctx context.Context, in *PingInput) (*PingOutput, error) {
            out := &PingOutput{}
            out.Body.Message = "pong"
            return out, nil
        }
    })
}

func run() error {
    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer stop()

    slog.SetDefault(logging.New(logging.Config{}))
    cfg := httpx.DefaultConfig()
    cfg.Addr = "127.0.0.1:8080"

    router := chi.NewRouter()
    router.Use(httpx.Defaults(cfg, nil)...)
    api := rest.New(rest.Options{Title: "Hello API", Version: "1.0.0", Router: router})
    registerPing(api)

    return httpx.ListenAndServe(ctx, cfg, api.Handler(), httpx.ServeOptions{})
}

func main() {
    if err := run(); err != nil {
        slog.Error("application stopped", "error", err)
        os.Exit(1)
    }
}
```

这个程序只做了四件事：

1. 安装全局日志器，安装[默认中间件](middleware.md#default-middleware)，再创建 API 实例；
2. 通过 `registerPing` 注册一条 `GET /ping` 路由；
3. 在 `127.0.0.1:8080` 上监听；
4. 收到 Ctrl+C 或 `SIGTERM` 时，等待正在处理的请求完成后退出。

现在不必理解每一行代码，[路由](routing.md)和[请求生命周期](lifecycle.md)章节会详细解释它们。

<a id="running"></a>
### 启动服务

整理依赖并启动应用：

```bash
go mod tidy
go run .
```

保持服务运行，在另一个终端发起请求：

```bash
curl -i http://localhost:8080/ping
```

你会得到一个 `200 OK` 响应：

```http
HTTP/1.1 200 OK
Content-Type: application/json
X-Request-Id: 3UHKWQUZP5TTP24PG3SQNGA5EO

{"message":"pong"}
```

`X-Request-Id` 是框架为每个请求生成的唯一 ID，排查问题时可以用它在日志中找到这个请求，见[请求](requests.md#request-id)。

mygoravel 还为你生成了接口文档。在浏览器中打开 `http://localhost:8080/docs` 即可查看，原始的 OpenAPI 文档位于 `http://localhost:8080/openapi.json`。

恭喜！你的第一个 mygoravel 应用已经运行起来了。在服务终端按 Ctrl+C 即可停止它。

<a id="next-steps"></a>
## 下一步

现在你已经有了一个可以运行的 API，接下来可以根据需要逐步加入更多能力：

- 用[配置](configuration.md)把监听地址、日志级别等参数放进 YAML 文件；
- 阅读[路由](routing.md)、[请求](requests.md)和[响应](responses.md)，编写真正的业务接口；
- 用[认证](authentication.md)保护需要登录才能访问的接口；
- 用[数据库](database.md)连接 PostgreSQL，用[迁移](migrations.md)管理表结构；
- 用[命令行](commands.md)为应用加上 `serve`、`migrate`、`openapi`、`routes` 等子命令。

如果想先了解一个 mygoravel 应用整体是如何组织的，可以阅读[目录结构](directory-structure.md)和[依赖装配](dependencies.md)。
