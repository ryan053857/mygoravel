# 中间件

- [简介](#introduction)
- [默认中间件](#default-middleware)
- [HTTP 配置](#http-configuration)
    - [可信代理](#trusted-proxies)
- [限流](#rate-limiting)
- [自行组装](#custom-stack)
- [自定义中间件](#custom-middleware)
    - [全局中间件](#chi-middleware)
    - [路由中间件](#route-middleware)
- [用于普通 HTTP 处理器](#plain-handlers)

<a id="introduction"></a>
## 简介

中间件提供了一种方便的机制，用来检查和处理进入应用的 HTTP 请求。例如，为每个请求生成请求 ID、记录访问日志、限制请求体大小等。

mygoravel 的中间件都是标准的 `func(http.Handler) http.Handler`，安装在 chi Router 上。`rest.New` 不会替你安装任何中间件：安装哪些、按什么顺序执行，都写在你的入口代码里。对大多数应用，一行 `httpx.Defaults` 就够了。

<a id="default-middleware"></a>
## 默认中间件

`httpx.Defaults` 返回一组推荐的中间件。先把它们安装到 Router 上，再把 Router 交给 `rest.New`：

```go
router := chi.NewRouter()
router.Use(httpx.Defaults(cfg.HTTP, httpx.DefaultsOptions{Logger: logger, Languages: bundle})...)

api := rest.New(rest.Options{Router: router, Logger: logger, Languages: bundle})
```

这里使用了 `github.com/go-chi/chi/v5`。`Logger` 和 `Languages` 都可以不传，不传时相应的中间件不会生效。

`httpx.Defaults` 按以下顺序包含这些中间件，每一个都可以单独使用：

| 中间件 | 行为 |
| --- | --- |
| `AssignRequestID()` | 确定请求 ID，写入 context、日志字段和 `X-Request-ID` 响应头；入站的 `X-Request-ID` 合法时沿用 |
| `NoSniff()` | 设置 `X-Content-Type-Options: nosniff` |
| `ClientIP(proxies)` | 只信任显式配置的代理，从 `X-Forwarded-For` 解析客户端地址 |
| `AccessLog(logger)` | 记录请求方法、状态码、耗时、客户端地址，以及业务错误的错误码 |
| `Recover(logger)` | 处理器 panic 时，若尚未写出响应则返回通用的 500 错误；若已开始写响应则中止连接 |
| `RateLimit(cfg, key)` | 按客户端限流，超出时返回 429；默认不启用，见[限流](#rate-limiting) |
| `BodyLimit(n)` | 限制请求体大小，默认 1 MiB，超出时返回 413 |
| `Timeout(d)` | 为请求 context 设置超时，默认 30 秒 |
| `Locale(bundle)` | 根据 `Accept-Language` 选择语言 |

参数为零值或 `nil` 的中间件会直接放行请求，不做任何处理。例如 `httpx.Timeout(0)` 不设置超时，`httpx.AccessLog(nil)` 不记录日志。

框架的其他部分不依赖这些中间件是否安装。没有 `AccessLog` 时，`httpx.AddAccessLogAttrs` 什么也不做；没有 `Locale` 时，错误消息使用默认语言；没有 `AssignRequestID` 时，`httpx.RequestID` 返回空字符串。

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
| `request_timeout` | 每个请求 context 的超时（`Timeout`） | `30s` |
| `shutdown_timeout` | 停机时排空请求的上限 | `10s` |
| `max_body_bytes` | 请求体的最大字节数（`BodyLimit`） | 1 MiB |
| `trusted_proxies` | 可信的反向代理网段（`ClientIP`） | 不信任任何代理 |
| `rate_limit` | 按客户端限流（`RateLimit`），见[限流](#rate-limiting) | 不限流 |

把 `httpx.Config` 放进应用的根配置结构体（`config.Load` 会自动填充上表中的默认值），再把它交给 `httpx.Defaults` 和 `httpx.ListenAndServe`：

```go
router.Use(httpx.Defaults(cfg.HTTP, httpx.DefaultsOptions{Logger: logger})...)
// ...
return httpx.ListenAndServe(ctx, cfg.HTTP, api.Handler(), httpx.ServeOptions{Logger: logger})
```

`httpx.Defaults` 会把 `httpx.Config` 中没有设置（零值）的字段替换为默认值。日志器的构造见[日志](logging.md)，语言目录的构造见[本地化](localization.md)。

<a id="trusted-proxies"></a>
### 可信代理

当应用部署在 Nginx、负载均衡器等反向代理之后时，请求的直接来源是代理的地址，而真实的客户端地址在 `X-Forwarded-For` 头中。但这个头可以被客户端随意伪造，所以只有来自**可信代理**的请求，才应该使用它。

你可以通过 `http.trusted_proxies` 声明代理所在的网段：

```yaml
http:
  trusted_proxies: [10.20.0.0/16]
```

对于来自可信代理的请求，`ClientIP` 会从右向左读取 `X-Forwarded-For`，跳过所有可信代理的地址，把遇到的第一个不可信地址作为客户端地址。没有配置 `trusted_proxies` 时，始终使用直接连接的地址。

> [!WARNING]
> 只填写你实际使用的代理网段。如果把 `0.0.0.0/0` 之类的宽泛网段设为可信，任何客户端都可以伪造自己的地址。

<a id="rate-limiting"></a>
## 限流

`httpx.RateLimit` 为每个客户端维护一个令牌桶：桶里有令牌时请求照常处理，没有令牌时直接返回 `429 Too Many Requests`，并通过 `Retry-After` 响应头告诉客户端多少秒后重试。

限流默认不启用。在配置文件中写上 `rate_limit` 段，`httpx.Defaults` 中的限流中间件就会生效；删掉这一段，限流就会关闭：

```yaml
http:
  rate_limit:
    rate: 10         # 每个 interval 补充 10 个令牌
    interval: 1s
    burst: 20        # 桶容量，允许短时间内连续发出 20 个请求
    max_entries: 10000
```

| 字段 | 说明 | 默认值 |
| --- | --- | --- |
| `rate` | 每个 `interval` 补充的令牌数，为 0 时不限流 | `0` |
| `interval` | 补充 `rate` 个令牌的周期 | `1s` |
| `burst` | 桶容量，即允许的瞬时突发 | 等于 `rate` |
| `max_entries` | 同时跟踪的客户端数，超出时淘汰最久没有请求的客户端 | `10000` |

上面的配置表示：每个客户端平均每秒最多 10 个请求，短时间内最多连续 20 个。被拒绝的请求不消耗令牌。

<a id="rate-limit-key"></a>
### 限流的 key

默认按客户端地址限流。它使用 `ClientIP` 解析后的地址，所以部署在反向代理之后时，请同时配置[可信代理](#trusted-proxies)，否则所有请求都会被算到代理头上。

如果需要按别的维度限流，可以通过 `DefaultsOptions.RateLimitKey` 传入一个函数。例如，博客的开放 API 按 token 限流，没有 token 的请求仍然按地址限流：

```go
router.Use(httpx.Defaults(cfg.HTTP, httpx.DefaultsOptions{
    Logger: logger,
    RateLimitKey: func(r *http.Request) string {
        if token := r.Header.Get("Authorization"); token != "" {
            return token
        }
        return httpx.KeyByIP(r)
    },
})...)
```

函数返回空字符串时，这个请求不受限流。

限流中间件安装在路由匹配和认证之前，因此这里还拿不到 `rest.RequireBearer` 解析出的用户身份，只能使用请求本身携带的信息。

> [!WARNING]
> 令牌桶保存在进程内存中。部署多个实例时，每个实例分别计数，客户端实际能达到的速率是配置值乘以实例数。需要跨实例的精确限流时，请在网关中配置，或者编写基于 Redis 等共享存储的[自定义中间件](#custom-middleware)。

<a id="custom-stack"></a>
## 自行组装

`httpx.Defaults` 只是一个返回切片的普通函数。当默认组合不合适时，可以直接挑选需要的中间件，自己决定顺序和参数。

例如，博客允许作者在 `/uploads/` 下上传最大 20 MiB 的图片，其他接口仍然限制在 1 MiB。`BodyLimit` 一旦安装就无法在内层放宽，所以要在安装时按路径选择上限：

```go
router.Use(
    httpx.AssignRequestID(),
    httpx.NoSniff(),
    httpx.ClientIP(cfg.HTTP.TrustedProxies),
    httpx.AccessLog(logger),
    httpx.Recover(logger),
    httpx.RateLimit(cfg.HTTP.RateLimit, nil),
    httpx.Timeout(cfg.HTTP.RequestTimeout),
    httpx.Locale(bundle),
    func(next http.Handler) http.Handler {
        small := httpx.BodyLimit(cfg.HTTP.MaxBodyBytes)(next)
        large := httpx.BodyLimit(20 << 20)(next)
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            if strings.HasPrefix(r.URL.Path, "/uploads/") {
                large.ServeHTTP(w, r)
                return
            }
            small.ServeHTTP(w, r)
        })
    },
)
```

上传路由本身还要把 Huma 的 `Operation.MaxBodyBytes` 设为相同的上限，见[请求体大小](requests.md#body-size)。用同样的方式，也可以为流式接口去掉 `Timeout`。

自行组装时请保持 `AccessLog` 在 `Recover` 之外，这样 panic 产生的 500 才会出现在访问日志中。

<a id="custom-middleware"></a>
## 自定义中间件

<a id="chi-middleware"></a>
### 全局中间件

自己编写的中间件和默认中间件安装方式完全相同，`router.Use` 的先后顺序就是执行顺序。

例如，博客在数据库维护期间需要进入只读模式：读请求照常处理，修改文章等写请求统一返回 `503`：

```go
var readOnly atomic.Bool // 由运维开关控制，例如收到某个信号时切换

router := chi.NewRouter()
router.Use(httpx.Defaults(cfg.HTTP, httpx.DefaultsOptions{Logger: logger})...)
router.Use(func(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if readOnly.Load() && r.Method != http.MethodGet && r.Method != http.MethodHead {
            httpx.WriteProblem(w, http.StatusServiceUnavailable, "read-only mode")
            return
        }
        next.ServeHTTP(w, r)
    })
})

api := rest.New(rest.Options{Router: router, Logger: logger})
```

安装在默认中间件之后，被拒绝的写请求同样带有请求 ID、出现在访问日志中，panic 也会被恢复。

chi 要求所有中间件在注册路由之前安装，所以请先安装中间件，再调用 `rest.New` 并注册路由。

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

路由中间件在 Router 上的全局中间件之后、处理器之前执行。

> [!TIP]
> 认证请不要自己写路由中间件，而是使用 [`rest.RequireBearer`](authentication.md#protecting-routes)。它在检查凭据的同时，还会在 OpenAPI 文档中标记该路由需要认证，确保运行行为和文档一致。

<a id="plain-handlers"></a>
## 用于普通 HTTP 处理器

这些中间件不依赖 `rest` 包。如果你有一个普通的 `http.Handler`（例如标准库的 `http.ServeMux`），可以用 `chi.Chain` 为它安装：

```go
handler := chi.Chain(httpx.Defaults(cfg.HTTP, httpx.DefaultsOptions{Logger: logger})...).Handler(mux)
```
