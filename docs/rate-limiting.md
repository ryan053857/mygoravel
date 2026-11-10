# 限流

- [简介](#introduction)
- [限流器](#limiters)
- [限制请求频率](#http-middleware)
    - [限流的 key](#rate-limit-key)
- [在处理器中限流](#in-handlers)
- [使用数据库存储](#database-limiter)
    - [清理](#cleanup)
- [使用 Redis 存储](#redis-limiter)

<a id="introduction"></a>
## 简介

限流用来防止某个客户端或某项操作过于频繁：例如每个 IP 每秒最多 10 个请求，或者同一个邮箱每分钟最多尝试登录 5 次。

`ratelimit` 包与传输方式无关。同一个限流器既可以作为 HTTP 中间件按请求限流，也可以在处理器、命令或任务中直接调用。

<a id="limiters"></a>
## 限流器

限流器只有一个方法：

```go
type Limiter interface {
    Allow(ctx context.Context, key string) error
}
```

每次调用为 `key` 消耗一次配额。配额用完时返回 `ratelimit.ErrExceeded`，错误中带有下一次调用可以成功的最短等待时间；存储出错时返回其他错误。

配额由 `ratelimit.Config` 描述：

```go
ratelimit.Config{
    Rate:     10,          // 每个 Interval 补充 10 次
    Interval: time.Second,
    Burst:    20,          // 最多连续 20 次
}
```

上面的配置表示：每个 key 平均每秒最多 10 次，短时间内最多连续 20 次。被拒绝的调用不消耗配额，按返回的等待时间重试即可。

框架提供两种实现：

| 实现 | 计数保存在 | 适合 |
| --- | --- | --- |
| `ratelimit.NewMemory(cfg)` | 进程内存，每个实例分别计数 | 每个请求都要经过的全局限流 |
| `ratelimit.NewDatabase(db, name, cfg)` | 数据库，多个实例共享 | 登录、验证码、发邮件等低频操作 |
| `redislimit.New(rdb, name, cfg)` | Redis，多个实例共享 | 多实例共享的全局限流，以及低频操作 |

三者的算法相同，换实现不会改变限流结果。

<a id="http-middleware"></a>
## 限制请求频率

`httpx.RateLimit` 把限流器安装为中间件。配额用完时返回 `429 Too Many Requests`，并通过 `Retry-After` 响应头告诉客户端多少秒后重试。限流不在默认中间件中，需要时在 `httpx.Defaults` 之后安装：

```go
router.Use(httpx.Defaults(cfg.HTTP, nil, nil)...)
router.Use(httpx.RateLimit(ratelimit.NewMemory(cfg.HTTP.RateLimit), httpx.KeyByIP))
```

`cfg.HTTP.RateLimit` 来自 `http.rate_limit` 配置段，下面是它的默认值：

```yaml
http:
  rate_limit:
    rate: 10
    interval: 1s
    burst: 20
```

内存限流器会自动删除已经回满的 key，内存占用只与最近活跃的客户端数量有关，不需要另行配置上限。

限流器返回存储错误时，中间件返回 `503`，并把错误写入访问日志的 `error` 字段。

<a id="rate-limit-key"></a>
### 限流的 key

`RateLimit` 的第二个参数决定按什么维度计数。`httpx.KeyByIP` 按客户端地址计数，它使用 `ClientIP` 解析后的地址，所以部署在反向代理之后时，请同时配置[可信代理](middleware.md#trusted-proxies)，否则所有请求都会被算到代理头上。

你也可以传入自己的函数。函数返回空字符串时，这个请求不受限流：

```go
skipHealth := func(r *http.Request) string {
    if r.URL.Path == "/healthz" {
        return ""
    }
    return httpx.KeyByIP(r)
}
router.Use(httpx.RateLimit(ratelimit.NewMemory(cfg.HTTP.RateLimit), skipHealth))
```

中间件运行在路由匹配和认证之前，只能使用请求本身携带的信息。不要用 `Authorization` 等未经校验的请求头做 key：客户端每次换一个值就能得到一份新配额。需要按用户限流时，请[在处理器中限流](#in-handlers)。

<a id="in-handlers"></a>
## 在处理器中限流

博客的编辑后台使用邮箱和密码[登录](authentication.md#login-and-logout)。为了防止密码被暴力猜测，同一个邮箱每分钟最多尝试 5 次。只需在登录处理器开头调用限流器：

```go
func login(users *user.Service, sessions *scs.SessionManager, attempts ratelimit.Limiter) func(context.Context, *LoginInput) (*struct{}, error) {
    return func(ctx context.Context, in *LoginInput) (*struct{}, error) {
        if err := attempts.Allow(ctx, in.Body.Email); err != nil {
            return nil, err
        }
        u, err := users.Authenticate(ctx, in.Body.Email, in.Body.Password)
        // ...
    }
}
```

`ErrExceeded` 是分类为 `errorx.ResourceExhausted` 的业务错误，处理器直接返回即可：响应 `429`，带 `Retry-After` 响应头，错误码为 `rate_limit.exceeded`，提示文案按请求语言翻译。

入口中创建限流器并传入处理器：

```go
attempts := ratelimit.NewDatabase(db, "login", ratelimit.Config{Rate: 5, Interval: time.Minute, Burst: 5})
```

登录需要多个实例共享计数，否则攻击者把请求分散到各个实例，就能得到成倍的尝试次数，所以这里使用数据库限流器。

<a id="database-limiter"></a>
## 使用数据库存储

`ratelimit.NewDatabase` 接收应用的 `*bun.DB`，支持 PostgreSQL 和 SQLite。第二个参数 `name` 区分共用一张表的不同限流器：同名的限流器共享配额，所以每个 `name` 应该只对应一种配额。

使用前先发布 `rate_limits` 表的迁移，见[发布框架迁移](migrations.md#publishing-framework-migrations)：

```go
migrationCmd.AddCommand(migratecmd.Publish(postgres.Driver,
    migrate.Source{Name: "ratelimit", FS: ratelimit.Migrations()},
))
```

每个 key 只占一行，每次 `Allow` 是一条原子的 SQL 语句，多个实例并发调用时计数也是精确的。在 PostgreSQL 上，时间取自数据库时钟；这张表是 `UNLOGGED` 表，不写 WAL，数据库崩溃后表会被清空，相当于所有 key 的配额回满。SQLite 只在单机上使用，时间取自应用时钟。

每次 `Allow` 都是一次数据库写入，因此不要用它做每个请求都经过的全局限流，那种场景请使用内存或 [Redis](#redis-limiter) 限流器，或者在网关中配置。

<a id="cleanup"></a>
### 清理

配额已经回满的行与不存在的行等价，删除它们不会改变任何限流结果。`Cleanup` 每次最多删除 `limit` 条这样的行：

```go
n, err := attempts.Cleanup(ctx, 1000)
```

清理作用于整张表，不限于调用者的 `name`，用任意一个数据库限流器调用即可。`Cleanup` 可以反复执行，多个实例同时执行也是安全的。

> [!NOTE]
> 异步队列功能还在开发中，完成后会提供把 `Cleanup` 注册为定时任务的方式。在此之前，可以在应用自己的定时器或命令中定期调用它，例如每小时一次。

<a id="redis-limiter"></a>
## 使用 Redis 存储

应用已经使用 [Redis](redis.md) 时，也可以把计数放进 Redis。`redislimit.New` 接收[入口中创建的客户端](redis.md#connecting) `rdb`，其余参数与 `NewDatabase` 相同：

```go
import "github.com/ryan053857/mygoravel/ratelimit/redislimit"

attempts := redislimit.New(rdb, "blog:login", ratelimit.Config{Rate: 5, Interval: time.Minute, Burst: 5})
```

每个 key 对应一个 Redis 键，键名是 `name:key`，例如 `blog:login:alice@example.com`；多个应用共用一个 Redis 时，在 `name` 中带上应用的命名空间。每次 `Allow` 是一次原子的脚本调用，时间取自 Redis 服务端时钟，多个实例并发调用时计数也是精确的。键在配额回满时自动过期，不需要迁移，也不需要清理。

Redis 的写入很快，也适合多个实例共享的全局限流：

```go
router.Use(httpx.RateLimit(redislimit.New(rdb, "blog:http", cfg.HTTP.RateLimit), httpx.KeyByIP))
```

这时 Redis 不可用会让所有请求都得到 `503`。如果宁可短时间不限流也要保证可用，请继续使用内存限流器。
