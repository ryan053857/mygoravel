# API 令牌

- [简介](#introduction)
- [配置](#configuration)
- [装配](#assembling)
- [登录与登出](#login-and-logout)
- [个人访问令牌](#personal-access-tokens)
- [设备与令牌列表](#listing-tokens)
- [撤销令牌](#revoking-tokens)
- [加载权限](#loading-permissions)
- [过期清理](#cleanup)
- [单页应用](#single-page-applications)

<a id="introduction"></a>
## 简介

JSON API 使用 Bearer token [认证](authentication.md)。`apitoken` 包把 token 存在数据库中（支持 PostgreSQL、SQLite 和 MySQL），提供签发、校验、滑动过期和撤销，校验函数可以直接交给 `rest.RequireBearer`。

token 分为两类：

| 类别 | 签发方式 | 有效期 | 用途 |
| --- | --- | --- | --- |
| 登录 token | `Login`，用户在单页应用或 App 中登录时 | 按[配置](#configuration)滑动：经常使用就一直有效，空闲太久或超过最长有效期后失效 | 代表一台设备 |
| 个人访问令牌 | `Create`，用户在“API 令牌”页面手动创建 | 由创建者指定，可以不过期 | 脚本、CI 和第三方集成 |

表中只保存 token 的 SHA-256，明文只在签发时返回一次。

<a id="configuration"></a>
## 配置

把 `apitoken.Config` 放进应用的根配置结构体，配置段为 `api_token`：

```go
type Config struct {
    // ...
    APIToken apitoken.Config `yaml:"api_token"`
}
```

```yaml
api_token:
  lifetime: 720h              # 登录 token 的最长有效期，到期后必须重新登录
  idle_timeout: 24h           # 超过这么久没有使用即失效，0 表示不限制
  remember_idle_timeout: 168h # 登录时选择“记住我”的空闲时长
```

上面是默认值。登录 token 的截止时间是“签发时间 + `lifetime`”与“最近使用时间 + 空闲时长”中较早的一个，与[会话](session.md#configuration)的 `lifetime`、`idle_timeout` 含义相同。客户端不需要刷新 token：每次使用都会延长它的有效期，收到 `401` 时让用户重新登录即可。

<a id="assembling"></a>
## 装配

`apitoken.NewStore` 接收应用的 `*bun.DB` 和配置。使用前先发布 `api_tokens` 表的迁移，见[发布框架迁移](migrations.md#publishing-framework-migrations)：

```go
migrationCmd.AddCommand(migratecmd.Publish(postgres.Driver,
    migrate.Source{Name: "apitoken", FS: apitoken.Migrations()},
))
```

`Store.Validate` 的签名就是 `rest.TokenValidator`，可以直接用来保护路由：

```go
tokens := apitoken.NewStore(a.db, a.cfg.APIToken)
requireArticles := rest.RequireBearer(api, tokens.Validate, "articles")
```

`Validate` 返回的主体 `Method` 为 `bearer`，`Credential` 是 token 的 ID。token 只说明“这是谁”，不携带用户的权限：权限在每个请求中[加载](#loading-permissions)。它每分钟最多为一个 token 写一次库，记录最近使用时间并延长登录 token 的有效期，因此空闲时长的精度约为一分钟。

<a id="login-and-logout"></a>
## 登录与登出

单页应用和 App 用账号密码换取登录 token。下面是 `internal/auth/authhttp/routes.go` 中的登录和登出接口，`authenticate` 是用户模块校验密码的 `user.Authenticate` action，见[哈希](hashing.md#hashing-passwords)：

```go
type TokenInput struct {
    UserAgent string `header:"User-Agent"`
    Body      struct {
        Email    string `json:"email" format:"email"`
        Password string `json:"password" minLength:"1"`
        Remember bool   `json:"remember,omitempty"`
    }
    ip string
}

// Resolve 记录客户端地址，见 Huma 的 Resolver。
func (in *TokenInput) Resolve(ctx huma.Context) []error {
    in.ip, _, _ = net.SplitHostPort(ctx.RemoteAddr())
    return nil
}

type TokenOutput struct {
    Body struct {
        Token string `json:"token"`
    }
}

func issueToken(authenticate *user.Authenticate, tokens *apitoken.Store) func(context.Context, *TokenInput) (*TokenOutput, error) {
    return func(ctx context.Context, in *TokenInput) (*TokenOutput, error) {
        u, err := authenticate.Handle(ctx, in.Body.Email, in.Body.Password)
        if err != nil {
            return nil, err
        }
        sub := identity.Subject{Type: user.Type, ID: u.ID}
        plain, err := tokens.Login(ctx, sub, apitoken.Device{
            Remember:  in.Body.Remember,
            IP:        in.ip,
            UserAgent: in.UserAgent,
        })
        if err != nil {
            return nil, err
        }
        out := &TokenOutput{}
        out.Body.Token = plain
        return out, nil
    }
}

func revokeToken(tokens *apitoken.Store) func(context.Context, *struct{}) (*struct{}, error) {
    return func(ctx context.Context, _ *struct{}) (*struct{}, error) {
        sub, _ := identity.FromContext(ctx)
        return nil, tokens.Revoke(ctx, sub, sub.Credential)
    }
}
```

登录接口是公开的，登出接口用 `RequireBearer` 保护：

```go
op := huma.Operation{OperationID: "auth-token-create", Method: http.MethodPost, Path: "/auth/tokens"}
rest.Public()(&op)
rest.Register(api, op, issueToken(authenticate, tokens))

op = huma.Operation{OperationID: "auth-token-delete", Method: http.MethodDelete, Path: "/auth/tokens/current"}
rest.RequireBearer(api, tokens.Validate)(&op)
rest.Register(api, op, revokeToken(tokens))
```

`Login` 记录 IP 和 User-Agent，用于[设备列表](#listing-tokens)。登出时把主体的 `Credential` 交给 `Revoke`，撤销的就是本次请求所用的 token。防止密码被暴力猜测，见[在处理器中限流](rate-limiting.md#in-handlers)。

<a id="personal-access-tokens"></a>
## 个人访问令牌

用户可以为脚本或第三方集成创建令牌，并把它限制在部分权限内。下面是“API 令牌”[页面](views.md)的创建处理器，令牌明文只显示这一次：

```go
type CreateTokenInput struct {
    Name string `json:"name" minLength:"1" maxLength:"100"`
}

func (h *Handlers) CreateToken(w http.ResponseWriter, r *http.Request) error {
    f, err := web.ParseForm[CreateTokenInput](r)
    if err != nil {
        return err
    }
    if !f.Valid() {
        return web.Render(w, r, http.StatusUnprocessableEntity, views.NewToken(f))
    }
    sub, _ := identity.FromContext(r.Context())
    plain, err := h.tokens.Create(r.Context(), sub, apitoken.Personal{
        Name:   f.Data.Name,
        Scopes: []string{"articles:read"},
    })
    if err != nil {
        return err
    }
    return web.Render(w, r, http.StatusOK, views.TokenCreated(plain))
}
```

`apitoken.Personal` 的字段：

| 字段 | 说明 |
| --- | --- |
| `Name` | 在令牌列表中显示的名称，1 到 255 个字符 |
| `Scopes` | 令牌被限制在内的权限，至少一个，支持通配符，例如 `articles:*` |
| `Tenant` | 不为空时，令牌只能在这个租户中使用 |
| `ExpiresAt` | 过期时间，零值表示不过期 |

令牌能做的是**用户当前的权限与 `Scopes` 的交集**：`Scopes` 写了 `*`，也不会超出用户本人的权限；用户被收回权限后，令牌随之收窄，不需要重新创建。个人访问令牌不按空闲时间过期，也不受 `RevokeOthers` 影响。

<a id="listing-tokens"></a>
## 设备与令牌列表

`List` 返回用户全部未过期的 token，最近使用的在前：

```go
list, err := tokens.List(ctx, sub)
for _, t := range list {
    // t.Kind、t.Name、t.IP、t.UserAgent、t.LastUsedAt、t.ExpiresAt
    // t.Current：是否为本次请求所用的 token
    // t.Impersonator：代为登录时签发 token 的操作者，见代为登录
}
```

`Current` 根据 `sub.Credential` 判断，所以在 API 中传入 `identity.FromContext` 读到的主体即可。`Impersonator` 不为零值的 token 是客服等人[代为登录](authentication.md#impersonation)时签发的，在设备列表中标出来，用户可以看到并撤销它。

<a id="revoking-tokens"></a>
## 撤销令牌

| 方法 | 撤销范围 | 常见用途 |
| --- | --- | --- |
| `Revoke(ctx, sub, id)` | `sub` 的一个 token；`id` 为 `sub.Credential` 时即登出 | 登出、在列表中移除某台设备或某个令牌 |
| `RevokeOthers(ctx, sub)` | `sub` 除本次请求所用 token 以外的全部**登录 token** | “退出其他设备” |
| `RevokeSubject(ctx, ref)` | 主体 `ref`（`identity.Ref`）的全部 token，包括个人访问令牌 | 封禁账号、修改密码 |

撤销立即生效，被撤销的 token 在下一次请求时返回 `401`；用它建立的 SSE 长连接最多 30 秒内结束，见[凭据失效](sse.md#revoked-credentials)。撤销后调用 `RecheckCredential` 或 `RecheckSubject`，连接会立即结束，见[立即断开](sse.md#immediate-revocation)。

同一个用户可能还在浏览器中通过[会话](session.md)登录了页面。让用户在所有地方下线时，两边都要撤销。下面是用户模块的 `SignOutEverywhere` action 的 `Handle`，会话存储和 token 存储由构造函数传入：

```go
func (a *SignOutEverywhere) Handle(ctx context.Context, target identity.Ref) error {
    if _, err := a.sessions.RevokeSubject(ctx, target); err != nil {
        return err
    }
    _, err := a.tokens.RevokeSubject(ctx, target)
    return err
}
```

“退出其他设备”也是如此。会话存储的 [`RevokeOthers`](session.md#revoking-sessions) 与 token 存储的签名相同，都只保留 `sub.Credential` 对应的那一个凭据，所以页面和单页应用可以调用同一个 action：

```go
func (a *SignOutOthers) Handle(ctx context.Context, subject identity.Subject) error {
    if _, err := a.sessions.RevokeOthers(ctx, subject); err != nil {
        return err
    }
    _, err := a.tokens.RevokeOthers(ctx, subject)
    return err
}
```

在页面中发起时，`Credential` 是会话 ID，保留当前会话、撤销全部登录 token；在单页应用中发起时，`Credential` 是 token 的 ID，保留当前 token、撤销全部会话。

<a id="loading-permissions"></a>
## 加载权限

token 不保存用户的权限，所以权限变化、账号停用都能立即生效。权限由 `rest.Options.Resolve` 在每个请求中按用户当前的状态加载，见[每次请求加载权限](authentication.md#resolving-subjects)：

```go
api := rest.New(rest.Options{
    Resolve: func(ctx context.Context, sub identity.Subject) (identity.Subject, error) {
        u, err := user.Find(ctx, a.db, sub.ID)
        if err != nil {
            return identity.Subject{}, err
        }
        if u.Disabled {
            return identity.Subject{}, auth.ErrUnauthenticated.New()
        }
        sub.Permissions = u.Permissions
        return sub, nil
    },
    // ...
})
```

个人访问令牌的 `Scopes` 由 `Validate` 填入，`Resolve` 不需要处理：`sub.Can` 同时检查两者。

<a id="cleanup"></a>
## 过期清理

过期的 token 不会再通过校验，但行仍然留在表中。`Store` 不在后台启动清理协程，而是提供 `Cleanup`，每次最多删除 `limit` 个过期 token，可以注册为[定时任务](scheduling.md)：

```go
n, err := tokens.Cleanup(ctx, 1000)
```

<a id="single-page-applications"></a>
## 单页应用

独立部署的单页应用（例如 React）和 App 一样使用登录 token：登录后保存 token，之后的请求都带上 `Authorization: Bearer <token>`，收到 `401` 时跳转到登录页。token 通常保存在 `localStorage` 中，这样刷新页面和打开新标签页后仍保持登录。页面上的脚本都能读到它，所以要防范 XSS；有效期和撤销可以限制 token 泄露后造成的影响。

单页应用与 API 不在同一个源时，浏览器要求 API 返回 CORS 响应头，在 `/api` 的路由组上安装 [httpx.CORS](middleware.md#cors)。Bearer token 不是 cookie，不需要允许携带凭据。
