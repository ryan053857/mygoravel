# 原子锁

- [简介](#introduction)
- [装配锁](#assembling-locks)
- [只让一个实例执行](#running-once)
- [等待锁](#waiting-for-locks)
- [锁的有效期](#lock-ttl)
- [使用数据库存储](#database-store)
    - [过期清理](#cleanup)
- [使用 Redis 存储](#redis-store)
- [其他存储](#other-stores)

<a id="introduction"></a>
## 简介

应用部署多个实例时，同一段逻辑可能在多个实例上同时执行，例如定时任务在每个实例上都会触发。原子锁保证同一时刻只有一个持有者执行它。

`lock` 包提供带过期时间的锁：取得锁后在有效期内独占，持有者崩溃时锁到期自动失效，不会永远卡住。

锁只负责「谁来执行」。并发修改同一行数据时，应优先使用带条件的更新或 `SELECT ... FOR UPDATE`（SQLite 的写事务本身就是串行的），由数据库保证一致，不需要原子锁。

本章为博客应用实现定时发布：每个实例每分钟检查一次到期的定时文章并发布，用锁保证只有一个实例真正执行。

<a id="assembling-locks"></a>
## 装配锁

在[入口](dependencies.md#assembling-the-application)的 `open` 中创建 `Locker`，传给需要它的服务：

```go
import "github.com/ryan053857/mygoravel/lock"

locker := lock.New(lock.NewMemoryStore())
articles := article.NewService(db, locker)
```

`lock.New` 接收锁存储。`MemoryStore` 只在同一进程内互斥，适合开发、测试和单实例部署；多实例部署请使用[数据库存储](#database-store)或 [Redis 存储](#redis-store)。

<a id="running-once"></a>
## 只让一个实例执行

`Do` 取得锁后执行函数，结束后释放锁。锁已被其他持有者占用时不执行函数，返回 `lock.ErrNotAcquired`：

```go
func (s *Service) PublishScheduled(ctx context.Context) error {
    err := s.locker.Do(ctx, "articles:publish-scheduled", time.Minute, func(ctx context.Context) error {
        _, err := s.db.NewUpdate().Model((*Article)(nil)).
            Set("status = ?", StatusPublished).
            Where("status = ?", StatusScheduled).
            Where("publish_at <= ?", time.Now()).
            Exec(ctx)
        return err
    })
    if errors.Is(err, lock.ErrNotAcquired) {
        return nil // 其他实例正在执行
    }
    return err
}
```

锁名是任意字符串，建议用 `领域:动作` 的形式，需要按对象加锁时带上 ID，例如 `articles:42:render`。

释放锁不受 `ctx` 取消影响：请求或命令被取消后，`Do` 仍会释放锁。

> [!TIP]
> 按时触发的任务交给[调度器](scheduling.md)执行时，调度器已经保证每次触发只在一个实例上执行，任务里不需要再加锁。锁适合调度器之外的场景，例如下面的缓存重建。

<a id="waiting-for-locks"></a>
## 等待锁

`Acquire` 尝试取得锁，失败时立即返回 `lock.ErrNotAcquired`；`Wait` 在锁被占用时最多等待一段时间。两者都返回 `*lock.Lock`，用完后调用 `Release`。

例如[缓存](cache.md#cache-aside)一章的文章详情在缓存失效时，多个请求会同时查询数据库。用锁让一个请求查询数据库并写回缓存，其他请求等它完成后重读缓存。下面是改写后的 `load`，`cached` 是从原来的 `load` 中拆出的读缓存和解码部分：

```go
func (s *Service) load(ctx context.Context, id uuid.UUID) (*Article, error) {
    key := cacheKey(id)
    if item, ok := s.cached(ctx, key); ok {
        return item, nil
    }

    // 未命中：只让一个请求重建缓存，其他请求最多等待 3 秒
    if lk, err := s.locker.Wait(ctx, key+":rebuild", 10*time.Second, 3*time.Second); err == nil {
        defer lk.Release(ctx)
        if item, ok := s.cached(ctx, key); ok {
            return item, nil // 等待期间已由其他请求重建
        }
    }

    item, err := s.find(ctx, id)
    // 写回缓存，与原来相同……
}
```

没等到锁时照常查询数据库，和缓存一样，锁出问题不应让请求失败。

`Wait` 每 100 毫秒重试一次，`ctx` 取消时立即返回 `ctx` 的错误。

<a id="lock-ttl"></a>
## 锁的有效期

`Do`、`Acquire` 和 `Wait` 的 `ttl` 参数是锁的有效期。超过有效期的锁可以被其他持有者取得，因此 `ttl` 应明显长于任务的执行时间。

执行时间难以预估的长任务可以定期续期，把有效期重设为从现在起的 `ttl`：

```go
if err := lk.Refresh(ctx, time.Minute); err != nil {
    return err // lock.ErrNotHeld：锁已过期并被他人取得，应停止执行
}
```

锁过期并被他人取得后，原持有者的 `Refresh` 和 `Release` 都返回 `lock.ErrNotHeld`，不会影响新的持有者。`Do` 在函数返回后释放锁，此时得到 `ErrNotHeld` 也会一并返回，提示任务超过了有效期。

<a id="database-store"></a>
## 使用数据库存储

多实例部署时把锁保存在数据库中。`lock.NewDBStore` 接收应用的 `*bun.DB`，支持 PostgreSQL 和 SQLite：

```go
locker := lock.New(lock.NewDBStore(db))
```

使用前先发布 `locks` 表的迁移，见[发布框架迁移](migrations.md#publishing-framework-migrations)：

```go
migrationCmd.AddCommand(migratecmd.Publish(postgres.Driver,
    migrate.Source{Name: "lock", FS: lock.Migrations()},
))
```

表中每行是一把锁：`name` 是主键，最长 255 字节（`lock.MaxNameLength`），`owner` 是持有者随机生成的标识，`expires_at` 是过期时间。取得锁时先插入，锁已存在时只接管已过期的锁，两步都是单条原子语句。过期时间按应用服务器的时钟计算，各实例的时钟需要同步。

<a id="cleanup"></a>
### 过期清理

正常释放的锁会删除对应的行。持有者崩溃留下的过期行不影响加锁，会被下一次 `Acquire` 直接接管；不再使用的锁名可以用 `Cleanup` 删除，每次最多删除 `limit` 条：

```go
n, err := store.Cleanup(ctx, 1000)
```

用[定时任务](scheduling.md#framework-cleanups)定期执行，例如每小时一次。[调度器](scheduling.md#multiple-instances)本身也用锁去重，每次触发会留下一行，同样由它清理：

```go
sched.Add("locks:cleanup", "@hourly", schedule.Cleanup(store.Cleanup, 1000))
```

<a id="redis-store"></a>
## 使用 Redis 存储

应用已经使用 [Redis](redis.md) 时，也可以把锁放进 Redis。`redislock.New` 接收[入口中创建的客户端](redis.md#connecting) `rdb`：

```go
import "github.com/ryan053857/mygoravel/lock/redislock"

locker := lock.New(redislock.New(rdb))
```

每把锁是一个 Redis 键：键名就是锁名，值是持有者标识，键的过期时间就是锁的有效期。过期由 Redis 计算，不依赖各实例的时钟；过期的键由 Redis 自动删除，不需要迁移，也不需要定期清理。多个应用共用一个 Redis 时，在锁名前加上应用的命名空间，例如 `blog:articles:publish-scheduled`。

锁只写在一个 Redis 主节点上。使用哨兵或集群时，主节点故障切换可能丢失刚写入的锁，短时间内出现两个持有者；对互斥要求严格的任务请使用数据库存储。

<a id="other-stores"></a>
## 其他存储

锁存储是一个接口，三个方法都必须是原子的：

```go
type Store interface {
    Acquire(ctx context.Context, name, owner string, ttl time.Duration) (bool, error)
    Refresh(ctx context.Context, name, owner string, ttl time.Duration) (bool, error)
    Release(ctx context.Context, name, owner string) (bool, error)
}
```

实现时可以参考 [`lock/redislock`](../lock/redislock/redislock.go)：`Refresh` 和 `Release` 需要先比较 `owner` 再修改，两步必须在一个原子操作中完成。
