# 缓存

- [简介](#introduction)
- [安装](#installation)
- [装配缓存](#assembling-cache)
- [基本操作](#basic-operations)
- [为文章详情加缓存](#caching-articles)
    - [action 接收缓存](#using-in-services)
    - [读取：先缓存后数据库](#cache-aside)
    - [失效：提交后删除](#invalidation)
- [缓存键与序列化](#keys-and-serialization)
- [命中率](#hit-rate)
- [测试](#testing)
- [使用 Redis](#redis)

<a id="introduction"></a>
## 简介

对于查询代价高、读多写少的数据，可以把查询结果放进缓存，让后续请求不再访问数据库。

mygoravel 没有自己的缓存抽象，推荐直接使用 [eko/gocache](https://github.com/eko/gocache)。和数据库一样，缓存在入口创建，通过构造函数传给 [action](actions.md)。一个 gocache 缓存由三层对象组成：

```go
client := memory.New(10*time.Minute, 10*time.Minute) // 存储客户端
memoryStore := gocachestore.NewGoCache(client)        // 后端适配
c := cache.New[string](memoryStore)                   // 带类型的 Get / Set / Delete
```

业务代码只接触最后一层，换成 Redis 等其他后端时只需替换前两层，见[使用 Redis](#redis)。

本章以进程内的内存缓存为例，为博客应用的文章详情加上缓存：在入口装配缓存，让“获取文章”先读缓存、再查数据库，并在文章发布后删除旧缓存。

<a id="installation"></a>
## 安装

缓存依赖由应用按需安装：

```bash
go get github.com/eko/gocache/lib/v4@v4.2.0
go get github.com/eko/gocache/store/go_cache/v4@v4.2.2
go get github.com/patrickmn/go-cache@v2.1.0+incompatible
```

gocache 的核心库和各个 store 分别发布版本，本章示例以这组版本为准。代码中用以下别名导入：

```go
import (
    "github.com/eko/gocache/lib/v4/cache"
    "github.com/eko/gocache/lib/v4/store"
    gocachestore "github.com/eko/gocache/store/go_cache/v4"
    memory "github.com/patrickmn/go-cache"
)
```

<a id="assembling-cache"></a>
## 装配缓存

先在应用配置中加入缓存段。`CacheConfig` 由应用自己定义，放进根配置结构体：

```go
type CacheConfig struct {
    DefaultTTL time.Duration `yaml:"default_ttl"`
}

type Config struct {
    // ...
    Cache CacheConfig `yaml:"cache"`
}
```

```yaml
cache:
  default_ttl: 10m
```

然后在[入口](dependencies.md#assembling-the-application)的 `open` 中，打开数据库之后创建缓存，保存在 `App` 中，和数据库一起传给 `article.Register`：

```go
memoryClient := memory.New(cfg.Cache.DefaultTTL, 10*time.Minute)
articleCache := cache.New[string](gocachestore.NewGoCache(memoryClient))
```

`memory.New` 的第一个参数是默认 TTL，第二个参数是清理过期数据的间隔。过期的键在读取时立即视为未命中，后台任务按间隔回收它们占用的内存。

> [!NOTE]
> 默认 TTL 要设置在 `memory.New` 上。go_cache store v4.2.2 会忽略 `NewGoCache(client, store.WithExpiration(...))` 中传入的选项。

内存缓存没有需要关闭的连接，也就不需要登记到 `cleanup.Stack`。

内存缓存只在当前进程内有效，重启后清空。如果应用部署了多个副本，每个副本各有一份缓存，一个副本删除缓存不会影响其他副本；这种情况请改用 [Redis](#redis)。

<a id="basic-operations"></a>
## 基本操作

`Get` 在键不存在或已过期时返回 `store.NotFound`，用 `errors.Is` 与其他错误区分：

```go
value, err := c.Get(ctx, "blog:greeting:v1")
switch {
case errors.Is(err, store.NotFound{}):
    // 未命中
case err != nil:
    return err
}
```

不要用 `value == ""` 判断未命中，空字符串也可能是有效的缓存内容。

`Set` 写入或覆盖一个键。不传选项时使用 `memory.New` 设置的默认 TTL：

```go
err := c.Set(ctx, "blog:greeting:v1", "hello")
err = c.Set(ctx, "blog:greeting:v1", "hello", store.WithExpiration(30*time.Second))
err = c.Set(ctx, "blog:greeting:v1", "hello", store.WithExpiration(memory.NoExpiration))
```

`Delete` 删除一个键，键不存在时也返回 `nil`，因此修改数据后可以直接删除，不必先读取。`Clear` 清空整个缓存：

```go
err := c.Delete(ctx, "blog:greeting:v1")
err = c.Clear(ctx)
```

`Loadable`、`Chain`（多级缓存）、按标签失效、Prometheus 指标等组合能力，请参阅 [gocache 文档](https://github.com/eko/gocache)。

<a id="caching-articles"></a>
## 为文章详情加缓存

<a id="using-in-services"></a>
### action 接收缓存

`Get` 和 `Publish` 通过构造函数接收 `cache.CacheInterface`，与接收数据库连接的方式相同。`Register` 也多了一个参数，把它传给这两个 action：

```go
type Get struct {
    db    *bun.DB
    cache cache.CacheInterface[string]
}

func NewGet(db *bun.DB, c cache.CacheInterface[string]) *Get {
    return &Get{db: db, cache: c}
}
```

action 依赖的是接口，不关心背后是内存还是 Redis。

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

把这套逻辑放在 `internal/article/cache.go` 中，作为包内的函数供各个 action 使用。`find` 是直接查询数据库的函数，见[模块内共享的代码](actions.md#shared-code)：

```go
package article

func cacheKey(id uuid.UUID) string {
    return "blog:article:v1:" + id.String()
}

func load(ctx context.Context, db bun.IDB, c cache.CacheInterface[string], id uuid.UUID) (*Article, error) {
    key := cacheKey(id)
    raw, err := c.Get(ctx, key)
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

    item, err := find(ctx, db, id)
    if err != nil {
        return nil, err
    }
    if data, err := json.Marshal(item); err == nil {
        if err := c.Set(ctx, key, string(data)); err != nil {
            slog.WarnContext(ctx, "write article cache", "key", key, "error", err)
        }
    }
    return item, nil
}
```

然后把[授权章节](authorization.md#ownership)中 `Get` 的 `find` 换成 `load`，权限检查保持不变：

```go
func (a *Get) Handle(ctx context.Context, subject identity.Subject, id uuid.UUID) (*Article, error) {
    if subject.ID == "" {
        return nil, ErrForbidden.New()
    }
    item, err := load(ctx, a.db, a.cache, id)
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

文章修改后，删除对应的缓存键，下次读取时重新加载。删除要放在**事务提交成功之后**，事务回滚时缓存保持不变。以[发布文章](actions.md#writing-actions)为例，在 `Publish.Handle` 的事务中用 [AfterCommit](database.md#after-commit) 登记删除：

```go
err := database.RunInTx(ctx, a.db, nil, func(ctx context.Context, tx database.Tx) error {
    // 加锁读取、检查权限、更新状态……
    tx.AfterCommit(func(ctx context.Context) error {
        return errors.Wrapf(a.cache.Delete(ctx, cacheKey(id)), "invalidate article cache %s", id)
    })
    return nil
})
```

删除失败时框架记录一条 Error 日志，数据库的修改仍然成功。

这是**最终一致**的缓存：并发的读请求可能在删除之后，又把它之前读到的旧值写回，直到 TTL 到期才会更新。所以缓存应始终设置有限的 TTL；不能接受短暂旧数据的场景，请直接读数据库。

如果还缓存了文章列表，修改文章时列表的键也要一并删除。

<a id="keys-and-serialization"></a>
## 缓存键与序列化

缓存键建议由命名空间、资源类型、格式版本和资源 ID 组成，例如 `blog:article:v1:<id>`：

- 命名空间区分不同的应用和部署环境，多个环境共用一个 Redis 时不会互相覆盖；
- 修改缓存内容的结构时，把 `v1` 升为 `v2`，新代码不再读取旧格式，旧键等 TTL 到期后自然消失；
- 多租户应用中键还要带上租户，例如 `blog:article:v1:<tenant>:<id>`，见[缓存、广播与其他资源](tenancy.md#other-resources)。

上面的例子把文章编码为 JSON 字符串存入 `cache.New[string]`。内存缓存其实可以直接保存结构体，但这样切换到 Redis 时业务代码就要改：Redis store 读出的总是字符串，如果声明为 `cache.New[Article]`，`Get` 会返回零值和 `nil` 错误，而不是报错。统一存 JSON 字符串，两种后端可以无缝替换。

<a id="hit-rate"></a>
## 命中率

缓存有没有起作用，要看命中率。`cachemon.Wrap` 包装一个缓存，把每次读取命中、未命中还是出错记录为[运行统计](monitor.md)。包装后仍是 `cache.CacheInterface`，action 不用修改：

```go
articleCache := cachemon.Wrap("articles", cache.New[string](gocachestore.NewGoCache(memoryClient)), mon)
```

第一个参数是缓存名，`mon` 是 `open` 中创建的 [Monitor](dependencies.md#assembling-the-application)。键不存在或已过期记为未命中，其他错误记为出错：Redis 断线表现为出错次数增加，而不是命中率下降。

`cachecmd.New(a.monitor)` 返回 `cache stats` 命令，和其他需要数据库的命令一样在 [bootstrap.Commands](actions.md#as-commands) 中声明。它列出每个缓存最近一段时间的情况，`AVG GET` 是读取的平均耗时，`--since` 默认一小时：

```text
最近 1h
CACHE     HITS   MISSES  ERRORS  HIT RATE  AVG GET
articles  18342  1204    0       93.8%     41µs
```

后台页面用 `cachemon.Activity(ctx, a.monitor, from, to)` 查询同样的数据。

<a id="testing"></a>
## 测试

内存缓存不依赖外部服务，测试中直接为每个测试创建一份即可：

```go
func newTestCache() cache.CacheInterface[string] {
    return cache.New[string](gocachestore.NewGoCache(memory.New(time.Minute, 0)))
}

get := article.NewGet(db, newTestCache())
```

清理间隔传 `0` 表示不启动后台清理任务。数据库仍使用 [dbtest](testing.md#database-tests)。值得覆盖的行为：

- 第一次读取查询数据库并写入缓存，第二次命中缓存；
- 缓存中是无法解码的内容时，回退到数据库并覆盖它；
- 发布文章后缓存被删除，再次读取得到新状态；
- 缓存中有别人的草稿时，读取仍返回 `404`。

<a id="redis"></a>
## 使用 Redis

应用有多个副本、需要共享缓存时，改用 Redis。安装 Redis store：

```bash
go get github.com/eko/gocache/store/redis/v4@v4.2.2
```

这个 store 的包名也是 `redis`，与 go-redis 冲突，导入时起个别名：

```go
redisstore "github.com/eko/gocache/store/redis/v4"
```

在入口中把前两层换成 Redis store，传入[创建好的 Redis 客户端](redis.md#connecting) `rdb`：

```go
articleCache := cache.New[string](redisstore.NewRedis(rdb, store.WithExpiration(cfg.Cache.DefaultTTL)))
```

action 和其余代码无需修改。与内存缓存的区别：

- 默认 TTL 通过 `NewRedis` 的选项设置；`store.WithExpiration(0)` 表示不过期；
- `Clear` 执行 `FLUSHALL`，会清空整个 Redis 实例的所有数据库，不要在共享的 Redis 上调用；
- 网络故障等行为内存缓存无法模拟，请用真实的 Redis 做集成测试，写法见 [Redis](redis.md#testing)。
