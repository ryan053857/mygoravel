# 认证

- [简介](#introduction)
    - [核心概念](#key-concepts)
- [编写 Token 校验器](#token-validators)
- [保护路由](#protecting-routes)
    - [认证失败的响应](#failed-authentication)
- [每次请求加载权限](#resolving-subjects)
- [多套账号体系](#account-systems)
- [多租户](#tenants)
- [代为登录](#impersonation)
- [获取已认证的主体](#retrieving-the-subject)
- [管理凭据](#managing-credentials)

<a id="introduction"></a>
## 简介

很多 API 都需要知道“是谁在调用”。mygoravel 内置了基于 Bearer Token 的认证：客户端在请求头中携带 `Authorization: Bearer <token>`，框架调用你提供的校验函数确认身份，然后把身份信息交给处理器。

JSON API 只使用 Bearer token，不使用会话。面向用户的一端，无论是网页、桌面客户端还是移动端，都调用 JSON API。服务端渲染的页面用于运营后台和内部管理后台，使用会话登录，见[页面登录](views.md#login)。两者产生的都是 `identity.Subject`，action 和[授权](authorization.md)规则不需要区分。

mygoravel 不限定 token 的种类和来源。它可以是数据库中的 API Key，也可以是由认证服务签发的 JWT——你只需要写一个函数，告诉框架这个 token 对应谁。

> [!NOTE]
> 认证回答的是“调用者是谁”，而“调用者能不能做这件事”属于授权，请参阅[授权](authorization.md)。

<a id="key-concepts"></a>
### 核心概念

**主体（Subject）** 表示一个已经通过认证的调用者，它由 `identity` 包定义：

```go
type Subject struct {
    Type         SubjectType
    ID           string
    Impersonator Ref
    Method       string
    Credential   string
    Tenant       string
    Permissions  []string
    Scopes       []string
}
```

| 字段 | 说明 |
| --- | --- |
| `Type` | 调用者所属的账号体系，例如 `user`、`staff`、`customer`，由应用声明；队列任务等系统自己的操作使用 `identity.System` |
| `ID` | 调用者在这套账号体系中的标识，是一个不透明的字符串，例如用户表的主键 |
| `Impersonator` | 代为登录时实际操作的人，例如客服；零值表示调用者本人，见[代为登录](#impersonation) |
| `Method` | 认证方式，例如 `bearer` 或 `session` |
| `Credential` | 本次请求所用凭据的 ID，例如 [API 令牌](api-tokens.md)或[会话](session.md#listing-sessions)的 ID；用于在设备列表中标出当前设备、退出其他设备，可以为空 |
| `Tenant` | 本次请求所在的租户，`""` 表示平台本身；由凭据设置时，凭据只能在这个租户中使用，见[多租户](#tenants) |
| `Permissions` | 调用者拥有的权限，例如 `articles:write` |
| `Scopes` | 本次请求所用凭据被限制在内的权限，例如只读令牌的 `articles:read`；`nil` 表示不限制 |

`Type` 和 `ID` 合起来是 `identity.Ref`，用 `sub.Ref()` 取得。框架保存主体的地方——会话、令牌、角色、操作日志、通知等——都同时记录两者，所以 `ID` 只需在同一种 `Type` 中唯一：员工表和客户表可以各自使用自增 ID，互不混淆。博客应用只有一套账号，在用户模块 `internal/user/user.go` 中声明它的类型：

```go
const Type identity.SubjectType = "user"
```

`Type` 以小写字母开头，只包含小写字母、数字和下划线。

`Permissions` 回答“这个人能做什么”，`Scopes` 回答“这把钥匙被允许做什么”。`sub.Can(perm)` 在两者都允许时返回 `true`，路由检查、业务服务和页面都用它判断。权限名以冒号分隔，授予时可以用 `*` 作为最后一段：`articles:*` 包含 `articles:read` 和 `articles:write`，单独的 `*` 包含全部权限。

**校验器（TokenValidator）** 是你提供的函数，负责把 token 转换为主体：

```go
type TokenValidator func(ctx context.Context, token string) (identity.Subject, error)
```

<a id="token-validators"></a>
## 编写 Token 校验器

下面是博客应用完整的 `internal/auth/auth.go`。为了便于理解，它把可用的 token 直接写在配置文件中，每个 token 对应一个调用者和它的权限：

```go
package auth

import (
    "context"
    "errors"

    "example.com/blog/internal/user"
    "github.com/ryan053857/mygoravel/errorx"
    "github.com/ryan053857/mygoravel/httpx/rest"
    "github.com/ryan053857/mygoravel/identity"
)

var ErrUnauthenticated = errorx.Define(errorx.Unauthenticated, "auth.unauthenticated")

// Credential 是一个 API token 及其代表的调用者。
type Credential struct {
    Token       string   `yaml:"token"`
    SubjectID   string   `yaml:"subject_id"`
    Permissions []string `yaml:"permissions"`
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
    return func(_ context.Context, token string) (identity.Subject, error) {
        cred, ok := byToken[token]
        if !ok {
            return identity.Subject{}, ErrUnauthenticated.New()
        }
        return identity.Subject{
            Type:        user.Type,
            ID:          cred.SubjectID,
            Method:      "bearer",
            Permissions: cred.Permissions,
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
      permissions: [articles]
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

需要认证的 API 先用 `rest.Options.Accounts` 声明它接受哪些账号体系。博客只有一套账号：

```go
api := rest.New(rest.Options{Accounts: identity.Accounts{user.Type: nil}})
```

键是账号体系，值是这套账号的 `identity.Resolver`，`nil` 表示直接使用校验器给出的主体，见[每次请求加载权限](#resolving-subjects)。校验器给出的主体的 `Type` 不在其中时，请求与 token 无效一样返回 `401`。产品有多套账号时，每个 API 只接受它服务的那几种，见[多套账号体系](#account-systems)。没有设置 `Accounts` 就注册需要认证的路由会 panic。

使用 `rest.RequireBearer` 为路由启用认证。它的参数是 API 实例、校验器，以及调用者必须能执行的权限。

下面是注册“获取文章”接口的片段：`api` 由 `rest.New` 创建，`validate` 是入口中用 `auth.Validator(a.cfg.Auth)` 创建的校验器，`db` 是入口打开的数据库。`Get` action 的定义见[路由](routing.md#http-actions)：

```go
requireArticles := rest.RequireBearer(api, validate, "articles")

op := huma.Operation{
    OperationID: "article-get",
    Method:      http.MethodGet,
    Path:        "/articles/{id}",
}
requireArticles(&op)
rest.Register(api, op, NewGet(db).HTTP)
```

`RequireBearer` 返回的函数可以复用于多条路由。可以传入多个权限，`sub.Can` 必须对**全部**权限返回 `true` 才能访问；不传权限则只要求认证通过。

`RequireBearer` 同时做了两件事：

- 在运行时检查 `Authorization` 请求头并调用校验器；
- 在 OpenAPI 文档中把这条路由标记为需要 Bearer 认证，所需的权限记录在 `x-required-permissions` 扩展字段中。

需要认证的响应还会自动加上 `Cache-Control: no-store`，防止响应被中间代理缓存。

`RequireBearer` 基于更通用的 `rest.RequireAuth` 实现。需要其他认证方式（例如从自定义请求头读取 API Key）时，可以用 `RequireAuth` 声明自己的 OpenAPI 安全方案和认证函数，认证失败的响应与本章相同。

> [!TIP]
> 不需要认证的路由也必须显式调用 `rest.Public()(&op)`。忘记声明访问策略的路由会在注册时 panic，因此你不会意外地发布一个未受保护的接口。

<a id="failed-authentication"></a>
### 认证失败的响应

| 情况 | 状态码 | 错误码 |
| --- | --- | --- |
| 没有携带 `Authorization` 头，或格式不是 `Bearer <token>` | 401 | `auth.unauthenticated` |
| 校验器拒绝了 token | 401 | `auth.unauthenticated` |
| token 属于 API 不接受的账号体系 | 401 | `auth.unauthenticated` |
| 缺少所需的权限 | 403 | `auth.forbidden` |
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

<a id="resolving-subjects"></a>
## 每次请求加载权限

上面的校验器直接给出了权限。更常见的情况是：凭据只说明“这是谁”，权限来自数据库中用户的角色，而且应当随时生效。这时在 `Accounts` 中为这套账号指定 Resolver，它在认证通过后、权限检查之前运行，收到凭据给出的主体，返回本次请求的主体：

```go
api := rest.New(rest.Options{
    Accounts: identity.Accounts{
        user.Type: func(ctx context.Context, sub identity.Subject) (identity.Subject, error) {
            u, err := users.Get(ctx, sub.ID)
            if err != nil {
                return identity.Subject{}, err
            }
            if u.Disabled {
                return identity.Subject{}, auth.ErrUnauthenticated.New()
            }
            sub.Permissions = u.Permissions
            return sub, nil
        },
    },
})
```

| Resolver 返回 | 结果 |
| --- | --- |
| 主体 | 权限检查和处理器都使用它 |
| `errorx.Unauthenticated` 类错误，或 `ID` 为空的主体 | `401`，与凭据无效相同 |
| 其他 4xx 类错误 | 按错误分类响应，错误码就是你定义的业务错误码，例如不是租户成员时返回 `errorx.NotFound` 得到 `404` |
| 5xx 类错误 | 与校验器的 5xx 相同 |

Resolver 只负责加载权限和租户：返回的主体中 `Type`、`ID`、`Method`、`Credential`、`Scopes` 和 `Impersonator` 总是取自凭据，像上面这样修改收到的主体，或者新建一个主体返回，结果都一样，Resolver 不能把主体换成另一个人。

页面的 [`web.Options.Accounts`](views.md#login) 和实时频道的 [`realtime.Options.Accounts`](realtime.md#tokens) 与它是同一个类型 `identity.Accounts`，所以同一个 Resolver 可以同时交给它们，三者按相同的规则处理它的返回值。[API 令牌](api-tokens.md)不保存权限，总是和 `Resolve` 一起使用。

<a id="account-systems"></a>
## 多套账号体系

框架不定义用户表。一个产品可以有几套互相独立的账号，例如平台的运营人员、商家的员工、商家的终端客户，各自一张表，主键、登录方式都由产品决定。框架只需要知道每个主体属于哪一套，也就是它的 `Type`。

**1. 每套账号声明一个类型**，放在拥有那张表的模块中：

```go
// internal/staff/staff.go
const Type identity.SubjectType = "staff"

// internal/member/member.go
const Type identity.SubjectType = "member"

// internal/customer/customer.go
const Type identity.SubjectType = "customer"
```

登录时用它构造主体，例如 `identity.Subject{Type: customer.Type, ID: c.ID}`。

**2. 每个入口声明接受哪几套账号。** 运营后台的页面只接受运营人员，商家后台的 API 只接受商家员工，App 和小程序调用的 API 只接受客户：

```go
ops := web.New(web.Options{Accounts: identity.Accounts{staff.Type: roles.Resolve} /* 其余字段见视图 */})
console := rest.New(rest.Options{Accounts: identity.Accounts{member.Type: roles.Resolve}, Tenant: fromPath})
shop := rest.New(rest.Options{Accounts: identity.Accounts{customer.Type: customers.Resolve}, Tenant: fromHost})
```

几个 API 可以共用一个 token 校验器：客户的 token 发到商家后台的 API，与无效 token 一样返回 `401`。一个入口也可以接受多套账号，例如 `identity.Accounts{member.Type: roles.Resolve, customer.Type: customers.Resolve}`，`sub.Can` 和 action 不需要区分。[`authz`](authorization.md#roles) 按主体的类型和 ID 保存角色，可以同时服务几套账号；终端客户通常不需要角色，它的 Resolver 只要检查账号是否停用、按“数据归属本人”授权即可。

**3. 每套账号可以有自己的令牌有效期**，用各自的配置创建 `apitoken.Store` 签发，见[装配](api-tokens.md#assembling)。

**4. ID 只需在同一套账号中唯一。** 会话、令牌、角色、[操作日志](activity.md)、[通知](notifications.md)、[功能开关](features.md)和实时频道的在线成员都同时保存主体的类型和 ID，员工表和客户表都用自增 ID 也不会混淆。主键是 `(tenant_id, id)` 这类组合时，把租户编进 ID，例如 `t42:1001`。新表推荐使用 UUIDv7。需要把主体放进一个字符串时，例如缓存键、广播主题，使用 `sub.Ref().String()`，结果形如 `customer:42`。

[代为登录](#impersonation)也可以跨账号体系：运营人员代客户登录时，主体是客户，`Impersonator` 是运营人员的 `identity.Ref`。

<a id="tenants"></a>
## 多租户

一个 SaaS 应用中，同一个用户可能属于多个组织，在每个组织中有不同的权限。本次请求属于哪个租户由请求本身决定，例如路径中的 `/t/{tenant}`、子域名或请求头。用 `rest.Options.Tenant` 取出它：

```go
api := rest.New(rest.Options{
    Tenant:   func(ctx huma.Context) string { return ctx.Param("tenant") },
    Accounts: identity.Accounts{user.Type: resolve},
})
```

`Tenant` 的结果在每个操作开始时写入 context，公开的操作也一样，用 `identity.TenantFromContext` 读取；没有设置 `Tenant` 或取到空字符串时，租户是 `""`，表示平台本身，例如个人中心和运营后台。Resolver 据此加载用户在这个租户中的权限，并把租户写入主体：

```go
func resolve(ctx context.Context, sub identity.Subject) (identity.Subject, error) {
    tenant := identity.TenantFromContext(ctx)
    perms, err := members.Permissions(ctx, tenant, sub.Ref())
    if err != nil {
        return identity.Subject{}, err // 不是成员时返回 errorx.NotFound 类错误，得到 404
    }
    sub.Tenant, sub.Permissions = tenant, perms
    return sub, nil
}
```

凭据也可以绑定一个租户，例如只属于某个租户的个人访问令牌：校验器返回的主体 `Tenant` 不为空时，它与本次请求的租户不同的请求一律返回 `403`，不会调用 Resolver。

角色和成员可以交给 [`authz`](authorization.md#roles) 管理，它的 `Resolve` 方法就是上面这样的函数。租户的确定方式、成员管理、数据隔离和运营后台，见[多租户](tenancy.md)。

<a id="impersonation"></a>
## 代为登录

客服排查问题时常常需要“以用户的身份看一看”。代为登录得到的主体就是这个用户，权限也是他的，`Impersonator` 记录实际操作的人。`Impersonator` 和其他字段一起保存在会话或令牌中，之后的每个请求都带着它。

运营人员和用户在同一组页面中时，例如内部管理后台里“以这位同事的身份看一看”，“以此用户登录”按钮提交到一个要求 `users:impersonate` 的页面路由：

```go
func (h *Handlers) Impersonate(w http.ResponseWriter, r *http.Request) error {
    staff, _ := identity.FromContext(r.Context())
    if !staff.Impersonator.IsZero() {
        return web.ErrForbidden.New() // 先回到自己的账号，不能层层代为登录
    }
    target := identity.Subject{Type: user.Type, ID: chi.URLParam(r, "id"), Impersonator: staff.Ref()}
    if err := h.pages.Login(r, target); err != nil {
        return err
    }
    web.Redirect(w, r, "/")
    return nil
}

func (h *Handlers) StopImpersonating(w http.ResponseWriter, r *http.Request) error {
    sub, _ := identity.FromContext(r.Context())
    if !sub.Impersonator.IsZero() {
        if err := h.pages.Login(r, identity.Subject{Type: sub.Impersonator.Type, ID: sub.Impersonator.ID}); err != nil {
            return err
        }
    }
    web.Redirect(w, r, "/admin/users")
    return nil
}
```

`StopImpersonating` 注册为只要求登录的路由，页面顶部在 `sub.Impersonator` 不为零值时显示“正在代 {用户} 操作”和退出按钮。`pages.Login` 每次都更换会话 token，代为登录期间的会话在退出后不能继续使用。

用户使用的是调用 API 的网页或 App 时，用[登录令牌](api-tokens.md#login-and-logout)完成同样的事：运营后台为目标用户签发一个带 `Impersonator` 的令牌，交给用户端的前端使用，排查完毕[撤销](api-tokens.md#revoking-tokens)它。运营人员和用户属于不同的[账号体系](#account-systems)时也是如此，主体是用户，`Impersonator` 是运营人员：

```go
plain, err := tokens.Login(ctx, identity.Subject{Type: customer.Type, ID: customerID, Impersonator: staff.Ref()}, device)
```

令牌不要放进跳转地址的查询参数，它会留在浏览器历史和访问日志中。

几件需要应用自己决定的事：

- **操作者仍然有效**：在 Resolver 中检查 `Impersonator` 仍然拥有代为登录的权限。客服离职或被收回权限后，他代为登录的会话和令牌在下一个请求就失效：

  ```go
  if !sub.Impersonator.IsZero() {
      perms, _, err := roles.Permissions(ctx, sub.Impersonator, "")
      if err != nil {
          return identity.Subject{}, err
      }
      if !identity.Grants(perms, "users:impersonate") {
          return identity.Subject{}, auth.ErrUnauthenticated.New()
      }
  }
  return roles.Resolve(ctx, sub)
  ```

- **谁可以被代为登录**：通常不允许代为登录其他运营人员，在 `Impersonate` 中用 `roles.Permissions(ctx, target.Ref(), "")` 确认目标在平台中没有角色；
- **哪些操作不允许代为执行**：修改密码和邮箱、管理设备和令牌、付款等，在对应的 action 中拒绝 `!subject.Impersonator.IsZero()` 的主体。代为登录期间创建的[个人访问令牌](api-tokens.md#personal-access-tokens)会记录 `Impersonator`，令牌的每次使用都归于这个操作者；
- **审计**：[操作日志](activity.md)自动记下 `Impersonator`；自己写日志时同样同时写下两者，例如 `slog.InfoContext(ctx, "article published", "subject", subject.Ref().String(), "impersonator", subject.Impersonator.String())`。

<a id="retrieving-the-subject"></a>
## 获取已认证的主体

认证通过后，主体会被放入请求的 context。在处理器中使用 `identity.FromContext` 读取它，再传给 action 的 `Handle`。

例如，你可以把 `internal/article/get.go` 中[获取文章](routing.md#http-actions)的 `HTTP` 方法改为下面这样，显式拒绝没有主体的请求。`auth.ErrUnauthenticated` 定义在本章的 `internal/auth/auth.go` 中：

```go
func (a *Get) HTTP(ctx context.Context, in *GetInput) (*ArticleOutput, error) {
    subject, ok := identity.FromContext(ctx)
    if !ok {
        return nil, auth.ErrUnauthenticated.New()
    }
    item, err := a.Handle(ctx, subject, in.ID)
    if err != nil {
        return nil, err
    }
    return &ArticleOutput{Body: toDTO(item)}, nil
}
```

对于受 `RequireBearer` 保护的路由，`ok` 总是为 `true`。保留这个检查可以防止路由配置被误改为公开后，处理器在没有身份的情况下继续执行。

另一种做法是让 `Handle` 拒绝空的主体，见[授权](authorization.md#ownership)。这样即使路由被误改为公开，请求也会在 `Handle` 中被拒绝，`HTTP` 就可以简单地写成 `subject, _ := identity.FromContext(ctx)`，[路由](routing.md#organizing-routes)一章中的 action 就是这样做的。

<a id="managing-credentials"></a>
## 管理凭据

上面把 token 写在配置文件中的做法适合内部服务或少量固定的调用方。面向用户的 API 使用 [API 令牌](api-tokens.md)：单页应用和 App 登录后得到一个滑动过期的 token，或者一对 [JWT access token 和 refresh token](api-tokens.md#jwt)，用户也可以为脚本创建个人访问令牌，`Store.Validate` 或 `JWT.Validate` 直接作为校验器使用。

使用外部认证服务签发的 JWT 时，在校验器中用成熟的库验证签名、签发者和有效期，再把结果转换为 `identity.Subject`。无论采用哪种凭据，路由和业务代码都不需要改变。用 GitHub、Google 等账号登录见[第三方登录](social-login.md)；用户注册、找回密码等功能由应用自己实现。
