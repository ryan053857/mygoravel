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

在 mygoravel 中，没有容器、没有全局注册表，也没有基于反射的自动注入。每个 [action](actions.md) 和服务通过构造函数参数声明它需要什么，应用的入口负责按顺序创建这些依赖并把它们传进去。这种方式的好处是：

- 阅读入口代码就能看清应用由哪些部分组成、它们是如何连接的；
- 编译器会检查每一个依赖，缺少依赖的代码无法通过编译；
- 资源由谁创建、由谁关闭非常清楚。

<a id="constructor-injection"></a>
## 构造函数注入

action 在构造函数中接收它需要的依赖，并保存在结构体字段中。例如，需要访问数据库的“获取文章”：

```go
package article

import "github.com/uptrace/bun"

type Get struct {
    db *bun.DB
}

func NewGet(db *bun.DB) *Get {
    return &Get{db: db}
}
```

注意这里直接接收的是 Bun 的 `*bun.DB`，而不是框架包装过的类型。在 mygoravel 中，你使用的始终是社区组件的原生对象。

它的方法通过第一个参数 `ctx context.Context` 接收请求的取消信号和截止时间，并把它继续传给数据库查询等下游调用：

```go
func (a *Get) Handle(ctx context.Context, subject identity.Subject, id uuid.UUID) (*Article, error) {
    var item Article
    err := a.db.NewSelect().Model(&item).Where("id = ?", id).Scan(ctx)
    // ...
}
```

