# 依赖装配

- [简介](#introduction)
- [构造函数注入](#constructor-injection)
- [在入口中组装应用](#assembling-the-application)
- [关闭资源](#closing-resources)
    - [使用 cleanup.Stack](#using-cleanup-stack)
    - [清理的执行规则](#cleanup-rules)
- [在测试中替换依赖](#replacing-dependencies-in-tests)

<a id="introduction"></a>
## 简介

很多框架使用“服务容器”来管理依赖：你把服务注册到容器中，框架在需要时自动创建并注入。mygoravel 选择了另一种方式——**显式装配**。

在 mygoravel 中，没有容器、没有全局注册表，也没有基于反射的自动注入。每个服务通过构造函数参数声明它需要什么，应用的入口负责按顺序创建这些依赖并把它们传进去。这种方式的好处是：

- 阅读入口代码就能看清应用由哪些部分组成、它们是如何连接的；
- 编译器会检查每一个依赖，缺少依赖的代码无法通过编译；
- 资源由谁创建、由谁关闭非常清楚。

<a id="constructor-injection"></a>
## 构造函数注入

服务在构造函数中接收它需要的依赖，并保存在结构体字段中。例如，一个需要访问数据库的服务：

```go
package article

import "github.com/uptrace/bun"

type Service struct {
    db *bun.DB
}

func NewService(db *bun.DB) *Service {
    return &Service{db: db}
}
```

注意这里直接接收的是 Bun 的 `*bun.DB`，而不是框架包装过的类型。在 mygoravel 中，你使用的始终是社区组件的原生对象。

服务的方法通过第一个参数 `ctx context.Context` 接收请求的取消信号和截止时间，并把它继续传给数据库查询等下游调用：

```go
func (s *Service) Find(ctx context.Context, id uuid.UUID) (*Article, error) {
    var item Article
    err := s.db.NewSelect().Model(&item).Where("id = ?", id).Scan(ctx)
    // ...
}
```

<a id="assembling-the-application"></a>
## 在入口中组装应用

应用的入口（通常是 `internal/bootstrap` 包）负责创建所有依赖，并按正确的顺序把它们连接起来。下面是博客应用完整的 `internal/bootstrap/serve.go`，`serve` 命令会调用其中的 `Serve`（见[命令行](commands.md#root-command)）：

```go
package bootstrap

import (
    "context"
    "log/slog"

    "example.com/blog/internal/article"
    "example.com/blog/internal/article/articlehttp"
    "example.com/blog/internal/auth"
    "example.com/blog/internal/settings"
    "github.com/go-chi/chi/v5"
    "github.com/ryan053857/mygoravel/cleanup"
    "github.com/ryan053857/mygoravel/database"
    "github.com/ryan053857/mygoravel/httpx"
    "github.com/ryan053857/mygoravel/httpx/rest"
)

// Routes 注册应用的全部路由。serve、openapi 和 routes 命令共用它。
func Routes(api *rest.API, validate rest.TokenValidator, articles *article.Service) {
    articlehttp.Register(api, rest.RequireBearer(api, validate, "articles"), articles)
}

func Serve(ctx context.Context, cfg settings.Config, logger *slog.Logger) (err error) {
    var stack cleanup.Stack
    defer func() {
        err = stack.CloseWith(context.Background(), err)
    }()

    // 1. 打开资源，并登记关闭操作
    db, err := database.Open(ctx, cfg.Database)
    if err != nil {
        return err
    }
    stack.MustAdd("database", func(context.Context) error {
        return db.Close()
    })

    // 2. 构造业务服务
    articles := article.NewService(db)

    // 3. 安装中间件，创建 API 并注册路由
    router := chi.NewRouter()
    router.Use(httpx.Defaults(cfg.HTTP, logger)...)
    api := rest.New(rest.Options{
        Title: "Blog API", Version: "1.0.0",
        Router: router, Logger: logger,
    })
    Routes(api, auth.Validator(cfg.Auth), articles)

    // 4. 开始监听，直到 ctx 被取消
    return httpx.ListenAndServe(ctx, cfg.HTTP, api.Handler(), httpx.ServeOptions{Logger: logger})
}
```

其中 `settings.Config` 是应用的[配置结构](configuration.md#loading-configuration)，`auth.Validator` 是 token 校验器（见[认证](authentication.md#token-validators)），`article` 和 `articlehttp` 是应用自己的业务包（见[路由](routing.md#organizing-routes)）。路由声明保留在各模块的 `routes.go`，`bootstrap.Routes` 统一调用各模块的 `Register`，让 `serve`、`openapi` 和 [routes](commands.md#routes-command) 命令复用同一份声明。后两个命令在文档模式下注册，不打开运行时资源；action 在处理器工厂内部构造，只有 `serve` 会调用它们。

整个过程就是普通的函数调用：先创建被依赖的对象，再创建依赖它们的对象。`httpx.ListenAndServe` 返回后，`defer` 中的 `stack.CloseWith` 才会关闭数据库连接，因此正在处理的请求不会因为数据库提前关闭而失败。请求处理与停机的完整顺序见[请求生命周期](lifecycle.md)。

> [!TIP]
> 入口只打开当前命令真正需要的资源。例如 `serve` 命令需要数据库和 HTTP 监听，而上面的 `openapi`、`routes` 命令不需要这些资源。把不同命令的组装逻辑分开，可以让每个命令都快速、独立地运行。

<a id="closing-resources"></a>
## 关闭资源

<a id="using-cleanup-stack"></a>
### 使用 cleanup.Stack

如果只有一两个资源，直接使用 `defer` 关闭就足够了。当资源变多时，你会希望：后打开的资源先关闭；启动到一半失败时，已经打开的资源也能被释放；关闭时产生的错误不会被吞掉。`cleanup.Stack` 正是为此设计的。

下面是一个完整的例子，它打开数据库、执行传入的函数，最后关闭数据库：

```go
package bootstrap

import (
    "context"

    "github.com/ryan053857/mygoravel/cleanup"
    "github.com/ryan053857/mygoravel/database"
    "github.com/uptrace/bun"
)

func WithDatabase(ctx context.Context, cfg database.Config,
    run func(*bun.DB) error,
) (err error) {
    var stack cleanup.Stack
    defer func() {
        err = stack.CloseWith(context.Background(), err)
    }()

    db, err := database.Open(ctx, cfg)
    if err != nil {
        return err
    }
    stack.MustAdd("database", func(context.Context) error {
        return db.Close()
    })
    return run(db)
}
```

`cleanup.Stack` 的零值即可直接使用。每成功打开一个资源，就用 `MustAdd`（或返回错误的 `Add`）登记一个具名的清理函数。

<a id="cleanup-rules"></a>
### 清理的执行规则

- 清理函数按**登记的逆序**执行，后打开的资源先关闭；
- 每个清理函数只执行一次；
- `CloseWith` 会把运行时的错误和所有清理错误合并后返回，`errors.Is` 依然可以匹配其中任何一个；
- 清理时使用的 context 保留传入 context 中的值和截止时间，但不会继承它的取消信号——即使应用因为收到信号而取消，清理工作也能继续执行；
- 如果传入的 context 没有截止时间，清理的总时长默认为 10 秒。清理函数应当遵守这个 context。

> [!NOTE]
> `cleanup.Stack` 设计为由入口串行使用：在构建阶段登记，在退出阶段关闭。不要在多个 goroutine 中同时操作它。

<a id="replacing-dependencies-in-tests"></a>
## 在测试中替换依赖

在 mygoravel 中，大多数测试可以直接使用真实的依赖，例如用 `dbtest` 创建一个真实的测试数据库（见[测试](testing.md#database-tests)）。

当你确实需要替换某个依赖时（例如把文章配图上传到对象存储），遵循 Go 的惯例：**由使用方定义接口**。在业务包中声明一个只包含所需方法的小接口，构造函数接收这个接口：

```go
package article

type ImageStore interface {
    Put(ctx context.Context, key string, data []byte) error
}

func NewService(db *bun.DB, images ImageStore) *Service {
    return &Service{db: db, images: images}
}
```

在测试中传入一个简单的内存实现即可，不需要真实的对象存储：

```go
type memoryImages map[string][]byte

func (m memoryImages) Put(_ context.Context, key string, data []byte) error {
    m[key] = data
    return nil
}

svc := article.NewService(db, memoryImages{})
```

不需要为每个服务预先定义接口，只在真正需要替换实现时再引入。
