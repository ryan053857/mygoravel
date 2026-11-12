# 缓存

- [简介](#introduction)
- [安装](#installation)
- [装配缓存](#assembling-cache)
- [为文章详情加缓存](#caching-articles)
    - [action 接收缓存](#using-in-services)
    - [读取：先缓存后数据库](#cache-aside)
    - [热点、集中过期与不存在的数据](#hot-keys)
    - [失效：提交后删除](#invalidation)
    - [一次失效一批：tag](#tags)
- [缓存键与序列化](#keys-and-serialization)
- [部署](#deployment)
- [测试](#testing)

<a id="introduction"></a>
## 简介

对于查询代价高、读多写少的数据，可以把查询结果放进缓存，让后续请求不再访问数据库。

缓存放在 [Memcached](https://memcached.org) 中。它只做缓存一件事：内存满时淘汰最久没用的数据，多个实例共享同一份缓存，重启后清空，丢了可以从数据库重建。锁、限流这类不能被淘汰的数据放在 [Redis](redis.md) 中。

mygoravel 不封装 Memcached 客户端，约定使用 [gomemcache](https://github.com/bradfitz/gomemcache)。和数据库一样，客户端在入口创建，通过构造函数传给 [action](actions.md)。在它之上，框架的 `cachex` 提供「先读缓存、没有就查库再写回」的读取方式，并处理热点键过期、集中过期和不存在的数据，业务代码通过它读写。

本章为博客应用的文章详情加上缓存：在入口装配缓存，让“获取文章”先读缓存、再查数据库，并在文章发布后删除旧缓存。

<a id="installation"></a>
## 安装

```bash
go get github.com/bradfitz/gomemcache@latest
```

导入路径是 `github.com/bradfitz/gomemcache/memcache`。

<a id="assembling-cache"></a>
## 装配缓存

先在应用配置中加入缓存段。`CacheConfig` 由应用自己定义，放进根配置结构体：

```go
type CacheConfig struct {
    Servers    []string      `yaml:"servers"`
    DefaultTTL time.Duration `yaml:"default_ttl"`
}

type Config struct {
    // ...
    Cache CacheConfig `yaml:"cache"`
}
```

```yaml
cache:
  servers: ["10.0.0.5:11211"]
  default_ttl: 10m
```

然后在[入口](dependencies.md#assembling-the-application)的 `open` 中创建客户端，保存在 `App` 中：

```go
mc := memcache.New(cfg.Cache.Servers...)
mc.Timeout = 200 * time.Millisecond
stack.MustAdd("memcached", func(context.Context) error { return mc.Close() })
```

客户端可以并发使用，整个应用共享这一个 `mc`。gomemcache 的方法不接收 `ctx`，每次操作的超时由 `Timeout` 控制，默认 500 毫秒；缓存是请求路径上的一环，超时宜短，Memcached 慢的时候直接查数据库。

<a id="caching-articles"></a>
## 为文章详情加缓存

<a id="using-in-services"></a>
### action 接收缓存

`cachex.New` 创建一种值的缓存。文章详情的缓存在 `open` 中创建，保存在 `App` 中，和数据库一起传给 `article.Register`，再由它传给 `Get` 和 `Publish`：

```go
articles := cachex.New[*article.Article](mc, "blog:article:v1:", cfg.Cache.DefaultTTL,
    cachex.Missing(article.ErrNotFound.New))
```

第二个参数是键的前缀，见[缓存键与序列化](#keys-and-serialization)；第三个参数是过期时间；`cachex.Missing` 声明文章不存在时的错误，见[不存在的数据](#hot-keys)。

```go
type Get struct {
    db       *bun.DB
    articles *cachex.Cache[*Article]
}

func NewGet(db *bun.DB, articles *cachex.Cache[*Article]) *Get {
    return &Get{db: db, articles: articles}
}
```

`cachex.New` 遇到 `nil` 客户端会 panic，所以在 `open` 中创建，而不是在 `Register` 中：[生成 OpenAPI 文档](routing.md#exporting-openapi)时 `Register` 收到的依赖都是 `nil`。一种值只创建一个 `Cache`，读取和失效共用它。

<a id="cache-aside"></a>
### 读取：先缓存后数据库

查询缓存最常用的模式是 **cache-aside**：先读缓存；没有时查询数据库，再把结果写回缓存。`Remember` 就是这个模式，`find` 是直接查询数据库的函数，见[模块内共享的代码](actions.md#shared-code)。把[授权章节](authorization.md#ownership)中 `Get` 的 `find` 换成 `Remember`，权限检查保持不变：

```go
func (a *Get) Handle(ctx context.Context, subject identity.Subject, id uuid.UUID) (*Article, error) {
    if subject.ID == "" {
        return nil, ErrForbidden.New()
    }
    item, err := a.articles.Remember(ctx, id.String(), func(ctx context.Context) (*Article, error) {
        return find(ctx, a.db, id)
    })
    if err != nil {
        return nil, err
    }
    if item.Status == StatusDraft && item.AuthorID != subject.ID {
        return nil, ErrNotFound.New()
    }
    return item, nil
}
```

`Remember` 的第二个参数是文章 ID，`cachex` 在它前面加上创建时的前缀，得到 `blog:article:v1:<id>`。

> [!WARNING]
> 草稿同样会被缓存，所以命中缓存后**仍要检查访问权限**，否则会把别人的草稿返回给当前用户。写操作的权限检查则应使用事务中读取的数据，不要依赖缓存，见[在事务中检查](authorization.md#checking-in-transactions)。

查询缓存随时可以从数据库重建，所以缓存出问题时 `Remember` 不让请求失败：

| 情况 | 处理方式 |
| --- | --- |
| 命中 | 解码后返回，每次得到独立的一份 |
| 未命中 | 调用加载函数，结果写回缓存 |
| 读取失败（例如 Memcached 不可用）、内容无法解码 | 记录 `warn` 日志，调用加载函数 |
| 加载函数返回错误 | 原样返回；除了 `Missing` 声明的错误，都不写缓存 |
| 写回失败（例如值超过 1 MB） | 记录 `warn` 日志，照常返回加载的结果 |

缓存后端不可用时每个请求都会出错，所以这些日志每个 `Cache` 每分钟最多一条，附上期间省略的条数。出错的次数用[指标](telemetry.md)观察。

这套降级策略只适用于可丢弃的查询缓存。Memcached 只用来放这类数据：会话在数据库中，限流计数和锁在 [Redis](redis.md) 中。

<a id="hot-keys"></a>
### 热点、集中过期与不存在的数据

查询缓存有三个常见问题，`Remember` 都处理了：

- **击穿**：热点文章的缓存过期的瞬间，大量请求同时发现没有缓存，一起查数据库。`Remember` 把同一个进程中同一个键的并发加载合并为一次，其他请求等它的结果。一个请求取消时只有它自己返回，加载继续，其他请求照常得到结果；加载使用第一个请求的截止时间。部署多个实例时，每个实例最多同时加载一次，通常已经足够。
- **雪崩**：同一批写入的大量键在同一时刻过期。写入时过期时间随机增加至多 10%，让它们错开。
- **穿透**：请求不存在的 ID 时，缓存永远没有，每次都查数据库。`cachex.Missing` 声明「不存在」的错误后，加载返回的错误与它匹配（`errors.Is`）时也写入缓存，保留约一分钟，期间直接返回一个新的同类错误。`errorx` 的错误按错误码匹配，传入模板的 `New` 方法即可，所以上面的 `find` 不用改：它返回的 `ErrNotFound` 被缓存，`Get` 收到的仍是 `ErrNotFound`。其他错误都不缓存。

<a id="invalidation"></a>
### 失效：提交后删除

文章修改后，删除对应的缓存，下次读取时重新加载。删除要放在**事务提交成功之后**，事务回滚时缓存保持不变。以[发布文章](actions.md#writing-actions)为例，在 `Publish.Handle` 的事务中用 [AfterCommit](database.md#after-commit) 登记删除：

```go
err := database.RunInTx(ctx, a.db, nil, func(ctx context.Context, tx database.Tx) error {
    // 加锁读取、检查权限、更新状态……
    tx.AfterCommit(func(ctx context.Context) error {
        return a.articles.Forget(ctx, id.String())
    })
    return nil
})
```

删除失败时框架记录一条 Error 日志，数据库的修改仍然成功。`Forget` 之后的读取不会共用删除之前就开始的加载。

这是**最终一致**的缓存：删除之前开始的加载可能在删除之后才写回读到的旧值，直到过期才会更新。所以过期时间应当有限；不能接受短暂旧数据的场景，请直接读数据库。

如果还缓存了文章列表，修改文章时列表也要失效，见下一节。

<a id="tags"></a>
### 一次失效一批：tag

文章列表按页缓存，发布一篇文章会影响每一页，逐个删除需要知道缓存了哪些页。这时给值加上 tag，修改后让 tag 失效，带有它的值一起作废。`Remember` 最后的参数是值的 tag：

```go
page, err := a.lists.Remember(ctx, strconv.Itoa(n), func(ctx context.Context) ([]Summary, error) {
    return listPublished(ctx, a.db, n)
}, "blog:articles")
```

发布文章的事务提交后，`Invalidate` 让这个 tag 失效：

```go
tx.AfterCommit(func(ctx context.Context) error {
    return a.lists.Invalidate(ctx, "blog:articles")
})
```

tag 是一个版本号。`Remember` 在同一次请求中读取值和它的 tag 的版本，版本变了就重新加载；`Invalidate` 只是把版本加一，不需要知道有哪些键。tag 不属于某个 `Cache`，共用同一个 Memcached 的 `Cache` 之间可以共用 tag，所以 tag 由应用自己加上命名空间。

和 `Forget` 不同，tag 没有[短暂的旧数据](#invalidation)：版本在加载之前读取，`Invalidate` 返回之后，`Remember` 不会再返回失效之前开始加载的值。代价是每次读取都要多读 tag 的版本（与值在同一次请求中），值所带的 tag 越多，任何一个失效都会让它重新加载。读取 tag 的版本失败时，`Remember` 直接加载，结果不写入缓存；tag 被 Memcached 淘汰时，带有它的值全部重新加载。

<a id="keys-and-serialization"></a>
## 缓存键与序列化

缓存键建议由命名空间、资源类型、格式版本和资源 ID 组成，例如 `blog:article:v1:<id>`。前三部分是传给 `cachex.New` 的前缀，`Remember` 和 `Forget` 只传 ID：

- 命名空间区分不同的应用和部署环境，多个环境共用一个 Memcached 时不会互相覆盖；
- 修改缓存内容的结构时，把 `v1` 升为 `v2`，新代码不再读取旧格式，旧键等过期后自然消失；
- 多租户应用中键还要带上租户，例如传入 `<tenant>:<id>`，得到 `blog:article:v1:<tenant>:<id>`，见[缓存、广播与其他资源](tenancy.md#other-resources)。

Memcached 的键最长 250 字节，不能有空白和控制字符。超出这些限制的键（例如很长的搜索条件）由 `cachex` 换成 SHA-256 摘要保存，调用方不用处理。值编码为 JSON 保存，每次读取得到独立的一份。

<a id="deployment"></a>
## 部署

- **值的大小。** Memcached 默认单个值最大 1 MB，更大的值写入失败，`cachex` 记录日志后照常返回加载的结果，相当于不缓存。缓存大的列表时只缓存 ID，或者调大 Memcached 的 `-I` 参数。
- **过期时间。** Memcached 按秒计时，`cachex` 把过期时间向上取整到秒；超过 30 天的过期时间会换算成 Unix 时间写入，因为 Memcached 把超过 30 天的数值当作到期时刻。
- **多个节点。** `memcache.New` 传入多个地址时，键按哈希取模分到各个节点，节点之间不复制。增减节点时大部分键会换到别的节点，相当于一次大面积失效。需要扩缩容的部署换成 `cachex.NewRing` 的一致性哈希，增加一个节点只有约 1/(N+1) 的键换节点，去掉一个节点只影响它自己的键：

  ```go
  ring, err := cachex.NewRing(cfg.Cache.Servers...)
  if err != nil {
      return nil, err
  }
  mc := memcache.NewFromSelector(ring)
  ```

  节点在环上的位置只由地址字符串决定，所有实例的地址写法要一致，顺序无关。地址在建立连接时解析，节点换了 IP 不用重启。某个节点不可用时，它的键读写超时，`cachex` 照常加载，不会自动换到别的节点。
- **内存。** 内存满时 Memcached 淘汰最久没用的数据，这正是缓存需要的行为，不需要别的配置。

<a id="testing"></a>
## 测试

`cachex.New` 的第一个参数是 `cachex.Client` 接口，包含 `cachex` 用到的几个 Memcached 操作，`*memcache.Client` 实现了它。测试读取逻辑时连接真实的 Memcached，和数据库测试一样通过环境变量取得地址，每个测试使用随机的前缀，互不影响：

```go
func newTestCache(t *testing.T) *cachex.Cache[*article.Article] {
    t.Helper()
    addr := os.Getenv("MEMCACHED_TEST_ADDR")
    if addr == "" {
        t.Fatal("MEMCACHED_TEST_ADDR is not set")
    }
    mc := memcache.New(addr)
    t.Cleanup(func() { mc.Close() })
    return cachex.New[*article.Article](mc, "test:"+rand.Text()+":article:", time.Minute,
        cachex.Missing(article.ErrNotFound.New))
}

get := article.NewGet(db, newTestCache(t))
```

数据库仍使用 [dbtest](testing.md#database-tests)。合并加载、抖动和降级由框架测试，应用值得覆盖的是自己的规则：

- 第一次读取查询数据库并写入缓存，第二次命中缓存；
- 发布文章后缓存被删除，再次读取得到新状态；
- 缓存中有别人的草稿时，读取仍返回 `404`；
- 不存在的文章返回 `404`。
