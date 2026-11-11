# Redis

- [简介](#introduction)
- [安装](#installation)
- [配置](#configuration)
- [连接 Redis](#connecting)
- [执行命令](#running-commands)
- [在其他组件中使用](#using-with-components)
- [测试](#testing)

<a id="introduction"></a>
## 简介

Redis 是一个内存键值存储，适合保存缓存、会话、计数器这类读写频繁、需要在多个实例之间共享的数据。

mygoravel 不封装 Redis 客户端，约定统一使用 [go-redis](https://github.com/redis/go-redis)（`github.com/redis/go-redis/v9`）。Go 社区还有 rueidis、redigo 等客户端，但 gocache、scs 等组件的 Redis 适配大多基于 go-redis。统一用它，整个应用只需要一个客户端、一个连接池。

和数据库一样，客户端在入口创建一次，通过构造函数传给需要它的服务。各个命令的用法请直接参考 [go-redis 文档](https://redis.uptrace.dev/guide/)。

<a id="installation"></a>
## 安装

```bash
go get github.com/redis/go-redis/v9@v9.23.0
```

导入路径是 `github.com/redis/go-redis/v9`，包名是 `redis`。

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

URL 形如 `redis://:password@localhost:6379/0`，路径部分是 DB 编号；使用 TLS 时把协议换成 `rediss://`。连接池大小、超时等参数也写在查询参数里，例如 `?pool_size=20&dial_timeout=3s`，支持的参数见 [`ParseURL`](https://pkg.go.dev/github.com/redis/go-redis/v9#ParseURL)。

<a id="connecting"></a>
## 连接 Redis

在[入口](dependencies.md#assembling-the-application)的 `open` 中，打开数据库之后创建客户端，并登记到 [`cleanup.Stack`](dependencies.md#closing-resources)：

```go
opts, err := redis.ParseURL(cfg.Redis.URL)
if err != nil {
    return nil, err
}
opts.ContextTimeoutEnabled = true
rdb := redis.NewClient(opts)
stack.MustAdd("redis", func(context.Context) error {
    return rdb.Close()
})
if err := rdb.Ping(ctx).Err(); err != nil {
    return nil, err
}
```

- `ContextTimeoutEnabled` 让命令遵守 ctx 的截止时间。不开启时，go-redis 只按自己的 `ReadTimeout`、`WriteTimeout` 设置网络超时，会忽略 ctx 的截止时间。
- `NewClient` 不会立即建立连接，启动时用 `Ping` 确认 Redis 可达。先登记 `Close` 再 `Ping`，这样 `Ping` 失败时客户端也会被关闭。

客户端可以并发使用，整个应用共享这一个 `rdb`：把它存进 `App` 的 `rdb` 字段，各个运行部分通过 `a.rdb` 使用。

客户端的连接池指标和每条命令的链路由 go-redis 自带的 redisotel 报告，见 [Redis 的指标](telemetry.md#redis-metrics)和[链路](telemetry.md#redis-tracing)。

<a id="running-commands"></a>
## 执行命令

以统计文章浏览量为例。服务接收的是 `redis.UniversalClient` 接口而不是 `*redis.Client`：单机、哨兵和集群客户端都实现了这个接口，部署方式变化时服务代码不用修改。

下面是完整的 `internal/article/views.go`：

```go
package article

import (
    "context"
    "errors"

    "github.com/google/uuid"
    "github.com/redis/go-redis/v9"
)

type Views struct {
    rdb redis.UniversalClient
}

func NewViews(rdb redis.UniversalClient) *Views {
    return &Views{rdb: rdb}
}

// Incr 记录一次浏览，返回累计浏览量。
func (v *Views) Incr(ctx context.Context, id uuid.UUID) (int64, error) {
    return v.rdb.Incr(ctx, viewsKey(id)).Result()
}

// Count 返回累计浏览量，没有记录时返回 0。
func (v *Views) Count(ctx context.Context, id uuid.UUID) (int64, error) {
    n, err := v.rdb.Get(ctx, viewsKey(id)).Int64()
    if errors.Is(err, redis.Nil) {
        return 0, nil
    }
    return n, err
}

func viewsKey(id uuid.UUID) string {
    return "blog:article:views:" + id.String()
}
```

在入口中用 `article.NewViews(rdb)` 构造它，传给文章详情的处理器。

- 每个命令方法返回一个结果对象。`Result()` 同时取出值和错误，`Int64()`、`Bool()` 等方法在取值时顺便转换类型。
- 键不存在时，`Get` 返回的错误是 `redis.Nil`。用 `errors.Is` 把“没有数据”和真正的故障区分开。
- 键名约定与[缓存](cache.md#keys-and-serialization)相同，以应用的命名空间开头，多个应用共用一个 Redis 时不会互相覆盖。

Pipeline、事务、Lua 脚本、发布订阅等用法见 [go-redis 文档](https://redis.uptrace.dev/guide/)。

<a id="using-with-components"></a>
## 在其他组件中使用

把同一个 `rdb` 传给需要 Redis 的组件，不要为每个组件各建一个客户端：

- **缓存**：`redisstore.NewRedis(rdb)`，见[缓存](cache.md#redis)；
- **会话**：scs 的 `goredisstore.New(rdb)`，见[会话](session.md#redis-store)；
- **原子锁**：`redislock.New(rdb)`，见[原子锁](locks.md#redis-store)；
- **限流**：`redislimit.New(rdb, name, cfg)`，见[限流](rate-limiting.md#redis-limiter)；
- **队列**：`redisqueue.New(rdb, prefix)`，见[队列](queue.md#redis-driver)。

scs 的 `goredisstore.New` 要求传入 `*redis.Client`，不接受集群客户端。

<a id="testing"></a>
## 测试

依赖 Redis 的代码用真实的 Redis 测试。和[数据库测试](testing.md#database-tests)一样，测试文件加上 `integration` 构建标签。下面是 `internal/article/views_test.go`：

```go
//go:build integration

package article_test

import (
    "context"
    "os"
    "testing"

    "example.com/blog/internal/article"
    "github.com/google/uuid"
    "github.com/redis/go-redis/v9"
)

func newRedis(t *testing.T) *redis.Client {
    t.Helper()
    url := os.Getenv("REDIS_TEST_URL")
    if url == "" {
        t.Fatal("REDIS_TEST_URL is not set")
    }
    opts, err := redis.ParseURL(url)
    if err != nil {
        t.Fatal(err)
    }
    rdb := redis.NewClient(opts)
    t.Cleanup(func() { rdb.Close() })
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

本地可以用 Docker 启动一个临时的 Redis：

```bash
docker run --name app-test-redis --rm -d -p 127.0.0.1:56379:6379 redis:8
export REDIS_TEST_URL='redis://127.0.0.1:56379/0'
go test -race -count=1 -tags=integration ./...
```
