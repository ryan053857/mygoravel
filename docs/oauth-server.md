# OAuth 授权服务器

- [简介](#introduction)
- [安装](#installation)
- [装配](#assembling)
    - [范围](#scopes)
    - [挂到 MCP 服务](#mcp)
- [授权流程](#flow)
- [客户端](#clients)
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

授权页是 `web` 的页面，用户用页面的会话登录，所以能授权的是页面接受的账号体系。本章的例子是博客的编辑：他们在编辑后台登录，在 Claude 中查找和发布文章。

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

在编辑后台的路由中创建授权服务器，挂在应用的根上：

```go
oauth := oauthserver.New(a.db, tokens, oauthserver.Options{
    Issuer:    "https://blog.example.com",
    Resources: []string{"https://blog.example.com/mcp"},
    Scopes: []oauthserver.Scope{
        {Name: "articles:read", Description: "查看文章"},
        {Name: "articles:write", Description: "撰写和发布文章"},
    },
    Brand:   "Blog 编辑后台",
    Account: editorEmail, // func(ctx, sub) string：同意页上显示的当前账号，例如邮箱
})
oauth.Routes(router, pages)
```

| 选项 | 说明 |
| --- | --- |
| `Issuer` | 授权服务器的地址，也是各端点的前缀。必须是没有路径的 https 地址，本机开发可以用 `http://localhost:8080` |
| `Resources` | 令牌可以用于的 MCP 服务地址，与用户在客户端中填写的完全相同 |
| `Scopes` | 客户端可以申请的范围，见下一节 |
| `Clients` | 预先登记的客户端，见[客户端](#clients) |
| `Brand`、`Account` | 同意页上方的名称和当前账号 |
| `Consent` | 换成自己的同意页，见[同意页](#consent-page) |
| `TokenLifetime`、`TokenIdleTimeout` | 令牌的最长有效期和最长空闲时间，默认 1 年和 30 天 |

`Routes` 注册以下端点。授权页注册为 `pages` 中需要登录的页面，没有登录时先去 `pages` 的登录页，登录后回到授权页：

| 端点 | 说明 |
| --- | --- |
| `GET /.well-known/oauth-authorization-server` | 授权服务器元数据 |
| `GET /oauth/authorize` | 同意页 |
| `POST /oauth/authorize` | 用户允许或拒绝 |
| `POST /oauth/token` | 授权码换令牌 |
| `POST /oauth/revoke` | 客户端交回令牌（RFC 7009） |

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
2. Claude 打开授权页。用户没有登录编辑后台时先登录，然后看到同意页：Claude 申请哪些范围、以哪个账号、授权后跳转到 `claude.ai`。
3. 用户点“允许”，浏览器带着授权码回到 Claude；Claude 用授权码和 PKCE 的 verifier 换取令牌。
4. Claude 带着令牌调用 MCP 工具，以这个编辑的身份执行。

用户点“拒绝”时，Claude 收到 `access_denied`。授权请求本身有问题时：client_id 或回调地址不对的，显示错误页，不跳回去；其他问题（缺少 PKCE、未知的范围）带着错误跳回客户端。

<a id="clients"></a>
## 客户端

- **https 地址形式的 client_id**（Client ID Metadata Document）：claude.ai、ChatGPT、Claude Code 都用这种方式。授权时用[出站请求](outbound.md)取回这个地址上的 JSON，检查其中的 `client_id` 与地址完全相同、`redirect_uris` 包含本次的回调地址。不需要任何登记，取回的地址拒绝内网地址。
- **预先登记的客户端**：自研的 agent 在 `Options.Clients` 中登记 ID、名称和回调地址。

只支持公开客户端（不用客户端密钥，靠 PKCE 保证安全）。回调地址按完整地址匹配；本机地址（`localhost`、`127.0.0.1`）忽略端口，Claude Code 每次用不同的端口接收回调。

<a id="tokens"></a>
## 令牌

令牌是 `apitoken` 中 `Kind` 为 `oauth` 的令牌，记录客户端、名称和资源：

- **不发刷新令牌**，令牌按使用滑动过期：30 天内用过就继续有效，最长 1 年。到期后客户端让用户重新授权一次。每次请求都查库校验，撤销立即生效。
- 出现在用户的[令牌列表](api-tokens.md#listing-tokens)中，`ClientName` 是授权时的应用名，用来做“已连接的应用”页面；用 `Revoke` 断开连接。
- “退出其他设备”（`RevokeOthers`）不影响已连接的应用；封禁账号、修改密码时的 `RevokeSubject` 一并撤销它们。

<a id="consent-page"></a>
## 同意页

默认的同意页是 `ui.ConsentPage`，使用这组页面的[主题](components.md)。换成自己的页面时设置 `Options.Consent`，收到的 `ui.ConsentProps` 中有应用名、账号、范围、跳转去向，以及要原样提交回去的参数：

```go
Consent: func(r *http.Request, p ui.ConsentProps) templ.Component {
    return views.Consent(p) // 表单 POST 到 p.Action，带上 p.Fields 和 decision=allow 或 deny
},
```

同意页禁止被嵌入其他网站的框架，防止诱导点击。提交由页面的跨站请求防护保护。

<a id="deployment"></a>
## 部署

- 授权服务器和 MCP 服务都要能从公网访问。Claude 的请求来自 `160.79.104.0/21`，前面有防火墙时放行这个网段。
- `Issuer` 和 `Resources` 是用户看到的地址，在反向代理之后也写对外的 https 地址。
- 取回客户端元数据要访问 claude.ai 等地址，服务器需要能访问外网。

<a id="testing"></a>
## 测试

`web/webtest` 的客户端登录后访问授权页、提交同意，再用授权码换令牌，就能在测试中走完整个流程。框架自己的测试还用 MCP 官方 Go SDK 的 OAuth 客户端（`auth.NewAuthorizationCodeHandler`）走过一遍：从 `401` 开始发现元数据、授权、换取令牌到调用工具。
