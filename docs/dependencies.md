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
package note

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
func (s *Service) Find(ctx context.Context, id uuid.UUID) (*Note, error) {
    var item Note
    err := s.db.NewSelect().Model(&item).Where("id = ?", id).Scan(ctx)
    // ...
}
```

<a id="assembling-the-application"></a>
## 在入口中组装应用

应用的入口（通常是 `internal/bootstrap` 包）负责创建所有依赖，并按正确的顺序把它们连接起来。下面是一个 `serve` 命令背后的组装函数片段，`Config` 是应用自己定义的配置结构，`note` 和 `notehttp` 是应用自己的业务包：

```go
func Serve(ctx context.Context, cfg Config, logger *slog.Logger) (err error) {
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
    notes := note.NewService(db)

    // 3. 创建 API 并注册路由
    api := rest.New(rest.Options{
        Title: "Notes API", Version: "1.0.0",
        HTTP: cfg.HTTP, Logger: logger,
    })
    notehttp.Register(api, notes)

    // 4. 开始监听，直到 ctx 被取消
    return httpx.ListenAndServe(ctx, cfg.HTTP, api.Handler(), httpx.ServeOptions{Logger: logger})
}
```

整个过程就是普通的函数调用：先创建被依赖的对象，再创建依赖它们的对象。`httpx.ListenAndServe` 返回后，`defer` 中的 `stack.CloseWith` 才会关闭数据库连接，因此正在处理的请求不会因为数据库提前关闭而失败。请求处理与停机的完整顺序见[请求生命周期](lifecycle.md)。

> [!TIP]
> 入口只打开当前命令真正需要的资源。例如 `serve` 命令需要数据库和 HTTP 监听，而 `openapi` 命令什么资源都不需要。把不同命令的组装逻辑分开，可以让每个命令都快速、独立地运行。

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

当你确实需要替换某个依赖时（例如调用第三方支付接口），遵循 Go 的惯例：**由使用方定义接口**。在业务包中声明一个只包含所需方法的小接口，构造函数接收这个接口，测试中传入一个简单的实现即可：

```go
type PaymentGateway interface {
    Charge(ctx context.Context, orderID string, amount int64) error
}

func NewCheckoutService(db *bun.DB, payments PaymentGateway) *CheckoutService {
    return &CheckoutService{db: db, payments: payments}
}
```

不需要为每个服务预先定义接口，只在真正需要替换实现时再引入。
