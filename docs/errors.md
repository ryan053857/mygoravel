# 错误处理

- [简介](#introduction)
- [一个完整的例子](#example)
    - [声明输入格式](#declaring-input)
    - [定义业务错误](#defining-errors)
    - [在服务中返回错误](#returning-errors)
    - [在处理器中调用服务](#calling-from-handlers)
    - [客户端看到的响应](#what-clients-see)
    - [服务端日志](#what-operators-see)
- [选择错误分类](#error-kinds)
- [处理底层错误](#wrapping-errors)
- [在提示中带上参数](#public-parameters)
- [参考](#reference)
    - [判断错误](#inspecting-errors)
    - [响应格式](#response-format)
    - [状态码映射](#status-mapping)
    - [错误日志](#reporting-errors)
    - [原生 Huma 错误](#huma-errors)
    - [跨进程传递](#cross-process-errors)

<a id="introduction"></a>
## 简介

一个接口可能遇到的失败，大致分为三类，mygoravel 对它们的处理方式各不相同：

| 失败类型 | 例子 | 由谁处理 |
| --- | --- | --- |
| **输入格式错误** | 标题为空、slug 含有空格 | 在输入类型上声明规则，框架自动验证，处理器根本不会被调用 |
| **可预期的业务结果** | 文章不存在、slug 已被使用 | 用 `errorx` 定义带错误码的业务错误，在服务中返回 |
| **意料之外的故障** | 数据库连不上、代码有 bug | 用 `errors.Wrap` 补充上下文后返回，客户端只会收到通用的 `500` |

无论哪一类，业务代码都只需要**返回错误**。选择 HTTP 状态码、生成错误响应、记录日志，都由框架在边界处完成。下面通过一个完整的例子，看看这三类失败分别是怎样被处理的。

<a id="example"></a>
## 一个完整的例子

我们要实现“修改文章”的接口 `PUT /articles/{id}`，它可能遇到以下失败：

- 提交的标题为空，或者 slug 的格式不对；
- 文章不存在；
- 当前用户不是文章的作者；
- 文章已经发布，却试图修改它的 slug（已发布文章的网址不应改变）；
- 新的 slug 已被其他文章使用；
- 数据库出现故障。

文章模型 `Article` 和数据表的定义见[数据库](database.md#defining-models)。

<a id="declaring-input"></a>
### 声明输入格式

格式上的要求直接写在输入类型的结构体标签上。不符合要求的请求会被框架拦截，并返回 `422`。按照[按业务动作组织处理器](routing.md#http-actions)的约定，把下面的输入类型放在 `internal/article/articlehttp/actions/update_article.go` 中，与修改文章的处理器放在一起，包名为 `actions`：

```go
type UpdateArticleInput struct {
    ID   uuid.UUID `path:"id"`
    Body struct {
        Title   string `json:"title" minLength:"1" maxLength:"200"`
        Slug    string `json:"slug" pattern:"^[a-z0-9]+(-[a-z0-9]+)*$" maxLength:"100"`
        Content string `json:"content"`
    }
}
```

可用的验证规则见[验证](validation.md)。

<a id="defining-errors"></a>
### 定义业务错误

需要查询数据或依赖业务状态才能判断的失败，用 `errorx.Define` 定义为业务错误。第一个参数是**分类**，决定了它的性质；第二个参数是**错误码**：

```go
package article

import "github.com/ryan053857/mygoravel/errorx"

var (
    ErrNotFound   = errorx.Define(errorx.NotFound, "article.not_found")
    ErrForbidden  = errorx.Define(errorx.PermissionDenied, "article.forbidden")
    ErrSlugLocked = errorx.Define(errorx.InvalidArgument, "article.slug_locked")
    ErrSlugTaken  = errorx.Define(errorx.Conflict, "article.slug_taken")
)
```

错误码推荐使用 `资源.原因` 的形式，全部小写，单词之间用下划线连接。错误码在整个应用中必须唯一。

<a id="returning-errors"></a>
### 在服务中返回错误

下面是 `article` 包中的 `Update` 方法。读取、检查和更新在同一个事务中完成，原因见[授权](authorization.md#checking-in-transactions)：

```go
import (
    "context"
    "database/sql"

    "github.com/cockroachdb/errors"
    "github.com/google/uuid"
    "github.com/uptrace/bun"

    "github.com/ryan053857/mygoravel/database"
    "github.com/ryan053857/mygoravel/errorx"
    "github.com/ryan053857/mygoravel/identity"
)

type Changes struct {
    Title, Slug, Content string
}

func (s *Service) Update(ctx context.Context, subject identity.Subject, id uuid.UUID, c Changes) (*Article, error) {
    item := new(Article)
    err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
        err := tx.NewSelect().Model(item).Where("id = ?", id).For("UPDATE").Scan(ctx)
        if errors.Is(err, sql.ErrNoRows) {
            return ErrNotFound.New() // ① 可预期：文章不存在
        }
        if err != nil {
            return errors.Wrap(err, "load article") // ③ 意料之外：数据库故障
        }
        if item.AuthorID != subject.ID {
            return ErrForbidden.New() // ① 可预期：不是作者
        }
        if item.Status == StatusPublished && c.Slug != item.Slug {
            return ErrSlugLocked.New() // ① 可预期：业务规则不允许
        }

        item.Title, item.Slug, item.Content = c.Title, c.Slug, c.Content
        _, err = tx.NewUpdate().Model(item).Column("title", "slug", "content").WherePK().Exec(ctx)
        if database.IsUniqueViolation(err, "articles_slug_key") {
            return ErrSlugTaken.Wrap(err, errorx.Public("slug", c.Slug)) // ② 可预期，保留底层原因
        }
        if err != nil {
            return errors.Wrap(err, "update article") // ③ 意料之外
        }
        return nil
    })
    if err != nil {
        return nil, err
    }
    return item, nil
}
```

注释中的三种写法覆盖了绝大多数场景：

1. **可预期的失败**：调用错误模板的 `New`；
2. **由底层错误引起的可预期失败**：例如唯一约束冲突，调用模板的 `Wrap`，把底层错误作为原因保留下来，方便排查。`errorx.Public` 附加的参数可以出现在提示文案中，见[在提示中带上参数](#public-parameters)；
3. **意料之外的故障**：用 `errors.Wrap` 说明“在做什么时失败了”，然后原样返回。

<a id="calling-from-handlers"></a>
### 在处理器中调用服务

处理器不需要做任何错误转换，直接返回服务的错误即可。在同一个 `update_article.go` 中，为“修改文章”定义 `UpdateArticle`，通过构造函数接收上面的业务服务。下面是处理器部分的代码；`context`、`article` 和 `identity` 分别来自标准库、应用的业务包和框架的身份包：

```go
type UpdateArticle struct {
    articles *article.Service
}

func NewUpdateArticle(articles *article.Service) *UpdateArticle {
    return &UpdateArticle{articles: articles}
}

func (a *UpdateArticle) Handle(ctx context.Context, in *UpdateArticleInput) (*ArticleOutput, error) {
    subject, _ := identity.FromContext(ctx) // 未认证时主体为空，服务会以 article.forbidden 拒绝
    item, err := a.articles.Update(ctx, subject, in.ID, article.Changes{
        Title:   in.Body.Title,
        Slug:    in.Body.Slug,
        Content: in.Body.Content,
    })
    if err != nil {
        return nil, err
    }
    return &ArticleOutput{Body: toDTO(item)}, nil
}
```

共享的 `ArticleOutput` 和 `toDTO` 定义在 `actions/dto.go` 中，完整代码见[共享响应结构](routing.md#shared-responses)。接着在父包 `articlehttp` 的 `routes.go` 的 `Register` 函数中加入下面的路由声明。`api`、`requireArticles` 和 `articles` 都由 `Register` 的参数传入，`actions` 的导入路径是 `example.com/blog/internal/article/articlehttp/actions`：

```go
op := huma.Operation{
    OperationID: "article-update",
    Method:      http.MethodPut,
    Path:        "/articles/{id}",
    Summary:     "修改文章",
}
requireArticles(&op)
rest.Register(api, op, func() func(context.Context, *actions.UpdateArticleInput) (*actions.ArticleOutput, error) {
    return actions.NewUpdateArticle(articles).Handle
})
```

`requireArticles` 要求请求携带有效的 token，见[认证](authentication.md)。处理器在工厂内部构造，因此离线查看路由或导出 OpenAPI 时不会创建它，也不会访问业务服务。

<a id="what-clients-see"></a>
### 客户端看到的响应

各种情况下，客户端收到的响应如下：

| 情况 | 状态码 | `code` |
| --- | --- | --- |
| 没有携带有效的 token | 401 | `auth.unauthenticated` |
| 标题为空、slug 格式不对 | 422 | 无，带有 `errors` 数组 |
| 文章不存在 | 404 | `article.not_found` |
| 不是文章的作者 | 403 | `article.forbidden` |
| 修改已发布文章的 slug | 422 | `article.slug_locked` |
| slug 已被其他文章使用 | 409 | `article.slug_taken` |
| 数据库故障 | 500 | 无 |

**输入格式错误**时，响应中的 `errors` 数组逐条指出哪个字段有什么问题，客户端可以把提示显示在对应的表单字段旁边：

```http
HTTP/1.1 422 Unprocessable Entity
Content-Type: application/problem+json

{
  "title": "Unprocessable Entity",
  "status": 422,
  "detail": "validation failed",
  "errors": [
    {"message": "expected string to match pattern ^[a-z0-9]+(-[a-z0-9]+)*$", "location": "body.slug", "value": "Hello World"},
    {"message": "expected length >= 1", "location": "body.title", "value": ""}
  ]
}
```

**业务错误**的响应带有 `code` 字段，客户端根据它决定如何处理；`detail` 是展示给用户的文案，启用[本地化](localization.md)后会按请求的语言翻译：

```http
HTTP/1.1 409 Conflict
Content-Type: application/problem+json

{"title": "Conflict", "status": 409, "detail": "slug“hello-world”已被使用", "code": "article.slug_taken"}
```

注意 `422` 可能来自两种情况：输入格式错误带有 `errors` 数组，业务规则不允许（`article.slug_locked`）则带有 `code`。客户端只要先看有没有 `code`，就能区分它们。

**意料之外的故障**时，客户端只会收到一个通用的 `500`，不会看到任何内部信息：

```http
HTTP/1.1 500 Internal Server Error
Content-Type: application/problem+json

{"title": "Internal Server Error", "status": 500, "detail": "unexpected error occurred"}
```

<a id="what-operators-see"></a>
### 服务端日志

安装默认中间件后，每个请求都有一条访问日志。对于业务错误，访问日志中会带上错误码，这就足够了：

```json
{"time":"…","level":"INFO","msg":"http request","method":"PUT","status":409,"duration":52417,"client":"203.0.113.7:51234","code":"article.slug_taken","request_id":"9F2K…"}
```

对于 `500`，框架还会额外记录一条错误日志。它的 `error` 字段包含完整的错误链，以及错误发生位置的堆栈：

```text
load article: dial tcp 10.0.0.5:5432: connect: connection refused
(1) attached stack trace
  -- stack trace:
  | example.com/blog/article.(*Service).Update.func1
  |     /app/article/service.go:42
  | ...
Wraps: (2) load article
Wraps: (3) dial tcp 10.0.0.5:5432: connect: connection refused
Error types: (1) *withstack.withStack (2) *errutil.withPrefix (3) *net.OpError
```

两条日志带有相同的 `request_id`，与响应头 `X-Request-ID` 一致。用户反馈问题时，凭这个 ID 就能找到对应的日志。

<a id="error-kinds"></a>
## 选择错误分类

分类描述的是失败的**性质**，与调用方式无关：同一个错误在 HTTP 接口中会被转换为[状态码](#status-mapping)，在命令行中会被转换为[退出码](commands.md#exit-codes)。定义错误时，选择最贴切的一个即可：

| 分类 | 什么时候使用 | 例子 |
| --- | --- | --- |
| `errorx.InvalidArgument` | 输入格式正确，但不符合业务规则 | 已发布文章的 slug 不能修改 |
| `errorx.Unauthenticated` | 不知道调用者是谁 | 缺少或无效的 token |
| `errorx.PermissionDenied` | 知道是谁，但无权执行 | 修改别人的文章 |
| `errorx.NotFound` | 资源不存在 | 文章已被删除 |
| `errorx.Conflict` | 与当前状态冲突 | slug 已被其他文章使用 |
| `errorx.Unavailable` | 依赖暂时不可用，稍后重试可能成功 | 图片存储服务超时 |
| `errorx.Internal` | 不应该发生的内部错误 | 数据出现了不一致 |

前五种是可预期的业务结果，调用方需要修正请求，原样重试没有意义。后两种属于系统故障，`New` 和 `Wrap` 会额外记录调用处的堆栈，见下一节。

> [!TIP]
> 能用结构体标签描述的格式要求，例如“标题不能为空”、“长度不超过 200”，请写在输入类型上，由框架自动验证。`errorx` 用于那些需要查询数据或依赖业务状态才能判断的规则。

<a id="wrapping-errors"></a>
## 处理底层错误

处理数据库、网络、文件等底层错误时，遵循一条简单的原则：**把堆栈留给意料之外的故障，把简洁的错误码留给可预期的结果。**

- 如果底层错误代表一种**可预期的结果**，用错误模板的 `Wrap` 把它转换为业务错误，就像上面例子中的唯一约束冲突。业务错误不记录堆栈，错误码已经足以说明发生了什么；
- 如果**无法归类**，在它刚进入你的代码时用 `errors.Wrap` 包装，堆栈就记录在这里。之后每经过一层，都可以再用 `errors.Wrap` 补充上下文：

```go
if _, err := s.articles.Update(ctx, subject, id, changes); err != nil {
    return errors.Wrap(err, "import article")
}
```

> [!WARNING]
> 请不要只用 `fmt.Errorf` 传递数据库驱动、标准库返回的原始错误。它们本身不带堆栈，到了错误日志中就只剩一行文本，无法知道是在哪里失败的。

<a id="public-parameters"></a>
## 在提示中带上参数

有时你希望提示文案更具体，例如告诉用户是哪个 slug 被占用了。用 `errorx.Public` 为错误附加参数：

```go
return ErrSlugTaken.Wrap(err, errorx.Public("slug", c.Slug))
```

启用[本地化](localization.md)后，参数会作为翻译模板的数据。在翻译文件中这样写：

```yaml
article.slug_taken: "slug“{{.slug}}”已被使用"
```

响应中的 `detail` 就会变成 `slug“hello-world”已被使用`。

之所以叫“公开”参数，是因为它们会出现在给用户看的文案中，请只附加可以展示给调用者的信息。参数不会作为独立字段出现在响应中。

<a id="reference"></a>
## 参考

<a id="inspecting-errors"></a>
### 判断错误

业务错误兼容 Go 标准的错误处理方式。即使被多层包装，也可以用 `errors.Is` 判断它是否来自某个错误模板：

```go
if errors.Is(err, article.ErrSlugTaken) {
    // 例如：自动在 slug 后面加上序号再试一次
}
```

也可以直接读取错误码和分类：

```go
errorx.CodeOf(err) // "article.slug_taken"，不是业务错误时为空字符串
errorx.KindOf(err) // errorx.Conflict，不是业务错误时为 errorx.Internal
```

<a id="response-format"></a>
### 响应格式

错误响应直接使用 [Huma](https://huma.rocks) 的错误结构，即 [Problem Details](https://www.rfc-editor.org/rfc/rfc9457) 格式，`Content-Type` 为 `application/problem+json`。mygoravel 只为业务错误增加了 `code` 字段：

| 字段 | 说明 |
| --- | --- |
| `status` | HTTP 状态码 |
| `title` | 状态码的标准描述 |
| `detail` | 展示给用户的文案 |
| `code` | 稳定的错误码，**只有业务错误包含** |
| `errors` | 字段级的错误明细，**只有输入格式错误包含**，见[验证](validation.md#validation-error-response) |

客户端可以这样区分错误：有 `code` 时按 `code` 处理；有 `errors` 时把提示显示在对应字段旁边；两者都没有时（路由不存在、服务端故障等）按 `status` 处理。

`detail` 的内容：启用本地化时，以错误码为消息 ID 翻译，找不到翻译时使用状态码描述并记录一条警告日志；未启用本地化时，使用状态码描述，例如 `Not Found`。

> [!WARNING]
> `detail` 可能随语言和版本变化，请不要根据它做逻辑判断。

<a id="status-mapping"></a>
### 状态码映射

| 分类 | HTTP 状态 |
| --- | --- |
| `errorx.InvalidArgument` | 422 Unprocessable Entity |
| `errorx.Unauthenticated` | 401 Unauthorized |
| `errorx.PermissionDenied` | 403 Forbidden |
| `errorx.NotFound` | 404 Not Found |
| `errorx.Conflict` | 409 Conflict |
| `errorx.Unavailable` | 503 Service Unavailable |
| `errorx.Internal` 及其他错误 | 500 Internal Server Error |

<a id="reporting-errors"></a>
### 错误日志

mygoravel 遵循“**只在边界记录一次**”的原则：业务代码只负责补充上下文并返回错误，不需要自己记录错误日志，否则同一个错误会在日志中出现很多遍。

| 日志 | 何时记录 | 内容 |
| --- | --- | --- |
| 访问日志 | 每个请求 | 方法、状态码、耗时；业务错误还包含 `code` |
| 错误日志 | 仅 `5xx` | 错误码和完整的错误链（`%+v` 格式，含堆栈） |

需要为访问日志追加其他字段时，可以在处理过程中调用 `httpx.AddAccessLogAttrs(ctx, attrs...)`。日志的配置方式见[日志](logging.md)。

<a id="huma-errors"></a>
### 原生 Huma 错误

处理器也可以直接返回 Huma 的错误，例如 `huma.Error404NotFound("article does not exist")`。它们会被原样输出，保留你写的 `detail`，但没有 `code`，也不会被翻译。需要稳定的错误码时，请使用 `errorx`。

<a id="cross-process-errors"></a>
### 跨进程传递

通过 gRPC、消息队列等方式在服务之间传递错误时，可以使用 `cockroachdb/errors` 的编解码功能。`errorx` 已经注册了编解码器，还原后错误码、分类、参数和 `errors.Is` 的结果都保持不变：

```go
encoded := errors.EncodeError(ctx, err)     // 发送方
decoded := errors.DecodeError(ctx, encoded) // 接收方
errors.Is(decoded, article.ErrNotFound)     // true
```

参数以 JSON 形式传输，还原后数值类型会变为 `float64`。
