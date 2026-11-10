# 日志

- [简介](#introduction)
- [创建日志器](#creating-a-logger)
- [写入日志](#writing-log-messages)
- [上下文信息](#contextual-information)
    - [请求关联](#request-correlation)
    - [添加自定义字段](#custom-context-fields)
    - [使用自己的 slog Handler](#custom-handlers)
- [错误日志](#error-logging)

<a id="introduction"></a>
## 简介

mygoravel 使用 Go 标准库的 [`log/slog`](https://pkg.go.dev/log/slog) 记录结构化日志。框架没有全局日志器：你创建一个 `*slog.Logger`，并把它显式传给 API 和需要记录日志的服务。

`logging` 包提供了两个便利：一是用几行配置构造日志器；二是让日志自动带上请求 ID 等关联字段。除此之外，你使用的都是标准的 slog API。

<a id="creating-a-logger"></a>
## 创建日志器

日志级别、格式这些需要按环境调整的值来自 `logging.Config`，通常放在配置文件的 `logs` 段：

```yaml
logs:
  level: ${LOG_LEVEL:-info}
  format: json
```

| 字段 | 说明 | 默认值 |
| --- | --- | --- |
| `level` | 最低记录级别：`debug`、`info`、`warn`、`error` | `info` |
| `format` | `json` 或 `text` | `json` |
| `add_source` | 是否记录源码位置 | `false` |

把 `logging.Config` 放进应用的根配置结构体（`config.Load` 会自动填充默认值），然后调用 `logging.New`。输出位置和固定字段属于代码层面的决定，通过第二个参数传入：

```go
logger := logging.New(cfg.Logs, logging.Options{
    Writer: os.Stderr,
    Attrs: []slog.Attr{
        slog.String("service", "blog"),
        slog.String("version", "0.1.0"),
    },
})
```

| 选项 | 说明 | 默认值 |
| --- | --- | --- |
| `Writer` | 日志输出位置 | `os.Stdout` |
| `Attrs` | 附加到每条日志的固定字段 | 无 |

`logging.Config` 和 `logging.Options` 的零值都可以直接使用。本地开发时，`text` 格式的输出更便于阅读；生产环境中，JSON 格式更便于日志系统采集和检索。

> [!TIP]
> API 服务建议显式输出到 `os.Stderr`。这样，当同一个程序作为命令行工具使用时（例如 `openapi` 命令），标准输出可以完整地留给命令结果。

<a id="writing-log-messages"></a>
## 写入日志

需要记录业务事件的服务，像接收数据库一样，通过构造函数接收日志器，见[依赖装配](dependencies.md#constructor-injection)。然后使用 slog 的原生方法记录日志，以键值对的形式附加结构化字段。例如在文章发布成功后记录一条日志：

```go
func (s *Service) Publish(ctx context.Context, subject identity.Subject, id uuid.UUID) (*Article, error) {
    // ……在事务中检查并更新文章状态
    s.logger.InfoContext(ctx, "article published",
        "article_id", item.ID,
        "author_id", item.AuthorID,
    )
    return item, nil
}
```

请尽量使用带 `Context` 后缀的方法（`InfoContext`、`ErrorContext` 等）并传入当前的 `ctx`，这样日志才能自动带上请求 ID，见下一节。

字段名应当保持稳定，例如始终使用 `article_id` 而不是有时写 `articleId`，这样才方便在日志系统中检索和聚合。

<a id="contextual-information"></a>
## 上下文信息

<a id="request-correlation"></a>
### 请求关联

[默认中间件](middleware.md#default-middleware)中的 `httpx.AccessLog` 会用传入的日志器为每个请求记录一条访问日志：

```go
router.Use(httpx.Defaults(cfg.HTTP, logger)...)
api := rest.New(rest.Options{Router: router, Logger: logger})
```

`rest.New` 的 `Logger` 用于记录 5xx 错误的诊断信息。`httpx.AssignRequestID` 还会把请求 ID 放入请求的 context。之后，只要你在业务代码中用 `logging.New` 创建的日志器配合请求的 `ctx` 记录日志，这些日志就会自动带上 `request_id` 字段：

```json
{"level":"INFO","msg":"article published","service":"blog","request_id":"9F2K…","article_id":"0f8c…","author_id":"alice"}
```

这样，同一个请求的访问日志、错误诊断和业务日志都可以通过 `request_id` 关联起来。

<a id="custom-context-fields"></a>
### 添加自定义字段

你也可以把自己的字段放入 context，之后使用这个 context 记录的所有日志都会带上它们：

```go
ctx = logging.WithAttrs(ctx, slog.String("component", "article"))
```

多次调用 `WithAttrs` 会累加字段，并且不会影响父 context。

<a id="custom-handlers"></a>
### 使用自己的 slog Handler

如果你已经有一个自己构造的 `*slog.Logger`（例如使用了第三方 Handler），它同样可以传给 API。若希望它在业务代码中也能读取 context 中的关联字段，使用 `logging.NewContextHandler` 包装它的 Handler：

```go
logger := slog.New(logging.NewContextHandler(myHandler))
```

<a id="error-logging"></a>
## 错误日志

业务代码通常**不需要**自己记录错误日志，只需添加上下文并返回错误。mygoravel 会在 HTTP 边界为每个 `5xx` 错误记录一条诊断日志，包含错误码和完整的错误链（含堆栈）。如果每一层都记录一次，同一个错误就会在日志中出现很多遍，反而难以排查。

| 日志 | 记录者 | 内容 |
| --- | --- | --- |
| 访问日志 | `httpx.AccessLog` | 每个请求的方法、状态码、耗时、客户端地址，以及业务错误的错误码 |
| 错误诊断 | `rest.Register` | `5xx` 错误的错误码和完整错误链 |
| 认证后端故障 | `rest.RequireBearer` | 校验器返回 `5xx` 类错误时的错误码和完整错误链 |
| 缺失翻译 | 错误响应转换 | 找不到错误码对应翻译时的警告 |

详见[错误处理](errors.md#reporting-errors)。
