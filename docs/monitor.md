# 运行统计

- [简介](#introduction)
- [装配](#assembling)
- [队列、定时任务和缓存](#components)
- [记录](#recording)
    - [按分钟汇总](#aggregates)
    - [最新值](#latest-values)
- [查询](#querying)
- [清理](#cleanup)
- [测试](#testing)

<a id="introduction"></a>
## 简介

「最近一小时发了多少封邮件、平均多久」「汇率上一次同步是什么时候、结果如何」，这类数据要在命令和后台页面中查看。`monitor` 包把它们保存在应用的数据库中：部署多个实例时，各实例的记录汇总在一起；任何进程都能查询，包括刚启动、自己什么也没记录过的命令。

它与[可观测性](telemetry.md)中的指标互不依赖。指标推送到监控系统，用于告警和长期保存；运行统计留在应用自己的数据库中，用于应用内查看，默认保留 7 天。两者可以同时使用。

<a id="assembling"></a>
## 装配

先发布两张表的迁移，见[发布框架迁移](migrations.md#publishing-framework-migrations)：

```go
migrationCmd.AddCommand(migratecmd.Publish(
    migrate.Source{Name: "monitor", FS: monitor.Migrations()},
))
```

然后在[入口](dependencies.md#assembling-the-application)的 `open` 中，打开数据库之后创建 `Monitor`，保存为 `App` 的 `monitor` 字段：

```go
mon := monitor.New(db, nil)
stack.MustAdd("monitor", mon.Close)
```

记录只修改内存，每 10 秒批量写入一次；查询直接读数据库。`Close` 写入剩余的记录，它登记在数据库之后，所以会在关闭数据库之前执行。

写入失败时，这一批记录被丢弃，并记录一条 `error` 日志（`component=monitor`）：统计只用于查看，不值得为它阻塞业务或无限积压。

<a id="components"></a>
## 队列、定时任务和缓存

框架的组件把自己的运行统计记录到同一个 `Monitor`，各自提供查询函数和命令，与队列驱动、缓存后端无关：

| 组件 | 记录 | 查询 | 命令 |
| --- | --- | --- | --- |
| [队列](queue.md#monitoring) | `queue.WithMonitor(a.monitor)`：每个 topic 的处理次数、耗时、等待时长 | `queue.Activity` | `queue stats` |
| [定时任务](scheduling.md#run-history) | `schedule.WithMonitor(a.monitor)`：每个任务的执行次数、耗时、上一次的结果 | `schedule.Activity`、`schedule.LastRuns` | `schedules` |
| [缓存](cache.md#hit-rate) | `cachemon.Wrap(name, c, a.monitor)`：命中、未命中、出错次数和读取耗时 | `cachemon.Activity` | `cache stats` |

它们使用的 metric 以 `queue.`、`schedule.`、`cache.` 开头，常量在各自的包中，例如 `queue.MetricSucceeded`，画趋势图时传给 `Series`。应用自己的记录用自己模块的名字开头，不会与它们冲突。

<a id="recording"></a>
## 记录

每条记录属于一个 metric 和其中的一个 label。metric 是记录的种类，用点分隔、以模块名开头，例如 `mail.sent`，最长 100 字节；label 是其中的一项，例如邮件类型。label 的取值要有限，例如类型、topic、任务名，不要用用户 ID、文章 ID 这类不断增长的值：每个 label 每分钟一行，取值越多，表越大。超过 255 字节的 label 以 SHA-256 摘要保存。

<a id="aggregates"></a>
### 按分钟汇总

`Record` 把一个值计入当前这一分钟的汇总：次数加 1，值计入总和与最大值；`RecordDuration` 记录耗时。下面在发送邮件的 action 中记录每类邮件的发送耗时和失败次数：

```go
started := time.Now()
err := a.mailer.Send(ctx, msg)
if err == nil {
    a.monitor.RecordDuration("mail.sent", msg.Kind, time.Since(started))
} else {
    a.monitor.Record("mail.failed", msg.Kind, 1)
}
```

只关心次数时值传 1。记录只修改内存，可以在任何热路径上调用。

<a id="latest-values"></a>
### 最新值

`Set` 只保留每个 label 最新的值，例如一个任务上一次的结果。值编码为 JSON 保存：

```go
type syncResult struct {
    Rates int    `json:"rates"`
    Error string `json:"error,omitempty"`
}

a.monitor.Set("rates.sync", "usd", syncResult{Rates: len(rates)})
```

多个实例先后记录同一个 label 时，以记录时间最晚的为准，与写入数据库的先后无关。

<a id="querying"></a>
## 查询

`Totals` 返回一段时间内每个 metric、每个 label 的汇总，一次可以查询几个 metric，按 label、metric 排序。下面的命令列出最近一小时各类邮件的发送情况：

```go
now := time.Now()
totals, err := a.monitor.Totals(ctx, now.Add(-time.Hour), now, "mail.sent", "mail.failed")
for _, t := range totals {
    switch t.Metric {
    case "mail.sent":
        fmt.Printf("%s  发送 %d 封  平均 %s  最长 %s\n", t.Label, t.Count, t.MeanDuration(), t.MaxDuration())
    case "mail.failed":
        fmt.Printf("%s  失败 %d 封\n", t.Label, t.Count)
    }
}
```

每个汇总是一个 `Aggregate`：`Count`、`Sum`、`Max` 是次数、总和与最大值，`Mean` 是平均值，`MeanDuration` 和 `MaxDuration` 读取 `RecordDuration` 记录的耗时，`Add` 把两个汇总合在一起。

`Series` 返回一个 label 按时间分段的汇总，用来在后台页面上画趋势图。下面按小时分段，没有记录的小时为零值，可以直接画出来：

```go
points, err := a.monitor.Series(ctx, "mail.sent", "welcome", now.Add(-24*time.Hour), now, time.Hour)
// points[i].At 是整点，points[i].Count、MeanDuration() 是这一小时的汇总
```

`Latest` 返回一个 metric 每个 label 最新的值，`Decode` 把它解码回原来的类型：

```go
values, err := a.monitor.Latest(ctx, "rates.sync")
for _, v := range values {
    var r syncResult
    if err := v.Decode(&r); err != nil {
        return err
    }
    // v.Label、v.At 是记录的时间
}
```

汇总的最小单位是分钟：计入从 `from` 所在的那一分钟起、在 `to` 之前开始的每一分钟，分段按 Unix 时间对齐。还在内存中、最近 10 秒内的记录查询不到。

<a id="cleanup"></a>
## 清理

按分钟汇总的记录每分钟每个 label 一行，要定期清理。`Cleanup` 返回清理函数，交给[定时任务](scheduling.md#running-the-scheduler)：

```go
sched.Add("monitor:cleanup", "@hourly", schedule.Cleanup(a.monitor.Cleanup(7*24*time.Hour), 1000),
    schedule.Describe("删除 7 天前的运行统计"))
```

最新值每个 label 只有一行，不随时间增长，不会被清理。

<a id="testing"></a>
## 测试

用 [`dbtest.Open`](testing.md#database-tests) 打开执行过迁移的测试数据库。`Monitor` 定期写入，测试中调用 `Flush` 立即写入，再查询结果：

```go
mon := monitor.New(dbtest.Open(t, migrations.FS), nil)
t.Cleanup(func() { mon.Close(context.Background()) })

// ... 执行发送邮件的 action

if err := mon.Flush(ctx); err != nil {
    t.Fatal(err)
}
totals, err := mon.Totals(ctx, time.Now().Add(-time.Minute), time.Now().Add(time.Minute), "mail.sent")
```
