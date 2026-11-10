# 缓存

- [简介](#introduction)
- [安装](#installation)
- [使用缓存](#using-the-cache)
    - [创建缓存实例](#creating-a-cache)
    - [在服务中使用](#using-in-services)
- [缓存读取策略](#cache-aside)
- [缓存键与序列化](#keys-and-serialization)
- [缓存失效](#invalidation)
- [关闭缓存客户端](#closing-clients)

<a id="introduction"></a>
## 简介

有些数据的查询代价较高，或者被频繁读取但很少变化。把这些数据缓存起来，可以显著降低数据库压力并加快响应速度。

mygoravel 没有自己的缓存抽象，而是推荐直接使用 [eko/gocache](https://github.com/eko/gocache)。它提供了统一的缓存接口，以及内存、Redis 等多种官方存储后端。你的应用自己选择后端、创建客户端，再把缓存对象传给需要它的业务服务。

如果你的应用不需要缓存，就完全不必引入这些依赖。

<a id="installation"></a>
## 安装

缓存依赖由使用缓存的应用自己安装。以 Redis 为例：

```bash
go get github.com/eko/gocache/lib/v4@v4.2.0
go get github.com/eko/gocache/store/redis/v4@v4.2.2
go get github.com/redis/go-redis/v9@v9.23.0
```

如果只需要进程内的内存缓存，可以改用官方的 `github.com/eko/gocache/store/go_cache/v4`。

> [!NOTE]
> 这些版本是编写本文档时经过验证的组合。请在应用的集成测试中验证你所选的版本与实际部署的缓存服务。

<a id="using-the-cache"></a>
## 使用缓存

<a id="creating-a-cache"></a>
### 创建缓存实例

下面是一个完整的示例，它连接 Redis 并写入一个值：

```go
package cachedemo

import (
    "context"
    "time"

    "github.com/eko/gocache/lib/v4/cache"
    "github.com/eko/gocache/lib/v4/store"
    redisstore "github.com/eko/gocache/store/redis/v4"
    "github.com/redis/go-redis/v9"
)

func Put(ctx context.Context, addr string, value string) error {
    client := redis.NewClient(&redis.Options{Addr: addr})
    defer client.Close()

    c := cache.New[string](redisstore.NewRedis(client))
    return c.Set(ctx, "demo:message:v1:hello", value,
        store.WithExpiration(time.Minute),
    )
}
```

这个示例为了完整，每次调用都创建并关闭客户端。在长期运行的应用中，你应该在入口创建一次客户端，在整个运行期间复用它，退出时再关闭。

<a id="using-in-services"></a>
### 在服务中使用

业务服务通过构造函数接收 gocache 的 `cache.CacheInterface` 接口，与接收数据库连接的方式相同。下面为文章服务加上缓存，缓存故障用全局日志器 `slog` 记录：

```go
type Service struct {
    db    *bun.DB
    cache cache.CacheInterface[string]
}

func NewService(db *bun.DB, c cache.CacheInterface[string]) *Service {
    return &Service{db: db, cache: c}
}
```

由于接收的是接口，测试时你可以传入内存缓存，而不需要真实的 Redis。

<a id="cache-aside"></a>
## 缓存读取策略

对于查询结果的缓存，最常用的模式是 **cache-aside**：先读缓存；未命中时查询数据库，再把结果写回缓存。

缓存只是一种加速手段，即使它出了问题，也不应该影响业务的正确性。因此，各种情况推荐这样处理：

| 情况 | 处理方式 |
| --- | --- |
| 命中缓存 | 检查当前用户是否有权访问，然后返回 |
| 未命中（`store.NotFound`） | 正常查询数据库 |
| 读取缓存失败 | 记录诊断日志，然后查询数据库 |
| 缓存内容无法解码 | 记录诊断日志，然后查询数据库 |
| 写回缓存失败 | 记录诊断日志，照常返回数据库查询结果 |

请注意区分“未命中”和“缓存服务故障”：前者是正常情况，后者需要记录日志以便排查。

下面是按照这个策略实现的读取文章。`find` 是直接查询数据库的方法，写法见[数据库](database.md#handling-not-found)：

```go
func cacheKey(id uuid.UUID) string {
    return "blog:article:v1:" + id.String()
}

func (s *Service) Get(ctx context.Context, subject identity.Subject, id uuid.UUID) (*Article, error) {
    if subject.ID == "" {
        return nil, ErrForbidden.New()
    }
    item, err := s.load(ctx, id)
    if err != nil {
        return nil, err
    }
    // 无论是否来自缓存，都要检查权限
    if item.Status == StatusDraft && item.AuthorID != subject.ID {
        return nil, ErrNotFound.New()
    }
    return item, nil
}

// load 先读缓存，未命中或缓存不可用时查询数据库，再把结果写回缓存。
func (s *Service) load(ctx context.Context, id uuid.UUID) (*Article, error) {
    key := cacheKey(id)
    raw, err := s.cache.Get(ctx, key)
    switch {
    case err == nil:
        var item Article
        if err := json.Unmarshal([]byte(raw), &item); err == nil {
            return &item, nil
        }
        slog.WarnContext(ctx, "discard undecodable cache entry", "key", key)
    case !errors.Is(err, store.NotFound{}):
        slog.WarnContext(ctx, "read article cache", "key", key, "error", err)
    }

    item, err := s.find(ctx, id)
    if err != nil {
        return nil, err
    }
    data, _ := json.Marshal(item)
    if err := s.cache.Set(ctx, key, string(data), store.WithExpiration(10*time.Minute)); err != nil {
        slog.WarnContext(ctx, "write article cache", "key", key, "error", err)
    }
    return item, nil
}
```

gocache 在未命中时返回 `store.NotFound`，用 `errors.Is(err, store.NotFound{})` 判断即可。文章被序列化为 JSON 后存入缓存，草稿也会被缓存，因此 `Get` 在拿到文章之后，无论它来自缓存还是数据库，都要检查当前用户能否看到它。

> [!WARNING]
> 命中缓存时，同样要执行资源所有权检查。缓存中的数据可能属于其他用户，跳过检查会导致越权访问。

上面的策略适用于可以随时丢弃的查询缓存。如果你用缓存存储身份信息或限流计数，应根据用途单独设计失败时的行为。

<a id="keys-and-serialization"></a>
## 缓存键与序列化

推荐的缓存键格式包含命名空间、资源类型、格式版本和资源 ID：

```text
<namespace>:article:v1:<id>
```

上面例子中的 `blog:article:v1:<id>` 就是按这个格式构造的。

- `namespace` 用于隔离不同的应用和部署环境，避免多个环境共用一个 Redis 时互相干扰；
- `v1` 表示缓存内容的序列化格式。当你修改了缓存的数据结构，只需把版本升为 `v2`，旧格式的缓存就会自然失效。

对象可以编码为 JSON 字符串后存入缓存，读写两端使用相同的结构即可。

<a id="invalidation"></a>
## 缓存失效

当数据被修改时，需要让对应的缓存失效。推荐的做法是在数据库**事务提交成功之后**删除缓存键；如果事务回滚，缓存保持原样。删除缓存失败时，记录诊断日志即可，数据库的修改仍然算作成功：

```go
func (s *Service) invalidate(ctx context.Context, id uuid.UUID) {
    if err := s.cache.Delete(ctx, cacheKey(id)); err != nil {
        slog.WarnContext(ctx, "invalidate article cache", "article_id", id, "error", err)
    }
}
```

例如在[发布文章](authorization.md#checking-in-transactions)的 `RunInTx` 成功返回之后，调用 `s.invalidate(ctx, id)`。

这是一种**最终一致**的缓存：在并发场景下，一个读请求可能在更新删除缓存之后，又把旧值写回缓存，直到 TTL 到期才会更新。因此请为缓存设置合理的 TTL。如果业务需要更强的一致性，可以引入版本号等机制，或者直接读取数据库。

<a id="closing-clients"></a>
## 关闭缓存客户端

Redis 客户端由创建它的入口负责关闭，并且只关闭一次。即使多个服务共享同一个客户端，也由入口统一关闭，服务本身不应关闭它。

推荐使用 `cleanup.Stack` 按创建的逆序关闭资源，见[依赖装配](dependencies.md#closing-resources)。内存缓存的后台清理任务则按照 gocache 自身的约定释放。
