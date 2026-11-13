# 请求生命周期

- [简介](#introduction)
- [生命周期概览](#lifecycle-overview)
    - [启动](#startup)
    - [请求的处理过程](#request-flow)
    - [平滑停止](#graceful-shutdown)
- [健康检查](#health-checks)

<a id="introduction"></a>
## 简介

使用任何工具时，如果了解它的工作原理，用起来就会更加得心应手。本章将从整体上介绍一个 Tenon 应用从启动、处理请求到停止的完整过程。

如果有些概念现在还不太理解，不必担心，先建立一个整体印象即可。后续章节会逐一深入介绍。

<a id="lifecycle-overview"></a>
## 生命周期概览

<a id="startup"></a>
### 启动

一个 Tenon 应用的入口拥有所有运行时资源。对于需要数据库的 API，启动过程通常是这样的：

1. 读取并校验当前命令需要的[配置](configuration.md)；
2. 打开数据库等资源，每成功打开一个，就登记对应的关闭操作；
3. 创建 API，构造 action 并注册路由，取得监听端口；
4. 调用 `httpx.Serve` 开始处理请求。

这些步骤都是普通的 Go 代码，具体写法见[依赖装配](dependencies.md#assembling-the-application)。

> [!NOTE]
> 数据库迁移不在 API 启动时自动执行，而是作为独立的发布步骤运行。详见[迁移](migrations.md)。

<a id="request-flow"></a>
### 请求的处理过程

按[依赖装配](dependencies.md#assembling-the-application)中的写法安装 `httpx.Defaults` 和 `httpx.Timeout` 后，一个请求从进入到返回，会依次经过：

```text
确定请求 ID，写入安全响应头
  → 解析客户端地址（仅信任配置的代理）
  → 根据 Accept-Language 协商语言
  → 开始记录访问日志
  → panic 恢复
  → 请求体大小限制，设置请求超时
  → 你自己安装的中间件（例如限流）
  → 路由匹配
  → Bearer 认证（仅限需要认证的路由）
  → Huma 解析并验证输入
  → action 的 HTTP 方法调用 Handle
  → 错误转换为统一的错误响应
```

以[错误处理](errors.md#example)一章中“修改文章”的请求 `PUT /articles/{id}` 为例：

- 没有携带有效 token 的请求，在 Bearer 认证这一步就返回 `401`；
- 标题为空或 slug 格式不对的请求，在 Huma 验证输入这一步返回 `422`，你的处理器不会被调用；
- 通过了前面所有步骤，`Update.HTTP` 才会调用 `Handle`。它返回的业务错误（例如 slug 已被使用）在最后一步被转换为 `409` 等响应。

前半部分由安装在 Router 上的中间件完成，作用于所有请求，详见[中间件](middleware.md)；认证由路由上声明的策略决定，详见[认证](authentication.md)；输入解析与验证见[请求](requests.md)和[验证](validation.md)；处理器返回的错误如何转换为 HTTP 响应，见[错误处理](errors.md)。

中间件、语言目录等都是在创建 API 时显式传入的；日志器默认使用 `slog.Default()`，也可以显式传入。因此，同一个进程中的两个 API 实例可以拥有各自不同的配置，互不影响。

<a id="graceful-shutdown"></a>
### 平滑停止

当应用收到 `SIGTERM` 或 Ctrl+C 时，我们希望它**不要粗暴地中断正在处理的请求**。`httpx.Serve` 负责完成这件事：

```go
return httpx.Serve(ctx, a.cfg.HTTP, ln, api.Handler(), httpx.ServeOptions{
    OnDraining: draining.Set,
})
```

`ln` 是入口用 [`command.Daemon`](dependencies.md#assembling-the-application) 取得的监听端口；`httpx.Serve` 按 [`httpx.Config`](middleware.md#http-configuration) 设置服务器超时。不使用 `command.Daemon` 的简单应用可以改用 `httpx.ListenAndServe(ctx, cfg, handler, opts)`，它自己监听 `cfg.Addr`。当 `ctx` 被取消（收到停止信号，或者[不停机升级](deployment.md#zero-downtime-upgrades)时交接给了新进程），或者监听发生异常时，它会按以下顺序停止服务：

1. 调用 `OnDraining`，通知外界服务即将停止（例如让就绪检查开始返回失败）；
2. 停止接收新连接，等待正在处理的请求完成——这个过程称为**排空**；
3. 如果超过 `shutdown_timeout`（默认 10 秒）仍有请求未完成，则取消这些请求的 context 并强制关闭连接。

`httpx.Serve` 返回之后，入口才会关闭数据库等资源。这个顺序保证了正在处理的请求在结束前始终可以访问数据库。

> [!WARNING]
> 平滑停止依赖于 context 的传递。业务代码中的数据库查询和外部调用都应该使用请求的 `ctx`，这样它们才能在超时后及时停止。忽略 context 的 goroutine 会在停机后继续运行。

<a id="health-checks"></a>
## 健康检查

部署平台（如 Kubernetes）和负载均衡器通常通过两类接口判断应用的状态：

- **存活检查**（liveness）：进程是否还能响应，失败时平台重启它。只需要返回成功，不要检查依赖：数据库故障时重启应用无济于事。
- **就绪检查**（readiness）：应用现在能否处理请求，失败时停止向它转发流量。它检查是否正在排空，以及必需的依赖是否可用。

Tenon 不会自动注册这两个路由，路径由部署环境决定。就绪检查用 `httpx.Ready`，下面注册在 `/readyz`。其中 `draining` 与上面 `ServeOptions.OnDraining` 使用的是同一个 `httpx.Draining`：

```go
var draining httpx.Draining

router.Get("/livez", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
router.Handle("/readyz", httpx.Ready(httpx.ReadyOptions{
    Draining: &draining,
    Checks: []httpx.Check{
        {Name: "database", Ping: a.db.PingContext},
        {Name: "redis", Ping: func(ctx context.Context) error {
            return a.rdb.Do(ctx, a.rdb.B().Ping().Build()).Error()
        }},
        {Name: "cache", Ping: func(context.Context) error { return a.mc.Ping() }, Optional: true},
    },
}))
```

`a.rdb` 是 [Redis](redis.md) 客户端，`a.mc` 是[缓存](cache.md)用的 Memcached 客户端。每项检查是一个 `func(ctx) error`。全部检查并行执行，每项最多 1 秒（`ReadyOptions.Timeout`），超时按失败处理，不理会 `ctx` 的检查（例如 Memcached 客户端的 `Ping`）也不会拖住响应。必需的检查都通过时返回 `200`，否则返回 `503`；正在排空时不再检查，直接返回 `503`，负载均衡器随即停止向这个实例转发新请求。响应体列出每项的结果：

```json
{"status":"ready","checks":{"cache":"failed","database":"ok","redis":"ok"}}
```

失败的原因不出现在响应中，而是记录在日志里。日志只记录变化：一项检查从通过变为失败时记录 `Warn` 和原因，恢复时记录 `Info`，平台每隔几秒的探测不会让日志刷屏。

只把处理请求**必需**的依赖放进检查。缓存这类坏了也能降级运行的依赖标为 `Optional`：它失败时结果中报告，但不让就绪检查失败。否则缓存的短暂故障会让全部实例同时被摘除，整个服务因为一个可选的依赖而不可用。队列（请求只写数据库中的 outbox）、PDF、向量检索这类只有部分接口用到的服务也不要放进来，它们的故障由各自的接口返回错误、由[告警](telemetry.md#alerting)发现。

探测很频繁，在[访问日志](middleware.md#http-configuration)的 `ignore_paths` 中排除这两个路径。

