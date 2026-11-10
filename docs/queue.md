# 队列

- [简介](#introduction)
- [准备数据表](#preparing-tables)
- [发布任务](#publishing-jobs)
    - [延迟执行](#delayed-jobs)
- [处理任务](#processing-jobs)
    - [启动 worker](#running-workers)
    - [配置](#configuration)
- [失败与重试](#failures-and-retries)
    - [失败任务](#failed-jobs)
- [任务的租约与停止](#leases-and-shutdown)
- [日志](#logging)
- [测试](#testing)
- [使用 Watermill](#using-watermill)

<a id="introduction"></a>
## 简介

有些工作不需要让用户等它完成，例如发送邮件、生成缩略图、通知第三方。`queue` 包把这些工作作为任务保存在数据库中，由单独的 worker 进程取出执行；执行失败的任务按退避时间自动重试，次数用完后转入失败任务表，等待人工处理。

队列基于 [Watermill](https://watermill.io) 的消息模型：任务就是一条 `message.Message`，worker 用 Watermill 的 `Router` 把任务分发给处理函数，`queue` 提供的发布者和订阅者负责把任务存进数据库、领取、重试和记录失败。

每个任务**至少执行一次**：worker 崩溃或停止超时时，正在处理的任务会交给其他 worker 重新执行，处理函数需要能安全地重复执行。任务之间不保证执行顺序。

[邮件](mail.md#sending-mail)一章中，文章发布后直接同步发送通知邮件：接口要等邮件服务器响应，发送失败也不会重试。本章把它改为交给队列发送。

<a id="preparing-tables"></a>
## 准备数据表

队列使用两张表：`queue_jobs` 保存待处理和处理中的任务，处理成功后删除；`queue_failed_jobs` 保存失败的任务。使用前先[发布](migrations.md#publishing-framework-migrations)它们的迁移：

```go
migrationCmd.AddCommand(migratecmd.Publish(postgres.Driver,
    migrate.Source{Name: "queue", FS: queue.Migrations()},
))
```

队列支持 PostgreSQL 和 SQLite，发布时按应用的数据库选择对应的迁移；MySQL 暂不支持，发布它的迁移会报错。

使用 SQLite 时，同一时刻只有一个连接可以写入，领取、确认任务和业务写入排队进行：

- 一个长时间不提交的写事务会让领取任务等待，最长等到 SQLite 的 `busy_timeout`（5 秒）后报错重试。事务中不要做耗时的外部调用。
- 多个 worker 进程可以打开同一个数据库文件，每个任务只会被其中一个领取；但它们必须在同一台机器上，见[使用 SQLite 部署](deployment.md#sqlite)。
- 写入量由单个写者的吞吐决定，任务量大时请改用 PostgreSQL。

<a id="publishing-jobs"></a>
## 发布任务

`queue.Publish` 把任意可以编码为 JSON 的值发布到一个 topic。`queue.NewPublisher` 接收 `*bun.DB` 或 `bun.Tx`，传入事务时，任务和业务数据一起提交，事务回滚时任务也不会执行。

下面是改写后的 `Publish`：通知邮件在发布文章的同一个事务中渲染并入队，文章服务不再需要发送器：

```go
func (s *Service) Publish(ctx context.Context, subject identity.Subject, id uuid.UUID) (*Article, error) {
    item := new(Article)
    err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
        // ... 检查权限并更新文章状态

        msg, err := s.emails.Render(ctx, "article_published", map[string]any{
            "Title": item.Title,
            "URL":   "https://blog.example.com/" + item.Slug,
        })
        if err != nil {
            return err
        }
        msg.To = []mail.Address{s.editors}
        return queue.Publish(ctx, queue.NewPublisher(tx), "mail.send", msg)
    })
    if err != nil {
        return nil, err
    }
    return item, nil
}
```

和同步发送相比：

- **不会丢通知**：文章发布成功，通知任务就一定在库里；邮件服务器暂时不可用时任务会自动重试。
- **不会发错通知**：事务回滚时任务一起回滚，不会出现「邮件发出去了，文章没发布」。

每个任务有一个 UUIDv7 作为 ID。`ctx` 中的 `request_id` 会随任务保存，处理任务时还原到处理函数的 `ctx` 中，见[日志](#logging)。

<a id="delayed-jobs"></a>
### 延迟执行

`queue.Delay` 让任务在一段时间之后才执行，`queue.At` 指定执行时间：

```go
err := queue.Publish(ctx, queue.NewPublisher(tx), "articles.publish", item.ID, queue.At(item.PublishAt))
```

<a id="processing-jobs"></a>
## 处理任务

`queue.Handle` 把一个函数注册为某个 topic 的处理函数，任务的内容按 JSON 解码为函数的参数类型。`mail.Mailer` 的 `Send` 方法正好符合要求：

```go
queue.Handle(router, sub, "mail.send", mailer.Send)
```

处理函数返回 `nil` 时任务完成并被删除；返回错误时任务稍后重试，见[失败与重试](#failures-and-retries)。

<a id="running-workers"></a>
### 启动 worker

worker 是 `App` 的一个运行部分（见[依赖装配](dependencies.md#assembling-the-application)）。下面是博客应用完整的 `internal/bootstrap/worker.go`：

```go
package bootstrap

import (
    "context"

    "example.com/blog/internal/settings"
    "github.com/ryan053857/mygoravel/queue"
)

// Worker 处理队列任务，直到 ctx 被取消。
func (a *App) Worker(ctx context.Context) error {
    mailer, err := newMailer(a.cfg.Mail)
    if err != nil {
        return err
    }

    sub := queue.NewSubscriber(a.db, a.cfg.Queue, nil)
    defer sub.Close()
    router := queue.NewRouter(nil)
    queue.Handle(router, sub, "mail.send", mailer.Send)
    return router.Run(ctx)
}

// Work 只运行队列 worker，work 命令调用它。
func Work(ctx context.Context, cfg settings.Config) error {
    return run(ctx, cfg, (*App).Worker)
}
```

`newMailer` 见[装配发送器](mail.md#assembling-mailer)。在根命令中注册它：

```go
root.AddCommand(command.Run("work", "处理队列任务", bootstrap.Work))
```

`router.Run` 一直运行到进程收到停止信号。`defer sub.Close()` 在它返回后等待最后的处理结果写回数据库；数据库由 `run` 在所有部分都返回之后才关闭。

worker 可以和 HTTP 服务、调度器[运行在同一个进程中](commands.md#running-together)，任务量大了再单独部署，按任务量增减实例数。多个 worker 同时运行是安全的，每个任务只会被其中一个领取。

<a id="configuration"></a>
### 配置

在应用配置中加入队列段：

```go
type Config struct {
    // ...
    Queue queue.Config `yaml:"queue"`
}
```

不写这一段时使用下面的默认值：

```yaml
queue:
  concurrency: 10       # 每个 topic 同时处理的任务数
  poll_interval: 1s     # 没有任务时多久查询一次
  lease: 30s            # 领取任务后的租约，见下文
  max_attempts: 10      # 最多执行的次数
  shutdown_timeout: 30s # 停止时等待处理中的任务的最长时间
```

`concurrency` 是每个 worker 实例的上限，部署 3 个实例时同一个 topic 最多有 30 个任务同时执行。任务在发布后最多 `poll_interval` 被领取，对时效敏感的 topic 可以调小它，代价是更频繁的查询。

<a id="failures-and-retries"></a>
## 失败与重试

处理函数返回错误或 panic 时，任务在一段时间后重试，第 n 次失败后等待约 n⁴ 秒（上下浮动 10%）：

| 失败次数 | 1 | 2 | 3 | 4 | 5 | 9 |
| --- | --- | --- | --- | --- | --- | --- |
| 等待 | 1 秒 | 16 秒 | 81 秒 | 约 4 分钟 | 约 10 分钟 | 约 1.8 小时 |

默认最多执行 10 次，从第一次失败到最后一次重试约 4.3 小时，足以跨过大多数第三方服务的故障。重试的任务不会阻塞同一个 topic 的其他任务。

有些错误重试也不会成功，例如文章已经被删除。用 `queue.Permanent` 包装这类错误，任务直接转入失败任务表：

```go
if errors.Is(err, article.ErrNotFound) {
    return queue.Permanent(err)
}
```

任务内容无法解码为处理函数的参数类型时，同样视为永久失败。

<a id="failed-jobs"></a>
### 失败任务

失败任务保存在 `queue_failed_jobs` 表中，`error` 列是最后一次的错误信息，包括堆栈。直接用 SQL 查询它们：

```sql
SELECT id, topic, attempts, failed_at, error FROM queue_failed_jobs ORDER BY failed_at DESC;
```

修复问题之后，用 `FailedJobs.Retry` 把任务放回队列，执行次数从 0 开始计算：

```go
failed := queue.NewFailedJobs(db)
err := failed.Retry(ctx, id) // 不存在时返回 queue.ErrFailedJobNotFound
```

失败任务一直保留，用 `Cleanup` 删除一段时间之前的，每次最多删除 `limit` 条：

```go
n, err := failed.Cleanup(ctx, time.Now().AddDate(0, 0, -30), 1000)
```

用[定时任务](scheduling.md#framework-cleanups)每天执行一次：

```go
sched.Add("queue:prune-failed", "@daily", func(ctx context.Context) error {
    _, err := failed.Cleanup(ctx, time.Now().AddDate(0, 0, -30), 1000)
    return err
})
```

<a id="leases-and-shutdown"></a>
## 任务的租约与停止

worker 领取任务时同时取得一个租约（默认 30 秒），处理期间每 10 秒自动续租一次。任务执行多久都可以，不需要为长任务调整配置。

- **worker 崩溃**：租约不再续期，到期后任务被其他 worker 重新领取。
- **租约丢失**：worker 与数据库失联超过租约时，任务可能已被其他 worker 领取。此时处理函数的 `ctx` 被取消，`context.Cause(ctx)` 是 `queue.ErrLeaseLost`，应当停止处理；它的处理结果也不会再写回。
- **停止 worker**：收到停止信号后，worker 不再领取新任务，等待处理中的任务完成，最多等待 `shutdown_timeout`。处理函数的 `ctx` 不会因为停止信号被取消。超时仍未完成的任务立即交给其他 worker，这次执行不计入次数，处理函数的 `ctx` 以 `queue.ErrShuttingDown` 取消。

处理函数应当把 `ctx` 传给数据库查询和外部调用，这样租约丢失或停止超时时能尽快退出。

<a id="logging"></a>
## 日志

队列的日志带有 `component=queue` 字段，可以[单独设置级别](logging.md#component-levels)。任务失败后等待重试记录为 `warn`，转入失败任务表记录为 `error`，日志中包含 topic、错误和下次重试的等待时间。

处理函数的 `ctx` 中带有 `message_id`（任务 ID）、`attempt`（第几次执行），以及发布时的 `request_id`。使用 `slog.InfoContext(ctx, ...)` 记录的日志会自动带上它们，可以把一个请求和它产生的任务串起来。

<a id="testing"></a>
## 测试

处理函数是普通的函数，直接调用它测试即可。发布任务的一方，用 [`dbtest.Open`](testing.md) 打开执行过 `queue.Migrations()` 的测试数据库，检查 `queue_jobs` 中的任务：

```go
db := dbtest.Open(t, postgres.Driver, migrations.FS) // 包含已发布的 queue 迁移
svc := article.NewService(db, emails, editors)

// ... 创建并发布一篇文章

n, err := db.NewSelect().Table("queue_jobs").Where("topic = ?", "mail.send").Count(ctx)
if err != nil || n != 1 {
    t.Fatalf("queued %d jobs, err %v", n, err)
}
```

<a id="using-watermill"></a>
## 使用 Watermill

`queue.NewRouter` 返回的就是 Watermill 的 `*message.Router`，`queue.Handle` 返回 `*message.Handler`，可以使用 Watermill 提供的[中间件](https://watermill.io/docs/middlewares/)，例如用 `Throttle` 限制处理速率、用 `Timeout` 限制单次执行时间。

`queue.NewRouter` 已经安装了 `Recoverer`，把处理函数的 panic 转为普通的失败。不要再安装 Watermill 的 `Retry` 和 `PoisonQueue`：它们在进程内重试、把失败消息发到另一个 topic，而队列的重试和失败任务是持久化的，两者叠加会让一次失败被重试很多次。

`queue.Publisher` 和 `queue.Subscriber` 实现了 Watermill 的 `message.Publisher` 和 `message.Subscriber`，也可以用 `router.AddConsumerHandler` 直接注册处理 `*message.Message` 的函数。
