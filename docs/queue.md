# 队列

- [简介](#introduction)
- [准备数据表](#preparing-tables)
- [发布任务](#publishing-jobs)
    - [延迟执行](#delayed-jobs)
    - [带类型的 topic](#typed-topics)
- [处理任务](#processing-jobs)
    - [启动 worker](#running-workers)
    - [配置](#configuration)
- [失败与重试](#failures-and-retries)
    - [失败任务](#failed-jobs)
- [任务的租约与停止](#leases-and-shutdown)
- [日志](#logging)
- [观测](#observability)
    - [指标](#metrics)
    - [任务事件](#job-events)
    - [队列状态](#queue-stats)
    - [一个现成的命令](#stats-command)
- [测试](#testing)
- [使用 Redis](#redis-driver)
- [使用 NATS](#nats-driver)
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

队列支持 PostgreSQL、MySQL 和 SQLite，发布时按应用的数据库选择对应的迁移。PostgreSQL 和 MySQL 上，多个 worker 用 `SKIP LOCKED` 跳过彼此正在领取的任务，可以部署在多台机器上。

使用 SQLite 时，同一时刻只有一个连接可以写入，领取、确认任务和业务写入排队进行：

- 一个长时间不提交的写事务会让领取任务等待，最长等到 SQLite 的 `busy_timeout`（5 秒）后报错重试。事务中不要做耗时的外部调用。
- 多个 worker 进程可以打开同一个数据库文件，每个任务只会被其中一个领取；但它们必须在同一台机器上，见[使用 SQLite 部署](deployment.md#sqlite)。
- 写入量由单个写者的吞吐决定，任务量大时请改用 PostgreSQL。

<a id="publishing-jobs"></a>
## 发布任务

`queue.Publish` 把任意可以编码为 JSON 的值发布到一个 topic。第二个参数是队列驱动，`queue.Database` 把任务保存在数据库中，它接收 `*bun.DB` 或 `bun.Tx`：传入事务时，任务和业务数据一起提交，事务回滚时任务也不会执行。

下面是改写后的 `Publish` action：通知邮件在发布文章的同一个事务中渲染并入队，它不再需要发送器：

```go
func (a *Publish) Handle(ctx context.Context, subject identity.Subject, id uuid.UUID) (*Article, error) {
    item := new(Article)
    err := a.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
        // ... 检查权限并更新文章状态

        msg, err := a.emails.Render(ctx, "article_published", map[string]any{
            "Title": item.Title,
            "URL":   "https://blog.example.com/" + item.Slug,
        })
        if err != nil {
            return err
        }
        msg.To = []mail.Address{a.editors}
        return queue.Publish(ctx, queue.Database(tx), "mail.send", msg)
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
err := queue.Publish(ctx, queue.Database(tx), "articles.publish", item.ID, queue.At(item.PublishAt))
```

<a id="typed-topics"></a>
### 带类型的 topic

同一个 topic 的发布和处理通常不在一处：上面发布 `articles.publish`，worker 中处理它。用 `queue.Topic` 把 topic 名称和 payload 类型声明在一起，两边共用这个声明，名称写错或类型不一致时无法通过编译：

```go
var PublishAt = queue.Topic[uuid.UUID]("articles.publish")

err := PublishAt.Publish(ctx, queue.Database(tx), item.ID, queue.At(item.PublishAt))
```

`Topic` 的 `Publish` 和 `Handle` 方法与 `queue.Publish`、`queue.Handle` 相同，只是不再需要 topic 参数。把它和处理任务的 action 声明在同一个文件中，见[队列任务](actions.md#as-jobs)。

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

    sub := queue.NewSubscriber(queue.Database(a.db), a.cfg.Queue, nil)
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

`newMailer` 见[装配发送器](mail.md#assembling-mailer)。action 的任务入口也注册在这里，例如定时发布文章，见[队列任务](actions.md#as-jobs)。在根命令中注册它：

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

第三方服务限流时往往会告诉你多久之后再试，例如 HTTP 429 响应的 `Retry-After`。用 `queue.RetryAfter` 包装错误，任务在指定的时间之后重试，而不是按上表等待：

```go
if resp.StatusCode == http.StatusTooManyRequests {
    wait, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
    return queue.RetryAfter(errors.New("rate limited"), time.Duration(wait)*time.Second)
}
```

这次重试同样计入执行次数，次数用完时照常转入失败任务表。

<a id="failed-jobs"></a>
### 失败任务

失败任务保存在 `queue_failed_jobs` 表中，`queue.FailedJobs` 用来查看和处理它们：

```go
failed := queue.NewFailedJobs(db)

jobs, err := failed.List(ctx, queue.FailedQuery{Topic: "mail.send", Limit: 50}) // 从新到旧
job, err := failed.Get(ctx, id)      // job.Error 是最后一次的错误信息，包括堆栈
counts, err := failed.Count(ctx)     // 每个 topic 的失败任务数
err = failed.Delete(ctx, id)         // 不再需要的任务直接删除
```

修复问题之后，用 `Retry` 把任务放回队列，执行次数从 0 开始计算：

```go
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

<a id="observability"></a>
## 观测

观测分两层：接入监控系统用[指标](#metrics)，所有驱动报告同一套指标；写命令、做后台页面、保存执行历史用原始数据：[任务事件](#job-events)、[队列状态](#queue-stats)和[失败任务](#failed-jobs)。两层的状态名称相同。

<a id="metrics"></a>
### 指标

指标使用 [OpenTelemetry](https://opentelemetry.io/docs/languages/go/) 的接口，导出到 Prometheus、OTLP 还是其他系统由应用选择。例如导出给 Prometheus 抓取，在 `open` 中创建 `MeterProvider`：

```go
import (
    "go.opentelemetry.io/otel/exporters/prometheus"
    sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

exporter, err := prometheus.New()
if err != nil {
    return nil, err
}
meters := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter))
stack.MustAdd("metrics", meters.Shutdown)
```

同样在 `open` 中注册队列中的任务数，并把 `meters` 保存在 `App` 中：

```go
if _, err := queue.ObserveJobs(meters, queue.Database(db), queue.NewFailedJobs(db)); err != nil {
    return nil, err
}
```

`Worker` 创建订阅者时传入它：

```go
sub := queue.NewSubscriber(queue.Database(a.db), a.cfg.Queue, nil, queue.WithMeterProvider(a.meters))
```

Prometheus 通过 `promhttp.Handler()`（`github.com/prometheus/client_golang/prometheus/promhttp`）抓取，把它挂在 `/metrics` 上；不要把这个路径暴露到公网。

| 指标 | 类型 | 属性 | 含义 |
| --- | --- | --- | --- |
| `queue.job.wait` | 直方图，秒 | | 任务从可以执行到开始处理等了多久 |
| `queue.job.duration` | 直方图，秒 | `queue.job.outcome` | 处理耗时；按结果计数就是吞吐和失败率 |
| `queue.jobs` | gauge | `queue.job.state` | 各状态的任务数：`ready`、`delayed`、`retrying`、`running`、`failed` |
| `queue.job.oldest_ready_age` | gauge，秒 | | 积压最久的任务已经等了多久，没有积压时为 0 |

所有指标都带有 `queue.driver` 和 `queue.topic` 属性。`queue.job.outcome` 是 `succeeded`、`retrying`、`failed`、`released`、`lease_lost`，与[任务事件](#job-events)对应。导出到 Prometheus 后，点换成下划线，单位成为后缀，例如 `queue_job_wait_seconds`。

前两个由订阅者在处理任务时记录。后两个在采集时读取[队列状态](#queue-stats)和失败任务数，5 秒内的多次采集复用同一次读取；出现过的 topic 在任务清空后报告 0，而不是消失。每个实例读到的都是同一份全局数据，所以汇总多个实例时取最大值，不要求和。例如积压超过 5 分钟时告警：

```promql
max by (queue_topic) (queue_job_oldest_ready_age_seconds) > 300
```

[NATS 驱动](#nats-driver)给不出的数据不报告：`queue.jobs` 只有 `ready`、`running` 和 `failed`，没有 `queue.job.oldest_ready_age`，`queue.job.wait` 只记录首次执行。

<a id="job-events"></a>
### 任务事件

`queue.WithObserver` 让订阅者在每次状态变化时通知你。成功的任务会从队列中删除，执行历史、耗时这类数据只能从事件中得到：

```go
sub := queue.NewSubscriber(queue.Database(a.db), a.cfg.Queue, nil,
    queue.WithObserver(func(ctx context.Context, e queue.Event) {
        if e.Kind == queue.JobStarted {
            return
        }
        _, err := a.db.NewInsert().Model(&JobRun{
            JobID: e.Job.ID, Topic: e.Job.Topic, Attempt: e.Job.Attempt,
            Status: e.Kind.String(), Duration: e.Duration, Error: errorText(e.Err),
        }).Exec(ctx)
        if err != nil {
            slog.ErrorContext(ctx, "record job run failed", "error", err)
        }
    }))
```

这里把结束的任务写进应用自己的 `job_runs` 表，后台页面查询这张表就能看到执行历史；保留多久由应用决定，用[定时任务](scheduling.md)清理。

| 事件 | 时机 |
| --- | --- |
| `JobStarted` | 任务交给处理函数 |
| `JobSucceeded` | 处理成功并已确认 |
| `JobRetrying` | 处理失败，`RetryAt` 是下一次执行时间 |
| `JobFailed` | 转入失败任务表 |
| `JobReleased` | 停止等待超时，还回队列 |
| `JobLeaseLost` | 租约过期，任务可能已被其他实例领取 |

除 `JobStarted` 外，事件都在结果写回队列之后报告，`Duration` 是处理耗时，`Err` 是失败原因。`e.Job.AvailableAt` 是任务本次可以执行的时间，驱动给不出时为零值。Observer 在处理任务的 goroutine 中同步调用，耗时会占用并发名额：上面的写库很快没有问题，更重的工作请缓冲后批量处理。

<a id="queue-stats"></a>
### 队列状态

驱动的 `Stats` 返回每个还有任务的 topic 中各状态的任务数：

```go
stats, err := queue.Database(db).Stats(ctx)
for _, s := range stats {
    // s.Jobs[queue.StateReady]、s.OldestReadyAt
}
```

| 状态 | 含义 |
| --- | --- |
| `StateReady` | 已经到期、等待领取，包括租约过期、等待重新领取的任务 |
| `StateDelayed` | 还没到执行时间、从未执行过 |
| `StateRetrying` | 失败后等待重试 |
| `StateRunning` | 正在处理 |

`OldestReadyAt` 是 `StateReady` 中最早到期的时间，`time.Since(s.OldestReadyAt)` 就是积压最久的任务已经等了多久。失败任务数由 `FailedJobs.Count` 给出，对应状态 `StateFailed`。

驱动给不出的状态不出现在 `s.Jobs` 中，给得出的状态即使为 0 也会出现。[NATS 驱动](#nats-driver)只给出 `StateReady` 和 `StateRunning`（包括等待重试的任务），也给不出 `OldestReadyAt`。统计会扫描队列中的任务，适合命令、后台页面和按分钟级频率采集的指标。

<a id="stats-command"></a>
### 一个现成的命令

下面的命令列出每个 topic 的任务数和失败数。它和 `seed` 一样在 [bootstrap.Commands](actions.md#as-commands) 中声明，`db` 在命令执行时装配，之后执行 `./bin/api queue stats`：

```go
func queueCmd(db *bun.DB) *cobra.Command {
    stats := &cobra.Command{
        Use:   "stats",
        Short: "查看队列中的任务数",
        Args:  command.NoArgs,
        RunE: func(cmd *cobra.Command, _ []string) error {
            stats, err := queue.Database(db).Stats(cmd.Context())
            if err != nil {
                return err
            }
            failed, err := queue.NewFailedJobs(db).Count(cmd.Context())
            if err != nil {
                return err
            }
            w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
            fmt.Fprintln(w, "TOPIC\tREADY\tDELAYED\tRETRYING\tRUNNING\tFAILED\tWAITING")
            for _, s := range stats {
                waiting := "-"
                if !s.OldestReadyAt.IsZero() {
                    waiting = time.Since(s.OldestReadyAt).Round(time.Second).String()
                }
                fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t%d\t%s\n", s.Topic,
                    s.Jobs[queue.StateReady], s.Jobs[queue.StateDelayed], s.Jobs[queue.StateRetrying],
                    s.Jobs[queue.StateRunning], failed[s.Topic], waiting)
            }
            return w.Flush()
        },
    }
    root := &cobra.Command{Use: "queue", Short: "队列"}
    root.AddCommand(stats)
    return root
}
```

查看和重试失败任务的命令写法相同，调用 `FailedJobs` 的 `List` 和 `Retry` 即可。

<a id="testing"></a>
## 测试

处理函数是普通的函数，直接调用它测试即可。发布任务的一方，用 [`dbtest.Open`](testing.md) 打开执行过 `queue.Migrations()` 的测试数据库，检查 `queue_jobs` 中的任务：

```go
db := dbtest.Open(t, postgres.Driver, migrations.FS) // 包含已发布的 queue 迁移
publish := article.NewPublish(db, emails, editors)

// ... 创建并发布一篇文章

n, err := db.NewSelect().Table("queue_jobs").Where("topic = ?", "mail.send").Count(ctx)
if err != nil || n != 1 {
    t.Fatalf("queued %d jobs, err %v", n, err)
}
```

<a id="redis-driver"></a>
## 使用 Redis

任务量大、不想让队列的轮询和写入占用数据库时，可以把任务放进 [Redis](redis.md)。`redisqueue.New` 接收[入口中创建的客户端](redis.md#connecting)和一个键名前缀：

```go
import "github.com/ryan053857/mygoravel/queue/redisqueue"

jobs := redisqueue.New(a.rdb, "blog:queue")

// 发布
err := queue.Publish(ctx, jobs, "mail.send", msg)

// 处理：失败任务仍然写入数据库
failed := queue.NewFailedJobs(a.db, jobs)
sub := queue.NewSubscriber(jobs, a.cfg.Queue, nil, queue.WithFailedJobs(failed))
```

其余的用法和数据库驱动完全相同：退避重试、失败任务、租约与平滑停止的行为一致，`failed.Retry` 把任务放回 Redis。与数据库驱动的区别：

- **不能在事务中发布。** Redis 不参与数据库事务，发布任务和业务写入不是原子的，在事务提交之后再发布，见[下文](#publish-after-commit)。需要「业务数据提交，任务就一定在」的场景，仍然使用 `queue.Database(tx)`。
- **失败任务需要数据库。** 用 `WithFailedJobs` 指定，表结构见[准备数据表](#preparing-tables)。
- **持久性取决于 Redis 的配置。** 只开启 RDB 快照时，Redis 重启会丢失最近一次快照之后发布的任务；需要可靠的队列时开启 AOF（`appendonly yes`）。主从切换同样可能丢失刚写入的任务。

每个 topic 在 Redis 中有四个键，键名以 `blog:queue:{mail.send}:` 开头，花括号让同一个 topic 的键落在同一个集群槽位，因此也可以使用 Redis 集群。任务完成后对应的记录都会删除，不需要清理。租约和延迟按 Redis 服务端的时钟计算，不受各实例时钟偏差影响。

<a id="publish-after-commit"></a>
### 事务提交后再发布

在事务中调用 `queue.Publish(ctx, jobs, ...)` 时，任务会立刻进入 Redis：worker 可能在事务提交之前就领到任务、读不到新数据；事务回滚了，任务也照样执行。所以要在事务函数返回之后再发布。改写[发布文章](#publishing-jobs)的例子，`a.jobs` 是上面创建的 Redis 驱动，事务里只准备好邮件：

```go
func (a *Publish) Handle(ctx context.Context, subject identity.Subject, id uuid.UUID) (*Article, error) {
    item := new(Article)
    var msg mail.Message
    err := a.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) (err error) {
        // ... 检查权限并更新文章状态

        msg, err = a.emails.Render(ctx, "article_published", map[string]any{
            "Title": item.Title,
            "URL":   "https://blog.example.com/" + item.Slug,
        })
        return err
    })
    if err != nil {
        return nil, err
    }
    msg.To = []mail.Address{a.editors}
    if err := queue.Publish(ctx, a.jobs, "mail.send", msg); err != nil {
        return nil, err
    }
    return item, nil
}
```

这样事务回滚时不会发布，worker 领到任务时文章一定已经提交。代价是两步之间不是原子的：提交成功后、发布之前进程崩溃或 Redis 不可用，这封通知就丢了，`Handle` 返回错误时文章也已经发布。能接受偶尔丢失的任务（通知、刷新缓存）这样做即可；不能丢的任务使用 `queue.Database(tx)`。

<a id="nats-driver"></a>
## 使用 NATS

已经在使用 [NATS](https://nats.io) 时，也可以把任务放进 JetStream。`natsqueue.New` 接收应用创建的连接和一个流名：

```go
import "github.com/ryan053857/mygoravel/queue/natsqueue"

jobs := natsqueue.New(nc, "BLOG_QUEUE")

err := queue.Publish(ctx, jobs, "mail.send", msg)

failed := queue.NewFailedJobs(a.db, jobs)
sub := queue.NewSubscriber(jobs, a.cfg.Queue, nil, queue.WithFailedJobs(failed))
```

服务器需要 NATS 2.12 或更高版本并开启 JetStream（`nats-server -js`），延迟任务使用服务器的定时消息；版本过低时发布和领取任务都会返回错误。驱动在第一次使用时创建名为 `BLOG_QUEUE` 的流，任务确认后即从流中删除；集群部署时用 `natsqueue.Replicas(3)` 设置副本数。每个 topic 对应一个持久消费者，没有任务时拉取请求在服务器上等待，任务到达后立即被领取，不受 `poll_interval` 影响。

和 [Redis](#redis-driver) 一样，NATS 不能在数据库事务中发布，需要[在事务提交后再发布](#publish-after-commit)，失败任务需要用 `WithFailedJobs` 写入数据库。此外还有几处不同：

- **延迟的精度是秒。** 延迟任务向上取整到秒，最多晚 1 秒执行，不会提前。
- **租约在启动时确定。** 租约就是消费者的确认等待时间，取自进程启动后第一次领取时的 `lease`，修改配置后重启生效。
- **租约过期后不能阻止旧的确认。** 一个 worker 与 NATS 失联超过租约、任务被重新投递后，原来的 worker 仍然可以确认它。处理函数本来就需要能重复执行，这通常没有影响。
- **停止超时释放的任务计入一次执行。** NATS 的投递次数不能回退。
- **不检查重复的任务 ID。**

<a id="using-watermill"></a>
## 使用 Watermill

`queue.NewRouter` 返回的就是 Watermill 的 `*message.Router`，`queue.Handle` 返回 `*message.Handler`，可以使用 Watermill 提供的[中间件](https://watermill.io/docs/middlewares/)，例如用 `Throttle` 限制处理速率、用 `Timeout` 限制单次执行时间。

`queue.NewRouter` 已经安装了 `Recoverer`，把处理函数的 panic 转为普通的失败。不要再安装 Watermill 的 `Retry` 和 `PoisonQueue`：它们在进程内重试、把失败消息发到另一个 topic，而队列的重试和失败任务是持久化的，两者叠加会让一次失败被重试很多次。

`queue.Subscriber` 实现了 Watermill 的 `message.Subscriber`，也可以用 `router.AddConsumerHandler` 直接注册处理 `*message.Message` 的函数。`queue.NewPublisher(driver)` 返回实现 `message.Publisher` 的发布者，用来发布 Watermill 消息，例如交给 Watermill 中间件使用。
