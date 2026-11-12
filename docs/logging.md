# 日志

- [简介](#introduction)
- [创建日志器](#creating-a-logger)
    - [按组件设置级别](#component-levels)
- [写入日志](#writing-log-messages)
- [上下文信息](#contextual-information)
    - [请求关联](#request-correlation)
    - [添加自定义字段](#custom-context-fields)
- [错误日志](#error-logging)
- [扩展处理链](#pipeline)
    - [格式化字段](#formatting)
    - [多个输出](#multiple-outputs)
    - [Handler 中间件](#handler-middleware)
    - [使用自己的 slog Handler](#custom-handlers)
- [敏感信息脱敏](#redacting-sensitive-data)
- [测试日志](#testing-logs)

<a id="introduction"></a>
## 简介

mygoravel 使用 Go 标准库的 [`log/slog`](https://pkg.go.dev/log/slog) 记录结构化日志，全局日志器就是 `slog.Default()`。在入口处安装一次：

```go
slog.SetDefault(logging.New(cfg.Logs))
```

之后，框架组件（访问日志、panic 恢复、5xx 诊断、平滑停止）和业务代码都通过它记录日志，不需要把日志器层层传递。

`logging` 包在标准 slog 之上做了三件事：按配置构造日志器；让日志自动带上请求 ID 等关联字段；把 [slog-formatter](https://github.com/samber/slog-formatter) 和 [slog-multi](https://github.com/samber/slog-multi) 接入处理链，用于格式化字段和分发到多个输出。除此之外，你使用的都是标准的 slog API。

<a id="creating-a-logger"></a>
## 创建日志器

日志级别、格式这些需要按环境调整的值来自 `logging.Config`，通常放在配置文件的 `logs` 段：

```yaml
logs:
  level: ${LOG_LEVEL:-info}
  format: ${LOG_FORMAT:-json}
  service: blog
  time_zone: UTC
```

| 字段 | 说明 | 默认值 |
| --- | --- | --- |
| `level` | 最低记录级别：`debug`、`info`、`warn`、`error` | `info` |
| `format` | `json` 或 `text`，见[输出格式](#formats) | `json` |
| `add_source` | 是否记录源码位置 | `false` |
| `service` | 附加到每条日志的 `service` 字段，为空时省略 | 空 |
| `version` | 附加到每条日志的 `version` 字段 | 构建信息中的模块版本，取不到时省略 |
| `time_zone` | 日志时间使用的时区，例如 `UTC`、`Asia/Shanghai`；同时作用于字段中的时间值 | 本地时区 |
| `levels` | 按组件覆盖 `level`，见[按组件设置级别](#component-levels) | 无 |

把 `logging.Config` 放进应用的根配置结构体（`config.Load` 会自动填充默认值并校验），然后在入口处构造并安装：

```go
slog.SetDefault(logging.New(cfg.Logs))
```

`logging.Config` 的零值可以直接使用，不写 `logs` 段也能得到合理的日志。

<a id="formats"></a>
两种输出格式分别面向机器和人：

- `json`：每行一个 JSON 对象，供日志系统采集和检索。生产环境使用它。
- `text`：本地开发时阅读，基于 [tint](https://github.com/lmittmann/tint)。时间显示到毫秒，级别缩写为 `DBG`、`INF`、`WRN`、`ERR`，分组字段以 `.` 连接：

  ```text
  2026-11-10 17:05:12.345 INF http request service=blog component=http request.method=GET response.status=200 request_id=9F2K…
  2026-11-10 17:05:12.401 ERR request failed error.message=boom request_id=9F2K…
  ```

  输出到终端时，时间变暗、级别按颜色区分。输出到管道、文件或 CI 时自动去掉颜色；设置 `NO_COLOR=1` 也会关闭颜色（见 [no-color.org](https://no-color.org)）。

`text` 不是严格的 [logfmt](https://brandur.org/logfmt)，不要让日志系统解析它。确实需要 logfmt 时，用 `logging.Fanout(slog.NewTextHandler(w, nil))` 追加一个输出，见[多个输出](#multiple-outputs)。

> [!TIP]
> 让 AI agent 读取或分析日志时，请使用 `json`：字段完整、结构明确。agent 执行命令时通常不在终端中，`text` 也不会带颜色码；如果 agent 工具分配了伪终端，设置 `NO_COLOR=1` 可以避免输出转义码。

没有设置 `time_zone` 时使用进程的本地时区，它由 `TZ` 环境变量或 `/etc/localtime` 决定：开发机上通常是 `+08:00`，容器中往往是 UTC。时间始终带有时区偏移，不会产生歧义；多个环境混合检索时，建议统一设为 `UTC`。

日志默认输出到标准错误，这样当同一个程序作为命令行工具使用时（例如 `openapi` 命令），标准输出可以完整地留给命令结果。需要改变输出位置时（例如测试中写入缓冲区），在代码中设置 `Writer` 字段，它不会从配置文件读取：

```go
var buf bytes.Buffer
cfg.Logs.Writer = &buf
```

`slog.SetDefault` 还会让标准库 `log` 包的输出也经过这个日志器，第三方库通过 `log.Printf` 打印的内容同样会变成结构化日志。

> [!NOTE]
> `logging.New` 本身不修改全局状态，是否安装为全局日志器由入口决定。框架组件的日志器参数为 `nil` 时使用 `slog.Default()`，因此也可以只给某个组件传入单独的日志器，例如 `rest.Options{Logger: auditLogger}`；需要关闭某个组件的日志时，传入 `slog.New(slog.DiscardHandler)`。

<a id="component-levels"></a>
### 按组件设置级别

有时只想调整一部分日志的级别：排查业务问题时打开 `debug`，又不想被访问日志刷屏；或者全局只看 `warn`，但保留某个模块的 `info`。`levels` 按日志的 `component` 字段覆盖全局的 `level`：

```yaml
logs:
  level: debug
  levels:
    http: warn       # 访问日志只保留 4xx、5xx
    billing: info
```

没有 `component` 字段，或者 `component` 不在 `levels` 中的日志，使用全局的 `level`。覆盖的级别可以比全局级别更高，也可以更低。

框架组件记录的日志都带有 `component` 字段：

| component | 日志 |
| --- | --- |
| `http` | 访问日志、panic 堆栈、`5xx` 诊断、认证后端故障、平滑停止 |
| `queue` | 任务失败与重试、转入失败任务表、领取和写回任务的数据库错误 |
| `schedule` | 定时任务失败、因重叠跳过、调度落后 |

你自己的模块通过 `With` 或 context 设置 `component`：

```go
// 服务持有的日志器
logger := slog.Default().With(logging.AttrComponent, "billing")

// 或者放进 context，之后用这个 ctx 记录的日志都属于该组件
ctx = logging.WithAttrs(ctx, slog.String(logging.AttrComponent, "billing"))
```

> [!NOTE]
> 只写在单条日志里的 `component`（例如 `slog.Debug("…", "component", "billing")`）只能让级别**更严格**：比全局级别更低的日志在 slog 判断是否记录时就已经被丢弃了。需要放宽级别的组件，请用上面两种方式设置。

<a id="writing-log-messages"></a>
## 写入日志

业务代码直接使用 slog 的包级函数记录日志，以键值对的形式附加结构化字段。例如在文章发布成功后记录一条日志：

```go
func (a *Publish) Handle(ctx context.Context, subject identity.Subject, id uuid.UUID) (*Article, error) {
    // ……在事务中检查并更新文章状态
    slog.InfoContext(ctx, "article published",
        "article_id", item.ID,
        "author_id", item.AuthorID,
    )
    return item, nil
}
```

请尽量使用带 `Context` 后缀的方法（`InfoContext`、`ErrorContext` 等）并传入当前的 `ctx`，这样日志才能自动带上请求 ID，见下一节。

如果某个 action 需要单独的日志器（例如写入审计日志），像接收数据库一样通过构造函数接收 `*slog.Logger` 即可，见[依赖装配](dependencies.md#constructor-injection)。

字段名应当保持稳定，例如始终使用 `article_id` 而不是有时写 `articleId`，这样才方便在日志系统中检索和聚合。

<a id="contextual-information"></a>
## 上下文信息

<a id="request-correlation"></a>
### 请求关联

安装[默认中间件](middleware.md#default-middleware)后，`httpx.AccessLog` 会为每个请求记录一条[访问日志](middleware.md#access-log)，`rest.New` 创建的 API 会为 5xx 错误记录诊断信息：

```go
router.Use(httpx.Defaults(a.cfg.HTTP, nil, nil)...)
api := rest.New(rest.Options{Router: router})
```

`httpx.AssignRequestID` 还会把[请求 ID](middleware.md#request-id) 放入请求的 context。之后，只要你在业务代码中配合请求的 `ctx` 记录日志（`slog.InfoContext(ctx, ...)`），这些日志就会自动带上 `request_id` 字段：

```json
{"level":"INFO","msg":"article published","service":"blog","request_id":"9F2K…","article_id":"0f8c…","author_id":"alice"}
```

这样，同一个请求的访问日志、错误诊断和业务日志都可以通过 `request_id` 关联起来。部署在反向代理之后时，还可以沿用代理生成的请求 ID，把代理日志也关联进来。

`ctx` 中有 OpenTelemetry 的 span 时，日志还会自动带上 `trace_id` 和 `span_id`，在链路追踪系统中可以从一个 span 直接找到它的日志，见[链路追踪](telemetry.md#tracing)。

<a id="custom-context-fields"></a>
### 添加自定义字段

你也可以把自己的字段放入 context，之后使用这个 context 记录的所有日志都会带上它们：

```go
ctx = logging.WithAttrs(ctx, slog.String("component", "article"))
```

多次调用 `WithAttrs` 会累加字段，并且不会影响父 context。

<a id="error-logging"></a>
## 错误日志

业务代码通常**不需要**自己记录错误日志，只需添加上下文并返回错误。mygoravel 会在 HTTP 边界为每个 `5xx` 错误记录一条诊断日志。如果每一层都记录一次，同一个错误就会在日志中出现很多遍，反而难以排查。

| 日志 | 记录者 | 内容 |
| --- | --- | --- |
| 访问日志 | `httpx.AccessLog` | 每个请求的方法、路由、状态码、耗时、客户端地址，以及业务错误的错误码 |
| 错误诊断 | `rest.Register` | `5xx` 错误的完整错误链 |
| 认证后端故障 | `rest.RequireBearer` | 校验器返回 `5xx` 类错误时的完整错误链 |

确实需要自己记录错误时（例如后台任务失败），把错误直接作为字段值传入即可：

```go
slog.ErrorContext(ctx, "sync feed failed", "feed_id", id, "error", err)
```

`logging.New` 默认启用 `logging.ErrorFormatter`，它会把 `error` 类型的字段展开为一个分组：

```json
{"level":"ERROR","msg":"sync feed failed","feed_id":"…","error":{"message":"article.slug_taken: pq: duplicate key","code":"article.slug_taken","stack":"article.slug_taken: pq: duplicate key\n(1) article.slug_taken\nWraps: (2) attached stack trace\n  -- stack trace:\n  | example.com/blog/internal/article.(*Update).Handle\n …"}}
```

| 字段 | 内容 |
| --- | --- |
| `message` | `err.Error()` |
| `code` | [错误码](errors.md)，没有时省略 |
| `stack` | `%+v` 格式的完整错误链，包含错误**创建处**的堆栈；与 `message` 相同（普通错误没有堆栈）时省略 |

不需要再手写 `fmt.Sprintf("%+v", err)`。详见[错误处理](errors.md#reporting-errors)。

<a id="pipeline"></a>
## 扩展处理链

`logging.New` 按固定顺序组装处理链，你通过选项把自己的部分放进指定位置：

```text
slog.Logger
 └─ context 字段             request_id 等（框架，始终在最外层）
     └─ 组件级别              按 component 应用 levels
         └─ service / version 固定字段（框架）
             └─ Pipe          Handler 中间件：过滤、采样（logging.Pipe）
                 └─ 格式化     ErrorFormatter + 你的 Formatter（logging.Format）
                     └─ 分发 ─┬─ 基础输出：Writer，json / text
                              └─ 追加输出（logging.Fanout）
```

context 字段和格式化都在分发之前完成，所以**每个输出拿到的内容完全一致**：都带有 `request_id`，`error` 的格式相同，脱敏规则也同样生效。

```go
slog.SetDefault(logging.New(cfg.Logs,
    logging.Format(slogformatter.PIIFormatter("user")),
    logging.Pipe(dropHealthChecks),
    logging.Fanout(alerts),
))
```

三个选项接收的都是社区库的原生类型，社区里现成的 Formatter、中间件和 Handler 都可以直接传入。

<a id="formatting"></a>
### 格式化字段

`logging.Format` 接收 [slog-formatter](https://github.com/samber/slog-formatter) 的 `slogformatter.Formatter`，按字段名、类型或种类改写字段值。例如统一 `time.Duration` 的输出，或者为自己的类型定义日志表示：

```go
logging.Format(
    slogformatter.FormatByType(func(d time.Duration) slog.Value {
        return slog.StringValue(d.String()) // 输出 "52.4ms" 而不是纳秒数
    }),
    slogformatter.FormatByType(func(u *account.User) slog.Value {
        return slog.GroupValue(slog.String("id", u.ID), slog.String("name", u.Name))
    }),
)
```

常用的内置 Formatter：

| Formatter | 作用 |
| --- | --- |
| `FormatByKey(key, fn)` | 改写指定字段名的值，包括嵌套分组中的同名字段 |
| `FormatByType[T](fn)` | 改写某个类型的值，无论字段名是什么 |
| `FormatByKind(kind, fn)` | 改写某种 `slog.Kind` 的值 |
| `TimeFormatter(layout, loc)` | 按指定格式输出时间值 |
| `UnixTimestampFormatter(precision)` | 把时间值输出为 Unix 时间戳 |
| `PIIFormatter(key)`、`IPAddressFormatter(key)` | 脱敏，见[敏感信息脱敏](#redacting-sensitive-data) |

> [!NOTE]
> slog-formatter 自带的 `ErrorFormatter` 记录的是**写日志那一行**的堆栈，而不是错误创建处的堆栈。请使用框架默认启用的 `logging.ErrorFormatter`，不要再追加前者。

<a id="multiple-outputs"></a>
### 多个输出

`logging.Fanout` 追加的输出与基础输出并列接收日志。每个输出按自己的级别过滤，例如把错误同时发到告警渠道：

```go
alerts := slogslack.Option{Level: slog.LevelError, WebhookURL: cfg.Alert.SlackWebhook}.NewSlackHandler()

slog.SetDefault(logging.New(cfg.Logs, logging.Fanout(alerts)))
```

需要按条件分发时，使用 slog-multi 的 Router：

```go
logging.Fanout(
    slogmulti.Router().
        Add(alerts, slogmulti.LevelIs(slog.LevelError)).
        Add(audit, slogmulti.AttrValueIs("component", "audit")).
        Handler(),
)
```

社区中常用的输出：

| Handler | 输出到 |
| --- | --- |
| [slog-syslog](https://github.com/samber/slog-syslog) | syslog |
| [slog-slack](https://github.com/samber/slog-slack)、[slog-telegram](https://github.com/samber/slog-telegram) | 即时通讯告警 |
| [slog-nats](https://github.com/samber/slog-nats) | NATS 消息 |
| [slog-channel](https://github.com/samber/slog-channel) | Go channel，用于进程内消费 |

> [!WARNING]
> 发送到第三方服务的输出会产生网络请求。请只发送需要的级别（通常是 `error`），并确认它是异步发送的，或者用 slog-multi 的 `Pool` 放到后台，否则第三方服务变慢时，请求的处理也会跟着变慢。

<a id="handler-middleware"></a>
### Handler 中间件

`logging.Pipe` 接收 slog-multi 的 `slogmulti.Middleware`，在格式化和分发之前处理每条日志，适合过滤或采样。例如丢弃健康检查产生的日志：

```go
dropHealthChecks := slogmulti.NewHandleInlineMiddleware(
    func(ctx context.Context, r slog.Record, next func(context.Context, slog.Record) error) error {
        if r.Message == "health check" {
            return nil
        }
        return next(ctx, r)
    },
)
```

多个中间件按传入顺序由外到内执行。只是不想记录某些路径的访问日志时，直接配置 [`http.access_log.ignore_paths`](middleware.md#access-log) 更简单。

<a id="custom-handlers"></a>
### 使用自己的 slog Handler

已有的第三方 Handler 用 `logging.Fanout` 接入即可，它会得到完整的 context 字段和格式化结果。推送到 OpenTelemetry 的日志平台用 `logging.Export`，见[推送](telemetry.md#setup)。

如果要完全绕开 `logging.New`，自己构造日志器，请用 `logging.NewContextHandler` 包装 Handler，日志才能读取 context 中的关联字段：

```go
slog.SetDefault(slog.New(logging.NewContextHandler(myHandler)))
```

此时 `ErrorFormatter` 等格式化规则需要你自己安装。

<a id="redacting-sensitive-data"></a>
## 敏感信息脱敏

框架默认不脱敏任何字段。需要时，通过 `logging.Format` 加上 slog-formatter 的脱敏规则。规则在分发之前执行，所以对所有输出都生效，框架自己记录的访问日志、错误诊断也同样会经过它们。

### 使用内置规则

`PIIFormatter` 对指定字段及其所有子字段脱敏，`IPAddressFormatter` 整体隐藏指定字段：

```go
slog.SetDefault(logging.New(cfg.Logs,
    logging.Format(
        slogformatter.PIIFormatter("user"),
        slogformatter.IPAddressFormatter("client_ip"),
    ),
))

slog.InfoContext(ctx, "user signed up",
    slog.Group("user", "id", u.ID, "email", u.Email, "phone", u.Phone),
    "client_ip", ip,
)
```

```json
{"msg":"user signed up","user":{"id":"bd57ffbd-…","email":"foob*******","phone":"1380*******"},"client_ip":"*******"}
```

`PIIFormatter` 的规则：

- 字段名是 `id`，或以 `_id`、`-id` 结尾时保留原值，便于关联；
- 长度大于 5 的字符串保留前 4 个字符，其余替换为 `*******`；
- 其他值（短字符串、数字、时间等）整体替换为 `*******`。

### 自定义规则

规则不合适时，用 `FormatByKey` 或 `FormatByType` 自己写。例如手机号只保留后 4 位：

```go
logging.Format(slogformatter.FormatByKey("phone", func(v slog.Value) slog.Value {
    s := v.String()
    if len(s) <= 4 {
        return slog.StringValue("****")
    }
    return slog.StringValue("****" + s[len(s)-4:])
}))
```

按类型定义日志表示，可以让某个类型无论出现在哪个字段名下都不会泄露敏感内容：

```go
logging.Format(slogformatter.FormatByType(func(u *account.User) slog.Value {
    return slog.GroupValue(slog.String("id", u.ID)) // 只输出 ID
}))
```

### 注意事项

- 规则**只按字段名或类型匹配，不检查内容**。直接拼接进日志消息的文本不会被处理，所以敏感数据请始终作为结构化字段传入，不要写进消息。
- 访问日志开启 `request_header` 后，`Authorization`、`Cookie` 等认证相关的请求头始终隐藏，见[访问日志](middleware.md#access-log)。其他需要隐藏的请求头，用 `FormatByKey` 按请求头名称处理即可，它会进入嵌套的分组匹配字段名，例如 `slogformatter.FormatByKey("X-Api-Key", ...)`。

<a id="testing-logs"></a>
## 测试日志

`logging/logtest` 捕获日志，供测试断言。`Capture` 在测试期间把它安装为 `slog.Default`，测试结束后自动恢复：

```go
func TestPublishLogsEvent(t *testing.T) {
    rec := logtest.Capture(t)

    // ……调用 Publish

    got, ok := rec.Find("article published")
    if !ok || got.Attr("article_id") != id.String() {
        t.Fatal(rec.Records())
    }
}
```

| 方法 | 说明 |
| --- | --- |
| `Records()` | 返回全部捕获的日志 |
| `Find(msg)` | 返回第一条消息为 `msg` 的日志 |
| `Has(level, msg)` | 是否有指定级别和消息的日志 |
| `Record.Attr(key)` | 返回字段值，分组以 `.` 连接，例如 `response.status` |
| `Reset()` | 清空已捕获的日志 |

捕获的日志器同样会输出 context 字段并展开 `error`，所以也可以断言 `request_id` 或 `error.code`。整数字段的值是 `int64`，例如 `got.Attr("response.status") == int64(404)`。

> [!WARNING]
> `Capture` 修改全局状态，使用它的测试不能调用 `t.Parallel()`。并行测试请改用 `logtest.New()`，再把 `rec.Logger()` 显式传给被测组件，例如 `httpx.Defaults(cfg, rec.Logger(), nil)`。
