# 定时任务

- [简介](#introduction)
- [定义任务](#defining-tasks)
    - [表达式](#expressions)
    - [时区](#timezones)
- [运行调度器](#running-the-scheduler)
- [多个实例](#multiple-instances)
- [防止重叠](#preventing-overlaps)
- [失败与停止](#failures-and-shutdown)
- [查看任务](#listing-tasks)
    - [列出任务的命令](#schedules-command)
    - [执行记录](#run-history)
    - [后台页面](#admin-pages)
    - [手动执行](#running-manually)
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
sched := schedule.New(locker, cfg.Schedule, nil)
sched.Add("articles:publish-scheduled", "* * * * *", article.NewPublishScheduled(db).Handle)
```

任务通常是 [action](actions.md#as-scheduled-tasks) 的 `Handle` 方法：发布到期文章的 `PublishScheduled` 不需要参数，它的 `Handle` 正好是任务的签名。`schedule.Describe` 为任务加一句说明，在[查看任务](#listing-tasks)时展示。

`locker` 是[原子锁](locks.md)，调度器用它在多个实例之间去重、防止任务重叠，见[多个实例](#multiple-instances)。第二个参数是配置段，见[失败与停止](#failures-and-shutdown)；第三个参数是日志器，`nil` 时使用 `slog.Default()`。

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

调度器是 `App` 的一个运行部分（见[依赖装配](dependencies.md#assembling-the-application)）。下面是博客应用完整的 `internal/bootstrap/scheduler.go`，`a.locker` 是 `open` 中创建的[数据库锁](locks.md#database-store)。注册任务单独写在 `tasks` 中，调度器运行它，[查看任务](#listing-tasks)的命令和后台页面也用它，任务只定义一次：

```go
package bootstrap

import (
    "context"
    "time"

    "example.com/blog/internal/article"
    "example.com/blog/internal/settings"
    "github.com/ryan053857/mygoravel/command"
    "github.com/ryan053857/mygoravel/lock"
    "github.com/ryan053857/mygoravel/queue"
    "github.com/ryan053857/mygoravel/schedule"
    "github.com/ryan053857/mygoravel/session"
)

// tasks 创建调度器并注册全部定时任务。
func (a *App) tasks(opts ...schedule.SchedulerOption) *schedule.Scheduler {
    sched := schedule.New(a.locker, a.cfg.Schedule, nil, opts...)
    sched.Add("articles:publish-scheduled", "* * * * *", article.NewPublishScheduled(a.db).Handle,
        schedule.Describe("发布到了预定时间的文章"))
    sched.Add("sessions:cleanup", "*/10 * * * *", schedule.Cleanup(session.NewStore(a.db).Cleanup, 1000),
        schedule.Describe("删除过期的会话"))
    sched.Add("locks:cleanup", "@hourly", schedule.Cleanup(lock.NewDBStore(a.db).Cleanup, 1000),
        schedule.Describe("删除过期的锁"))
    sched.Add("queue:prune-failed", "@daily", func(ctx context.Context) error {
        _, err := queue.NewFailedJobs(a.db).Cleanup(ctx, time.Now().AddDate(0, 0, -30), 1000)
        return err
    }, schedule.Describe("删除 30 天前的失败任务"))
    return sched
}

// Scheduler 装配调度器，返回的函数按时执行定时任务，直到 ctx 被取消。
func (a *App) Scheduler(*command.Daemon) (func(context.Context) error, error) {
    return a.tasks().Run, nil
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

- 单实例部署可以使用 `lock.NewMemoryStore()`；
- 多实例部署，以及使用[不停机升级](deployment.md#upgrade-overlap)时，使用 `lock.NewDBStore(db)` 或 [Redis 存储](locks.md#redis-store)，所有进程共用同一个存储。

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

调度器的日志带有 `component=schedule` 字段，任务的 `ctx` 中带有 `task` 字段，用 `slog.InfoContext(ctx, ...)` 记录的日志会自动带上它。传入 `schedule.WithMeterProvider(a.meters)` 时，调度器还会报告每次执行的耗时、结果和被跳过的次数，用来发现长时间没有成功执行的任务，见[指标与链路追踪](telemetry.md#schedule-metrics)；传入 `schedule.WithTracerProvider(a.tracers)` 时，每次执行是一条链路的起点。

调度器不补跑错过的触发：进程暂停或调度落后不到一分钟时，错过的触发会补上，同一个任务只补一次；部署、重启期间错过的触发直接跳过。收到停止信号后，调度器不再触发新任务，等待执行中的任务结束，最多等待 `shutdown_timeout`，超时后取消它们的 `ctx`。这个时间在应用配置的调度段中设置：

```go
type Config struct {
    // ...
    Schedule schedule.Config `yaml:"schedule"`
}
```

```yaml
schedule:
  shutdown_timeout: 30s # 默认值；为 0 时立即取消执行中的任务
```

<a id="listing-tasks"></a>
## 查看任务

`Tasks` 返回注册的全部任务：名称、说明、表达式、时区和选项。它只读取任务的定义，不需要运行调度器，所以在任何进程中都可以调用：

```go
for _, t := range a.tasks().Tasks() {
    t.Name, t.Description, t.Expr  // "reports:daily"、"生成前一天的报表"、"0 3 * * *"
    t.Next(time.Now())             // 下一次执行的时间
    t.Prev(time.Now())             // 上一次应当执行的时间
}
```

`Next` 和 `Prev` 按表达式计算，结果与调度器实际触发的时刻一致；`@every` 按 Unix 时间对齐，与调度器何时启动无关。

`Running` 报告任务此刻是否正在执行，在任何一个实例上都算。它查询任务执行期间持有的锁 `schedule:<任务名>:running`，所以同样不需要运行调度器，但要和调度器共用锁存储：

```go
running, err := sched.Running(ctx, "reports:daily")
```

使用 `EveryInstance` 或 `AllowOverlap` 的任务不在锁存储中登记执行状态，`Running` 返回 `schedule.ErrNotTracked`。

<a id="schedules-command"></a>
### 列出任务的命令

下面是博客应用的 `internal/bootstrap/schedules.go`，它在 [bootstrap.Commands](actions.md#as-commands) 中声明，执行 `./bin/api schedules`：

```go
package bootstrap

import (
    "errors"
    "fmt"
    "text/tabwriter"
    "time"

    "github.com/spf13/cobra"

    "github.com/ryan053857/mygoravel/command"
    "github.com/ryan053857/mygoravel/schedule"
)

// schedulesCmd 列出定时任务、下一次执行的时间和是否正在执行。
func schedulesCmd(a *App) *cobra.Command {
    return &cobra.Command{
        Use:   "schedules",
        Short: "列出定时任务",
        Args:  command.NoArgs,
        RunE: func(cmd *cobra.Command, _ []string) error {
            sched, now := a.tasks(), time.Now()
            w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
            fmt.Fprintln(w, "TASK\tEXPRESSION\tNEXT RUN\tIN\tRUNNING\tDESCRIPTION")
            for _, t := range sched.Tasks() {
                running, err := sched.Running(cmd.Context(), t.Name)
                state := "no"
                switch {
                case errors.Is(err, schedule.ErrNotTracked):
                    state = "-"
                case err != nil:
                    return err
                case running:
                    state = "yes"
                }
                next := t.Next(now)
                fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", t.Name, t.Expr, next.Format("2006-01-02 15:04:05"),
                    next.Sub(now).Round(time.Second), state, t.Description)
            }
            return w.Flush()
        },
    }
}
```

```text
TASK                        EXPRESSION    NEXT RUN             IN      RUNNING  DESCRIPTION
articles:publish-scheduled  * * * * *     2026-11-11 17:01:00  42s     no       发布到了预定时间的文章
sessions:cleanup            */10 * * * *  2026-11-11 17:10:00  9m42s   no       删除过期的会话
locks:cleanup               @hourly       2026-11-11 18:00:00  59m42s  no       删除过期的锁
queue:prune-failed          @daily        2026-11-12 00:00:00  6h59m   no       删除 30 天前的失败任务
```

<a id="run-history"></a>
### 执行记录

上一次什么时候执行、结果如何，是运行时的数据：部署多个实例时，每次触发在哪个实例上执行并不确定。`schedule.WithObserver` 在每次执行时通知你，把它写进数据库，所有进程就都能看到：

```go
func (a *App) Scheduler(*command.Daemon) (func(context.Context) error, error) {
    return a.tasks(schedule.WithObserver(a.recordRun)).Run, nil
}

func (a *App) recordRun(ctx context.Context, e schedule.Event) {
    if e.Kind == schedule.TaskStarted {
        return
    }
    _, err := a.db.NewInsert().Model(&ScheduleRun{
        Task: e.Task, Tick: e.Tick, Status: e.Kind.String(), Duration: e.Duration,
        SkipReason: string(e.SkipReason), Error: errorText(e.Err),
    }).Exec(ctx)
    if err != nil {
        slog.ErrorContext(ctx, "record schedule run failed", "error", err)
    }
}
```

`ScheduleRun` 是应用自己的 `schedule_runs` 表，保留多久由应用决定，用一个定时任务清理。

| 事件 | 时机 |
| --- | --- |
| `TaskStarted` | 本实例开始执行 |
| `TaskSucceeded` | 执行完成 |
| `TaskFailed` | 返回错误或 panic，`Err` 是原因 |
| `TaskSkipped` | 应当在本实例执行却被跳过，`SkipReason` 是 `overlap`（上一次还没结束）或 `lock_failed`（锁存储出错） |

`Tick` 是这次触发的时刻，即任务应当执行的时间；`Started` 是实际开始的时间，两者之差是调度延迟。由其他实例执行的触发不报告。结束事件在释放执行锁之后发出，Observer 收到它时 `Running` 已经返回 `false`。Observer 在执行任务的 goroutine 中同步调用，写一行数据库很快，更重的工作请缓冲后批量处理。

<a id="admin-pages"></a>
### 后台页面

后台页面由 HTTP 进程提供，它同样调用 `a.tasks()` 拿到任务的定义，再查询执行记录：

```go
for _, t := range a.tasks().Tasks() {
    var last ScheduleRun
    err := a.db.NewSelect().Model(&last).Where("task = ?", t.Name).OrderExpr("tick DESC").Limit(1).Scan(ctx)
    // ... 没有记录时 err 是 sql.ErrNoRows

    // 应当执行的时间过去了一分钟，却没有这次的记录，也没有正在执行：这次触发丢了，
    // 例如调度器停止或部署期间。
    prev := t.Prev(time.Now())
    running, _ := sched.Running(ctx, t.Name)
    missed := prev.After(last.Tick) && time.Since(prev) > time.Minute && !running
}
```

页面上可以展示每个任务的说明、表达式、下一次执行的时间、上一次的结果和耗时，并把错过的触发标出来。

<a id="running-manually"></a>
### 手动执行

后台页面上的「立即执行」不要在 HTTP 进程中直接调用任务：耗时的任务会占用请求，HTTP 实例也可能有很多个。把它[发布到队列](queue.md#publishing-jobs)，由 worker 执行：

```go
// 后台页面：
err := queue.Publish(ctx, queue.Database(a.db), "articles.publish-scheduled", struct{}{})

// Worker 中：
queue.Handle(router, sub, "articles.publish-scheduled", func(ctx context.Context, _ struct{}) error {
    return article.NewPublishScheduled(a.db).Handle(ctx)
})
```

任务和队列处理函数调用的是同一个 action，手动执行和定时执行的效果相同。手动执行不受[防止重叠](#preventing-overlaps)的限制，需要互斥的任务在 action 中自己使用[原子锁](locks.md)。

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
| [通知](notifications.md#retention) | `notification.Cleanup(db, 保留期)` | 每天 |
| [导入](import-export.md#cleanup) | `imports.New(...)` 的 `Cleanup(保留期)` | 每天 |
| [导出](import-export.md#export-progress) | `exports.New(...)` 的 `Cleanup(保留期)` | 每天 |
| [失败任务](queue.md#failed-jobs) | `queue.NewFailedJobs(db).Cleanup(ctx, before, limit)` | 每天 |

使用 Redis 存储时过期的数据由 Redis 自动删除，不需要注册清理任务。

<a id="testing"></a>
## 测试

`RunDue` 执行在指定时刻到期的任务，等它们结束后返回合并的错误，用来确定性地验证调度配置：

```go
func TestDailyReport(t *testing.T) {
    sched := schedule.New(lock.New(lock.NewMemoryStore()), schedule.DefaultConfig(), nil)
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
