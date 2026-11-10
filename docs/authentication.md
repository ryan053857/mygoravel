# 认证

- [简介](#introduction)
    - [核心概念](#key-concepts)
- [编写 Token 校验器](#token-validators)
- [保护路由](#protecting-routes)
    - [认证失败的响应](#failed-authentication)
- [会话认证](#session-authentication)
    - [登录与登出](#login-and-logout)
    - [保护会话路由](#protecting-session-routes)
    - [保护页面](#protecting-pages)
- [获取已认证的主体](#retrieving-the-subject)
- [管理凭据](#managing-credentials)

<a id="introduction"></a>
## 简介

很多 API 都需要知道“是谁在调用”。mygoravel 内置两种认证方式：基于 Bearer Token 的认证，客户端在请求头中携带 `Authorization: Bearer <token>`，框架调用你提供的校验函数确认身份，然后把身份信息交给处理器；以及面向浏览器的[会话认证](#session-authentication)。

mygoravel 不限定 token 的种类和来源。它可以是数据库中的 API Key，也可以是由认证服务签发的 JWT——你只需要写一个函数，告诉框架这个 token 对应谁。

> [!NOTE]
> 认证回答的是“调用者是谁”，而“调用者能不能做这件事”属于授权，请参阅[授权](authorization.md)。

<a id="key-concepts"></a>
### 核心概念

**主体（Subject）** 表示一个已经通过认证的调用者，它由 `identity` 包定义：

```go
type Subject struct {
    ID     string
    Type   SubjectType
    Method string
    Scopes []string
}
```

| 字段 | 说明 |
| --- | --- |
| `ID` | 调用者的唯一标识，是一个不透明的字符串，例如用户 ID |
| `Type` | 调用者类别：`identity.SubjectUser`（用户）或 `identity.SubjectService`（服务） |
| `Method` | 认证方式，例如 `bearer` 或 `session` |
| `Scopes` | 调用者被授予的访问范围，例如 `articles` |

**校验器（TokenValidator）** 是你提供的函数，负责把 token 转换为主体：

```go
type TokenValidator func(ctx huma.Context, token string) (identity.Subject, error)
```

<a id="token-validators"></a>
## 编写 Token 校验器

下面是博客应用完整的 `internal/auth/auth.go`。为了便于理解，它把可用的 token 直接写在配置文件中，每个 token 对应一个调用者和它的 scope：

```go
package auth

import (
    "errors"

    "github.com/danielgtaylor/huma/v2"

    "github.com/ryan053857/mygoravel/errorx"
    "github.com/ryan053857/mygoravel/httpx/rest"
    "github.com/ryan053857/mygoravel/identity"
)

var ErrUnauthenticated = errorx.Define(errorx.Unauthenticated, "auth.unauthenticated")

// Credential 是一个 API token 及其代表的调用者。
type Credential struct {
    Token     string   `yaml:"token"`
    SubjectID string   `yaml:"subject_id"`
    Scopes    []string `yaml:"scopes"`
}

type Config struct {
    Credentials []Credential `yaml:"credentials"`
}

func (c Config) Validate() error {
    seen := make(map[string]bool, len(c.Credentials))
    for _, cred := range c.Credentials {
        if cred.Token == "" || cred.SubjectID == "" {
            return errors.New("credentials: token and subject_id are required")
        }
        if seen[cred.Token] {
            return errors.New("credentials: duplicate token")
        }
        seen[cred.Token] = true
    }
    return nil
}

// Validator 返回一个在配置的凭据中查找 token 的校验器。
func Validator(cfg Config) rest.TokenValidator {
    byToken := make(map[string]Credential, len(cfg.Credentials))
    for _, cred := range cfg.Credentials {
        byToken[cred.Token] = cred
    }
    return func(_ huma.Context, token string) (identity.Subject, error) {
        cred, ok := byToken[token]
        if !ok {
            return identity.Subject{}, ErrUnauthenticated.New()
        }
        return identity.Subject{
            ID:     cred.SubjectID,
            Type:   identity.SubjectUser,
            Method: "bearer",
            Scopes: cred.Scopes,
        }, nil
    }
}
```

在配置文件中这样写，并把 `auth.Config` 加入应用的[根配置结构体](configuration.md#loading-configuration)（字段名为 `Auth`，配置段为 `auth`）：

```yaml
auth:
  credentials:
    - token: ${API_TOKEN_ALICE}
      subject_id: alice
      scopes: [articles]
```

`Validate` 会在加载配置时检查空 token 和重复的 token，配置有误时应用无法启动。

在实际应用中，校验器通常会查询数据库中的 API Key，或者使用成熟的库验证 JWT 的签名和有效期。无论采用哪种方式，校验器都应当在返回主体之前完成**全部**的有效性检查：签名、过期时间、是否已吊销，以及权限范围的来源。

校验器的返回值决定了请求的结果：

| 校验器返回 | 结果 |
| --- | --- |
| 有效的主体（`ID` 非空） | 认证通过 |
| `errorx.Unauthenticated` 等 4xx 类错误，或 `ID` 为空的主体 | `401` |
| `errorx.Unavailable` 等 5xx 类错误 | 对应的 `5xx`，例如认证服务暂时不可用时返回 `503` |

<a id="protecting-routes"></a>
## 保护路由

使用 `rest.RequireBearer` 为路由启用认证。它的参数是 API 实例、校验器，以及调用者必须具备的 scope。

下面是 `articlehttp` 包中注册“获取文章”接口的片段：`api` 由 `rest.New` 创建，`cfg.Auth` 来自应用配置，`articles` 是已经构造好的业务服务。`actions` 的导入路径是 `example.com/blog/internal/article/articlehttp/actions`，其中的输入、输出和处理器定义见[路由](routing.md#organizing-routes)：

```go
requireArticles := rest.RequireBearer(api, auth.Validator(cfg.Auth), "articles")

op := huma.Operation{
    OperationID: "article-get",
    Method:      http.MethodGet,
    Path:        "/articles/{id}",
}
requireArticles(&op)
rest.Register(api, op, func() func(context.Context, *actions.GetArticleInput) (*actions.ArticleOutput, error) {
    return actions.NewGetArticle(articles).Handle
})
```

`RequireBearer` 返回的函数可以复用于多条路由。可以传入多个 scope，调用者必须**全部**具备才能访问；不传 scope 则只要求认证通过。

`RequireBearer` 同时做了两件事：

- 在运行时检查 `Authorization` 请求头并调用校验器；
- 在 OpenAPI 文档中把这条路由标记为需要 Bearer 认证，所需的 scope 记录在 `x-required-scopes` 扩展字段中。

需要认证的响应还会自动加上 `Cache-Control: no-store`，防止响应被中间代理缓存。

`RequireBearer` 基于更通用的 `rest.RequireAuth` 实现，[会话认证](#protecting-session-routes)的 `session.Require` 也是。需要其他认证方式（例如从自定义请求头读取 API Key）时，可以用 `RequireAuth` 声明自己的 OpenAPI 安全方案和认证函数，认证失败的响应与本章相同。

> [!TIP]
> 不需要认证的路由也必须显式调用 `rest.Public()(&op)`。忘记声明访问策略的路由会在注册时 panic，因此你不会意外地发布一个未受保护的接口。

<a id="failed-authentication"></a>
### 认证失败的响应

| 情况 | 状态码 | 错误码 |
| --- | --- | --- |
| 没有携带 `Authorization` 头，或格式不是 `Bearer <token>` | 401 | `auth.unauthenticated` |
| 校验器拒绝了 token | 401 | `auth.unauthenticated` |
| 缺少所需的 scope | 403 | `auth.forbidden` |
| 校验器返回 5xx 类错误 | 与错误分类对应，例如 503 | 无 |

无论校验器返回什么错误码，客户端看到的都是统一的 `auth.unauthenticated`，不会因此得知 token 是“不存在”还是“已过期”。`401` 响应带有 `WWW-Authenticate: Bearer` 响应头。校验器返回 5xx 类错误时，响应的 `detail` 为 `authentication unavailable`，不包含校验器的错误码；框架会记录一条诊断日志。

例如，不带 token 创建文章：

```bash
curl -i -X POST http://localhost:8080/articles \
  -H 'Content-Type: application/json' \
  -d '{"title":"Hello World","slug":"hello-world","content":"……"}'
```

```http
HTTP/1.1 401 Unauthorized
Content-Type: application/problem+json
Cache-Control: no-store
Www-Authenticate: Bearer

{"title":"Unauthorized","status":401,"detail":"Unauthorized","code":"auth.unauthenticated"}
```

启用[本地化](localization.md)后，`detail` 会被翻译，例如中文为“请先认证”。

认证在输入验证之前执行，所以未认证的请求不会看到验证错误。

<a id="session-authentication"></a>
## 会话认证

Bearer token 适合服务端调用方和移动端。浏览器中的前端更常用[会话](session.md)：用户登录后，服务器把主体记在会话里，浏览器通过 cookie 自动携带会话 token。

本节为博客应用加上编辑后台：编辑在浏览器中用邮箱和密码登录，之后访问后台的文章接口。开始之前，先按[装配会话](session.md#assembling-sessions)创建会话管理器 `sessions`，并安装它的中间件和跨站请求伪造防护。

<a id="login-and-logout"></a>
### 登录与登出

登录接口先校验密码（见[哈希](hashing.md#hashing-passwords)），然后调用 `session.Login` 把主体记入会话。下面是 `adminhttp` 包中的登录和登出处理器，`users` 是应用自己的用户服务，`sessions` 由入口传入：

```go
type LoginInput struct {
    Body struct {
        Email    string `json:"email" format:"email"`
        Password string `json:"password" minLength:"1"`
    }
}

func login(users *user.Service, sessions *scs.SessionManager) func(context.Context, *LoginInput) (*struct{}, error) {
    return func(ctx context.Context, in *LoginInput) (*struct{}, error) {
        u, err := users.Authenticate(ctx, in.Body.Email, in.Body.Password)
        if err != nil {
            return nil, err // 例如 errorx.Unauthenticated 类错误，响应 401
        }
        return nil, session.Login(ctx, sessions, identity.Subject{
            ID:     u.ID,
            Type:   identity.SubjectUser,
            Scopes: u.Scopes, // 例如 [articles]
        })
    }
}

func logout(sessions *scs.SessionManager) func(context.Context, *struct{}) (*struct{}, error) {
    return func(ctx context.Context, _ *struct{}) (*struct{}, error) {
        return nil, session.Logout(ctx, sessions)
    }
}
```

`Login` 会先更换会话 token，再保存主体，并把主体的 `Method` 设为 `session`。更换 token 可以防止登录前泄露的 token 在登录后继续有效。`Logout` 销毁会话，并让浏览器删除 cookie。

登录接口本身是公开的：

```go
op := huma.Operation{OperationID: "admin-login", Method: http.MethodPost, Path: "/admin/login"}
rest.Public()(&op)
rest.Register(api, op, func() func(context.Context, *LoginInput) (*struct{}, error) {
    return login(users, sessions)
})
```

会话保存的是登录时主体的快照。用户的 scope 变化后，已登录的会话仍然使用旧的 scope，直到重新登录。需要立即生效时，用 scs 的 `Iterate` 找到该用户的会话并销毁，见[读写会话数据](session.md#session-data)。

<a id="protecting-session-routes"></a>
### 保护会话路由

`session.Require` 的用法和 [`rest.RequireBearer`](#protecting-routes) 相同，只是凭据来自会话：

```go
requireEditor := session.Require(api, sessions, "articles")

op := huma.Operation{OperationID: "admin-article-update", Method: http.MethodPut, Path: "/admin/articles/{id}"}
requireEditor(&op)
rest.Register(api, op, func() func(context.Context, *actions.UpdateArticleInput) (*actions.ArticleOutput, error) {
    return actions.NewUpdateArticle(articles).Handle
})
```

处理器和业务服务的写法不变，仍然用 `identity.FromContext` 读取主体，见[获取已认证的主体](#retrieving-the-subject)。同一个业务处理器因此可以同时挂在 Bearer 保护的 `/articles` 和会话保护的 `/admin/articles` 下。

| 情况 | 状态码 | 错误码 |
| --- | --- | --- |
| 没有会话，或会话中没有登录的主体 | 401 | `auth.unauthenticated` |
| 缺少所需的 scope | 403 | `auth.forbidden` |

响应同样带有 `Cache-Control: no-store`，但不带 `WWW-Authenticate` 头。OpenAPI 文档中，这些路由使用名为 `session` 的 cookie 安全方案，`routes` 命令把它们的访问策略显示为 `session`。

一条路由只能声明一种认证方式，同时调用 `RequireBearer` 和 `session.Require` 会在注册时 panic。

<a id="protecting-pages"></a>
### 保护页面

服务端渲染的[页面](views.md)用 `session.RequirePage` 保护。它是普通的 chi 中间件，第一个参数是用来呈现错误的 `web.Pages`：

```go
r.Group(func(r chi.Router) {
    r.Use(session.RequirePage(pages, sessions, "/login", "articles"))
    r.Get("/admin/articles/{id}/edit", pages.Handle(h.Edit))
})
```

与 `session.Require` 不同，未登录的浏览器会被带到登录页，而不是收到 `401`：

| 情况 | 结果 |
| --- | --- |
| 未登录 | 跳转到 `/login?next=原地址`；htmx 请求通过 `HX-Redirect` 整页跳转，原地址取自发起请求的页面 |
| 缺少所需的 scope | 由 `pages` 呈现 `403`，见[错误页](views.md#error-pages) |

登录页的处理器校验密码后调用 `session.Login`，再回到 `next`。`next` 来自请求，必须用 `web.LocalPath` 确认它是本站路径：

```go
if err := session.Login(r.Context(), sessions, sub); err != nil {
    return err
}
web.Redirect(w, r, web.LocalPath(r.URL.Query().Get("next"), "/admin"))
return nil
```

<a id="retrieving-the-subject"></a>
## 获取已认证的主体

认证通过后，主体会被放入请求的 context。在处理器中使用 `identity.FromContext` 读取它，再传给业务服务。

例如，你可以把 `articlehttp/actions/get_article.go` 中[获取文章处理器](routing.md#http-actions)的 `Handle` 方法改为下面这样，显式拒绝没有主体的请求。这个方法位于 `actions` 包中，`a.articles` 由 `NewGetArticle` 注入，`auth.ErrUnauthenticated` 定义在本章的 `internal/auth/auth.go` 中：

```go
func (a *GetArticle) Handle(ctx context.Context, in *GetArticleInput) (*ArticleOutput, error) {
    subject, ok := identity.FromContext(ctx)
    if !ok {
        return nil, auth.ErrUnauthenticated.New()
    }
    item, err := a.articles.Get(ctx, subject, in.ID)
    if err != nil {
        return nil, err
    }
    return &ArticleOutput{Body: toDTO(item)}, nil
}
```

对于受 `RequireBearer` 保护的路由，`ok` 总是为 `true`。保留这个检查可以防止路由配置被误改为公开后，处理器在没有身份的情况下继续执行。

另一种做法是让业务服务拒绝空的主体，见[授权](authorization.md#ownership)。这样即使路由被误改为公开，请求也会在服务中被拒绝，处理器就可以简单地写成 `subject, _ := identity.FromContext(ctx)`，[路由](routing.md#organizing-routes)一章中 `actions` 包的处理器就是这样做的。

<a id="managing-credentials"></a>
## 管理凭据

凭据存放在哪里、如何签发和吊销，完全由应用决定。上面把 token 写在配置文件中的做法适合内部服务或少量固定的调用方；对外开放的 API 通常会把 API Key 存在数据库中，或者使用 JWT，此时只需替换校验器的实现，路由和业务代码都不需要改变。

浏览器中的前端通常使用[会话认证](#session-authentication)。用户注册、刷新 token 等功能不在 mygoravel 的范围内。你可以选择合适的认证服务或库来实现它们，最终在校验器中把验证结果转换为 `identity.Subject` 即可。
