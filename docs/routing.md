# 路由

- [基本路由](#basic-routing)
    - [路由声明](#operations)
    - [处理器](#handlers)
    - [注册时的检查](#registration-checks)
- [路由参数](#route-parameters)
- [访问策略](#access-policies)
- [组织路由](#organizing-routes)
    - [模块内注册](#module-registration)
    - [action 的 HTTP 入口](#http-actions)
    - [共享响应结构](#shared-responses)
    - [查看全部路由](#listing-routes)
- [API 版本](#api-versioning)
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
    rest.Register(api, op, func(ctx context.Context, in *PingInput) (*PingOutput, error) {
        out := &PingOutput{}
        out.Body.Message = "pong"
        return out, nil
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
| 处理器 | 处理请求的函数，通常是 [action](actions.md) 的 `HTTP` 方法 |

由于请求和响应的结构都写在 Go 类型中，Huma 可以据此自动完成参数解析、输入验证，并生成准确的 OpenAPI 文档。输入和输出类型的写法分别见[请求](requests.md)和[响应](responses.md)。

<a id="handlers"></a>
### 处理器

处理器的签名始终是：

```go
func(ctx context.Context, in *Input) (*Output, error)
```

业务接口的处理器通常是 [action](actions.md) 的方法值，例如 `article.NewPublish(db).HTTP`。

API 有两种运行模式：

- **运行模式**（`rest.ModeServe`，默认）：处理器挂载到路由上；
- **文档模式**（`rest.ModeOpenAPI`）：只生成接口文档，**不会调用处理器**，也可以传 `nil`。

文档模式下路由照常注册，action 也会被构造，只是传入的数据库等依赖都是 `nil`。只要构造函数只保存依赖、不使用它们，就可以在没有任何外部依赖的环境中（例如 CI）导出接口文档，详见[离线导出](#exporting-openapi)。

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

业务代码按 [action](actions.md) 组织，每个业务动作一个文件，HTTP 入口是 action 的 `HTTP` 方法。每个业务模块再用一个 `routes.go` 集中声明自己的路由，你可以在这里查看路径、方法和访问策略，再跳转到对应的 action：

```text
internal/article/
├── article.go      模型、业务错误与共享的规则
├── create.go       Create：创建文章
├── get.go          Get：获取文章
├── publish.go      Publish：发布文章
├── dto.go          共享的响应结构与转换
└── routes.go       注册文章模块的路由
```

<a id="module-registration"></a>
### 模块内注册

`internal/article/routes.go` 与 action 同属 `article` 包，提供模块的统一 `Register` 入口。它只声明路由，并把 action 的 `HTTP` 方法注册为处理器：

```go
package article

import (
    "net/http"

    "github.com/danielgtaylor/huma/v2"
    "github.com/uptrace/bun"

    "github.com/ryan053857/mygoravel/httpx/rest"
)

// Register 注册文章模块的全部路由。
func Register(api *rest.API, requireArticles func(*huma.Operation), db *bun.DB) {
    create := huma.Operation{
        OperationID:   "article-create",
        Method:        http.MethodPost,
        Path:          "/articles",
        Summary:       "创建文章",
        DefaultStatus: http.StatusCreated,
    }
    requireArticles(&create)
    rest.Register(api, create, NewCreate(db).HTTP)

    get := huma.Operation{
        OperationID: "article-get",
        Method:      http.MethodGet,
        Path:        "/articles/{id}",
        Summary:     "获取文章",
    }
    requireArticles(&get)
    rest.Register(api, get, NewGet(db).HTTP)

    publish := huma.Operation{
        OperationID: "article-publish",
        Method:      http.MethodPost,
        Path:        "/articles/{id}/publish",
        Summary:     "发布文章",
    }
    requireArticles(&publish)
    rest.Register(api, publish, NewPublish(db).HTTP)
}
```

`Register` 接收模块的 action 需要的依赖，在注册时构造它们。生成 OpenAPI 或查看路由时，`db` 为 `nil`，处理器不会被调用。

入口在组装应用时调用模块的注册函数，并决定这些路由的访问策略。这里的 `db` 是入口打开的数据库，`validateToken` 是 token 校验器，完整的组装代码见[依赖装配](dependencies.md#assembling-the-application)：

```go
article.Register(api, rest.RequireBearer(api, validateToken, "articles"), db)
```

<a id="http-actions"></a>
### action 的 HTTP 入口

`publish.go` 的完整代码见[编写 action](actions.md#writing-actions)。`create.go` 和 `get.go` 的写法相同，下面是 `internal/article/get.go`：

```go
package article

import (
    "context"

    "github.com/google/uuid"
    "github.com/uptrace/bun"

    "github.com/ryan053857/mygoravel/identity"
)

// Get 获取文章。
type Get struct {
    db *bun.DB
}

func NewGet(db *bun.DB) *Get {
    return &Get{db: db}
}

// Handle 返回一篇文章。草稿只有作者本人能看到。
func (a *Get) Handle(ctx context.Context, subject identity.Subject, id uuid.UUID) (*Article, error) {
    if subject.ID == "" {
        return nil, ErrForbidden.New()
    }
    item, err := find(ctx, a.db, id)
    if err != nil {
        return nil, err
    }
    if item.Status == StatusDraft && item.AuthorID != subject.ID {
        return nil, ErrNotFound.New() // 别人的草稿，当作不存在
    }
    return item, nil
}

type GetInput struct {
    ID uuid.UUID `path:"id"`
}

// HTTP 处理 GET /articles/{id}。
func (a *Get) HTTP(ctx context.Context, in *GetInput) (*ArticleOutput, error) {
    subject, _ := identity.FromContext(ctx)
    item, err := a.Handle(ctx, subject, in.ID)
    if err != nil {
        return nil, err
    }
    return &ArticleOutput{Body: toDTO(item)}, nil
}
```

`create.go` 的输入类型直接使用模块的 `Changes` 作为请求体，验证规则写在 `Changes` 上，见[数据对象](actions.md#data-objects)：

```go
type CreateInput struct {
    Body Changes
}
```

每个 `HTTP` 方法只做三件事：从输入和 context 中取出参数、调用 `Handle`、把结果转换为输出。业务规则，包括“只有作者能发布”、“不能重复发布”，都在 `Handle` 中。不同 action 使用各自的输入类型，后续可以独立增加字段或验证规则。

<a id="shared-responses"></a>
### 共享响应结构

这几个 action 返回同一种文章表示，因此把 `ArticleDTO`、`ArticleOutput` 和转换函数放在 `internal/article/dto.go` 中共享。某个 action 独有的输出类型直接放在它自己的文件中：

```go
package article

import "time"

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

func toDTO(a *Article) ArticleDTO {
    return ArticleDTO{
        ID: a.ID.String(), Title: a.Title, Slug: a.Slug,
        AuthorID: a.AuthorID, Status: a.Status, PublishedAt: a.PublishedAt,
    }
}
```

`ArticleDTO` 决定了 API 返回哪些字段。不要把模型 `Article` 直接作为响应体：给模型加一个内部字段，就会意外地把它暴露给客户端。请求和响应类型的详细写法分别见[请求](requests.md)和[响应](responses.md)。

<a id="listing-routes"></a>
### 查看全部路由

模块增多之后，你可以通过应用的 `routes` 命令查看全局路由表，无需逐个打开模块。按[命令行](commands.md#routes-command)一章安装命令后，执行：

```bash
./bin/api routes
```

该命令复用应用的路由注册入口，离线生成路由表，不需要启动 HTTP 服务或连接数据库。路由的真实声明仍保存在各模块的 `routes.go` 中，查看命令不需要另一份手工维护的路由清单。

<a id="api-versioning"></a>
## API 版本

只提供 JSON API 的应用通常直接挂在根路径，由部署决定前缀，例如 `https://api.example.com/v1`。和[页面](views.md)共用一个 Router 时，把 API 挂在带版本号的子路由下，并把同一个前缀设为 `BasePath`：

```go
router.Route("/api/v1", func(r chi.Router) {
    r.Use(httpx.Timeout(cfg.HTTP.Timeout))
    opts := API
    opts.Router, opts.BasePath = r, "/api/v1"
    Routes(rest.New(opts), auth.Validator(cfg.Auth), articles)
})
```

路由按子路由注册，接口地址是 `/api/v1/articles`。`BasePath` 写入 OpenAPI 文档的 `servers`，文档页 `/api/v1/docs` 和生成的客户端据此拼出完整地址；`routes` 命令显示的也是完整路径。

需要不兼容的修改时，新版本挂在另一个子路由下，用自己的 `rest.API`。两个版本各有一份 OpenAPI 文档，共用同一套 action：

```go
router.Route("/api/v2", func(r chi.Router) {
    r.Use(httpx.Timeout(cfg.HTTP.Timeout))
    opts := APIv2
    opts.Router, opts.BasePath = r, "/api/v2"
    RoutesV2(rest.New(opts), auth.Validator(cfg.Auth), articles)
})
```

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
