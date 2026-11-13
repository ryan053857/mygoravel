# OAuth 授权服务器

- [简介](#introduction)
- [安装](#installation)
- [装配](#assembling)
    - [范围](#scopes)
    - [挂到 MCP 服务](#mcp)
- [授权流程](#flow)
- [客户端](#clients)
    - [动态客户端注册](#dynamic-registration)
- [令牌](#tokens)
- [同意页](#consent-page)
- [部署](#deployment)
- [测试](#testing)

<a id="introduction"></a>
## 简介

[MCP 服务](mcp.md)可以用[个人访问令牌](api-tokens.md#personal-access-tokens)连接 Claude Code、Cursor，它们允许填写请求头。claude.ai（网页、桌面、手机）和 ChatGPT 的远程连接器不能填请求头，它们走 MCP 规定的 OAuth 流程：用户在浏览器中登录你的应用，在同意页上点“允许”，客户端拿到令牌。`oauthserver` 是这个流程中的授权服务器，相当于 Laravel 生态的 Passport。

- 遵循 OAuth 2.1 和 [MCP 授权规范](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization)：授权码 + PKCE S256，授权服务器元数据（RFC 8414），授权响应带 `iss`（RFC 9207），令牌绑定资源（RFC 8707）。
- 客户端不需要事先登记：claude.ai、ChatGPT 的 client_id 是一个 https 地址，授权时从这个地址取回客户端的名称和回调地址（Client ID Metadata Document）。
- 令牌是 [`apitoken`](api-tokens.md) 的 OAuth 令牌：只能用于授权时的 MCP 服务，权限是用户本人的权限与同意的范围的交集，按使用滑动过期，出现在用户的令牌列表中，可以随时撤销。

同意页是应用的[单页应用](spa.md)中的一个页面：授权端点校验请求后跳到同意页，同意页用登录 token 调用两个接口，读取请求、提交用户的选择。所以能授权的是这两个接口接受的账号体系。本章的例子是博客的编辑：他们在编辑后台登录，在 Claude 中查找和发布文章。

<a id="installation"></a>
## 安装

授权码保存在 `oauth_codes` 表中，令牌保存在 `apitoken` 的表中。把 `oauthserver` 的迁移加入[发布命令](migrations.md#publishing-framework-migrations)，发布后执行 `migrate up`（`apitoken` 的 OAuth 令牌需要它的第二个迁移）：

```go
migratecmd.Publish(
    migrate.Source{Name: "apitoken", FS: apitoken.Migrations()},
    migrate.Source{Name: "oauthserver", FS: oauthserver.Migrations()},
)
```

<a id="assembling"></a>
## 装配

创建授权服务器，端点挂在应用的根上，同意页的两个接口注册在编辑后台的 API 上：

```go
oauth := oauthserver.New(a.db, tokens, oauthserver.Options{
    Issuer:    "https://blog.example.com",
    Resources: []string{"https://blog.example.com/mcp"},
    Scopes: []oauthserver.Scope{
        {Name: "articles:read", Description: "查看文章"},
        {Name: "articles:write", Description: "撰写和发布文章"},
    },
    ConsentURL: "/oauth/consent", // 编辑后台单页应用中的同意页
})
oauth.Routes(router)
oauth.Register(api, rest.RequireBearer(api, tokens.Validate))
```

| 选项 | 说明 |
| --- | --- |
| `Issuer` | 授权服务器的地址，也是各端点的前缀。必须是没有路径的 https 地址，本机开发可以用 `http://localhost:8080` |
| `Resources` | 令牌可以用于的 MCP 服务地址，与用户在客户端中填写的完全相同 |
| `Scopes` | 客户端可以申请的范围，见下一节 |
| `Clients` | 预先登记的客户端，见[客户端](#clients) |
| `DynamicRegistration` | 开启动态客户端注册，默认关闭，见[动态客户端注册](#dynamic-registration) |
| `ConsentURL` | 单页应用中同意页的地址，见[同意页](#consent-page)。通常是本站的路径；单页应用部署在别的地址时写完整的 https 地址 |
| `TokenLifetime`、`TokenIdleTimeout` | 令牌的最长有效期和最长空闲时间，默认 1 年和 30 天 |

`Routes` 注册客户端访问的端点：

| 端点 | 说明 |
| --- | --- |
| `GET /.well-known/oauth-authorization-server` | 授权服务器元数据 |
| `GET /oauth/authorize` | 校验授权请求，跳到同意页 |
| `POST /oauth/token` | 授权码换令牌 |
| `POST /oauth/revoke` | 客户端交回令牌（RFC 7009） |

`Register` 在 API 上注册同意页调用的两个接口，第二个参数是它们的认证方式，与编辑后台的其他接口相同：

| 接口 | 操作 ID | 说明 |
| --- | --- | --- |
| `GET /oauth/consent?request=…` | `oauth-consent-get` | 读取授权请求：应用、范围、跳转去向 |
| `POST /oauth/consent` | `oauth-consent-decide` | 允许或拒绝，返回浏览器要跳转去的地址 |

授权码 10 分钟有效。用 `oauth.Cleanup` 定时删除过期的授权码，见[清理框架数据](scheduling.md#framework-cleanups)。

<a id="scopes"></a>
### 范围

范围是客户端申请的权限，同意页逐项列出 `Description`。名称通常就是[权限目录](authorization.md#permission-catalog)中的权限：令牌的 `Scopes` 是用户同意的范围，`Subject.Can` 同时检查用户本人的权限和它，与个人访问令牌相同。所以一个只有查看权限的编辑授权了 `articles:write`，令牌也发布不了文章。

客户端不传 `scope` 时申请全部范围。只把适合交给 AI 的权限放进来，删除文章、管理成员这类操作不要提供。

<a id="mcp"></a>
### 挂到 MCP 服务

MCP 服务要告诉客户端去哪里授权，并且只接受发给自己的令牌：

```go
res := oauth.Resource("https://blog.example.com/mcp")
router.Handle(res.MetadataPath(), mcpx.ResourceMetadata(res))
router.Handle("/mcp", mcpx.Handler(tools, mcpx.HandlerOptions{
    Validate: tokens.ValidateFor(res.URL),
    Accounts: identity.Accounts{editor.Type: editorweb.Resolve(a.db, acc.Editors)},
    Resource: &res,
}))
```

- `mcpx.ResourceMetadata` 是受保护资源元数据（RFC 9728），挂在 `/.well-known/oauth-protected-resource/mcp`，指向授权服务器。
- 设置了 `Resource` 后，未认证的请求得到 `401` 和 `WWW-Authenticate: Bearer resource_metadata="…"`，客户端由此开始授权。
- `tokens.ValidateFor(res.URL)` 只接受发给这个地址的 OAuth 令牌，个人访问令牌照常可用。JSON API 用的 `tokens.Validate` 不接受 OAuth 令牌：给 Claude 的令牌不能拿来调用其他接口。

<a id="flow"></a>
## 授权流程

用户在 Claude 中添加连接器，填写 `https://blog.example.com/mcp`：

1. Claude 访问 MCP 服务，得到 `401`，从受保护资源元数据和授权服务器元数据中找到各端点。
2. Claude 在浏览器中打开 `/oauth/authorize`。授权端点校验请求后跳到编辑后台的同意页，地址中带着 `request` 参数。
3. 编辑没有登录时，编辑后台先让他登录，再回到同意页。同意页显示 Claude 申请哪些范围、以哪个账号、授权后跳转到 `claude.ai`。
4. 编辑点“允许”，浏览器带着授权码回到 Claude；Claude 用授权码和 PKCE 的 verifier 换取令牌。
5. Claude 带着令牌调用 MCP 工具，以这个编辑的身份执行。

用户点“拒绝”时，Claude 收到 `access_denied`。授权请求本身有问题时：client_id 或回调地址不对的，不能跳回去，授权端点跳到同意页并带上 `error` 参数，值是错误码 `oauth.invalid_client` 或 `oauth.invalid_redirect_uri`；其他问题（缺少 PKCE、未知的范围）带着错误直接跳回客户端。

<a id="clients"></a>
## 客户端

- **https 地址形式的 client_id**（Client ID Metadata Document）：claude.ai、ChatGPT、Claude Code 都用这种方式。授权时用[出站请求](outbound.md)取回这个地址上的 JSON，检查其中的 `client_id` 与地址完全相同、`redirect_uris` 包含本次的回调地址。不需要任何登记，取回的地址拒绝内网地址。
- **预先登记的客户端**：自研的 agent 在 `Options.Clients` 中登记 ID、名称和回调地址。

只支持公开客户端（不用客户端密钥，靠 PKCE 保证安全）。回调地址按完整地址匹配；本机地址（`localhost`、`127.0.0.1`）忽略端口，Claude Code 每次用不同的端口接收回调。

<a id="dynamic-registration"></a>
### 动态客户端注册

有的客户端不提供 Client ID Metadata Document，只会先注册自己（RFC 7591）。MCP 规范已经把这种方式标为过时，claude.ai 和 ChatGPT 只在授权服务器不支持 Client ID Metadata Document 时才用它，所以默认关闭。需要兼容这类客户端时开启：

```go
oauthserver.Options{
    // ……
    DynamicRegistration: true,
}
```

开启后元数据中有 `registration_endpoint`，客户端 `POST /oauth/register` 提交名称和回调地址，得到 `dcr_` 开头的 client_id，之后的授权与其他客户端相同。只登记公开客户端，回调地址必须是 https 地址或本机的 http 地址，最多 10 个。

注册接口不需要认证，任何人都能调用：用[限流](rate-limiting.md)按 IP 限制它，并定时删除长期没有用于授权的注册：

```go
sched.Add("oauth:cleanup-clients", "@daily", schedule.Cleanup(oauth.CleanupClients(90*24*time.Hour), 1000))
```

删除注册不影响已经签发的令牌，客户端下次连接时重新注册。

<a id="tokens"></a>
## 令牌

令牌是 `apitoken` 中 `Kind` 为 `oauth` 的令牌，记录客户端、名称和资源：

- **不发刷新令牌**，令牌按使用滑动过期：30 天内用过就继续有效，最长 1 年。到期后客户端让用户重新授权一次。每次请求都查库校验，撤销立即生效。
- 出现在用户的[令牌列表](api-tokens.md#listing-tokens)中，`ClientName` 是授权时的应用名，用来做“已连接的应用”页面；用 `Revoke` 断开连接。
- “退出其他设备”（`RevokeOthers`）不影响已连接的应用；封禁账号、修改密码时的 `RevokeSubject` 一并撤销它们。

<a id="consent-page"></a>
## 同意页

同意页是编辑后台单页应用中的一个路由，路径与 `ConsentURL` 相同。它从地址中取出 `request`，用[客户端](clients.md#creating-the-client)读取授权请求，用户点击后提交选择，再跳转到返回的地址。下面是用 TanStack Router 写的 `src/routes/oauth/consent.tsx`：

```tsx
import { createFileRoute } from "@tanstack/react-router"
import { useMutation, useQuery } from "@tanstack/react-query"
import { api } from "../../client"

export const Route = createFileRoute("/oauth/consent")({
  validateSearch: (s: Record<string, unknown>) => ({
    request: String(s.request ?? ""),
    error: String(s.error ?? ""),
  }),
  component: Consent,
})

function Consent() {
  const { request, error } = Route.useSearch()
  const consent = useQuery({
    queryKey: ["oauth-consent", request],
    enabled: request !== "",
    queryFn: async () => {
      const { data, error } = await api.GET("/oauth/consent", { params: { query: { request } } })
      if (error) throw error
      return data
    },
  })
  const decide = useMutation({
    mutationFn: async (approve: boolean) => {
      const { data, error } = await api.POST("/oauth/consent", { body: { request, approve } })
      if (error) throw error
      location.assign(data.url) // 回到 Claude；整页跳转，不经过前端路由
    },
  })

  if (error) return <p>无法授权（{error}）</p> // 错误码，例如 oauth.invalid_client
  if (consent.error) return <p>{consent.error.detail}</p>
  if (!consent.data) return null
  const c = consent.data
  return (
    <div>
      <h1>{c.client_name} 请求访问你的账号</h1>
      <ul>{c.scopes.map((s) => <li key={s.name}>{s.description}</li>)}</ul>
      <p>授权后将跳转到 {c.redirect_host}</p>
      <button disabled={decide.isPending} onClick={() => decide.mutate(true)}>允许</button>
      <button disabled={decide.isPending} onClick={() => decide.mutate(false)}>拒绝</button>
    </div>
  )
}
```

- **登录**：同意页和编辑后台的其他页面一样要求登录。没有登录时，编辑后台照常跳到登录页，登录后回到原地址（包括 `request` 参数）。
- **只接受登录 token**：个人访问令牌和 OAuth 令牌带有权限限制，不代表用户本人，用它们调用这两个接口返回 `403 oauth.restricted_credential`。
- **重新校验**：`request` 就是原始的授权参数，两个接口每次都重新校验，被改过的请求返回 `422`（`oauth.invalid_client`、`oauth.invalid_redirect_uri` 或 `oauth.invalid_request`）。
- **不能被嵌入框架**：同意页要防止诱导点击。`httpx.SPA` 默认带 `frame-ancestors 'none'`，见[单页应用](spa.md)；放开了它时，至少不要放开同意页所在的路由。

<a id="deployment"></a>
## 部署

- 授权服务器和 MCP 服务都要能从公网访问。Claude 的请求来自 `160.79.104.0/21`，前面有防火墙时放行这个网段。
- `Issuer` 和 `Resources` 是用户看到的地址，在反向代理之后也写对外的 https 地址。
- 取回客户端元数据要访问 claude.ai 等地址，服务器需要能访问外网。

<a id="testing"></a>
## 测试

测试中代替浏览器和同意页走完整个流程：

1. 请求 `GET /oauth/authorize`，从 `Location` 中取出 `request`；
2. 用 `tokens.Login` 签发的登录 token 调用 `POST /oauth/consent`，得到带授权码的地址；
3. 用授权码换令牌，再用令牌调用 MCP 工具。

框架自己的测试还用 MCP 官方 Go SDK 的 OAuth 客户端（`auth.NewAuthorizationCodeHandler`）走过一遍：从 `401` 开始发现元数据、授权、换取令牌到调用工具。
