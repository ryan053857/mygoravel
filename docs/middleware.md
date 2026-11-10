# 中间件

- [简介](#introduction)
- [默认中间件](#default-middleware)
- [HTTP 配置](#http-configuration)
    - [请求 ID](#request-id)
    - [可信代理](#trusted-proxies)
    - [访问日志](#access-log)
- [限流](#rate-limiting)
- [自行组装](#custom-stack)
- [自定义中间件](#custom-middleware)
    - [全局中间件](#chi-middleware)
    - [路由中间件](#route-middleware)
- [用于普通 HTTP 处理器](#plain-handlers)

<a id="introduction"></a>
## 简介

中间件提供了一种方便的机制，用来检查和处理进入应用的 HTTP 请求。例如，为每个请求生成请求 ID、记录访问日志、限制请求体大小等。

mygoravel 的中间件都是标准的 `func(http.Handler) http.Handler`，安装在 chi Router 上。`rest.New` 不会替你安装任何中间件。

**一个中间件是否生效，只取决于代码里有没有安装它。** 配置文件只提供参数，任何配置值都不会开启或关闭中间件。反过来，安装中间件时参数不合法（例如超时为 0、日志器为 `nil`），构造时就会 panic，而不是静默跳过。

<a id="default-middleware"></a>
## 默认中间件

`httpx.Defaults` 返回每个应用都应该安装的基础中间件。先把它们安装到 Router 上，再把 Router 交给 `rest.New`：

```go
router := chi.NewRouter()
router.Use(httpx.Defaults(cfg.HTTP, nil)...)

api := rest.New(rest.Options{Router: router})
```

这里使用了 `github.com/go-chi/chi/v5`。`Defaults` 的第二个参数是日志器，传 `nil` 时使用全局日志器 `slog.Default()`，见[日志](logging.md#creating-a-logger)。

`httpx.Defaults` 按以下顺序包含这些中间件，每一个都可以单独使用：

| 中间件 | 行为 |
| --- | --- |
| `AssignRequestID(cfg.RequestID)` | 确定请求 ID，写入 context、日志字段和 `X-Request-ID` 响应头；入站请求 ID 合法时沿用，见[请求 ID](#request-id) |
| `NoSniff()` | 设置 `X-Content-Type-Options: nosniff` |
| `ClientIP(cfg.ClientIP)` | 只信任显式配置的代理，从 `X-Forwarded-For` 解析客户端地址 |
| `AccessLog(cfg.AccessLog, logger)` | 每个请求记录一条访问日志，见[访问日志](#access-log) |
| `Recover(logger)` | 处理器 panic 时记录堆栈；若尚未写出响应则返回通用的 500 错误，若已开始写响应则中止连接 |
| `BodyLimit(cfg.BodyLimit)` | 限制请求体大小，超出时返回 413 |
| `Timeout(cfg.Timeout)` | 为请求 context 设置超时 |

以下中间件是可选的，需要时在 `httpx.Defaults` 之后另行安装：

| 中间件 | 行为 |
| --- | --- |
| `RateLimit(cfg.RateLimit, key)` | 按客户端限流，超出时返回 429，见[限流](#rate-limiting) |
| `Locale(bundle)` | 根据 `Accept-Language` 选择语言，见[本地化](localization.md#enabling-localization) |

框架的其他部分不依赖这些中间件是否安装。没有 `AccessLog` 时，`httpx.AddAccessLogAttrs` 什么也不做；没有 `Locale` 时，错误消息使用默认语言；没有 `AssignRequestID` 时，`httpx.RequestID` 返回空字符串。

认证不在默认中间件中，因为它是按路由启用的，见[认证](authentication.md)。健康检查也由你自己注册，见[请求生命周期](lifecycle.md#health-checks)。

<a id="configuring-middleware"></a>
<a id="http-configuration"></a>
## HTTP 配置

`httpx.Config` 对应配置文件的 `http` 段：顶层是服务器参数，每个有参数的中间件对应一个同名子段。

```yaml
http:
  addr: ${HTTP_ADDR:-:8080}
  shutdown_timeout: 10s
  request_id:
    header: X-Request-ID
  client_ip:
    trusted_proxies: [10.20.0.0/16]
  access_log:
    ignore_paths: [/healthz, /readyz]
  timeout:
    duration: 15s
  body_limit:
    max_bytes: 2097152
```

服务器参数：

| 字段 | 说明 | 默认值 |
| --- | --- | --- |
| `addr` | 监听地址，格式为 `host:port`，端口必须是 0–65535 的数字 | `:8080` |
| `read_header_timeout` | 读取请求头的超时 | `5s` |
| `read_timeout` | 读取整个请求的超时 | `30s` |
| `write_timeout` | 写响应的超时 | `30s` |
| `idle_timeout` | keep-alive 连接的空闲超时 | `1m` |
| `shutdown_timeout` | 停机时排空请求的上限 | `10s` |

中间件参数：

| 字段 | 中间件 | 说明 | 默认值 |
| --- | --- | --- | --- |
| `request_id.header` | `AssignRequestID` | 读取入站请求 ID 的请求头 | `X-Request-ID` |
| `client_ip.trusted_proxies` | `ClientIP` | 可信的反向代理网段 | 不信任任何代理 |
| `access_log.*` | `AccessLog` | 见[访问日志](#access-log) | 只记录基本字段 |
| `timeout.duration` | `Timeout` | 每个请求 context 的超时 | `30s` |
| `body_limit.max_bytes` | `BodyLimit` | 请求体的最大字节数 | 1 MiB |
| `rate_limit.*` | `RateLimit` | 见[限流](#rate-limiting) | 每秒 10 个，突发 20 个 |

没有写出的字段使用默认值。所有超时和数值都必须为正数，写 `0` 或负数会在加载配置时报错；不需要某个中间件时，不安装它即可。

把 `httpx.Config` 放进应用的根配置结构体（`config.Load` 会自动填充默认值并校验），再把它交给 `httpx.Defaults` 和 `httpx.ListenAndServe`：

```go
router.Use(httpx.Defaults(cfg.HTTP, nil)...)
// ...
return httpx.ListenAndServe(ctx, cfg.HTTP, api.Handler(), httpx.ServeOptions{})
```

不经过 `config.Load` 时（例如在测试中），请从 `httpx.DefaultConfig()` 开始修改。零值的 `httpx.Config{}` 不合法，`httpx.Defaults` 会 panic，`httpx.ListenAndServe` 会返回错误。

<a id="request-id"></a>
### 请求 ID

`AssignRequestID` 为每个请求确定一个 ID：入站请求头中带有合法的 ID 时沿用它，否则生成一个随机 ID。这个 ID 会：

- 写入请求的 context，用 `httpx.RequestID(ctx)` 读取；
- 作为 `request_id` 字段出现在这个请求的所有日志中，见[请求关联](logging.md#request-correlation)；
- 通过 `X-Request-ID` 响应头返回给客户端，用户反馈问题时可以直接提供它。

它同时写入 chi 的 `middleware.RequestIDKey`，所以 `middleware.GetReqID(ctx)` 和读取它的社区中间件拿到的也是同一个值，不需要再安装 chi 的 `middleware.RequestID`。

合法的 ID 是 1–128 个可打印的 ASCII 字符，不含空格。不合法时（例如超长、包含控制字符）丢弃，重新生成。

部署在反向代理之后时，沿用代理生成的 ID，就能把代理日志和应用日志关联起来。默认读取 `X-Request-ID`，代理使用其他请求头时通过 `http.request_id.header` 指定：

| 代理 | 配置 |
| --- | --- |
| Nginx（`proxy_set_header X-Request-ID $request_id`）、Envoy、Istio、Traefik | 默认即可 |
| AWS ALB | `header: X-Amzn-Trace-Id` |
| Google Cloud 负载均衡 | `header: X-Cloud-Trace-Context` |
| Cloudflare | `header: CF-Ray` |

无论从哪个请求头读取，响应始终通过 `X-Request-ID` 返回。

客户端自己携带的 ID 能否到达应用，取决于代理的配置：上面 Nginx 的写法总是用 Nginx 生成的值覆盖客户端的值，Envoy 默认也会在入口重新生成。需要优先沿用客户端的 ID 时，请在代理中配置透传。

<a id="trusted-proxies"></a>
### 可信代理

当应用部署在 Nginx、负载均衡器等反向代理之后时，请求的直接来源是代理的地址，而真实的客户端地址在 `X-Forwarded-For` 头中。但这个头可以被客户端随意伪造，所以只有来自**可信代理**的请求，才应该使用它。

你可以通过 `http.client_ip.trusted_proxies` 声明代理所在的网段：

```yaml
http:
  client_ip:
    trusted_proxies: [10.20.0.0/16, fd00::/64]
```

每一项都必须是网络地址，即主机位全为零。只信任单台机器时写成 `/32`（IPv6 为 `/128`）：

| 写法 | 结果 |
| --- | --- |
| `10.20.0.0/16` | 合法 |
| `10.20.0.5/32` | 合法，只信任这一台 |
| `10.20.0.5/16` | 报错：`10.20.0.5/16 has host bits set; use 10.20.0.0/16 or 10.20.0.5/32` |
| `10.20.0.5` | 报错：缺少前缀长度 |
| `""`、`300.0.0.0/8`、`10.0.0.0/33` | 报错：不是有效的网段 |

对于来自可信代理的请求，`ClientIP` 会从右向左读取 `X-Forwarded-For`，跳过所有可信代理的地址，把遇到的第一个不可信地址作为客户端地址。没有配置可信代理时，始终使用直接连接的地址。

> [!WARNING]
> 只填写你实际使用的代理网段。如果把 `0.0.0.0/0` 之类的宽泛网段设为可信，任何客户端都可以伪造自己的地址。

<a id="access-log"></a>
### 访问日志

`AccessLog` 基于 [slog-chi](https://github.com/samber/slog-chi)，在每个请求结束后记录一条消息为 `http request` 的日志：

```json
{"time":"…","level":"WARN","msg":"http request","service":"blog","request":{"time":"…","method":"PUT","host":"api.example.com","path":"/articles/0f8c","query":"","params":{"id":"0f8c"},"route":"/articles/{id}","referer":"","ip":"203.0.113.7:51234","length":0},"response":{"time":"…","latency":32209,"status":409,"length":0},"code":"article.slug_taken","request_id":"9F2K…"}
```

- 消息固定为 `http request`，用 `msg = "http request"` 就能筛出所有访问日志；
- 级别随状态码变化：`5xx` 为 `ERROR`，`4xx` 为 `WARN`，其他为 `INFO`；
- `request.route` 是路由模板，适合按接口聚合统计；
- `request.ip` 是 [`ClientIP`](#trusted-proxies) 解析后的客户端地址；
- 业务错误的错误码由框架通过 `httpx.AddAccessLogAttrs` 追加为顶层的 `code` 字段。你也可以在处理过程中调用它追加自己的字段，同名字段后写的覆盖先写的。

更多字段通过 `http.access_log` 开启：

| 字段 | 说明 | 默认值 |
| --- | --- | --- |
| `ignore_paths` | 不记录访问日志的路径（精确匹配），例如健康检查 | 无 |
| `user_agent` | 记录 `User-Agent` | `false` |
| `request_header`、`response_header` | 记录请求头、响应头 | `false` |
| `request_body`、`response_body` | 记录请求体、响应体，各最多 64 KiB | `false` |
| `trace_id` | 记录 OpenTelemetry 的 `trace_id` 和 `span_id` | `false` |

记录请求头时，`Authorization`、`Cookie`、`X-Auth-Token`、`X-CSRF-Token`、`X-XSRF-Token` 始终隐藏，记录响应头时 `Set-Cookie` 始终隐藏。还需要隐藏其他字段时，见[敏感信息脱敏](logging.md#redacting-sensitive-data)。

> [!NOTE]
> 处理器没有写出任何响应时，客户端收到的是 `200`。`AccessLog` 会显式写出这个 `200`，让日志中的状态码与客户端看到的一致。

<a id="rate-limiting"></a>
## 限流

`httpx.RateLimit` 为每个客户端维护一个令牌桶：桶里有令牌时请求照常处理，没有令牌时直接返回 `429 Too Many Requests`，并通过 `Retry-After` 响应头告诉客户端多少秒后重试。被拒绝的请求不消耗令牌。

限流不在默认中间件中。需要时在 `httpx.Defaults` 之后安装：

```go
router.Use(httpx.Defaults(cfg.HTTP, nil)...)
router.Use(httpx.RateLimit(cfg.HTTP.RateLimit, httpx.KeyByIP))
```

参数来自 `http.rate_limit` 段：

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
| `rate` | 每个 `interval` 补充的令牌数 | `10` |
| `interval` | 补充 `rate` 个令牌的周期 | `1s` |
| `burst` | 桶容量，即允许的瞬时突发 | `20` |
| `max_entries` | 同时跟踪的客户端数，超出时淘汰最久没有请求的客户端 | `10000` |

上面的配置表示：每个客户端平均每秒最多 10 个请求，短时间内最多连续 20 个。

<a id="rate-limit-key"></a>
### 限流的 key

`RateLimit` 的第二个参数决定按什么维度计数。`httpx.KeyByIP` 按客户端地址计数，它使用 `ClientIP` 解析后的地址，所以部署在反向代理之后时，请同时配置[可信代理](#trusted-proxies)，否则所有请求都会被算到代理头上。

你也可以传入自己的函数。例如，博客的开放 API 按 token 限流，没有 token 的请求仍然按地址限流：

```go
router.Use(httpx.RateLimit(cfg.HTTP.RateLimit, func(r *http.Request) string {
    if token := r.Header.Get("Authorization"); token != "" {
        return token
    }
    return httpx.KeyByIP(r)
}))
```

函数返回空字符串时，这个请求不受限流。

限流中间件安装在路由匹配和认证之前，因此这里还拿不到 `rest.RequireBearer` 解析出的用户身份，只能使用请求本身携带的信息。

> [!WARNING]
> 令牌桶保存在进程内存中。部署多个实例时，每个实例分别计数，客户端实际能达到的速率是配置值乘以实例数。需要跨实例的精确限流时，请在网关中配置，或者编写基于 Redis 等共享存储的[自定义中间件](#custom-middleware)。

<a id="custom-stack"></a>
## 自行组装

`httpx.Defaults` 只是一个返回切片的普通函数。当默认组合不合适时，可以直接挑选需要的中间件，自己决定顺序和参数。

例如，博客允许作者在 `/uploads/` 下上传最大 20 MiB 的图片，其他接口仍然使用配置的上限。`BodyLimit` 一旦安装就无法在内层放宽，所以要在安装时按路径选择上限：

```go
router.Use(
    httpx.AssignRequestID(cfg.HTTP.RequestID),
    httpx.NoSniff(),
    httpx.ClientIP(cfg.HTTP.ClientIP),
    httpx.AccessLog(cfg.HTTP.AccessLog, nil),
    httpx.Recover(nil),
    httpx.Timeout(cfg.HTTP.Timeout),
    func(next http.Handler) http.Handler {
        small := httpx.BodyLimit(cfg.HTTP.BodyLimit)(next)
        large := httpx.BodyLimit(httpx.BodyLimitConfig{MaxBytes: 20 << 20})(next)
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
router.Use(httpx.Defaults(cfg.HTTP, nil)...)
router.Use(func(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if readOnly.Load() && r.Method != http.MethodGet && r.Method != http.MethodHead {
            httpx.WriteProblem(w, http.StatusServiceUnavailable, "read-only mode")
            return
        }
        next.ServeHTTP(w, r)
    })
})

api := rest.New(rest.Options{Router: router})
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
handler := chi.Chain(httpx.Defaults(cfg.HTTP, nil)...).Handler(mux)
```
