# 定时任务

- [简介](#introduction)
- [定义任务](#defining-tasks)
    - [表达式](#expressions)
    - [时区](#timezones)
- [运行调度器](#running-the-scheduler)
- [多个实例](#multiple-instances)
- [防止重叠](#preventing-overlaps)
- [失败与停止](#failures-and-shutdown)
- [清理框架数据](#framework-cleanups)
- [测试](#testing)

<a id="introduction"></a>
## 简介

会话、锁、限流和失败任务都会在数据库中留下过期的行，需要定期清理；应用自己也常有定时的工作，例如每天凌晨生成报表。`schedule` 包按 cron 表达式定时执行这些任务，精度到秒。

调度器只负责「按时触发」：任务就是一个 `func(ctx context.Context) error`，在调度器所在的进程中执行。部署多个实例时，同一个任务的同一次触发只会在一个实例上执行。

<a id="defining-tasks"></a>
## 定义任务

`schedule.New` 创建调度器，`Add` 注册任务。名称在调度器中唯一，也是锁名的一部分，最长 `schedule.MaxNameLength`（225）字节：

```go
sched := schedule.New(locker, nil)
sched.Add("articles:publish-scheduled", "* * * * *", article.NewPublishScheduled(db).Handle)
```

任务通常是 [action](actions.md#as-scheduled-tasks) 的 `Handle` 方法：发布到期文章的 `PublishScheduled` 不需要参数，它的 `Handle` 正好是任务的签名。

`locker` 是[原子锁](locks.md)，调度器用它在多个实例之间去重、防止任务重叠，见[多个实例](#multiple-instances)。第二个参数是日志器，`nil` 时使用 `slog.Default()`。

[原子锁](locks.md#running-once)一章中，每个实例都要自己定时调用 `PublishScheduled` 并用锁避免重复执行；交给调度器之后，`PublishScheduled` 里的 `locker.Do` 可以去掉，它也不再需要 `locker` 依赖，调度器已经保证每次只在一个实例上执行。

<a id="expressions"></a>
### 表达式

| 写法 | 含义 |
| --- | --- |
| `*/5 * * * *` | 标准的 5 段 cron 表达式，分钟精度，在第 0 秒触发 |
| `*/10 * * * * *` | 6 段，第一段是秒：每 10 秒 |
| `@hourly`、`@daily`、`@weekly`、`@monthly`、`@everysecond` | 预定义写法，`@daily` 即 `0 0 * * *` |
| `@every 15s`、`@every 90s` | 固定间隔，至少 1 秒，必须是整秒 |

cron 表达式的完整语法（包括 `L`、`W`、`#` 等）见 [gronx](https://github.com/adhocore/gronx#cron-expression)。`@every` 按 Unix 时间对齐，例如 `@every 15s` 总是在第 0、15、30、45 秒触发，与进程何时启动无关，因此所有实例算出的触发时刻都一样。

表达式不合法、名称重复或过长时，`Add` 直接 panic，应用在启动时就会发现。

<a id="timezones"></a>
### 时区

cron 表达式按 `time.Local` 计算，Go 程序从环境变量 `TZ` 读取它，例如 `TZ=Asia/Shanghai`。容器镜像通常是 UTC，单个任务需要按其他时区计算时，使用 `schedule.In`：

```go
shanghai, err := time.LoadLocation("Asia/Shanghai")
// ...
sched.Add("reports:daily", "0 3 * * *", reports.Generate, schedule.In(shanghai))
```

<a id="running-the-scheduler"></a>
## 运行调度器

调度器是 `App` 的一个运行部分（见[依赖装配](dependencies.md#assembling-the-application)）。下面是博客应用完整的 `internal/bootstrap/scheduler.go`，`a.locker` 是 `open` 中创建的[数据库锁](locks.md#database-store)：

```go
package bootstrap

import (
    "context"
    "time"

    "example.com/blog/internal/article"
    "example.com/blog/internal/settings"
    "github.com/ryan053857/mygoravel/lock"
    "github.com/ryan053857/mygoravel/queue"
    "github.com/ryan053857/mygoravel/schedule"
    "github.com/ryan053857/mygoravel/session"
)

// Scheduler 按时执行定时任务，直到 ctx 被取消。
func (a *App) Scheduler(ctx context.Context) error {
    sched := schedule.New(a.locker, nil)
    sched.Add("articles:publish-scheduled", "* * * * *", article.NewPublishScheduled(a.db).Handle)
    sched.Add("sessions:cleanup", "*/10 * * * *", schedule.Cleanup(session.NewStore(a.db).Cleanup, 1000))
    sched.Add("locks:cleanup", "@hourly", schedule.Cleanup(lock.NewDBStore(a.db).Cleanup, 1000))
    sched.Add("queue:prune-failed", "@daily", func(ctx context.Context) error {
        _, err := queue.NewFailedJobs(a.db).Cleanup(ctx, time.Now().AddDate(0, 0, -30), 1000)
        return err
    })
    return sched.Run(ctx)
}

// Schedule 只运行调度器，schedule 命令调用它。
func Schedule(ctx context.Context, cfg settings.Config) error {
    return run(ctx, cfg, (*App).Scheduler)
}
```

`Run` 每秒检查一次到期的任务，每个任务在自己的 goroutine 中执行，一个任务执行很久不会耽误其他任务按时触发。

在根命令中注册 `schedule` 命令；`app.go` 中的 `Start` 已经包含调度器，见[在一个进程中运行多个部分](commands.md#running-together)：

```go
root.AddCommand(command.Run("schedule", "执行定时任务", bootstrap.Schedule))
```

<a id="multiple-instances"></a>
## 多个实例

部署多个实例时，每个实例上的调度器都会在同一时刻触发同一个任务。调度器在执行之前取得一把锁，锁名包含任务名和触发时刻，例如 `schedule:sessions:cleanup:1794366000`，只有取得锁的实例执行这一次触发。锁不会主动释放，一分钟后过期，所以各实例的时钟偏差小于一分钟时就不会重复执行。

去重的范围取决于锁存储：

- 单实例部署使用 `lock.NewMemoryStore()`；
- 多实例部署使用 `lock.NewDBStore(db)` 或 [Redis 存储](locks.md#redis-store)，所有实例共用同一个存储。

使用数据库存储时，每次触发都会在 `locks` 表中留下一行，由上面的 `locks:cleanup` 定期删除。秒级任务每秒都会写一次 `locks` 表，这类任务较多时建议改用 Redis 存储，过期的键由 Redis 自动删除。

只想在每个实例上各执行一次的任务，例如清理本机的临时目录，使用 `schedule.EveryInstance()`：

```go
sched.Add("tmp:cleanup", "@hourly", cleanTmpDir, schedule.EveryInstance())
```

<a id="preventing-overlaps"></a>
## 防止重叠

任务上一次还没结束时，下一次触发会被跳过并记录一条 `info` 日志，不论上一次在哪个实例上执行。调度器在任务执行期间持有锁 `schedule:<任务名>:running`，每 20 秒续期一次，持有它的实例崩溃时，锁在一分钟内过期。

确实需要并行执行的任务，使用 `schedule.AllowOverlap()`。

<a id="failures-and-shutdown"></a>
## 失败与停止

任务返回错误或 panic 时，调度器记录一条 `error` 日志，下一次照常触发，不会重试。需要重试的工作，在任务中把它[发布到队列](queue.md#publishing-jobs)：

```go
sched.Add("reports:daily", "0 3 * * *", func(ctx context.Context) error {
    return queue.Publish(ctx, queue.Database(a.db), "reports.generate", time.Now().AddDate(0, 0, -1))
})
```

队列负责重试、记录失败，耗时的工作也不会占用调度器所在的进程。

调度器的日志带有 `component=schedule` 字段，任务的 `ctx` 中带有 `task` 字段，用 `slog.InfoContext(ctx, ...)` 记录的日志会自动带上它。

调度器不补跑错过的触发：进程暂停或调度落后不到一分钟时，错过的触发会补上，同一个任务只补一次；部署、重启期间错过的触发直接跳过。收到停止信号后，调度器不再触发新任务，等待执行中的任务结束，最多等待 30 秒，超时后取消它们的 `ctx`。

<a id="framework-cleanups"></a>
## 清理框架数据

框架中保存在数据库里的数据都提供 `Cleanup(ctx, limit)` 方法，每次最多删除 `limit` 条过期的行。`schedule.Cleanup` 把它包装成任务：反复调用，直到一次删除的数量少于 `limit`。

| 数据 | 清理方法 | 建议频率 |
| --- | --- | --- |
| [会话](session.md#cleanup) | `session.NewStore(db).Cleanup` | 每 10 分钟 |
| [锁](locks.md#cleanup) | `lock.NewDBStore(db).Cleanup` | 每小时 |
| [广播](broadcasting.md#database-backend) | `broadcast.NewDatabase(db, nil).Cleanup` | 每 10 分钟 |
| [限流](rate-limiting.md#cleanup) | 任意一个 `ratelimit.NewDatabase(...)` 的 `Cleanup` | 每小时 |
| [文件](filesystem.md#cleanup) | 文件服务的 `Cleanup` | 每 5 分钟 |
| [操作日志](activity.md#retention) | `activity.Cleanup(db, 保留期)` | 每天 |
| [失败任务](queue.md#failed-jobs) | `queue.NewFailedJobs(db).Cleanup(ctx, before, limit)` | 每天 |

使用 Redis 存储时过期的数据由 Redis 自动删除，不需要注册清理任务。

<a id="testing"></a>
## 测试

`RunDue` 执行在指定时刻到期的任务，等它们结束后返回合并的错误，用来确定性地验证调度配置：

```go
func TestDailyReport(t *testing.T) {
    sched := schedule.New(lock.New(lock.NewMemoryStore()), nil)
    var ran bool
    sched.Add("reports:daily", "0 3 * * *", func(context.Context) error {
        ran = true
        return nil
    }, schedule.In(time.UTC))

    sched.RunDue(ctx, time.Date(2026, 11, 11, 2, 59, 0, 0, time.UTC))
    if ran {
        t.Fatal("ran before 03:00")
    }
    if err := sched.RunDue(ctx, time.Date(2026, 11, 11, 3, 0, 0, 0, time.UTC)); err != nil || !ran {
        t.Fatalf("ran=%v err=%v", ran, err)
    }
}
```

任务本身是普通的函数，直接调用它测试即可。
