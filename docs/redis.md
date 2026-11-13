# Redis

- [简介](#introduction)
- [安装](#installation)
- [配置](#configuration)
- [连接 Redis](#connecting)
- [集群](#cluster)
- [执行命令](#running-commands)
- [在其他组件中使用](#using-with-components)
- [测试](#testing)

<a id="introduction"></a>
## 简介

Tenon 中每个组件只做它最擅长的事：业务数据在 PostgreSQL，缓存在 [Memcached](cache.md)，任务在[队列](queue.md)。Redis 用它的数据结构做多个实例之间的协调：[原子锁](locks.md)、[限流](rate-limiting.md)、[实时推送](realtime.md)的在线成员和[广播](broadcasting.md)，以及计数器这类需要原子更新的小数据。

这些数据被淘汰就会出错，例如锁被挤掉后两个实例同时执行任务。所以 Redis 要配置为内存满时拒绝写入，而不是淘汰键：

```text
maxmemory-policy noeviction
```

也因为这个原因，缓存不放在这个 Redis 里：缓存需要内存满时淘汰旧数据，两种策略不能共用一个实例。

Tenon 不封装 Redis 客户端，约定统一使用 [rueidis](https://github.com/redis/rueidis)。[实时推送](realtime.md)依赖的 Centrifuge 也使用它，整个应用只有一个 Redis 客户端库。和数据库一样，客户端在入口创建一次，通过构造函数传给需要它的服务。

<a id="installation"></a>
## 安装

```bash
go get github.com/redis/rueidis@v1.0.79
```

<a id="configuration"></a>
## 配置

Redis 配置由应用自己定义。地址、密码和 DB 编号用一个 URL 表示最简单：

```go
type RedisConfig struct {
    URL string `yaml:"url"`
}

type Config struct {
    // ...
    Redis RedisConfig `yaml:"redis"`
}
```

```yaml
redis:
  url: ${REDIS_URL}
```

URL 形如 `redis://:password@localhost:6379/0`，路径部分是 DB 编号；使用 TLS 时把协议换成 `rediss://`。集群的写法见[集群](#cluster)，哨兵的写法见 [`ParseURL`](https://pkg.go.dev/github.com/redis/rueidis#ParseURL)。

<a id="connecting"></a>
## 连接 Redis

在[入口](dependencies.md#assembling-the-application)的 `open` 中，打开数据库之后创建客户端，并登记到 [`cleanup.Stack`](dependencies.md#closing-resources)：

```go
opt, err := rueidis.ParseURL(cfg.Redis.URL)
if err != nil {
    return nil, err
}
rdb, err := rueidis.NewClient(opt)
if err != nil {
    return nil, err
}
stack.MustAdd("redis", func(context.Context) error {
    rdb.Close()
    return nil
})
```

`NewClient` 会立即连接，Redis 不可达时返回错误。客户端可以并发使用，并发的命令会自动合并成批量发送。整个应用共享这一个 `rdb`：把它存进 `App` 的 `rdb` 字段，各个运行部分通过 `a.rdb` 使用。

<a id="cluster"></a>
## 集群

连接集群不需要改代码，只改 URL：写出几个节点的地址，不写 DB 编号：

```yaml
redis:
  url: redis://:password@10.0.0.1:6379?addr=10.0.0.2:6379&addr=10.0.0.3:6379
```

`NewClient` 从这些节点读取集群的拓扑，之后按键所在的槽把命令发到对应的节点；只写一个地址也能连上，多写几个是为了其中一个不可达时仍能启动。

框架的组件都可以在集群上使用，CI 对单机和集群各运行一遍它们的测试：

- 锁和限流的每次操作只涉及一个键；
- 在线成员的每个频道有两个键，频道名写在花括号里（`<prefix>{<channel>}:exp`），两个键落在同一个槽，脚本可以同时操作它们；
- 广播使用分片 Pub/Sub，见[广播](broadcasting.md#backends)。

应用自己的命令也要遵守集群的限制：一条命令或一个 Lua 脚本涉及的所有键必须在同一个槽，否则 Redis 返回 `CROSSSLOT` 错误。需要一起操作的键，把相同的部分写在花括号里，例如 `blog:{user:42}:views` 和 `blog:{user:42}:likes`。

<a id="running-commands"></a>
## 执行命令

以统计文章浏览量为例。下面是完整的 `internal/article/views.go`：

```go
package article

import (
    "context"

    "github.com/google/uuid"
    "github.com/redis/rueidis"
)

type Views struct {
    rdb rueidis.Client
}

func NewViews(rdb rueidis.Client) *Views {
    return &Views{rdb: rdb}
}

// Incr 记录一次浏览，返回累计浏览量。
func (v *Views) Incr(ctx context.Context, id uuid.UUID) (int64, error) {
    return v.rdb.Do(ctx, v.rdb.B().Incr().Key(viewsKey(id)).Build()).AsInt64()
}

// Count 返回累计浏览量，没有记录时返回 0。
func (v *Views) Count(ctx context.Context, id uuid.UUID) (int64, error) {
    n, err := v.rdb.Do(ctx, v.rdb.B().Get().Key(viewsKey(id)).Build()).AsInt64()
    if rueidis.IsRedisNil(err) {
        return 0, nil
    }
    return n, err
}

func viewsKey(id uuid.UUID) string {
    return "blog:article:views:" + id.String()
}
```

在入口中用 `article.NewViews(rdb)` 构造它，传给文章详情的处理器。

- 命令用 `rdb.B()` 构造，`Do` 执行；`AsInt64()`、`ToString()` 等方法在取值时转换类型。
- 键不存在时，`Get` 返回的错误满足 `rueidis.IsRedisNil`。用它把“没有数据”和真正的故障区分开。
- 键名以应用的命名空间开头，多个应用共用一个 Redis 时不会互相覆盖。

Lua 脚本用 `rueidis.NewLuaScript`，其他用法见 [rueidis 文档](https://github.com/redis/rueidis)。

<a id="using-with-components"></a>
## 在其他组件中使用

把同一个 `rdb` 传给需要 Redis 的组件，不要为每个组件各建一个客户端：

- **原子锁**：`redislock.New(rdb)`，见[原子锁](locks.md#redis-store)；
- **限流**：`redislimit.New(rdb, name, cfg)`，见[限流](rate-limiting.md#redis-limiter)；
- **广播**：`redisbroadcast.New(rdb, channel)`，见[广播](broadcasting.md#backends)；
- **在线成员**：`redispresence.New(rdb, prefix)`，见[实时推送](realtime.md#presence)。

<a id="testing"></a>
## 测试

依赖 Redis 的代码用真实的 Redis 测试。和[数据库测试](testing.md#database-tests)一样，通过环境变量取得测试服务器的地址。下面是 `internal/article/views_test.go`：

```go
package article_test

import (
    "context"
    "os"
    "testing"

    "example.com/blog/internal/article"
    "github.com/google/uuid"
    "github.com/redis/rueidis"
)

func newRedis(t *testing.T) rueidis.Client {
    t.Helper()
    url := os.Getenv("REDIS_TEST_URL")
    if url == "" {
        t.Fatal("REDIS_TEST_URL is not set")
    }
    opt, err := rueidis.ParseURL(url)
    if err != nil {
        t.Fatal(err)
    }
    rdb, err := rueidis.NewClient(opt)
    if err != nil {
        t.Fatal(err)
    }
    t.Cleanup(rdb.Close)
    return rdb
}

func TestViews(t *testing.T) {
    t.Parallel()
    ctx := context.Background()
    views := article.NewViews(newRedis(t))
    id := uuid.Must(uuid.NewV7())

    if n, err := views.Count(ctx, id); err != nil || n != 0 {
        t.Fatalf("count = %d, %v", n, err)
    }
    for range 2 {
        if _, err := views.Incr(ctx, id); err != nil {
            t.Fatal(err)
        }
    }
    if n, err := views.Count(ctx, id); err != nil || n != 2 {
        t.Fatalf("count = %d, %v", n, err)
    }
}
```

每个测试使用新生成的 ID，测试之间的键不会冲突，因此可以并行运行，也不需要清空数据。没有设置 `REDIS_TEST_URL` 时测试会直接失败，而不是被跳过，以免在 CI 中被悄悄忽略。

> [!WARNING]
> 请让 `REDIS_TEST_URL` 指向专门的测试实例，不要指向存放真实数据的 Redis。

本地用 `compose.test.yml` 把 Redis 和 PostgreSQL 一起启动，见[在本地运行数据库测试](testing.md#running-database-tests-locally)。
