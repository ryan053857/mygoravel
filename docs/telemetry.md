# 可观测性

- [简介](#introduction)
- [推送](#setup)
    - [自己创建 Provider](#custom-providers)
- [指标](#metrics)
    - [HTTP 请求](#http-metrics)
    - [队列](#queue-metrics)
    - [定时任务](#schedule-metrics)
    - [限流](#ratelimit-metrics)
    - [实时频道](#realtime-metrics)
    - [告警示例](#alerting)
- [链路追踪](#tracing)
    - [HTTP 请求](#http-tracing)
    - [队列](#queue-tracing)
    - [定时任务](#schedule-tracing)
    - [日志](#trace-logs)

<a id="introduction"></a>
## 简介

框架的 HTTP 服务、队列、调度器、限流器和实时频道都可以报告 [OpenTelemetry](https://opentelemetry.io/docs/languages/go/) 指标，前三者还报告链路。`telemetry` 包把这些指标、链路和应用的日志一起推送到一个 OTLP 接收端，例如 [OpenObserve](https://openobserve.ai/)，不需要另外部署收集器。

这里只有框架自己的组件。数据库、Redis、NATS 这些依赖由各自的生态报告，例如 Bun 的 [bunotel](https://pkg.go.dev/github.com/uptrace/bun/extra/bunotel)、go-redis 的 [redisotel](https://pkg.go.dev/github.com/redis/go-redis/extra/redisotel/v9)，或者服务器一侧的 exporter，把同一个 Provider 交给它们即可。

和其他功能一样，指标和链路都需要显式开启：把 `MeterProvider`、`TracerProvider` 交给需要报告的部分，没有传入的部分不报告。

<a id="setup"></a>
## 推送

在配置文件的 `telemetry` 段填写接收端：

```yaml
telemetry:
  endpoint: ${OTLP_ENDPOINT:-}    # 例如 http://openobserve:5080/api/default
  headers:
    Authorization: Basic ${OTLP_TOKEN:-}
  service: blog
  version: ${APP_VERSION:-}
```

- `endpoint` 是基础地址，日志、指标、链路分别发往其下的 `/v1/logs`、`/v1/metrics`、`/v1/traces`。为空时不推送，本地开发不用配置。
- `headers` 附加到每个请求，通常是认证信息。
- `service` 为空时取可执行文件名；`version` 为空时取构建信息中的模块版本。日志、指标、链路都用这里的服务名和版本，`logs` 段不再重复。
- `sample_ratio` 是新链路的采样比例，默认 `1`，即全部记录。请求带着调用方的链路进来时，跟随调用方的决定。

把 `telemetry.Config` 放进[根配置](configuration.md#loading-configuration)，在[入口](dependencies.md#assembling-the-application)的 `open` 中最先打开它：

```go
tel, err := telemetry.Open(ctx, cfg.Telemetry)
if err != nil {
    return nil, err
}
stack.MustAdd("telemetry", tel.Shutdown)
slog.SetDefault(logging.New(cfg.Logs, logging.Export(tel.Logs)))
```

`tel.Meters`、`tel.Tracers` 保存在 `App` 的 `meters`、`tracers` 字段中，下面各节把它们交给需要报告的部分。`tel.Shutdown` 最先登记、最后执行，发出缓冲中的数据，其他资源关闭时记录的日志也能推送出去。

`logging.Export` 让日志同时推送出去，经过和标准错误上同样的级别和格式化。`ctx` 中有 span 时，日志记录带上它的链路，日志平台可以从一条日志跳到它所在的链路。标准错误上的日志照常输出，启动阶段的错误和接收端不可用时的排查都靠它。只想推送指标和链路时，不传 `logging.Export` 即可。

三种数据共用一组资源属性：`service.name`、`service.version`、`service.instance.id`、主机名和进程号。`service.instance.id` 每个进程随机生成，同一服务的 `serve`、`work` 进程和多个副本靠它区分，指标不会写进同一条序列。

推送在后台批量进行，接收端变慢不会阻塞请求，缓冲满时丢弃新数据。推送失败记录为 `component=telemetry` 的错误日志。

<a id="custom-providers"></a>
### 自己创建 Provider

框架组件只依赖 OpenTelemetry 的接口，不一定要用 `telemetry`。需要尾部采样（只保留出错或慢的链路）、同时发往多个后端，或者让 Prometheus 来抓取时，用 OpenTelemetry 的 SDK 自己创建 Provider，或者推送到 [OpenTelemetry Collector](https://opentelemetry.io/docs/collector/) 由它处理。日志用 `logging.Export` 接入任意 `LoggerProvider`。

<a id="metrics"></a>
## 指标

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

<a id="ratelimit-metrics"></a>
### 限流

```go
attempts := ratelimit.Observe(a.meters, "login", ratelimit.NewDatabase(a.db, "login", cfg))
```

| 指标 | 类型 | 属性 | 含义 |
| --- | --- | --- | --- |
| `ratelimit.requests` | 计数器 | `ratelimit.name`、`ratelimit.result` | 每次 `Allow` 的结果：`allowed`、`exceeded`（配额用完）或 `error`（存储出错） |

被拒绝的请求同时以 `429` 出现在 HTTP 请求的指标中。用法和查询示例见[限流的指标](rate-limiting.md#metrics)。

<a id="realtime-metrics"></a>
### 实时频道

设置 `realtime.Options.MeterProvider` 后报告本实例的连接数 `realtime.connections` 和订阅数 `realtime.subscriptions`，见[实时频道的指标](realtime.md#metrics)。

<a id="alerting"></a>
### 告警示例

导出到 Prometheus 后，指标名中的点换成下划线，单位成为后缀，例如 `http_server_request_duration_seconds`：

```promql
# 5 分钟内 5xx 的比例超过 1%
sum(rate(http_server_request_duration_seconds_count{http_response_status_code=~"5.."}[5m]))
  / sum(rate(http_server_request_duration_seconds_count[5m])) > 0.01

# 队列积压超过 5 分钟
max by (queue_topic) (queue_job_oldest_ready_age_seconds) > 300

# 限流器的存储出错：使用它的请求都在返回 503
sum by (ratelimit_name) (rate(ratelimit_requests_total{ratelimit_result="error"}[5m])) > 0

# 登录被拒绝的比例超过 20%，可能有人在暴力尝试
sum(rate(ratelimit_requests_total{ratelimit_name="login", ratelimit_result="exceeded"}[5m]))
  / sum(rate(ratelimit_requests_total{ratelimit_name="login"}[5m])) > 0.2

# 每天凌晨的报表任务 25 小时内没有成功过
increase(schedule_task_duration_seconds_count{schedule_task="reports:daily", schedule_task_outcome="succeeded"}[25h]) == 0
```

<a id="tracing"></a>
## 链路追踪

一个请求发布了任务、任务又调用了其他服务时，链路追踪把它们串成一条链路，可以看到每一步的耗时和在哪一步出错。

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

<a id="trace-logs"></a>
### 日志

`ctx` 中有 span 时，用它记录的日志自动带上 `trace_id` 和 `span_id`，包括访问日志、队列和调度器的日志，以及处理函数里 `slog.InfoContext(ctx, ...)` 记录的业务日志。在链路追踪系统中可以按 `trace_id` 找到一条链路的全部日志。

用 [`logging.Export`](#setup) 推送的日志把链路写在记录本身，而不是作为 `trace_id` 字段，日志平台据此在日志和链路之间跳转。

