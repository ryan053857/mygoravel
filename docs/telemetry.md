# 指标与链路追踪

- [简介](#introduction)
- [指标](#metrics)
    - [创建 MeterProvider](#meter-provider)
    - [HTTP 请求](#http-metrics)
    - [队列](#queue-metrics)
    - [定时任务](#schedule-metrics)
    - [数据库](#database-metrics)
    - [告警示例](#alerting)
- [链路追踪](#tracing)
    - [创建 TracerProvider](#tracer-provider)
    - [HTTP 请求](#http-tracing)
    - [队列](#queue-tracing)
    - [定时任务](#schedule-tracing)
    - [数据库](#database-tracing)
    - [日志](#trace-logs)

<a id="introduction"></a>
## 简介

框架的 HTTP 服务、队列和调度器都可以报告 [OpenTelemetry](https://opentelemetry.io/docs/languages/go/) 指标和链路，数据库的指标和链路由 Bun 自带的扩展报告。框架只依赖 OpenTelemetry 的接口，数据导出到哪里由应用决定：Prometheus、Jaeger、Grafana、Datadog 等都能接收。

和其他功能一样，两者都需要显式开启：把同一个 `MeterProvider`、`TracerProvider` 交给需要报告的部分即可，没有传入的部分不报告。

<a id="metrics"></a>
## 指标

<a id="meter-provider"></a>
### 创建 MeterProvider

在[入口](dependencies.md#assembling-the-application)的 `open` 中创建 `MeterProvider`，登记到 `cleanup.Stack`，并保存在 `App` 中。下面用 OTLP 定期把指标推送出去：

```go
import (
    "go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
    sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

exporter, err := otlpmetrichttp.New(ctx)
if err != nil {
    return nil, err
}
meters := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter)))
stack.MustAdd("metrics", meters.Shutdown)
```

接收地址和服务名来自 OpenTelemetry 的标准环境变量，例如 `OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4318`、`OTEL_SERVICE_NAME=blog`。Prometheus 3 可以直接接收 OTLP。

推送的好处是 worker、调度器这些不提供 HTTP 服务的进程也能报告指标。如果要让 Prometheus 来抓取，改用 `go.opentelemetry.io/otel/exporters/prometheus` 作为 Reader，每个进程都需要提供 `/metrics`。

<a id="http-metrics"></a>
### HTTP 请求

`httpx.Metrics` 是一个中间件，安装在 `httpx.Defaults` 之前，才能记录 panic 转换成的 `500`：

```go
router.Use(httpx.Metrics(a.meters))
router.Use(httpx.Defaults(a.cfg.HTTP, nil, nil)...)
```

| 指标 | 类型 | 属性 |
| --- | --- | --- |
| `http.server.request.duration` | 直方图，秒 | `http.request.method`、`http.response.status_code`、`http.route`、`url.scheme`；5xx 时还有 `error.type` |

属性遵循 OpenTelemetry 的 [HTTP 语义约定](https://opentelemetry.io/docs/specs/semconv/http/http-metrics/)，现成的 HTTP 仪表盘可以直接使用。`http.route` 是路由模板，例如 `/articles/{id}`；没有匹配到路由的请求不带这个属性，非标准的请求方法记为 `_OTHER`，客户端无法通过构造路径或方法制造大量的指标序列。

<a id="queue-metrics"></a>
### 队列

```go
sub := queue.NewSubscriber(queue.Database(a.db), a.cfg.Queue, nil, queue.WithMeterProvider(a.meters))
```

订阅者报告每个任务的等待时长 `queue.job.wait` 和处理耗时 `queue.job.duration`；`queue.ObserveJobs` 报告队列中各状态的任务数 `queue.jobs` 和积压时长 `queue.job.oldest_ready_age`。各指标的含义和注册位置见[队列的指标](queue.md#metrics)。

<a id="schedule-metrics"></a>
### 定时任务

```go
sched := schedule.New(a.locker, a.cfg.Schedule, nil, schedule.WithMeterProvider(a.meters))
```

| 指标 | 类型 | 属性 | 含义 |
| --- | --- | --- | --- |
| `schedule.task.duration` | 直方图，秒 | `schedule.task`、`schedule.task.outcome` | 执行耗时，结果为 `succeeded` 或 `failed`（返回错误或 panic） |
| `schedule.task.skipped` | 计数器 | `schedule.task`、`schedule.task.skip_reason` | 应当执行却被跳过的次数，原因为 `overlap`（上一次还没结束）或 `lock_failed`（锁存储出错） |

部署多个实例时，每次触发只在执行它的实例上记录；由其他实例执行的触发不算跳过。

<a id="database-metrics"></a>
### 数据库

数据库的指标不需要框架提供，Bun 自带的 [bunotel](https://pkg.go.dev/github.com/uptrace/bun/extra/bunotel) 就能报告。它是一个单独的模块：

```shell
go get github.com/uptrace/bun/extra/bunotel
```

在 `open` 中打开数据库之后，给它加上查询钩子：

```go
import "github.com/uptrace/bun/extra/bunotel"

db.AddQueryHook(bunotel.NewQueryHook(bunotel.WithMeterProvider(meters)))
```

最有用的是**连接池**的指标，它们每次导出时从 `db.Stats()` 读取，带有 `db.system` 属性：

| 指标 | 类型 | 含义 |
| --- | --- | --- |
| `go.sql.connections_in_use` | 仪表 | 正在使用的连接数 |
| `go.sql.connections_max_open` | 仪表 | 连接数上限，即配置中的 [`max_open_conns`](database.md#configuration) |
| `go.sql.connections_wait_count` | 计数器 | 因为没有空闲连接而等待的总次数 |
| `go.sql.connections_wait_duration` | 计数器，纳秒 | 等待连接的总时长 |
| `go.sql.connections_open`、`go.sql.connections_idle` | 仪表 | 已建立的连接数、其中空闲的连接数 |

数据库变慢时，最先出现的往往不是查询报错，而是请求开始排队等连接：慢查询或忘记提交的事务占住连接，连接池用满，后面的请求都在 `database/sql` 中等待。`wait_count` 开始增长就是这个信号。

每条查询的耗时记录在直方图 `go.sql.query_timing` 中，属性是 `db.operation`（`SELECT`、`INSERT` 等）和 `db.sql.table`。它适合发现慢查询，但有几点限制：

- 以**整毫秒**记录，1 毫秒以内的查询都记为 0，看不出细微的变化；
- 不区分成功和失败；
- 指标名和属性名来自旧版的语义约定，现成的数据库仪表盘需要调整查询。

同一个钩子还可以报告链路，见[数据库的链路](#database-tracing)。

<a id="alerting"></a>
### 告警示例

导出到 Prometheus 后，指标名中的点换成下划线，单位成为后缀，例如 `http_server_request_duration_seconds`：

```promql
# 5 分钟内 5xx 的比例超过 1%
sum(rate(http_server_request_duration_seconds_count{http_response_status_code=~"5.."}[5m]))
  / sum(rate(http_server_request_duration_seconds_count[5m])) > 0.01

# 队列积压超过 5 分钟
max by (queue_topic) (queue_job_oldest_ready_age_seconds) > 300

# 有请求在排队等数据库连接
rate(go_sql_connections_wait_count_total[5m]) > 0

# 每天凌晨的报表任务 25 小时内没有成功过
increase(schedule_task_duration_seconds_count{schedule_task="reports:daily", schedule_task_outcome="succeeded"}[25h]) == 0
```

<a id="tracing"></a>
## 链路追踪

一个请求发布了任务、任务又调用了其他服务时，链路追踪把它们串成一条链路，可以看到每一步的耗时和在哪一步出错。

<a id="tracer-provider"></a>
### 创建 TracerProvider

和 `MeterProvider` 一样在 `open` 中创建，保存在 `App` 中：

```go
import (
    "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
    sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

traceExporter, err := otlptracehttp.New(ctx)
if err != nil {
    return nil, err
}
tracers := sdktrace.NewTracerProvider(sdktrace.WithBatcher(traceExporter))
stack.MustAdd("tracing", tracers.Shutdown)
```

接收地址、服务名和采样率同样来自标准环境变量，例如 `OTEL_TRACES_SAMPLER=parentbased_traceidratio`、`OTEL_TRACES_SAMPLER_ARG=0.1` 只记录 10% 的链路。

框架按 [W3C Trace Context](https://www.w3.org/TR/trace-context/) 读写链路，不需要设置全局的 propagator。

<a id="http-tracing"></a>
### HTTP 请求

`httpx.Tracing` 为每个请求创建一个服务端 span，名称是方法和路由模板，例如 `GET /articles/{id}`。它安装在最外层，访问日志才能带上 `trace_id`：

```go
router.Use(httpx.Tracing(a.tracers), httpx.Metrics(a.meters))
router.Use(httpx.Defaults(a.cfg.HTTP, nil, nil)...)
```

请求头带有 `traceparent` 时（例如来自前端或另一个服务），span 接在调用方的链路之后。`5xx` 的请求标记为错误。处理器从请求的 `ctx` 中拿到这个 span，用 `tracers.Tracer(...)` 创建的 span 都会接在它下面。

<a id="queue-tracing"></a>
### 队列

`queue.Publish` 把 `ctx` 中的链路写入任务的元数据（`traceparent`），不需要任何设置。订阅者传入 `queue.WithTracerProvider` 后，为每次执行创建一个 `process <topic>` span，接在发布任务的 span 之后：

```go
sub := queue.NewSubscriber(queue.Database(a.db), a.cfg.Queue, nil,
    queue.WithMeterProvider(a.meters), queue.WithTracerProvider(a.tracers))
```

这样，发布文章的请求和随后发送通知邮件的任务出现在同一条链路中，中间隔了多久、邮件为什么失败都能看到。每次重试是一个新的 span，失败、重试和租约丢失时标记为错误。

<a id="schedule-tracing"></a>
### 定时任务

```go
sched := schedule.New(a.locker, a.cfg.Schedule, nil,
    schedule.WithMeterProvider(a.meters), schedule.WithTracerProvider(a.tracers))
```

每次执行创建一个 `schedule <任务名>` span，是一条新链路的起点，任务中发布的队列任务接在它之后。被跳过或由其他实例执行的触发没有 span。

<a id="database-tracing"></a>
### 数据库

把 `TracerProvider` 也交给[数据库指标](#database-metrics)中的钩子，每条查询就成为一个 span，接在请求或任务的 span 下面，一个请求慢在哪条 SQL 上一目了然：

```go
db.AddQueryHook(bunotel.NewQueryHook(
    bunotel.WithMeterProvider(meters),
    bunotel.WithTracerProvider(childSpans{tracers}),
))
```

span 的名称是操作（如 `SELECT`），带有 SQL 语句和发起查询的代码位置。语句中的参数保持为占位符，例如 `WHERE (id = ?)`，用户数据不会进入链路。

不要直接传入 `tracers`。bunotel 为每条查询都创建 span，不管 `ctx` 中有没有上级 span；而队列轮询、调度器加锁这些后台查询不属于任何请求，每秒都会各自成为一条只有一个 span 的链路，淹没真正有用的数据。`childSpans` 只在已有上级 span 时创建：

```go
// childSpans 只为已在链路中的查询创建 span，后台查询不产生孤立的链路。
type childSpans struct{ trace.TracerProvider }

func (p childSpans) Tracer(name string, opts ...trace.TracerOption) trace.Tracer {
    return childTracer{p.TracerProvider.Tracer(name, opts...)}
}

type childTracer struct{ trace.Tracer }

func (t childTracer) Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
    if !trace.SpanContextFromContext(ctx).IsValid() {
        return ctx, trace.SpanFromContext(ctx) // 不记录的空 span
    }
    return t.Tracer.Start(ctx, name, opts...)
}
```

`trace` 是 `go.opentelemetry.io/otel/trace`。查询要接在请求的链路上，前提是把请求的 `ctx` 一路传给 Bun 的 `Scan`、`Exec`；用 `context.Background()` 发起的查询不会出现在链路中。

<a id="trace-logs"></a>
### 日志

`ctx` 中有 span 时，用它记录的日志自动带上 `trace_id` 和 `span_id`，包括访问日志、队列和调度器的日志，以及处理函数里 `slog.InfoContext(ctx, ...)` 记录的业务日志。在链路追踪系统中可以按 `trace_id` 找到一条链路的全部日志。