构造函数只保存依赖，不使用它们。这样生成 OpenAPI 文档时，路由可以用 `nil` 依赖照常注册，见[处理器](routing.md#handlers)。

<a id="assembling-the-application"></a>
## 在入口中组装应用

应用的入口（通常是 `internal/bootstrap` 包）负责创建所有依赖，并按正确的顺序把它们连接起来。博客应用把入口分成两部分：`App` 装配各个运行部分共用的资源，每个运行部分（HTTP 服务、[队列 worker](queue.md#running-workers) 等）是 `App` 的一个方法。下面是完整的 `internal/bootstrap/app.go`：

```go
package bootstrap

import (
    "context"
    "log/slog"

    "example.com/blog/internal/settings"
    "github.com/ryan053857/mygoravel/cleanup"
    "github.com/ryan053857/mygoravel/command"
    "github.com/ryan053857/mygoravel/database"
    "github.com/ryan053857/mygoravel/database/postgres"
    "github.com/ryan053857/mygoravel/lock"
    "github.com/ryan053857/mygoravel/logging"
    "github.com/ryan053857/mygoravel/telemetry"
    "github.com/uptrace/bun"
    "go.opentelemetry.io/otel/metric"
    "go.opentelemetry.io/otel/trace"
)

// App 是各个运行部分共用的资源，每个进程只装配一次。
type App struct {
    cfg     settings.Config
    db      *bun.DB
    locker  *lock.Locker
    meters  metric.MeterProvider
    tracers trace.TracerProvider
}

// open 开启推送，安装全局日志器，打开数据库，把关闭操作登记到 stack。
func open(ctx context.Context, cfg settings.Config, stack *cleanup.Stack) (*App, error) {
    tel, err := telemetry.Open(ctx, cfg.Telemetry)
    if err != nil {
        return nil, err
    }
    stack.MustAdd("telemetry", tel.Shutdown)
    slog.SetDefault(logging.New(cfg.Logs, logging.Export(tel.Logs)))

    db, err := database.Open(ctx, postgres.Driver, cfg.Database)
    if err != nil {
        return nil, err
    }
    stack.MustAdd("database", func(context.Context) error {
        return db.Close()
    })

    return &App{
        cfg:     cfg,
        db:      db,
        locker:  lock.New(lock.NewDatabase(db)),
        meters:  tel.Meters,
        tracers: tel.Tracers,
    }, nil
}

// run 装配 App 和 parts，然后在同一个进程中运行 parts，直到收到停止信号。
func run(ctx context.Context, cfg settings.Config, parts ...func(*App, *command.Daemon) (func(context.Context) error, error)) (err error) {
    d, err := command.NewDaemon(nil)
    if err != nil {
        return err
    }
    defer d.Stop()

    var stack cleanup.Stack
    defer func() {
        err = stack.CloseWith(context.Background(), err)
    }()

    app, err := open(ctx, cfg, &stack)
    if err != nil {
        return err
    }
    runs := make([]func(context.Context) error, len(parts))
    for i, part := range parts {
        if runs[i], err = part(app, d); err != nil {
            return err
        }
    }
    return d.Run(ctx, runs...)
}

// Serve 只运行 HTTP 服务，serve 命令调用它。
func Serve(ctx context.Context, cfg settings.Config) error {
    return run(ctx, cfg, (*App).HTTP)
}

// Start 在一个进程中运行 HTTP 服务、队列 worker 和定时任务调度器，start 命令调用它。
func Start(ctx context.Context, cfg settings.Config) error {
    return run(ctx, cfg, (*App).HTTP, (*App).Worker, (*App).Scheduler)
}
```

`telemetry.Open` 把日志、指标和链路推送到配置的接收端，没有配置时什么也不做，见[可观测性](telemetry.md#setup)。它最先登记，最后关闭，其他资源关闭时记录的日志也能推送出去。`locker` 是[原子锁](locks.md)，[调度器](scheduling.md#multiple-instances)用它在多个实例之间去重。`Worker` 和 `Scheduler` 两个运行部分分别在[队列](queue.md#running-workers)和[定时任务](scheduling.md#running-the-scheduler)两章中介绍。

HTTP 服务在 `internal/bootstrap/http.go` 中：

```go
package bootstrap

import (
    "context"

    "example.com/blog/internal/article"
    "example.com/blog/internal/auth"
    "github.com/go-chi/chi/v5"
    "github.com/ryan053857/mygoravel/command"
    "github.com/ryan053857/mygoravel/httpx"
    "github.com/ryan053857/mygoravel/httpx/rest"
    "github.com/uptrace/bun"
)

// API 是应用的 API 选项，serve、openapi 和 routes 命令共用它。
var API = rest.Options{Title: "Blog API", Version: "1.0.0"}

// Routes 注册应用的全部路由，serve、openapi 和 routes 命令共用它。
func Routes(api *rest.API, validate rest.TokenValidator, db *bun.DB) {
    article.Register(api, rest.RequireBearer(api, validate, "articles"), db)
}

// DocRoutes 在文档模式下注册路由。文档模式不调用处理器和 token 校验器，
// 所以不需要配置和数据库。
func DocRoutes(api *rest.API) {
    Routes(api, auth.Validator(auth.Config{}), nil)
}

// DocRouter 在文档模式下构造全部路由，routes 命令用它列出路由表。
func DocRouter() chi.Routes {
    router := chi.NewRouter()
    opts := API
    opts.Mode, opts.Router = rest.ModeOpenAPI, router
    DocRoutes(rest.New(opts))
    return router
}

// HTTP 安装中间件、注册路由并取得监听端口，返回的函数运行 HTTP 服务，直到 ctx 被取消。
func (a *App) HTTP(d *command.Daemon) (func(context.Context) error, error) {
    router := chi.NewRouter()
    router.Use(httpx.Telemetry(a.meters, a.tracers))
    router.Use(httpx.Defaults(a.cfg.HTTP, nil, nil)...)
    router.Use(httpx.Timeout(a.cfg.HTTP.Timeout))
    opts := API
    opts.Router = router
    api := rest.New(opts)
    Routes(api, auth.Validator(a.cfg.Auth), a.db)

    ln, err := d.Listen("tcp", a.cfg.HTTP.Addr)
    if err != nil {
        return nil, err
    }
    return func(ctx context.Context) error {
        return httpx.Serve(ctx, a.cfg.HTTP, ln, api.Handler(), httpx.ServeOptions{})
    }, nil
}
```

其中 `settings.Config` 是应用的[配置结构](configuration.md#loading-configuration)，`auth.Validator` 是 token 校验器（见[认证](authentication.md#token-validators)），`article` 是应用自己的业务包（见[路由](routing.md#organizing-routes)）。路由声明保留在各模块的 `routes.go`，`bootstrap.Routes` 统一调用各模块的 `Register`，让 `serve`、`openapi` 和 [routes](commands.md#routes-command) 命令复用同一份声明。后两个命令分别通过 `DocRoutes`、`DocRouter` 在文档模式下注册，传入的 `db` 为 `nil`，不打开运行时资源。

整个过程就是普通的函数调用：先创建被依赖的对象，再创建依赖它们的对象。数据库、缓存、锁这类多个运行部分都用到的资源放进 `open`；只有某个部分用到的，例如 HTTP 中间件，在它自己的方法里创建。`run` 返回之前，`defer` 中的 `stack.CloseWith` 不会执行，因此正在处理的请求不会因为数据库提前关闭而失败。请求处理与停机的完整顺序见[请求生命周期](lifecycle.md)。

每个运行部分分两步：方法先装配好它需要的一切，包括取得监听端口，再返回运行它的函数。`run` 装配完全部部分之后才调用 `d.Run`，`d.Run` 报告进程已经就绪，再用 `command.Concurrently` 同时运行它们，让它们共用一个 `ctx`，一个出错时其余的一起停止。装配阶段的错误（例如路由冲突、端口被占用）在开始运行之前就返回，[不停机升级](deployment.md#zero-downtime-upgrades)时出错的新版本不会接管服务。把 HTTP 服务、队列和定时任务放在一个进程中运行，见[在一个进程中运行多个部分](commands.md#running-together)。

> [!TIP]
> 入口只打开当前命令真正需要的资源。例如 `serve` 命令需要数据库和 HTTP 监听，而 `openapi`、`routes` 命令不需要这些资源，它们直接使用 `API` 和 `DocRoutes`，不调用 `open`。

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

func WithDatabase(ctx context.Context, driver database.Driver, cfg database.Config,
    run func(*bun.DB) error,
) (err error) {
    var stack cleanup.Stack
    defer func() {
        err = stack.CloseWith(context.Background(), err)
    }()

    db, err := database.Open(ctx, driver, cfg)
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

当你确实需要替换某个依赖时（例如把文章配图上传到对象存储），遵循 Go 的惯例：**由使用方定义接口**。在业务包中声明一个只包含所需方法的小接口，action 的构造函数接收这个接口：

```go
package article

type ImageStore interface {
    Put(ctx context.Context, key string, data []byte) error
}

func NewSetCover(db *bun.DB, images ImageStore) *SetCover {
    return &SetCover{db: db, images: images}
}
```

在测试中传入一个简单的内存实现即可，不需要真实的对象存储：

```go
type memoryImages map[string][]byte

func (m memoryImages) Put(_ context.Context, key string, data []byte) error {
    m[key] = data
    return nil
}

setCover := article.NewSetCover(db, memoryImages{})
```

不需要为每个依赖预先定义接口，只在真正需要替换实现时再引入。
