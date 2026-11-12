# 队列

- [简介](#introduction)
- [准备](#preparing)
- [发布任务](#publishing-jobs)
    - [延迟执行](#delayed-jobs)
    - [多步任务](#multi-step-jobs)
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
    - [队列状态](#stats-command)
- [测试](#testing)

<a id="introduction"></a>
## 简介

有些工作不需要让用户等它完成，例如发送邮件、生成缩略图、通知第三方。`queue` 包把这些工作作为任务交给 [NSQ](https://nsq.io)，由单独的 worker 进程取出执行；执行失败的任务按退避时间自动重试，次数用完后转入失败任务表，等待人工处理。

任务按 topic 分类：发布方把任务发到一个 topic，worker 为每个 topic 注册一个处理函数。

任务和业务数据**在同一个事务中发布**：`Publish` 把任务写进 PostgreSQL 的 `queue_outbox` 表，事务提交后由 worker 转发到 NSQ，事务回滚时任务一起取消。这样既不会出现「文章发布了、通知任务丢了」，也不会出现「通知发出去了、文章却回滚了」。

每个任务**至少执行一次**：worker 崩溃或停止超时时，正在处理的任务会交给其他 worker 重新执行，处理函数需要能安全地重复执行。任务之间不保证执行顺序。

[邮件](mail.md#sending-mail)一章中，文章发布后直接同步发送通知邮件：接口要等邮件服务器响应，发送失败也不会重试。本章把它改为交给队列发送。

<a id="preparing"></a>
## 准备

部署 nsqd，见 [NSQ 的安装文档](https://nsq.io/deployment/installing.html)。nsqd 默认把超出内存队列（每个 channel 10000 条）的消息写到磁盘；任务不能丢，启动时加上 `--mem-queue-size=0`，让每条消息都落盘：

```bash
nsqd --data-path=/var/lib/nsqd --mem-queue-size=0
```

即使这样，nsqd 只把消息保存在本机，没有副本：nsqd 所在的机器损坏时，其中还没处理的任务会丢失；进程崩溃时，正在处理和等待重试的任务也会丢失，因为它们只在内存中。数据盘用可靠的存储，处理函数也要能安全地重复执行，丢失时可以从业务数据中找出没有完成的工作重新发布。

队列还使用两张表：`queue_outbox` 保存已经发布、还没转发到 NSQ 的任务；`queue_failed_jobs` 保存失败的任务。使用前先[发布](migrations.md#publishing-framework-migrations)它们的迁移：

```go
migrationCmd.AddCommand(migratecmd.Publish(
    migrate.Source{Name: "queue", FS: queue.Migrations()},
))
```

<a id="publishing-jobs"></a>
## 发布任务

`queue.Topic` 把 topic 名称和任务内容（payload）的类型声明在一起。发布和处理通常不在一处，两边共用这个声明，名称写错或类型不一致时无法通过编译。发送邮件的 topic 声明在博客应用的 `internal/mailjob/mailjob.go` 中，所有要发邮件的业务包都用它：

```go
package mailjob

import (
    "github.com/ryan053857/mygoravel/mail"
    "github.com/ryan053857/mygoravel/queue"
)

// Send 把邮件交给 worker 发送。
var Send = queue.Topic[mail.Message]("mail.send")
```

topic 名由字母、数字、`.`、`_`、`-` 组成，加上[配置](#configuration)中的前缀后最长 64 个字符。

`Publish` 把 payload 编码为 JSON，写入传入的数据库。传入事务时，任务和业务数据一起提交。下面是改写后的 `Publish` action：通知邮件在发布文章的同一个事务中渲染并发布，它不再需要发送器：

```go
func (a *Publish) Handle(ctx context.Context, subject identity.Subject, id uuid.UUID) (*Article, error) {
    item := new(Article)
    err := database.RunInTx(ctx, a.db, nil, func(ctx context.Context, tx database.Tx) error {
        // ... 检查权限并更新文章状态

        msg, err := a.emails.Render(ctx, "article_published", map[string]any{
            "Title": item.Title,
            "URL":   "https://blog.example.com/" + item.Slug,
        })
        if err != nil {
            return err
        }
        msg.To = []mail.Address{a.editors}
        return mailjob.Send.Publish(ctx, tx, msg)
    })
    if err != nil {
        return nil, err
    }
    return item, nil
}
```

事务提交时 PostgreSQL 通知 worker，任务随即被转发到 NSQ。`PublishAll` 一次发布多个同类任务，批量写入。

每个任务有一个 UUIDv7 作为 ID。`ctx` 中的 `request_id` 和 OpenTelemetry 链路会随任务保存，处理任务时还原到处理函数的 `ctx` 中，见[日志](#logging)和[链路追踪](telemetry.md#queue)。

<a id="delayed-jobs"></a>
### 延迟执行

`queue.Delay` 让任务在一段时间之后才执行，`queue.At` 指定执行时间：

```go
var PublishAt = queue.Topic[uuid.UUID]("articles.publish")

err := PublishAt.Publish(ctx, tx, item.ID, queue.At(item.PublishAt))
```

离执行时间不超过 `max_defer`（默认 1 小时）的任务转发为 NSQ 的延迟消息；更晚的先留在 `queue_outbox` 中，到时间再转发，所以延迟多久都可以。NSQ 定期检查到期的延迟消息，新建的 topic 上第一批延迟消息可能晚几秒执行。

业务包自己的 topic 和处理它的 action 声明在同一个文件中，见[队列任务](actions.md#as-jobs)。

<a id="multi-step-jobs"></a>
### 多步任务

有些工作要按顺序分几步完成，例如文章发布后先生成摘要，再推送给订阅者。不需要专门的链式 API：上一步完成后，在保存结果的事务中发布下一步：

```go
// Job 生成摘要，和下一步“推送给订阅者”一起提交。
func (a *Summarize) Job(ctx context.Context, id uuid.UUID) error {
    summary, err := a.generate(ctx, id) // 调用模型，不放在事务中
    if err != nil {
        return err
    }
    return database.RunInTx(ctx, a.db, nil, func(ctx context.Context, tx database.Tx) error {
        _, err := tx.NewUpdate().Model((*Article)(nil)).Set("summary = ?", summary).Where("id = ?", id).Exec(ctx)
        if err != nil {
            return err
        }
        return NotifySubscribers.Publish(ctx, tx, id)
    })
}
```

每一步只依赖自己的 payload，下一步需要的数据（例如上一步的结果）直接放进下一步的 payload，类型由 `queue.Topic` 检查。下一步和这一步的结果在同一个事务中提交：这一步失败重试时不会提前发出下一步，提交之后下一步也不会丢。

一组任务并行执行、全部结束后再做下一件事，用[后台操作的并行处理](operations.md#fanout)。

<a id="processing-jobs"></a>
## 处理任务

`Handle` 把一个函数注册为 topic 在 worker 上的处理函数，任务的内容按 JSON 解码为函数的参数类型。`mail.Mailer` 的 `Send` 方法正好符合要求：

```go
mailjob.Send.Handle(w, mailer.Send)
```

处理函数返回 `nil` 时任务完成；返回错误时任务稍后重试，见[失败与重试](#failures-and-retries)。

<a id="running-workers"></a>
### 启动 worker

worker 是 `App` 的一个运行部分（见[依赖装配](dependencies.md#assembling-the-application)）。它做两件事：把 `queue_outbox` 中的任务转发到 NSQ，以及处理已注册 topic 的任务。下面是博客应用完整的 `internal/bootstrap/worker.go`：

```go
package bootstrap

import (
    "context"

    "example.com/blog/internal/mailjob"
    "example.com/blog/internal/settings"
    "github.com/ryan053857/mygoravel/command"
    "github.com/ryan053857/mygoravel/queue"
)

// Worker 装配队列 worker，返回的函数转发并处理队列任务，直到 ctx 被取消。
func (a *App) Worker(*command.Daemon) (func(context.Context) error, error) {
    mailer, err := newMailer(a.cfg.Mail)
    if err != nil {
        return nil, err
    }

    w := queue.NewWorker(a.db, a.cfg.Queue, nil,
        queue.WithTelemetry(a.meters, a.tracers))
    mailjob.Send.Handle(w, mailer.Send)
    return w.Run, nil
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

发布的任务要等 worker 转发才会执行，所以部署时**至少运行一个 worker**。`w.Run` 连不上 nsqd 时返回错误；运行后一直到进程收到停止信号，然后停止接收任务，等处理中的任务写回结果才返回；数据库由 `run` 在所有部分都返回之后才关闭。

worker 可以和 HTTP 服务、调度器[运行在同一个进程中](commands.md#running-together)，任务量大了再单独部署，按任务量增减实例数。多个 worker 同时运行是安全的：转发时互不重复，同一个应用的 worker 使用同一个 channel，每个任务只交给其中一个。

<a id="configuration"></a>
### 配置

在应用配置中加入队列段：

```go
type Config struct {
    // ...
    Queue queue.Config `yaml:"queue"`
}
```

不写的项使用下面的默认值，生产环境至少要写 `nsqd` 和 `prefix`：

```yaml
queue:
  nsqd: 127.0.0.1:4150  # nsqd 的 TCP 地址，转发任务时发布到这里
  lookupd: []           # nsqlookupd 的 HTTP 地址；设置后从它们发现所有 nsqd 并消费
  prefix: ""            # 加在每个 topic 前面，例如 "blog."，多个应用或环境共用 nsqd 时区分
  channel: worker       # 同一个应用的 worker 使用同一个 channel
  concurrency: 10       # 每个 topic 同时处理的任务数
  max_attempts: 10      # 最多执行的次数
  lease: 30s            # NSQ 等待处理结果的时间，见下文
  max_defer: 1h         # NSQ 延迟消息的最长延迟，不能超过 nsqd 的 --max-req-timeout
  poll_interval: 1s     # 检查 queue_outbox 的最长间隔
  shutdown_timeout: 30s # 停止时等待处理中的任务的最长时间
```

`concurrency` 是每个 worker 实例的上限，部署 3 个实例时同一个 topic 最多有 30 个任务同时执行。

只有一个 nsqd 时写 `nsqd` 即可。部署多个 nsqd 时，每台应用服务器把任务发布到本机的 nsqd（`nsqd` 写本机地址），worker 通过 `lookupd` 找到全部 nsqd 并消费，一个 nsqd 宕机不影响其他 nsqd 上的任务，见 [NSQ 的拓扑](https://nsq.io/deployment/topology_patterns.html)。

`concurrency` 和 `max_attempts` 是所有 topic 的默认值。个别 topic 需要不同的值时，在注册时覆盖：

```go
article.Summarize.Handle(w, summarize.Job, queue.Concurrency(2)) // 调用模型，限制并发
```

[Webhook](webhooks.md) 的投递任务就用 `queue.MaxAttempts` 把重试延长到约 2 天，不影响其他 topic。

<a id="failures-and-retries"></a>
## 失败与重试

处理函数返回错误或 panic 时，任务在一段时间后重试，第 n 次失败后等待约 n⁴ 秒（上下浮动 10%）：

| 失败次数 | 1 | 2 | 3 | 4 | 5 | 9 |
| --- | --- | --- | --- | --- | --- | --- |
| 等待 | 1 秒 | 16 秒 | 81 秒 | 约 4 分钟 | 约 10 分钟 | 约 1.8 小时 |

默认最多执行 10 次，从第一次失败到最后一次重试约 4.3 小时，足以跨过大多数第三方服务的故障。不超过 `max_defer` 的等待由 NSQ 完成，更长的等待写回 `queue_outbox`。重试的任务不会阻塞同一个 topic 的其他任务。

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

任务自己保存了处理状态时，需要在放弃之前记下最终结果，例如把一条投递记录从「重试中」改为「失败」。`queue.AttemptFromContext` 返回本次是第几次执行，以及这是否是最后一次：

```go
if err != nil {
    if _, final := queue.AttemptFromContext(ctx); final {
        markFailed(ctx, a.db, id, err) // 本次之后不再重试
    }
    return err
}
```

从失败任务表[重新发布](#failed-jobs)的任务从第 1 次重新计数。

<a id="failed-jobs"></a>
### 失败任务

失败任务保存在 `queue_failed_jobs` 表中，`queue.FailedJobs` 用来查看和处理它们：

```go
failed := queue.NewFailedJobs(db)

jobs, err := failed.List(ctx, queue.FailedQuery{Topic: "mail.send", Limit: 50}) // 从新到旧
job, err := failed.Get(ctx, id)      // job.Error 是最后一次的错误信息，包括堆栈
err = failed.Delete(ctx, id)         // 不再需要的任务直接删除
```

修复问题之后，用 `Retry` 重新发布任务，执行次数从 0 开始计算：

```go
err := failed.Retry(ctx, id) // 不存在时返回 queue.ErrFailedJobNotFound
```

同一个原因导致的失败往往有很多条，例如邮件服务宕机了一个小时。`RetryAll` 和 `DeleteAll` 按 topic 批量处理，每次最多 `limit` 条，返回处理的条数；topic 为空时包括全部 topic：

```go
// 后台页面上的「全部重试」：只处理点击之前失败的任务
before := time.Now()
for {
    n, err := failed.RetryAll(ctx, "mail.send", before, 500)
    if err != nil || n < 500 {
        return err
    }
}
```

`before` 是必填的：重试后马上又失败的任务失败时间在它之后，不会在同一轮中被反复放回。`DeleteAll(ctx, topic, before, limit)` 的用法相同。

失败任务一直保留。`Cleanup` 返回删除保留期以前失败的任务的清理函数，交给[定时任务](scheduling.md#framework-cleanups)每天执行一次：

```go
sched.Add("queue:prune-failed", "@daily", schedule.Cleanup(failed.Cleanup(30*24*time.Hour), 1000))
```

<a id="leases-and-shutdown"></a>
## 任务的租约与停止

NSQ 把任务交给 worker 后，等待处理结果的时间是 `lease`（默认 30 秒），处理期间 worker 每 10 秒自动延长一次。任务执行多久都可以，不需要为长任务调整配置。`lease` 不能超过 nsqd 的 `--max-msg-timeout`（默认 15 分钟）。

- **worker 崩溃**：不再延长，`lease` 到期后 NSQ 把任务交给其他 worker。
- **停止 worker**：收到停止信号后，worker 不再接收新任务，等待处理中的任务完成，最多等待 `shutdown_timeout`。处理函数的 `ctx` 不会因为停止信号被取消。超时仍未完成的任务立即放回 NSQ，处理函数的 `ctx` 以 `queue.ErrShuttingDown` 取消；放回的任务再次执行时计入一次执行。

处理函数应当把 `ctx` 传给数据库查询和外部调用，这样停止超时时能尽快退出。

<a id="logging"></a>
## 日志

队列的日志带有 `component=queue` 字段，可以[单独设置级别](logging.md#component-levels)。任务失败后等待重试记录为 `warn`，转入失败任务表记录为 `error`，日志中包含 topic、错误和下次重试的等待时间。NSQ 客户端的警告和错误也记在这里。

处理函数的 `ctx` 中带有 `message_id`（任务 ID）、`attempt`（第几次执行），以及发布时的 `request_id`。使用 `slog.InfoContext(ctx, ...)` 记录的日志会自动带上它们，可以把一个请求和它产生的任务串起来。使用链路追踪时，任务的执行还会出现在发布它的请求的链路中，见[链路追踪](telemetry.md#queue)。

<a id="observability"></a>
## 观测

<a id="metrics"></a>
### 指标

指标使用 OpenTelemetry 的接口，`a.meters`、`a.tracers` 是入口中创建的 Provider，见[推送](telemetry.md#setup)。[`Worker`](#running-workers) 创建 worker 时用 `queue.WithTelemetry` 传入它们，记录每个任务的等待时长和处理耗时，并为每次执行创建 span，见[队列的链路](telemetry.md#queue)。

| 指标 | 类型 | 属性 | 含义 |
| --- | --- | --- | --- |
| `queue.job.wait` | 直方图，秒 | | 任务从可以执行到开始处理等了多久 |
| `queue.job.duration` | 直方图，秒 | `queue.job.outcome` | 处理耗时；按结果计数就是吞吐和失败率 |

所有指标都带有 `messaging.system`（`nsq`）和 `messaging.destination.name`（topic）属性，与队列的 span 相同。`queue.job.outcome` 是 `succeeded`、`retrying`、`failed`、`released`，与[任务事件](#job-events)对应。NSQ 重新投递的任务（等待不超过 `max_defer` 的重试、放回的任务）给不出可以执行的时间，不记录等待时长。导出到 Prometheus 后，点换成下划线，单位成为后缀，例如 `queue_job_wait_seconds`。

积压的任务数由 nsqd 统计，见[队列状态](#stats-command)。HTTP 请求和定时任务的指标见[可观测性](telemetry.md)。

<a id="job-events"></a>
### 任务事件

`queue.WithObserver` 让 worker 在每次状态变化时通知你，`WithTelemetry` 也建立在它之上。需要每个任务的执行历史时，把事件写进自己的表：成功的任务不会留下记录，这类数据只能从事件中得到：

```go
w := queue.NewWorker(a.db, a.cfg.Queue, nil,
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
| `JobReleased` | 停止等待超时，放回 NSQ |

除 `JobStarted` 外，事件都在结果写回之后报告，`Started` 是交给处理函数的时间，`Duration` 是处理耗时，`Err` 是失败原因。`e.Job.AvailableAt` 是任务本次可以执行的时间，给不出时为零值。Observer 在处理任务的 goroutine 中同步调用，耗时会占用并发名额：上面的写库很快没有问题，更重的工作请缓冲后批量处理。

<a id="stats-command"></a>
### 队列状态

每个 topic 积压、处理中、延迟中的任务数由 nsqd 统计，在 [nsqadmin](https://nsq.io/components/nsqadmin.html) 中查看，也可以在那里暂停一个 topic 的消费；接入监控系统用 nsqd 的 [statsd 输出](https://nsq.io/components/nsqd.html#statsd--graphite-integration)。还没转发的任务在 `queue_outbox` 中，平时只有延迟超过 `max_defer` 的任务。

失败任务在数据库中，`queuecmd.New` 返回列出它们的 `queue` 命令，它和其他需要数据库的命令一样在 [bootstrap.Commands](actions.md#as-commands) 中声明：

```go
queuecmd.New(a.failed),
```

`queue failed` 从新到旧列出失败任务，错误信息只显示第一行，完整的错误和堆栈用 `FailedJobs.Get` 查看：

```bash
./bin/api queue failed --topic mail.send --limit 10
```

```text
ID                                    TOPIC      ATTEMPTS  FAILED AT            ERROR
0193e7b1-2c4f-7d8e-9a1b-5f6c7d8e9f01  mail.send  5         2026-11-12 09:41:07  send mail: dial tcp 10.0.0.12:587: connection refused
```

<a id="testing"></a>
## 测试

处理函数是普通的函数，直接调用它测试即可。发布任务的一方，用 [`dbtest.Open`](testing.md) 打开执行过 `queue.Migrations()` 的测试数据库，检查 `queue_outbox` 中的任务：

```go
db := dbtest.Open(t, migrations.FS) // 包含已发布的 queue 迁移
publish := article.NewPublish(db, emails, editors)

// ... 创建并发布一篇文章

n, err := db.NewSelect().Table("queue_outbox").Where("topic = ?", "mail.send").Count(ctx)
if err != nil || n != 1 {
    t.Fatalf("queued %d jobs, err %v", n, err)
}
```

需要端到端运行时，`queue/queuetest` 提供连接测试 nsqd 的配置：每次使用随机的 topic 前缀，并行的测试互不干扰，测试结束时删除这些 topic。测试 nsqd 的地址由环境变量 `NSQD_TEST_ADDR`（TCP）和 `NSQD_TEST_HTTP`（HTTP）给出：

```go
w := queue.NewWorker(db, queuetest.Config(t), nil)
article.PublishJob.Handle(w, publish.Job)
queuetest.Run(t, w) // 测试结束时停止
```
