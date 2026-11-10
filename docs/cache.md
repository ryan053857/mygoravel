# 缓存

- [简介](#introduction)
- [安装](#installation)
- [装配缓存](#assembling-cache)
- [基本操作](#basic-operations)
- [为文章详情加缓存](#caching-articles)
    - [服务接收缓存](#using-in-services)
    - [读取：先缓存后数据库](#cache-aside)
    - [失效：提交后删除](#invalidation)
- [缓存键与序列化](#keys-and-serialization)
- [测试](#testing)

<a id="introduction"></a>
## 简介

对于查询代价高、读多写少的数据，可以把查询结果放进缓存，让后续请求不再访问数据库。

mygoravel 没有自己的缓存抽象，推荐直接使用 [eko/gocache](https://github.com/eko/gocache)。和数据库一样，缓存在入口创建，通过构造函数传给业务服务。一个 gocache 缓存由三层对象组成：

```go
client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"}) // 连接池
redisStore := redisstore.NewRedis(client)                          // 后端适配
c := cache.New[string](redisStore)                                 // 带类型的 Get / Set / Delete
```

业务代码只接触最后一层。换成内存后端时，只需替换前两层。

本章以博客应用为例，为文章详情加上缓存：在入口装配 Redis 缓存，让文章服务先读缓存、再查数据库，并在文章发布后删除旧缓存。

<a id="installation"></a>
## 安装

缓存依赖由应用按需安装。以 Redis 为例：

```bash
go get github.com/eko/gocache/lib/v4@v4.2.0
go get github.com/eko/gocache/store/redis/v4@v4.2.2
go get github.com/redis/go-redis/v9@v9.23.0
```

gocache 的核心库和各个 store 分别发布版本，本章示例以这组版本为准。

<a id="assembling-cache"></a>
## 装配缓存

先在应用配置中加入缓存段。`CacheConfig` 由应用自己定义，放进根配置结构体：

```go
type CacheConfig struct {
    Addr       string        `yaml:"addr"`
    Password   string        `yaml:"password"`
    DefaultTTL time.Duration `yaml:"default_ttl"`
}

type Config struct {
    // ...
    Cache CacheConfig `yaml:"cache"`
}
```

```yaml
cache:
  addr: ${REDIS_ADDR:-127.0.0.1:6379}
  password: ${REDIS_PASSWORD:-}
  default_ttl: 10m
```

然后在[入口](dependencies.md#assembling-the-application)的 `Serve` 中，打开数据库之后创建缓存，并传给文章服务：

```go
client := redis.NewClient(&redis.Options{
    Addr:                  cfg.Cache.Addr,
    Password:              cfg.Cache.Password,
    ContextTimeoutEnabled: true,
})
stack.MustAdd("redis", func(context.Context) error {
    return client.Close()
})

articleCache := cache.New[string](redisstore.NewRedis(client,
    store.WithExpiration(cfg.Cache.DefaultTTL),
))
articles := article.NewService(db, articleCache)
```

- `store.WithExpiration` 设置默认 TTL，单次 `Set` 可以覆盖它；
- `ContextTimeoutEnabled` 让 Redis 命令遵守请求 context 的截止时间；
- 客户端登记到 `cleanup.Stack`，在 HTTP 服务停止后关闭，见[关闭资源](dependencies.md#closing-resources)。多个缓存可以共享同一个客户端，服务本身不关闭它。

`redis.NewClient` 不会立即连接 Redis。查询缓存故障时可以回退到数据库，所以这里不要求启动时 Redis 可用；如果你的业务离不开 Redis，可以在启动时调用 `client.Ping(ctx)` 并返回错误。

<a id="basic-operations"></a>
## 基本操作

`Get` 在键不存在或已过期时返回 `store.NotFound`，用 `errors.Is` 与其他错误区分：

```go
value, err := c.Get(ctx, "blog:greeting:v1")
switch {
case errors.Is(err, store.NotFound{}):
    // 未命中
case err != nil:
    return err // Redis 故障等
}
```

不要用 `value == ""` 判断未命中，空字符串也可能是有效的缓存内容。

`Set` 写入或覆盖一个键。不传选项时使用 store 的默认 TTL：

```go
err := c.Set(ctx, "blog:greeting:v1", "hello")
err = c.Set(ctx, "blog:greeting:v1", "hello", store.WithExpiration(30*time.Second))
```

`Delete` 删除一个键，键不存在时也返回 `nil`，因此修改数据后可以直接删除，不必先读取：

```go
err := c.Delete(ctx, "blog:greeting:v1")
```

> [!WARNING]
> 不要在共享的 Redis 上调用 `Clear`。Redis store v4.2.2 的 `Clear` 执行 `FLUSHALL`，会清空整个 Redis 实例的所有数据库，不受键前缀限制。

`Loadable`、`Chain`（多级缓存）、按标签失效、Prometheus 指标等组合能力，请参阅 [gocache 文档](https://github.com/eko/gocache)。

<a id="caching-articles"></a>
## 为文章详情加缓存

<a id="using-in-services"></a>
### 服务接收缓存

文章服务通过构造函数接收 `cache.CacheInterface`，与接收数据库连接的方式相同：

```go
type Service struct {
    db    *bun.DB
    cache cache.CacheInterface[string]
}

func NewService(db *bun.DB, c cache.CacheInterface[string]) *Service {
    return &Service{db: db, cache: c}
}
```

服务依赖的是接口，测试时可以传入内存缓存，见[测试](#testing)。

<a id="cache-aside"></a>
### 读取：先缓存后数据库

查询缓存最常用的模式是 **cache-aside**：先读缓存；未命中时查询数据库，再把结果写回缓存。

查询缓存随时可以从数据库重建，因此缓存出问题时不应让请求失败：

| 情况 | 处理方式 |
| --- | --- |
| 命中 | 返回数据，继续执行权限检查 |
| 未命中 | 查询数据库 |
| 读取失败、内容无法解码 | 记录日志，查询数据库 |
| 数据库未找到或出错 | 返回错误，不写缓存 |
| 写回失败 | 记录日志，照常返回数据库结果 |

把这套逻辑放在 `internal/article/cache.go` 中。`find` 是直接查询数据库的方法，写法见[处理“未找到”](database.md#handling-not-found)：

```go
package article

func cacheKey(id uuid.UUID) string {
    return "blog:article:v1:" + id.String()
}

func (s *Service) load(ctx context.Context, id uuid.UUID) (*Article, error) {
    key := cacheKey(id)
    raw, err := s.cache.Get(ctx, key)
    switch {
    case err == nil:
        var item Article
        decodeErr := json.Unmarshal([]byte(raw), &item)
        if decodeErr == nil {
            return &item, nil
        }
        slog.WarnContext(ctx, "decode article cache", "key", key, "error", decodeErr)
    case !errors.Is(err, store.NotFound{}):
        slog.WarnContext(ctx, "read article cache", "key", key, "error", err)
    }

    item, err := s.find(ctx, id)
    if err != nil {
        return nil, err
    }
    if data, err := json.Marshal(item); err == nil {
        if err := s.cache.Set(ctx, key, string(data)); err != nil {
            slog.WarnContext(ctx, "write article cache", "key", key, "error", err)
        }
    }
    return item, nil
}
```

然后把[授权章节](authorization.md#ownership)中 `Get` 的 `s.find` 换成 `s.load`，权限检查保持不变：

```go
func (s *Service) Get(ctx context.Context, subject identity.Subject, id uuid.UUID) (*Article, error) {
    if subject.ID == "" {
        return nil, ErrForbidden.New()
    }
    item, err := s.load(ctx, id)
    if err != nil {
        return nil, err
    }
    if item.Status == StatusDraft && item.AuthorID != subject.ID {
        return nil, ErrNotFound.New()
    }
    return item, nil
}
```

> [!WARNING]
> 草稿同样会被缓存，所以命中缓存后**仍要检查访问权限**，否则会把别人的草稿返回给当前用户。写操作的权限检查则应使用事务中读取的数据，不要依赖缓存，见[在事务中检查](authorization.md#checking-in-transactions)。

这套降级策略只适用于可丢弃的查询缓存。如果用缓存保存会话或限流计数，需要根据用途单独设计故障时的行为。

<a id="invalidation"></a>
### 失效：提交后删除

文章修改后，删除对应的缓存键，下次读取时重新加载。删除失败只记录日志，数据库的修改仍然成功：

```go
func (s *Service) invalidate(ctx context.Context, id uuid.UUID) {
    if err := s.cache.Delete(ctx, cacheKey(id)); err != nil {
        slog.WarnContext(ctx, "invalidate article cache", "article_id", id, "error", err)
    }
}
```

删除要放在**事务提交成功之后**，事务回滚时缓存保持不变。以[发布文章](authorization.md#checking-in-transactions)为例，在 `RunInTx` 返回后调用：

```go
err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
    // 加锁读取、检查权限、更新状态……
})
if err != nil {
    return nil, err
}
s.invalidate(ctx, id)
return item, nil
```

这是**最终一致**的缓存：并发的读请求可能在删除之后，又把它之前读到的旧值写回，直到 TTL 到期才会更新。所以缓存应始终设置有限的 TTL；不能接受短暂旧数据的场景，请直接读数据库。

如果还缓存了文章列表，修改文章时列表的键也要一并删除。

<a id="keys-and-serialization"></a>
## 缓存键与序列化

缓存键建议由命名空间、资源类型、格式版本和资源 ID 组成，例如 `blog:article:v1:<id>`：

- 命名空间区分应用和部署环境，多个环境共用一个 Redis 时不会互相覆盖；
- 修改缓存内容的结构时，把 `v1` 升为 `v2`，新代码不再读取旧格式，旧键等 TTL 到期后自然消失。

上面的例子把文章编码为 JSON 字符串存入 `cache.New[string]`。泛型参数只是声明类型，**不负责序列化**：Redis store 读出的总是字符串，如果声明为 `cache.New[Article]`，`Get` 会返回零值和 `nil` 错误，而不是报错。因此请自己编码为字符串，或使用 gocache 的 `marshaler` 包。

<a id="testing"></a>
## 测试

业务测试可以用进程内的内存缓存代替 Redis：

```bash
go get github.com/eko/gocache/store/go_cache/v4@v4.2.2
go get github.com/patrickmn/go-cache@v2.1.0+incompatible
```

```go
func newTestCache() cache.CacheInterface[string] {
    return cache.New[string](gocachestore.NewGoCache(memory.New(time.Minute, 0)))
}

svc := article.NewService(db, newTestCache())
```

`memory.New` 的第一个参数是默认 TTL，第二个是后台清理间隔，传 `0` 表示不启动后台清理，适合测试。数据库仍使用 [dbtest](testing.md#database-tests)。值得覆盖的行为：

- 第一次读取查询数据库并写入缓存，第二次命中缓存；
- 缓存中是无法解码的内容时，回退到数据库并覆盖它；
- 发布文章后缓存被删除，再次读取得到新状态；
- 缓存中有别人的草稿时，读取仍返回 `404`。

> [!NOTE]
> 内存后端与 Redis 对 TTL 的处理不同：`store.WithExpiration(0)` 在 Redis 中表示不过期，在内存后端中表示使用 `memory.New` 的默认 TTL；内存后端也会忽略 `NewGoCache` 上传入的默认 TTL 选项。网络故障、TTL 等与 Redis 相关的行为，请用真实的 Redis 做集成测试。
