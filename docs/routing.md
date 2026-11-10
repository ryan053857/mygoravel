# 路由

- [基本路由](#basic-routing)
    - [路由声明](#operations)
    - [处理器工厂](#handler-factories)
    - [注册时的检查](#registration-checks)
- [路由参数](#route-parameters)
- [访问策略](#access-policies)
- [组织路由](#organizing-routes)
    - [模块内注册](#module-registration)
    - [按业务动作组织处理器](#http-actions)
    - [共享响应结构](#shared-responses)
    - [查看全部路由](#listing-routes)
- [OpenAPI 文档](#openapi)
    - [在线查看](#viewing-docs)
    - [离线导出](#exporting-openapi)
- [使用原生 chi 与 Huma](#native-apis)
    - [注册普通 HTTP 处理器](#plain-handlers)
    - [使用原生 huma.Register](#native-huma-register)

<a id="basic-routing"></a>
## 基本路由

mygoravel 的路由建立在 [Huma](https://huma.rocks) 之上。每条路由由三部分组成：一个 `huma.Operation` 描述路由本身，一个**输入类型**描述请求，一个**输出类型**描述响应。下面是[安装](installation.md#entrypoint)一章中注册 `/ping` 的代码：

```go
type PingInput struct{}

type PingOutput struct {
    Body struct {
        Message string `json:"message"`
    }
}

func registerPing(api *rest.API) {
    op := huma.Operation{
        OperationID: "ping",
        Method:      http.MethodGet,
        Path:        "/ping",
        Summary:     "检查 API 是否可响应",
    }
    rest.Public()(&op)
    rest.Register(api, op, func() func(context.Context, *PingInput) (*PingOutput, error) {
        return func(ctx context.Context, in *PingInput) (*PingOutput, error) {
            out := &PingOutput{}
            out.Body.Message = "pong"
            return out, nil
        }
    })
}
```

其中 `api` 是由 `rest.New` 创建的 API 实例：

```go
api := rest.New(rest.Options{
    Title:   "Hello API",
    Version: "1.0.0",
})
```

`Title` 和 `Version` 会出现在生成的 OpenAPI 文档中。

<a id="operations"></a>
### 路由声明

一条完整的路由声明包含以下几项：

| 声明 | 作用 |
| --- | --- |
| `OperationID` | 路由的唯一标识，会出现在 OpenAPI 文档中，例如 `article-create` |
| `Method`、`Path` | HTTP 方法与路径 |
| `Summary` | 路由的简短描述，显示在接口文档中 |
| 输入类型 | 路径参数、查询参数、请求头和请求体的来源、类型及验证规则 |
| 输出类型 | 响应体、状态码和响应头 |
| 访问策略 | `rest.Public()` 或 `rest.RequireBearer(...)`，必须选择一个 |
| 处理器工厂 | 返回真正处理请求的函数 |

由于请求和响应的结构都写在 Go 类型中，Huma 可以据此自动完成参数解析、输入验证，并生成准确的 OpenAPI 文档。输入和输出类型的写法分别见[请求](requests.md)和[响应](responses.md)。

<a id="handler-factories"></a>
### 处理器工厂

你可能已经注意到，`rest.Register` 的最后一个参数不是处理器本身，而是一个**返回处理器的函数**。处理器的签名始终是：

```go
func(ctx context.Context, in *Input) (*Output, error)
```

之所以多包一层，是因为 API 有两种运行模式：

- **运行模式**（`rest.ModeServe`，默认）：`rest.Register` 调用工厂，得到处理器并挂载到路由上；
- **文档模式**（`rest.ModeOpenAPI`）：只生成接口文档，**不会调用工厂**。

这意味着在文档模式下，处理器依赖的数据库、缓存等资源都不需要存在。你可以在没有任何外部依赖的环境中（例如 CI）导出接口文档，详见[离线导出](#exporting-openapi)。

<a id="registration-checks"></a>
### 注册时的检查

`rest.Register` 会在注册时检查以下问题，一旦发现就直接 panic，让错误在应用启动时立刻暴露：

- 路由没有声明访问策略（没有调用 `Public` 或 `RequireBearer`）；
- `OperationID` 与已注册的路由重复；
- 与已注册的路由方法相同且路径形状冲突，例如 `/articles/{id}` 与 `/articles/{articleID}`。

<a id="route-parameters"></a>
## 路由参数

路径中用花括号声明参数，并在输入类型中用 `path` 标签读取：

```go
type GetArticleInput struct {
    ID uuid.UUID `path:"id"`
}

op := huma.Operation{
    OperationID: "article-get",
    Method:      http.MethodGet,
    Path:        "/articles/{id}",
}
```

Huma 会把路径中的值转换为字段的类型（这里是 `github.com/google/uuid` 的 `uuid.UUID`），转换失败时自动返回验证错误。查询参数、请求头和请求体的读取方式见[请求](requests.md)。

<a id="access-policies"></a>
## 访问策略

mygoravel 要求每条路由都**显式**声明访问策略，避免因为疏忽而把需要保护的接口暴露出去。

公开路由在注册前调用 `rest.Public()`：

```go
rest.Public()(&op)
```

需要认证的路由使用 `rest.RequireBearer`，并可以要求调用者具备指定的 scope：

```go
requireArticles := rest.RequireBearer(api, validateToken, "articles")
requireArticles(&op)
```

`validateToken` 是你提供的 token 校验函数。`RequireBearer` 会同时完成两件事：在运行时检查请求的 `Authorization` 头，以及在 OpenAPI 文档中标记这条路由需要认证。完整用法见[认证](authentication.md)。

<a id="organizing-routes"></a>
## 组织路由

推荐在每个业务模块的 HTTP 子包中保留 `routes.go`，集中声明该模块的路由；在它旁边创建 `actions` 子目录，存放请求处理动作。目录名表达代码的职责，每个文件用“动作 + 业务对象”表达具体用途。这样你可以在 `routes.go` 中查看路径、方法和访问策略，再跳转到 `actions` 中的实现：

```text
internal/article/articlehttp/
├── routes.go                 注册文章模块的路由
└── actions/                  HTTP 请求处理动作
    ├── create_article.go     创建文章
    ├── get_article.go        获取文章
    ├── publish_article.go    发布文章
    └── dto.go                动作共享的响应结构与转换
```

下面给出这五个文件的完整代码。`article` 是同一应用中的业务包，业务服务的实现见[错误处理](errors.md#returning-errors)和[授权](authorization.md#ownership)。

> [!NOTE]
> 单文件 action 是文档推荐的代码组织方式。框架不检查文件名、目录或 action 类型，也不要求实现某个 `Action` 接口；普通函数同样可以作为处理器。小型示例可以保持在一个文件中。

<a id="module-registration"></a>
### 模块内注册

`routes.go` 属于 `articlehttp` 包，提供模块的统一 `Register` 入口。它导入 `actions` 子包，只声明路由并绑定对应的 action：

```go
package articlehttp

import (
    "context"
    "net/http"

    "github.com/danielgtaylor/huma/v2"

    "example.com/blog/internal/article"
    "example.com/blog/internal/article/articlehttp/actions"
    "github.com/ryan053857/mygoravel/httpx/rest"
)

// Register 注册文章模块的全部路由。
func Register(api *rest.API, requireArticles func(*huma.Operation), articles *article.Service) {
    create := huma.Operation{
        OperationID:   "article-create",
        Method:        http.MethodPost,
        Path:          "/articles",
        Summary:       "创建文章",
        DefaultStatus: http.StatusCreated,
    }
    requireArticles(&create)
    rest.Register(api, create, func() func(context.Context, *actions.CreateArticleInput) (*actions.ArticleOutput, error) {
        return actions.NewCreateArticle(articles).Handle
    })

    get := huma.Operation{
        OperationID: "article-get",
        Method:      http.MethodGet,
        Path:        "/articles/{id}",
        Summary:     "获取文章",
    }
    requireArticles(&get)
    rest.Register(api, get, func() func(context.Context, *actions.GetArticleInput) (*actions.ArticleOutput, error) {
        return actions.NewGetArticle(articles).Handle
    })

    publish := huma.Operation{
        OperationID: "article-publish",
        Method:      http.MethodPost,
        Path:        "/articles/{id}/publish",
        Summary:     "发布文章",
    }
    requireArticles(&publish)
    rest.Register(api, publish, func() func(context.Context, *actions.PublishArticleInput) (*actions.ArticleOutput, error) {
        return actions.NewPublishArticle(articles).Handle
    })
}
```

action 的构造函数在处理器工厂内部调用。文档模式不会执行这些工厂，因此生成 OpenAPI 或查看路由时不需要创建业务服务、打开数据库。

入口在组装应用时调用模块的注册函数，并决定这些路由的访问策略。这里的 `articles` 是入口构造的 `*article.Service`，`validateToken` 是 token 校验器，完整的组装代码见[依赖装配](dependencies.md#assembling-the-application)：

```go
articlehttp.Register(api, rest.RequireBearer(api, validateToken, "articles"), articles)
```

<a id="http-actions"></a>
### 按业务动作组织处理器

`actions` 目录中的文件都属于 `actions` 包，每个动作单独放在一个文件中，包含输入类型、依赖、构造函数和 `Handle` 方法。目录已经标明 action 的职责，文件名和类型名只需表达业务意图，例如 `create_article.go` 和 `CreateArticle`，不必重复添加 `action_` 前缀或 `Action` 后缀。

`actions/create_article.go` 定义创建文章的动作：

```go
package actions

import (
    "context"

    "example.com/blog/internal/article"
    "github.com/ryan053857/mygoravel/identity"
)

type CreateArticleInput struct {
    Body struct {
        Title   string `json:"title" minLength:"1" maxLength:"200"`
        Slug    string `json:"slug" pattern:"^[a-z0-9]+(-[a-z0-9]+)*$" maxLength:"100"`
        Content string `json:"content"`
    }
}

type CreateArticle struct {
    articles *article.Service
}

func NewCreateArticle(articles *article.Service) *CreateArticle {
    return &CreateArticle{articles: articles}
}

func (a *CreateArticle) Handle(ctx context.Context, in *CreateArticleInput) (*ArticleOutput, error) {
    subject, _ := identity.FromContext(ctx)
    item, err := a.articles.Create(ctx, subject, article.Changes{
        Title: in.Body.Title, Slug: in.Body.Slug, Content: in.Body.Content,
    })
    if err != nil {
        return nil, err
    }
    return &ArticleOutput{Body: toDTO(item)}, nil
}
```

`actions/get_article.go` 定义获取文章的动作：

```go
package actions

import (
    "context"

    "github.com/google/uuid"

    "example.com/blog/internal/article"
    "github.com/ryan053857/mygoravel/identity"
)

type GetArticleInput struct {
    ID uuid.UUID `path:"id"`
}

type GetArticle struct {
    articles *article.Service
}

func NewGetArticle(articles *article.Service) *GetArticle {
    return &GetArticle{articles: articles}
}

func (a *GetArticle) Handle(ctx context.Context, in *GetArticleInput) (*ArticleOutput, error) {
    subject, _ := identity.FromContext(ctx)
    item, err := a.articles.Get(ctx, subject, in.ID)
    if err != nil {
        return nil, err
    }
    return &ArticleOutput{Body: toDTO(item)}, nil
}
```

`actions/publish_article.go` 定义发布文章的动作：

```go
package actions

import (
    "context"

    "github.com/google/uuid"

    "example.com/blog/internal/article"
    "github.com/ryan053857/mygoravel/identity"
)

type PublishArticleInput struct {
    ID uuid.UUID `path:"id"`
}

type PublishArticle struct {
    articles *article.Service
}

func NewPublishArticle(articles *article.Service) *PublishArticle {
    return &PublishArticle{articles: articles}
}

func (a *PublishArticle) Handle(ctx context.Context, in *PublishArticleInput) (*ArticleOutput, error) {
    subject, _ := identity.FromContext(ctx)
    item, err := a.articles.Publish(ctx, subject, in.ID)
    if err != nil {
        return nil, err
    }
    return &ArticleOutput{Body: toDTO(item)}, nil
}
```

每个 `Handle` 只做三件事：把输入转换为业务参数、调用业务服务、把结果转换为输出。业务规则，包括“只有作者能发布”、“不能重复发布”，都留在服务中。不同 action 使用各自的输入类型，后续可以独立增加字段或验证规则。

action 在注册路由时构造，其 `Handle` 会被多个请求并发调用。只在字段中保存可安全共享的依赖；请求数据保存在方法参数和局部变量中。

<a id="shared-responses"></a>
### 共享响应结构

这三个动作返回同一种文章表示，因此将 `ArticleDTO`、`ArticleOutput` 和转换函数放在 `actions/dto.go` 中共享。它们和处理器属于同一个 `actions` 包，转换函数 `toDTO` 可以保持不导出。某个动作独有的输出类型可以直接放在该 action 文件中：

```go
package actions

import (
    "time"

    "example.com/blog/internal/article"
)

type ArticleDTO struct {
    ID          string     `json:"id"`
    Title       string     `json:"title"`
    Slug        string     `json:"slug"`
    AuthorID    string     `json:"author_id"`
    Status      string     `json:"status"`
    PublishedAt *time.Time `json:"published_at,omitempty"`
}

type ArticleOutput struct {
    Body ArticleDTO
}

func toDTO(a *article.Article) ArticleDTO {
    return ArticleDTO{
        ID: a.ID.String(), Title: a.Title, Slug: a.Slug,
        AuthorID: a.AuthorID, Status: a.Status, PublishedAt: a.PublishedAt,
    }
}
```

依赖方向是 `articlehttp → actions → article`：路由入口绑定动作，动作调用业务服务。共享响应放在 `actions` 包内，避免动作为了使用 DTO 而反过来导入 `articlehttp`，造成循环依赖。无需为每个动作再创建一个子包；这仍然是推荐的组织约定，框架不要求这种目录结构。请求和响应类型的详细写法分别见[请求](requests.md)和[响应](responses.md)。

<a id="listing-routes"></a>
### 查看全部路由

模块增多之后，你可以通过应用的 `routes` 命令查看全局路由表，无需逐个打开模块。按[命令行](commands.md#routes-command)一章安装命令后，执行：

```bash
./bin/api routes
```

该命令复用应用的路由注册入口，离线生成路由表，不需要启动 HTTP 服务或连接数据库。路由的真实声明仍保存在各模块的 `routes.go` 中，查看命令不需要另一份手工维护的路由清单。

<a id="openapi"></a>
## OpenAPI 文档

<a id="viewing-docs"></a>
### 在线查看

应用运行时，Huma 会自动提供接口文档：

- `/docs`：可交互的接口文档页面；
- `/openapi.json`：OpenAPI 3.1 文档。

<a id="exporting-openapi"></a>
### 离线导出

很多时候，你希望在不启动服务、不连接数据库的情况下生成 OpenAPI 文档，例如在 CI 中检查接口变更，或者为前端生成客户端代码。只需在创建 API 时指定文档模式，然后调用同一个注册函数：

```go
api := rest.New(rest.Options{
    Title: "Hello API", Version: "1.0.0",
    Mode:  rest.ModeOpenAPI,
})
registerPing(api)

data, err := api.OpenAPIJSON()
```

`data` 就是 OpenAPI 文档的 JSON 内容。因为运行模式和文档模式使用的是同一份路由声明，导出的文档与实际运行的接口始终保持一致。

> [!TIP]
> 结合[命令行](commands.md#openapi-command)中的 `openapi` 命令，你可以通过 `./bin/api openapi > openapi.json` 导出文档。

<a id="native-apis"></a>
## 使用原生 chi 与 Huma

<a id="plain-handlers"></a>
### 注册普通 HTTP 处理器

`api.Router()` 返回底层的 `chi.Router`，你可以直接在上面注册普通的 `http.Handler`，例如健康检查：

```go
api.Router().Get("/livez", func(w http.ResponseWriter, r *http.Request) {
    w.WriteHeader(http.StatusNoContent)
})
```

这类路由同样会经过 Router 上安装的中间件（例如请求 ID、访问日志、panic 恢复），但不会出现在 OpenAPI 文档中，也没有类型化的输入验证。对于业务接口，请使用 `rest.Register`。

<a id="native-huma-register"></a>
### 使用原生 huma.Register

`rest.API` 内嵌了 `huma.API`，因此你也可以调用 Huma 原生的 `huma.Register`。不过这样做会绕过 mygoravel 的访问策略检查、业务错误转换和文档模式支持，这些都需要你自己处理。除非有特殊需求，否则推荐使用 `rest.Register`。

`rest.New` 不安装任何中间件，中间件的安装方式请参阅[中间件](middleware.md)。
