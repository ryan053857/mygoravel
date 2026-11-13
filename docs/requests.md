# 请求

- [简介](#introduction)
- [定义输入类型](#input-types)
    - [路径参数](#path-parameters)
    - [查询参数](#query-parameters)
    - [请求头](#headers)
    - [JSON 请求体](#json-body)
- [请求 ID](#request-id)
- [请求体大小与超时](#limits)
    - [请求体大小](#body-size)
    - [超时与取消](#timeouts)

<a id="introduction"></a>
## 简介

在 Tenon 中，你不需要手动读取 `http.Request`。每条路由都有一个**输入类型**——一个普通的 Go 结构体，字段上的标签说明这个值从哪里来。Huma 会在调用处理器之前读取请求、转换类型并完成[验证](validation.md)，你的处理器拿到的就是一个已经填好的结构体。

例如，在[获取文章](routing.md#http-actions)的 `Get.HTTP` 方法中，输入已经完成类型转换。它和 `GetInput` 一起定义在 `internal/article/get.go` 中；共享的 `ArticleOutput` 定义在同包的 `dto.go` 中。下面只展示方法签名和读取输入的位置：

```go
func (a *Get) HTTP(ctx context.Context, in *GetInput) (*ArticleOutput, error) {
    // in.ID 已经是解析好的 uuid.UUID
    // ……调用 a.Handle 并返回输出
}
```

<a id="input-types"></a>
## 定义输入类型

<a id="path-parameters"></a>
### 路径参数

使用 `path` 标签读取路由路径中的参数。下面的输入类型对应 `/articles/{id}`，`uuid.UUID` 来自 `github.com/google/uuid`：

```go
type GetInput struct {
    ID uuid.UUID `path:"id"`
}
```

路径参数总是必填的。如果路径中的值无法转换为字段类型（例如不是合法的 UUID），请求会被拒绝，并返回验证错误。

<a id="query-parameters"></a>
### 查询参数

使用 `query` 标签读取查询参数。你可以同时声明默认值和取值范围：

```go
type SearchArticlesInput struct {
    Q     string `query:"q"`
    Limit int    `query:"limit" default:"20" minimum:"1" maximum:"100"`
}
```

客户端请求 `/articles?limit=50` 时，`in.Limit` 为 50；省略 `limit` 时为默认值 20；传入 500 时，请求会被拒绝。查询参数默认是可选的。

> [!NOTE]
> 上面的写法会把超出范围的 `limit` 当作错误拒绝。如果你希望把它悄悄截断为 100，就不要声明 `maximum`，而是在 action 中处理，并在接口描述中说明这一行为。

> [!TIP]
> 列表接口的分页参数不需要自己声明，嵌入 `page.OffsetParams` 或 `page.CursorParams` 即可，见[分页](responses.md#pagination)。

<a id="headers"></a>
### 请求头

使用 `header` 标签读取请求头：

```go
type RequestInput struct {
    ClientVersion string `header:"X-Client-Version"`
}
```

> [!TIP]
> `Authorization` 头不需要自己读取。使用 `rest.RequireBearer` 保护路由后，认证通过的身份可以直接从 context 中获取，见[认证](authentication.md)。

<a id="json-body"></a>
### JSON 请求体

输入结构体中名为 `Body` 的字段表示请求体。创建文章的请求体是文章模块的 `Changes`，验证规则写在它的字段上，见[数据对象](actions.md#data-objects)：

```go
type Changes struct {
    Title   string `json:"title" minLength:"1" maxLength:"200"`
    Slug    string `json:"slug" maxLength:"100" pattern:"^[a-z0-9]+(-[a-z0-9]+)*$" patternDescription:"小写字母、数字和连字符"`
    Content string `json:"content"`
}

type CreateInput struct {
    Body Changes
}
```

客户端发送：

```http
POST /articles
Content-Type: application/json
Authorization: Bearer <token>

{"title":"Hello World","slug":"hello-world","content":"这是我的第一篇文章。"}
```

处理器中通过 `in.Body.Title`、`in.Body.Slug` 等读取字段，或者把 `in.Body` 整个交给 `Handle`。请求体中的字段默认是**必填**的，验证规则的写法见[验证](validation.md)。给字段写说明和示例值，让它们出现在接口文档中，见[说明与示例](routing.md#describing-apis)。

一个输入类型可以同时包含路径参数、查询参数、请求头和请求体：

```go
type UpdateInput struct {
    ID   uuid.UUID `path:"id"`
    Body Changes
}
```

<a id="request-id"></a>
## 请求 ID

`httpx.AssignRequestID` 中间件会为每个请求确定一个 ID：如果请求头 `X-Request-ID` 已经携带合法的值（1–128 个可打印 ASCII 字符，不含空格，通常由网关生成），就沿用它，否则生成新的 ID。读取哪个请求头可以配置，见[请求 ID](middleware.md#request-id)。在处理器或业务代码中，可以通过请求的 context 读取它：

```go
requestID := httpx.RequestID(ctx)
```

同一个 ID 会出现在响应头 `X-Request-ID` 以及这个请求的所有日志中。当用户反馈问题时，请他们提供这个 ID，你就能在日志中找到对应的请求。

<a id="limits"></a>
## 请求体大小与超时

<a id="body-size"></a>
### 请求体大小

为了防止过大的请求耗尽服务器资源，[默认中间件](middleware.md#default-middleware)中的 `httpx.BodyLimit` 把请求体限制在 1 MiB，超出时返回 `413`。你可以在配置文件中调整这个上限：

```yaml
http:
  body_limit:
    max_bytes: 8388608 # 8 MiB
```

> [!NOTE]
> Huma 的 `Operation` 也有自己的 `MaxBodyBytes` 设置。如果某个接口需要接收更大的请求体，请同时检查该路由和全局中间件的上限，两者都需要足够大。只有少数接口需要更大上限时，可以[自行组装中间件](middleware.md#custom-stack)，只为这些接口放宽 `BodyLimit`。

<a id="timeouts"></a>
### 超时与取消

`httpx.Timeout` 中间件为请求的 context 设置超时，默认 30 秒，可以在配置文件中调整。它不在默认中间件中，需要单独安装，见[请求超时](middleware.md#request-timeout)：

```yaml
http:
  timeout:
    duration: 10s
```

超时后，请求的 context 会被取消。为了让超时真正生效，请把处理器收到的 `ctx` 一路传给数据库查询和外部 HTTP 调用。

除了请求 context 的超时，服务器本身还有读写超时：`read_header_timeout`、`read_timeout`、`write_timeout` 和 `idle_timeout`。完整字段见 [HTTP 配置](middleware.md#http-configuration)。
