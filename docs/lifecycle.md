# 请求生命周期

- [简介](#introduction)
- [生命周期概览](#lifecycle-overview)
    - [启动](#startup)
    - [请求的处理过程](#request-flow)
    - [平滑停止](#graceful-shutdown)
- [健康检查](#health-checks)

<a id="introduction"></a>
## 简介

使用任何工具时，如果了解它的工作原理，用起来就会更加得心应手。本章将从整体上介绍一个 mygoravel 应用从启动、处理请求到停止的完整过程。

如果有些概念现在还不太理解，不必担心，先建立一个整体印象即可。后续章节会逐一深入介绍。

<a id="lifecycle-overview"></a>
## 生命周期概览

<a id="startup"></a>
### 启动

一个 mygoravel 应用的入口拥有所有运行时资源。对于需要数据库的 API，启动过程通常是这样的：

1. 读取并校验当前命令需要的[配置](configuration.md)；
2. 打开数据库等资源，每成功打开一个，就登记对应的关闭操作；
3. 构造业务服务，创建 API 并注册路由；
4. 调用 `httpx.ListenAndServe` 开始处理请求。

这些步骤都是普通的 Go 代码，具体写法见[依赖装配](dependencies.md#assembling-the-application)。

> [!NOTE]
> 数据库迁移不在 API 启动时自动执行，而是作为独立的发布步骤运行。详见[迁移](migrations.md)。

<a id="request-flow"></a>
### 请求的处理过程

`rest.New` 创建的 API 已经内置了一组默认中间件。一个请求从进入到返回，会依次经过：

```text
解析客户端地址（仅信任配置的代理）
  → 生成请求 ID，设置请求超时
  → 写入安全响应头，开始记录访问日志
  → panic 恢复
  → 请求体大小限制
  → 语言协商（启用本地化时）
  → 路由匹配
  → Bearer 认证（仅限需要认证的路由）
  → Huma 解析并验证输入
  → 你的处理器调用业务服务
  → 错误转换为统一的错误响应
```

前半部分由 `httpx` 中间件完成，作用于所有请求，详见[中间件](middleware.md)；认证由路由上声明的策略决定，详见[认证](authentication.md)；输入解析与验证见[请求](requests.md)和[验证](validation.md)；处理器返回的错误如何转换为 HTTP 响应，见[错误处理](errors.md)。

日志器、语言目录等都是在创建 API 时显式传入的。因此，同一个进程中的两个 API 实例可以拥有各自不同的配置，互不影响。

<a id="graceful-shutdown"></a>
### 平滑停止

当应用收到 `SIGTERM` 或 Ctrl+C 时，我们希望它**不要粗暴地中断正在处理的请求**。`httpx.ListenAndServe` 负责完成这件事：

```go
return httpx.ListenAndServe(ctx, cfg.HTTP, api.Handler(), httpx.ServeOptions{
    Logger:     logger,
    OnDraining: draining.Set,
})
```

`httpx.ListenAndServe` 按 [`httpx.Config`](middleware.md#http-configuration) 监听地址、设置服务器超时。如果你已经有一个 `net.Listener`（例如测试中监听 `127.0.0.1:0`），可以改用 `httpx.Serve(ctx, cfg, listener, handler, opts)`。当 `ctx` 被取消（通常来自 `signal.NotifyContext`），或者监听发生异常时，它会按以下顺序停止服务：

1. 调用 `OnDraining`，通知外界服务即将停止（例如让就绪检查开始返回失败）；
2. 停止接收新连接，等待正在处理的请求完成——这个过程称为**排空**；
3. 如果超过 `shutdown_timeout`（默认 10 秒）仍有请求未完成，则取消这些请求的 context 并强制关闭连接。

`httpx.ListenAndServe` 返回之后，入口才会关闭数据库等资源。这个顺序保证了正在处理的请求在结束前始终可以访问数据库。

> [!WARNING]
> 平滑停止依赖于 context 的传递。业务代码中的数据库查询和外部调用都应该使用请求的 `ctx`，这样它们才能在超时后及时停止。忽略 context 的 goroutine 会在停机后继续运行。

<a id="health-checks"></a>
## 健康检查

部署平台（如 Kubernetes）通常通过两类接口判断应用的状态：

- **存活检查**（liveness）：进程是否还能响应。通常只需要返回成功即可。
- **就绪检查**（readiness）：应用现在能否处理业务请求。它应该检查是否正在排空，以及必需的依赖是否可用。

mygoravel 不会自动注册健康检查路由，你可以根据自己的部署环境决定路径和检查内容。下面的处理器可以注册为 `/readyz`。其中 `db` 是已经打开的数据库，`draining` 是一个 `httpx.Draining`，与上面 `ServeOptions.OnDraining` 使用的是同一个对象：

```go
var draining httpx.Draining

api.Router().Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
    ctx, cancel := context.WithTimeout(r.Context(), time.Second)
    defer cancel()
    if draining.Load() || db.PingContext(ctx) != nil {
        httpx.WriteProblem(w, http.StatusServiceUnavailable, "not ready")
        return
    }
    w.WriteHeader(http.StatusNoContent)
})
```

一旦开始排空，`draining.Load()` 就会返回 `true`，就绪检查随之失败，负载均衡器会停止向这个实例转发新请求。

> [!TIP]
> 就绪检查只应探测处理业务请求**必需**的依赖，并使用较短的超时。如果把可选依赖（例如缓存）也纳入检查，缓存的短暂故障就可能导致所有实例同时被摘除。
