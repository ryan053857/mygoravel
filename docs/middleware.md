# 中间件

- [简介](#introduction)
- [默认中间件](#default-middleware)
- [HTTP 配置](#http-configuration)
    - [可信代理](#trusted-proxies)
- [自定义中间件](#custom-middleware)
    - [全局中间件](#chi-middleware)
    - [路由中间件](#route-middleware)
- [用于普通 HTTP 处理器](#plain-handlers)

<a id="introduction"></a>
## 简介

中间件提供了一种方便的机制，用来检查和处理进入应用的 HTTP 请求。例如，为每个请求生成请求 ID、记录访问日志、限制请求体大小等。

mygoravel 已经为你准备好了一组常用的中间件。使用 `rest.New` 创建 API 时，它们会被自动安装，你只需通过 [HTTP 配置](#http-configuration)调整它们的行为。如果需要额外的功能，也可以添加原生的 chi 或 Huma 中间件。

<a id="default-middleware"></a>
## 默认中间件

`rest.New` 创建的每个 API 都会自动获得以下能力：

| 能力 | 行为 |
| --- | --- |
| 请求 ID | 为每个请求生成唯一 ID，写入 context 和 `X-Request-ID` 响应头 |
| 访问日志 | 提供日志器时，记录请求方法、状态码、耗时、客户端地址，以及业务错误的错误码 |
| panic 恢复 | 处理器 panic 时，若尚未写出响应则返回通用的 500 错误；若已开始写响应则中止连接 |
| 安全响应头 | 设置 `X-Content-Type-Options: nosniff` |
| 请求体大小限制 | 默认 1 MiB，超出时返回 413 |
| 请求超时 | 请求 context 默认 30 秒后超时 |
| 客户端地址 | 只信任显式配置的代理转发的地址 |
| 语言协商 | 提供语言目录时，根据 `Accept-Language` 选择语言 |

认证不在默认中间件中，因为它是按路由启用的，见[认证](authentication.md)。健康检查也由你自己注册，见[请求生命周期](lifecycle.md#health-checks)。

<a id="configuring-middleware"></a>
<a id="http-configuration"></a>
## HTTP 配置

请求超时、请求体上限、可信代理以及服务器超时都来自 `httpx.Config`，通常放在配置文件的 `http` 段：

```yaml
http:
  addr: ${HTTP_ADDR:-:8080}
  request_timeout: 15s
  max_body_bytes: 2097152
  trusted_proxies: [10.20.0.0/16]
```

| 字段 | 说明 | 默认值 |
| --- | --- | --- |
| `addr` | 监听地址 | `:8080` |
| `read_header_timeout` | 读取请求头的超时 | `5s` |
| `read_timeout` | 读取整个请求的超时 | `30s` |
| `write_timeout` | 写响应的超时 | `30s` |
| `idle_timeout` | keep-alive 连接的空闲超时 | `1m` |
| `request_timeout` | 每个请求 context 的超时 | `30s` |
| `shutdown_timeout` | 停机时排空请求的上限 | `10s` |
| `max_body_bytes` | 请求体的最大字节数 | 1 MiB |
| `trusted_proxies` | 可信的反向代理网段 | 不信任任何代理 |

把 `httpx.Config` 放进应用的根配置结构体（`config.Load` 会自动填充上表中的默认值），再把它交给 `rest.New` 和 `httpx.ListenAndServe`：

```go
api := rest.New(rest.Options{HTTP: cfg.HTTP, Logger: logger, Languages: bundle})
// ...
return httpx.ListenAndServe(ctx, cfg.HTTP, api.Handler(), httpx.ServeOptions{Logger: logger})
```

`Logger` 用于访问日志和错误诊断，`Languages` 用于语言协商和错误消息翻译，两者都可以不传。日志器的构造见[日志](logging.md)，语言目录的构造见[本地化](localization.md)。

`httpx.Config` 中没有设置（零值）的字段会使用默认值，所以测试中直接写 `rest.New(rest.Options{})` 也能正常工作。

<a id="trusted-proxies"></a>
### 可信代理

当应用部署在 Nginx、负载均衡器等反向代理之后时，请求的直接来源是代理的地址，而真实的客户端地址在 `X-Forwarded-For` 头中。但这个头可以被客户端随意伪造，所以只有来自**可信代理**的请求，才应该使用它。

你可以通过 `http.trusted_proxies` 声明代理所在的网段：

```yaml
http:
  trusted_proxies: [10.20.0.0/16]
```

对于来自可信代理的请求，中间件会从右向左读取 `X-Forwarded-For`，跳过所有可信代理的地址，把遇到的第一个不可信地址作为客户端地址。没有配置 `trusted_proxies` 时，始终使用直接连接的地址。

> [!WARNING]
> 只填写你实际使用的代理网段。如果把 `0.0.0.0/0` 之类的宽泛网段设为可信，任何客户端都可以伪造自己的地址。

<a id="custom-middleware"></a>
## 自定义中间件

<a id="chi-middleware"></a>
### 全局中间件

如果需要为所有请求添加自定义行为，可以先创建一个 chi Router 并安装中间件，再通过 `Options.Router` 传给 `rest.New`。

例如，博客在数据库维护期间需要进入只读模式：读请求照常处理，修改文章等写请求统一返回 `503`：

```go
var readOnly atomic.Bool // 由运维开关控制，例如收到某个信号时切换

router := chi.NewRouter()
router.Use(func(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if readOnly.Load() && r.Method != http.MethodGet && r.Method != http.MethodHead {
            httpx.WriteProblem(w, http.StatusServiceUnavailable, "read-only mode")
            return
        }
        next.ServeHTTP(w, r)
    })
})

api := rest.New(rest.Options{Router: router, HTTP: cfg.HTTP, Logger: logger})
```

这里使用了 `github.com/go-chi/chi/v5`。chi 要求所有中间件在注册路由之前安装，而 `rest.New` 会在构造时安装默认中间件，所以请先安装自己的中间件，再调用 `rest.New`，最后注册路由。

> [!NOTE]
> 以这种方式添加的中间件会在默认中间件**之前**执行。此时请求还没有请求 ID，panic 也不会被恢复，所以请让这类中间件保持简单，并自行处理可能出现的错误。

<a id="route-middleware"></a>
### 路由中间件

只对某条路由生效的逻辑，可以使用 Huma 原生的 `Operation.Middlewares`。

例如，博客早期的接口路径是 `/posts/{id}`，后来统一改为 `/articles/{id}`。旧路径暂时保留，但要在响应头中提醒客户端迁移：

```go
legacy := huma.Operation{
    OperationID: "post-get",
    Method:      http.MethodGet,
    Path:        "/posts/{id}",
    Summary:     "获取文章（已废弃，请使用 /articles/{id}）",
    Deprecated:  true,
}
legacy.Middlewares = append(legacy.Middlewares, func(ctx huma.Context, next func(huma.Context)) {
    ctx.SetHeader("Deprecation", "true")
    ctx.SetHeader("Link", "</articles/"+ctx.Param("id")+`>; rel="successor-version"`)
    next(ctx)
})
requireArticles(&legacy)
```

之后像注册 `article-get` 一样注册它即可。`Deprecated: true` 还会在 OpenAPI 文档中把这条路由标记为已废弃。

路由中间件在默认中间件之后、处理器之前执行。

> [!TIP]
> 认证请不要自己写路由中间件，而是使用 [`rest.RequireBearer`](authentication.md#protecting-routes)。它在检查凭据的同时，还会在 OpenAPI 文档中标记该路由需要认证，确保运行行为和文档一致。

<a id="plain-handlers"></a>
## 用于普通 HTTP 处理器

如果你有一个不使用 `rest` 包的普通 `http.Handler`（例如标准库的 `http.ServeMux`），也可以单独为它安装默认中间件：

```go
handler := httpx.Middleware(cfg.HTTP, httpx.MiddlewareOptions{Logger: logger})(mux)
```

请注意，同一条请求链上只应安装一次这组中间件。`rest.New` 已经安装过了，因此不需要对 `api.Handler()` 再次使用 `httpx.Middleware`。
