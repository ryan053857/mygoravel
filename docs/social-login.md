# 第三方登录

- [简介](#introduction)
- [提供方](#providers)
- [登录与注册](#signing-in)
- [在页面中登录](#page-login)
- [在单页应用和 App 中登录](#api-login)
    - [App 原生登录](#native-sign-in)
- [账号关联](#linking-accounts)
- [Sign in with Apple](#sign-in-with-apple)

<a id="introduction"></a>
## 简介

用户可以用 Google、GitHub 等账号登录，相当于 Laravel 的 Socialite。分两步：

1. `login/oauth` 与第三方交互，得到第三方确认过的账号 `login.External`：OpenID Connect 提供方（Google、Apple、Microsoft、企业的身份系统）用 `oauth.Discover`，只支持 OAuth 2.0 的提供方（GitHub、微信）用 `oauth.New`；
2. [`login.Realm`](login.md) 的 `External` 把第三方账号当作一种[登录标识](login.md#identifiers)：已经绑定就登录，还没有就返回注册凭证。

三种入口共用同一套提供方，只有与第三方交互的方式不同：

| 入口 | 谁与第三方交互 | 方法 |
| --- | --- | --- |
| [页面](#page-login) | 服务器：跳转到第三方，在回调中换取 token | `Start`、`Finish` |
| [单页应用和 App](#api-login) | 客户端：跳转到第三方，把授权码交给 API | `Exchange` |
| [App 原生登录](#native-sign-in) | SDK 直接返回 ID token | `IDToken` |

本章为博客的编辑后台加上 Google 和 GitHub 登录，读者 App 可以用它们注册。

<a id="providers"></a>
## 提供方

先在第三方开放平台登记应用，得到 client ID 和 client secret，并登记回调地址。下面是完整的 `internal/auth/social/social.go`：

```go
package social

import (
    "cmp"
    "context"
    "encoding/json"
    "fmt"
    "net/http"
    "strconv"
    "strings"

    "golang.org/x/oauth2"
    "golang.org/x/oauth2/github"

    "github.com/ryan053857/mygoravel/errorx"
    "github.com/ryan053857/mygoravel/login"
    "github.com/ryan053857/mygoravel/login/oauth"
)

var (
    ErrUnknownProvider = errorx.Define(errorx.NotFound, "social.unknown_provider")
    ErrNotLinked       = errorx.Define(errorx.PermissionDenied, "social.not_linked")
    ErrAlreadyLinked   = errorx.Define(errorx.Conflict, "social.already_linked")
)

// Client 是在第三方开放平台登记的应用。
type Client struct {
    ClientID     string `yaml:"client_id"`
    ClientSecret string `yaml:"client_secret"`
}

type Config struct {
    Google Client `yaml:"google"`
    GitHub Client `yaml:"github"`
    // RedirectURL 是页面登录的回调地址，{provider} 换成提供方的名称。
    RedirectURL string `yaml:"redirect_url"`
    // ClientRedirectURLs 是单页应用和 App 登记的回调地址，API 只接受这些地址。
    ClientRedirectURLs []string `yaml:"client_redirect_urls"`
    // GoogleAppClientIDs 是 App 原生登录使用的 Google client ID，例如 iOS 和 Android 各一个。
    GoogleAppClientIDs []string `yaml:"google_app_client_ids"`
}

// Providers 按名称创建全部提供方。Google 在这里读取发现文档，所以应当在启动时调用一次。
func Providers(ctx context.Context, cfg Config) (map[string]*oauth.Provider, error) {
    google, err := oauth.Discover(ctx, oauth.Config{
        Provider:           "google",
        Issuer:             "https://accounts.google.com",
        ClientID:           cfg.Google.ClientID,
        ClientSecret:       cfg.Google.ClientSecret,
        RedirectURL:        strings.ReplaceAll(cfg.RedirectURL, "{provider}", "google"),
        ClientRedirectURLs: cfg.ClientRedirectURLs,
        Audiences:          append([]string{cfg.Google.ClientID}, cfg.GoogleAppClientIDs...),
    })
    if err != nil {
        return nil, err
    }
    gh, err := oauth.New(oauth.Config{
        Provider:           "github",
        ClientID:           cfg.GitHub.ClientID,
        ClientSecret:       cfg.GitHub.ClientSecret,
        RedirectURL:        strings.ReplaceAll(cfg.RedirectURL, "{provider}", "github"),
        ClientRedirectURLs: cfg.ClientRedirectURLs,
        Scopes:             []string{"read:user", "user:email"},
    }, github.Endpoint, githubAccount)
    if err != nil {
        return nil, err
    }
    return map[string]*oauth.Provider{"google": google, "github": gh}, nil
}

// githubAccount 用 GitHub API 读取账号：用户 ID 来自 /user，邮箱和它是否已验证来自 /user/emails。
func githubAccount(ctx context.Context, client *http.Client, _ *oauth2.Token) (login.External, error) {
    var u struct {
        ID    int64  `json:"id"`
        Login string `json:"login"`
        Name  string `json:"name"`
    }
    if err := getJSON(ctx, client, "https://api.github.com/user", &u); err != nil {
        return login.External{}, err
    }
    var emails []struct {
        Email    string `json:"email"`
        Primary  bool   `json:"primary"`
        Verified bool   `json:"verified"`
    }
    if err := getJSON(ctx, client, "https://api.github.com/user/emails", &emails); err != nil {
        return login.External{}, err
    }
    ext := login.External{Subject: strconv.FormatInt(u.ID, 10), Name: cmp.Or(u.Name, u.Login)}
    for _, e := range emails {
        if e.Primary {
            ext.Email, ext.EmailVerified = e.Email, e.Verified
        }
    }
    return ext, nil
}

func getJSON(ctx context.Context, client *http.Client, url string, v any) error {
    req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
    if err != nil {
        return err
    }
    resp, err := client.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        return fmt.Errorf("GET %s: %s", url, resp.Status)
    }
    return json.NewDecoder(resp.Body).Decode(v)
}
```

把 `social.Config` 加入应用的根配置结构体，配置段为 `social`：

```yaml
social:
  google:
    client_id: ${GOOGLE_CLIENT_ID}
    client_secret: ${GOOGLE_CLIENT_SECRET}
  github:
    client_id: ${GITHUB_CLIENT_ID}
    client_secret: ${GITHUB_CLIENT_SECRET}
  redirect_url: https://blog.example.com/auth/{provider}/callback
  client_redirect_urls:
    - https://app.example.com/auth/callback
```

几点说明：

- OpenID Connect 提供方的账号来自 ID token：`oauth.Discover` 校验它的签名、签发者、受众、有效期和 nonce，读取 `sub`、`email`、`email_verified` 和 `name`。接入 Microsoft 或企业的身份系统时只需要换掉 `Issuer`。
- 只支持 OAuth 2.0 的提供方，由应用写一个函数用换到的 token 读取账号，例如上面的 `githubAccount`；`client` 会在请求中带上 token。
- 第三方返回的用户 ID 才是账号的唯一标识：GitHub 是数字 `id`，OpenID Connect 是 `sub`，微信是 `unionid`。用户名和邮箱都可能变化，不要用作 `Subject`。
- `oauth.Discover` 会请求发现文档，校验 ID token 的公钥按需下载并缓存，所以提供方在启动时创建一次，之后一直复用。

<a id="signing-in"></a>
## 登录与注册

得到 `login.External` 之后交给 `Realm.External`：

```go
out, err := users.External(ctx, ext)
```

| 结果 | 含义 | 应用要做的 |
| --- | --- | --- |
| `out.Subject` | 这个第三方账号已经绑定 | 签发会话或 token |
| `out.Proof` | 还没有绑定 | 注册新账号，或者让用户登录已有账号后[绑定](#linking-accounts) |

注册与[验证码注册](login.md#codes)相同：应用的注册 action 在事务中创建账号行，再把 `out.Proof` 交给 `Register`。第三方账号作为登录标识写入，第三方确认过（`EmailVerified`）的邮箱也一并写入，之后用户也可以用这个邮箱收验证码登录。第三方账号登录不再要求[二次验证](login.md#two-factor)，由提供方负责验证身份。

**框架不按邮箱把第三方账号自动关联到已有账号。** 否则任何人都可以在一个不验证邮箱的第三方平台上填上别人的邮箱，借此登录别人的账号。注册时邮箱已属于其他账号，`Register` 返回 `login.ErrTaken`：提示用户先用原来的方式登录，再绑定这个第三方账号。

<a id="page-login"></a>
## 在页面中登录

页面登录由服务器完成整个流程：

1. 登录页上的“使用 GitHub 登录”链接指向 `/auth/github`；
2. `Start` 生成跳转地址和 `oauth.Flow`（state、nonce 和 PKCE verifier），Flow 存入会话，然后跳转到第三方；
3. 用户同意授权后，第三方带着授权码和 state 跳回 `/auth/github/callback`；
4. `Finish` 比较 state，用授权码和 verifier 换取 token，读取账号。

编辑后台不开放注册，第三方账号还没有绑定时返回 `social.ErrNotLinked`，编辑先用密码登录，再在账号设置中[绑定](#linking-accounts)。下面是完整的 `internal/auth/authweb/social.go`：

```go
package authweb

import (
    "errors"
    "net/http"

    "github.com/alexedwards/scs/v2"
    "github.com/go-chi/chi/v5"

    "example.com/blog/internal/auth/social"
    "github.com/ryan053857/mygoravel/login"
    "github.com/ryan053857/mygoravel/login/oauth"
    "github.com/ryan053857/mygoravel/web"
)

type socialHandlers struct {
    pages     *web.Pages
    sessions  *scs.SessionManager
    providers map[string]*oauth.Provider
    users     *login.Realm
}

// RegisterSocial 注册第三方登录的跳转和回调页面。
func RegisterSocial(r chi.Router, pages *web.Pages, sessions *scs.SessionManager, providers map[string]*oauth.Provider, users *login.Realm) {
    h := &socialHandlers{pages: pages, sessions: sessions, providers: providers, users: users}
    public := pages.Routes(r, web.Public)
    public.Get("/auth/{provider}", h.redirect)
    public.Get("/auth/{provider}/callback", h.callback)
}

func (h *socialHandlers) redirect(w http.ResponseWriter, r *http.Request) error {
    p, ok := h.providers[chi.URLParam(r, "provider")]
    if !ok {
        return social.ErrUnknownProvider.New()
    }
    url, flow := p.Start()
    h.sessions.Put(r.Context(), "social.flow", flow.String())
    h.sessions.Put(r.Context(), "social.next", web.LocalPath(r.URL.Query().Get("next"), "/admin"))
    http.Redirect(w, r, url, http.StatusSeeOther)
    return nil
}

func (h *socialHandlers) callback(w http.ResponseWriter, r *http.Request) error {
    p, ok := h.providers[chi.URLParam(r, "provider")]
    if !ok {
        return social.ErrUnknownProvider.New()
    }
    ctx := r.Context()
    flow, err := oauth.ParseFlow(h.sessions.PopString(ctx, "social.flow"))
    if err != nil {
        return err
    }
    next := h.sessions.PopString(ctx, "social.next")
    ext, err := p.Finish(ctx, flow, r.URL.Query())
    if errors.Is(err, oauth.ErrDenied) { // 用户在第三方页面取消了授权
        web.Redirect(w, r, "/login")
        return nil
    }
    if err != nil {
        return err
    }
    out, err := h.users.External(ctx, ext)
    if err != nil {
        return err
    }
    if out.Proof != "" {
        return social.ErrNotLinked.New()
    }
    if err := h.pages.Login(r, out.Subject); err != nil {
        return err
    }
    web.Redirect(w, r, next)
    return nil
}
```

在[装配页面](views.md#assembling-pages)时和密码登录一起注册：

```go
providers, err := social.Providers(ctx, a.cfg.Social)
if err != nil {
    return err
}
authweb.RegisterSocial(r, pages, sessions, providers, users)
```

Flow 用 `Pop` 读取，只能使用一次。回调是从第三方跳回本站的顶层 GET 请求，SameSite 为 `lax` 的会话 cookie 会随它发送，`pages.Routes` 的跨站请求防护也只拦截 `POST` 等非安全请求，所以会话中的值都能读到。state 不符、授权码已用过、ID token 无效时返回 `oauth.ErrInvalid`，呈现为 `401`。

<a id="api-login"></a>
## 在单页应用和 App 中登录

单页应用和 App 不使用会话，由客户端自己完成跳转：

1. 客户端生成 state、nonce 和 PKCE verifier，自己保存，然后打开第三方的授权地址，`redirect_uri` 是它登记的回调地址；
2. 第三方带着授权码跳回客户端，客户端比较 state；
3. 客户端把授权码、verifier、nonce 和回调地址交给 API；
4. API 用 client secret 换取 token，读取账号，签发 [API 令牌](api-tokens.md)。

读者 App 可以用第三方账号注册：还没有绑定时，API 返回注册凭证和第三方提供的名称，App 进入注册页预填。下面是完整的 `internal/auth/authhttp/social.go`：

```go
package authhttp

import (
    "context"
    "net"
    "net/http"

    "github.com/danielgtaylor/huma/v2"

    "example.com/blog/internal/auth/social"
    "github.com/ryan053857/mygoravel/apitoken"
    "github.com/ryan053857/mygoravel/httpx/rest"
    "github.com/ryan053857/mygoravel/login"
    "github.com/ryan053857/mygoravel/login/oauth"
)

type SocialTokenInput struct {
    Provider  string `path:"provider"`
    UserAgent string `header:"User-Agent"`
    Body      struct {
        Code         string `json:"code,omitempty"`
        CodeVerifier string `json:"code_verifier,omitempty"`
        RedirectURI  string `json:"redirect_uri,omitempty"`
        IDToken      string `json:"id_token,omitempty" doc:"App 原生登录时 SDK 返回的 ID token，代替 code"`
        Nonce        string `json:"nonce,omitempty"`
    }
    ip string
}

// Resolve 记录客户端地址，见 Huma 的 Resolver。
func (in *SocialTokenInput) Resolve(ctx huma.Context) []error {
    in.ip, _, _ = net.SplitHostPort(ctx.RemoteAddr())
    return nil
}

type SocialTokenOutput struct {
    Body struct {
        Token string `json:"token,omitempty"`
        Proof string `json:"proof,omitempty" doc:"还没有账号：带着它调用注册接口"`
        Name  string `json:"name,omitempty"`
    }
}

// RegisterSocial 注册用第三方账号换取 API 令牌的接口。
func RegisterSocial(api *rest.API, providers map[string]*oauth.Provider, users *login.Realm, tokens *apitoken.Store) {
    op := huma.Operation{OperationID: "auth-social-token", Method: http.MethodPost, Path: "/auth/{provider}/tokens"}
    rest.Public()(&op)
    rest.Register(api, op, func(ctx context.Context, in *SocialTokenInput) (*SocialTokenOutput, error) {
        p, ok := providers[in.Provider]
        if !ok {
            return nil, social.ErrUnknownProvider.New()
        }
        var ext login.External
        var err error
        if in.Body.IDToken != "" {
            ext, err = p.IDToken(ctx, in.Body.IDToken, in.Body.Nonce)
        } else {
            ext, err = p.Exchange(ctx, oauth.Grant{
                Code: in.Body.Code, Verifier: in.Body.CodeVerifier, RedirectURI: in.Body.RedirectURI, Nonce: in.Body.Nonce,
            })
        }
        if err != nil {
            return nil, err
        }
        out, err := users.External(ctx, ext)
        if err != nil {
            return nil, err
        }
        resp := &SocialTokenOutput{}
        if out.Proof != "" {
            resp.Body.Proof, resp.Body.Name = out.Proof, ext.Name
            return resp, nil
        }
        resp.Body.Token, err = tokens.Login(ctx, out.Subject, apitoken.Device{IP: in.ip, UserAgent: in.UserAgent})
        return resp, err
    })
}
```

App 带着 `proof` 调用[注册接口](login.md#codes)，与验证码注册是同一个接口。

`Exchange` 检查两件事：`redirect_uri` 必须在 `client_redirect_urls` 中，否则攻击者可以用发往其他地址的授权码换取 token；客户端没有 client secret，PKCE 是它唯一的保护，所以 `code_verifier` 必填。client secret 始终留在服务器上。

<a id="native-sign-in"></a>
### App 原生登录

App 也可以使用系统或第三方 SDK 提供的原生登录，例如 Google 登录和 Sign in with Apple。SDK 直接返回 ID token，不经过授权码，App 把它放在 `id_token` 中交给同一个接口，由 `IDToken` 校验。

SDK 签发的 ID token 的受众是 App 自己的 client ID，它与网页使用的不同，所以要列在 `oauth.Config.Audiences` 中，见上面的 `GoogleAppClientIDs`。

nonce 用来确认 ID token 属于这一次登录：截获的 ID token 在有效期内可以被重放，只有服务器下发 nonce、App 把它放进登录请求，服务器再比较保存的那个值，才能防住。有的 SDK 要求在请求中放 nonce 的 SHA-256，这时比较哈希值。

<a id="linking-accounts"></a>
## 账号关联

一个账号可以绑定多个第三方账号。账号设置页面中的“绑定 GitHub”与页面登录的跳转相同，回调中用 `Link` 绑定到当前登录的账号：

```go
ext, err := p.Finish(ctx, flow, r.URL.Query())
if err != nil {
    return err
}
out, err := h.users.External(ctx, ext)
if err != nil {
    return err
}
if out.Proof == "" {
    return social.ErrAlreadyLinked.New() // 这个第三方账号已经绑定了某个账号
}
sub, _ := identity.FromContext(ctx)
if err := h.users.Link(ctx, h.db, sub.Ref(), out.Proof); err != nil {
    return err
}
```

`Identifiers` 列出账号的全部登录标识，第三方账号的 `Kind` 是提供方的名称；`Detach` 解除绑定，不能解除最后一种登录方式，见[管理登录方式](login.md#managing)。设置了 [`PerTenant`](login.md#identifiers) 的账号体系，第三方账号同样按租户隔离：同一个 Google 账号可以在两个租户中各绑定一个账号。

<a id="sign-in-with-apple"></a>
## Sign in with Apple

Apple 也是 OpenID Connect 提供方，签发者是 `https://appleid.apple.com`，与 Google 的写法基本相同，但有几点差别：

```go
apple, err := oauth.Discover(ctx, oauth.Config{
    Provider:     "apple",
    Issuer:       "https://appleid.apple.com",
    ClientID:     cfg.Apple.ServiceID,
    ClientSecret: cfg.Apple.ClientSecret,
    RedirectURL:  "https://blog.example.com/auth/apple/callback",
    Scopes:       []string{"openid", "email", "name"},
    NoPKCE:       true,
    FormPost:     true,
})
```

- **client secret 是你自己签名的 JWT**：用 Apple 下发的私钥（`.p8`）按 ES256 签名，有效期最长 6 个月，需要定期重新生成，见 [Apple 的文档](https://developer.apple.com/documentation/accountorganizationaldatasharing/creating-a-client-secret)。
- **不使用 PKCE**：Apple 的授权和 token 接口没有 PKCE 参数，依靠 state、nonce 和 client secret，所以设置 `NoPKCE`。
- **用户姓名只在第一次授权时返回**：它不在 ID token 中，而是回调表单中的 `user` 字段，第一次登录时就要保存下来。`email_verified` 是字符串，`oauth` 已经处理。
- **申请了姓名或邮箱时，回调是跨站 POST**：Apple 要求 `response_mode=form_post`（`FormPost`），浏览器从 `appleid.apple.com` 把授权码提交到回调地址。

最后一点影响[页面登录](#page-login)的写法：

1. `pages.Routes` 会拒绝跨站的 `POST`，需要把 Apple 加入 `web.Options.TrustedOrigins`：
   ```go
   TrustedOrigins: []string{"https://appleid.apple.com"},
   ```
2. 跨站 `POST` **不会**携带 SameSite 为 `lax` 的会话 cookie，回调中读不到会话里的 Flow。因此跳转时把 `flow.String()` 存进一个单独的短期 cookie，它必须是 `SameSite=None`：
   ```go
   http.SetCookie(w, &http.Cookie{
       Name: "apple_auth", Value: flow.String(),
       Path: "/auth/apple/callback", MaxAge: 600,
       HttpOnly: true, Secure: true, SameSite: http.SameSiteNoneMode,
   })
   ```
   回调用 `public.Post("/auth/apple/callback", ...)` 注册，从这个 cookie 中取出 Flow 并删除 cookie，再把 `r.PostForm`（先调用 `r.ParseForm`）交给 `Finish`。其余步骤与 Google 相同。

回调中调用 `pages.Login` 不受影响：它为这个请求创建新的会话，浏览器接受顶层跳转响应中的会话 cookie。
