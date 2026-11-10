# 第三方登录

- [简介](#introduction)
- [提供方](#providers)
- [账号关联](#linking-accounts)
- [在页面中登录](#page-login)
- [在单页应用和 App 中登录](#api-login)
    - [App 原生登录](#native-sign-in)
- [Sign in with Apple](#sign-in-with-apple)

<a id="introduction"></a>
## 简介

用户可以用 GitHub、Google 等账号登录，相当于 Laravel 的 Socialite。mygoravel 不封装这部分，直接使用两个成熟的库：

- [golang.org/x/oauth2](https://pkg.go.dev/golang.org/x/oauth2)：OAuth 2.0 授权码流程，包括 PKCE；
- [go-oidc](https://github.com/coreos/go-oidc)：校验 OpenID Connect 提供方（Google、Apple、Microsoft 等）签发的 id_token。

```bash
go get golang.org/x/oauth2 github.com/coreos/go-oidc/v3
```

两种登录入口共用同一套提供方和账号关联逻辑，只有最后一步不同：

| 入口 | 谁与第三方交互 | 登录结果 |
| --- | --- | --- |
| [页面](#page-login) | 服务器：跳转到第三方，在回调中换取 token | 写入[会话](views.md#login) |
| [单页应用和 App](#api-login) | 客户端：跳转到第三方，把授权码交给 API | 签发 [API 令牌](api-tokens.md) |

本章的示例为博客的编辑后台加上 GitHub 和 Google 登录。

<a id="providers"></a>
## 提供方

先在第三方开放平台登记应用，得到 client ID 和 client secret，并登记回调地址。下面是完整的 `internal/auth/social/social.go`，它为每个提供方准备好 `oauth2.Config`，以及用换到的 token 读取账号的方法：

```go
package social

import (
    "cmp"
    "context"
    "encoding/json"
    "fmt"
    "net/http"
    "strconv"

    "github.com/coreos/go-oidc/v3/oidc"
    "golang.org/x/oauth2"
    "golang.org/x/oauth2/github"

    "example.com/blog/internal/user"
    "github.com/ryan053857/mygoravel/errorx"
)

var (
    ErrUnknownProvider = errorx.Define(errorx.NotFound, "social.unknown_provider")
    ErrInvalid         = errorx.Define(errorx.Unauthenticated, "social.invalid")
)

// Client 是在第三方开放平台登记的应用。
type Client struct {
    ClientID     string `yaml:"client_id"`
    ClientSecret string `yaml:"client_secret"`
    RedirectURL  string `yaml:"redirect_url"` // 页面登录的回调地址
}

type Config struct {
    GitHub Client `yaml:"github"`
    Google Client `yaml:"google"`
    // ClientRedirectURLs 是单页应用和 App 登记的回调地址，API 只接受这些地址。
    ClientRedirectURLs []string `yaml:"client_redirect_urls"`
}

// Provider 是一个第三方登录提供方。
type Provider struct {
    OAuth2 *oauth2.Config
    // Account 用换到的 token 读取第三方账号。nonce 是发起授权时生成的随机值，
    // OpenID Connect 提供方用它确认 id_token 属于这一次登录。
    Account func(ctx context.Context, tok *oauth2.Token, nonce string) (user.ExternalAccount, error)
}

// Providers 按名称创建全部提供方。Google 在这里读取发现文档，所以应当在启动时调用一次。
func Providers(ctx context.Context, cfg Config) (map[string]*Provider, error) {
    google, err := Google(ctx, cfg.Google)
    if err != nil {
        return nil, err
    }
    return map[string]*Provider{"github": GitHub(cfg.GitHub), "google": google}, nil
}

// GitHub 使用 OAuth 2.0，账号信息来自 GitHub API。
func GitHub(c Client) *Provider {
    conf := &oauth2.Config{
        ClientID:     c.ClientID,
        ClientSecret: c.ClientSecret,
        Endpoint:     github.Endpoint,
        RedirectURL:  c.RedirectURL,
        Scopes:       []string{"read:user", "user:email"},
    }
    account := func(ctx context.Context, tok *oauth2.Token, _ string) (user.ExternalAccount, error) {
        client := conf.Client(ctx, tok)
        var u struct {
            ID    int64  `json:"id"`
            Login string `json:"login"`
            Name  string `json:"name"`
        }
        if err := getJSON(client, "https://api.github.com/user", &u); err != nil {
            return user.ExternalAccount{}, err
        }
        var emails []struct {
            Email    string `json:"email"`
            Primary  bool   `json:"primary"`
            Verified bool   `json:"verified"`
        }
        if err := getJSON(client, "https://api.github.com/user/emails", &emails); err != nil {
            return user.ExternalAccount{}, err
        }
        acc := user.ExternalAccount{Provider: "github", ProviderID: strconv.FormatInt(u.ID, 10), Name: cmp.Or(u.Name, u.Login)}
        for _, e := range emails {
            if e.Primary {
                acc.Email, acc.EmailVerified = e.Email, e.Verified
            }
        }
        return acc, nil
    }
    return &Provider{OAuth2: conf, Account: account}
}

// Google 使用 OpenID Connect，账号信息来自 token 响应中的 id_token。
func Google(ctx context.Context, c Client) (*Provider, error) {
    provider, err := oidc.NewProvider(ctx, "https://accounts.google.com")
    if err != nil {
        return nil, err
    }
    verifier := provider.Verifier(&oidc.Config{ClientID: c.ClientID})
    conf := &oauth2.Config{
        ClientID:     c.ClientID,
        ClientSecret: c.ClientSecret,
        Endpoint:     provider.Endpoint(),
        RedirectURL:  c.RedirectURL,
        Scopes:       []string{oidc.ScopeOpenID, "email", "profile"},
    }
    account := func(ctx context.Context, tok *oauth2.Token, nonce string) (user.ExternalAccount, error) {
        raw, _ := tok.Extra("id_token").(string)
        return VerifyIDToken(ctx, verifier, "google", raw, nonce)
    }
    return &Provider{OAuth2: conf, Account: account}, nil
}

// VerifyIDToken 校验 id_token 的签名、签发者、受众和有效期，再比较 nonce，然后读取账号。
func VerifyIDToken(ctx context.Context, v *oidc.IDTokenVerifier, provider, raw, nonce string) (user.ExternalAccount, error) {
    idToken, err := v.Verify(ctx, raw)
    if err != nil {
        return user.ExternalAccount{}, ErrInvalid.Wrap(err)
    }
    // Verify 不比较 nonce，必须在这里比较。
    if nonce == "" || idToken.Nonce != nonce {
        return user.ExternalAccount{}, ErrInvalid.New()
    }
    var claims struct {
        Email         string `json:"email"`
        EmailVerified bool   `json:"email_verified"`
        Name          string `json:"name"`
    }
    if err := idToken.Claims(&claims); err != nil {
        return user.ExternalAccount{}, ErrInvalid.Wrap(err)
    }
    return user.ExternalAccount{
        Provider:      provider,
        ProviderID:    idToken.Subject,
        Email:         claims.Email,
        EmailVerified: claims.EmailVerified,
        Name:          claims.Name,
    }, nil
}

func getJSON(client *http.Client, url string, v any) error {
    resp, err := client.Get(url)
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
  github:
    client_id: ${GITHUB_CLIENT_ID}
    client_secret: ${GITHUB_CLIENT_SECRET}
    redirect_url: https://blog.example.com/auth/github/callback
  google:
    client_id: ${GOOGLE_CLIENT_ID}
    client_secret: ${GOOGLE_CLIENT_SECRET}
    redirect_url: https://blog.example.com/auth/google/callback
  client_redirect_urls:
    - https://app.example.com/auth/callback
```

几点说明：

- 第三方返回的用户 ID 才是账号的唯一标识：GitHub 是数字 `id`，OpenID Connect 是 `sub`。用户名和邮箱都可能变化。
- GitHub 的邮箱需要单独请求 `/user/emails`，并带上是否已验证。
- `oidc.NewProvider` 会请求发现文档，`Verifier` 会按需下载并缓存公钥，所以提供方在启动时创建一次，之后一直复用。
- 接入其他 OpenID Connect 提供方时，只需要换掉 `Google` 中的签发者地址。

<a id="linking-accounts"></a>
## 账号关联

第三方账号通过一张关联表对应到应用的用户，一个用户可以关联多个第三方账号：

```sql
CREATE TABLE user_identities (
    provider text NOT NULL,
    provider_id text NOT NULL,
    user_id uuid NOT NULL REFERENCES users (id),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (provider, provider_id)
);
```

用户模块的 `user.SignInWith` action 在一个[事务](database.md#transactions)中按下面的顺序找到或创建用户：

1. 按 `(provider, provider_id)` 找到关联，返回关联的用户；
2. 没有关联，但第三方确认邮箱**已验证**，且应用中已有这个邮箱的用户：建立关联，返回该用户；
3. 否则创建新用户和关联。

第 2 步只能使用已验证的邮箱：否则任何人都可以在第三方填上别人的邮箱，借此登录别人的账号。不想自动关联时，可以去掉第 2 步，改为让用户先用密码登录，再在账号设置中绑定第三方账号。两个请求同时为同一个第三方账号创建关联时，主键冲突会让其中一个事务失败，见[约束冲突](database.md#constraint-errors)。

<a id="page-login"></a>
## 在页面中登录

页面登录由服务器完成整个流程：

1. 登录页上的“使用 GitHub 登录”链接指向 `/auth/github`；
2. 服务器生成 state、nonce 和 PKCE verifier 并存入会话，然后跳转到第三方；
3. 用户同意授权后，第三方带着授权码和 state 跳回 `/auth/github/callback`；
4. 服务器比较 state，用授权码和 verifier 换取 token，读取账号并登录。

下面是完整的 `internal/auth/authweb/social.go`：

```go
package authweb

import (
    "crypto/rand"
    "net/http"

    "github.com/alexedwards/scs/v2"
    "github.com/coreos/go-oidc/v3/oidc"
    "github.com/go-chi/chi/v5"
    "github.com/uptrace/bun"
    "golang.org/x/oauth2"

    "example.com/blog/internal/auth/social"
    "example.com/blog/internal/user"
    "github.com/ryan053857/mygoravel/identity"
    "github.com/ryan053857/mygoravel/web"
)

type socialHandlers struct {
    pages     *web.Pages
    sessions  *scs.SessionManager
    providers map[string]*social.Provider
    signIn    *user.SignInWith
}

// RegisterSocial 注册第三方登录的跳转和回调页面。
func RegisterSocial(r chi.Router, pages *web.Pages, sessions *scs.SessionManager, providers map[string]*social.Provider, db *bun.DB) {
    h := &socialHandlers{pages: pages, sessions: sessions, providers: providers, signIn: user.NewSignInWith(db)}
    public := pages.Routes(r, web.Public)
    public.Get("/auth/{provider}", h.redirect)
    public.Get("/auth/{provider}/callback", h.callback)
}

func (h *socialHandlers) redirect(w http.ResponseWriter, r *http.Request) error {
    p, ok := h.providers[chi.URLParam(r, "provider")]
    if !ok {
        return social.ErrUnknownProvider.New()
    }
    ctx := r.Context()
    state, nonce, verifier := rand.Text(), rand.Text(), oauth2.GenerateVerifier()
    h.sessions.Put(ctx, "social.state", state)
    h.sessions.Put(ctx, "social.nonce", nonce)
    h.sessions.Put(ctx, "social.verifier", verifier)
    h.sessions.Put(ctx, "social.next", web.LocalPath(r.URL.Query().Get("next"), "/admin"))
    url := p.OAuth2.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier))
    http.Redirect(w, r, url, http.StatusSeeOther)
    return nil
}

func (h *socialHandlers) callback(w http.ResponseWriter, r *http.Request) error {
    p, ok := h.providers[chi.URLParam(r, "provider")]
    if !ok {
        return social.ErrUnknownProvider.New()
    }
    ctx := r.Context()
    state := h.sessions.PopString(ctx, "social.state")
    nonce := h.sessions.PopString(ctx, "social.nonce")
    verifier := h.sessions.PopString(ctx, "social.verifier")
    next := h.sessions.PopString(ctx, "social.next")
    q := r.URL.Query()
    if state == "" || q.Get("state") != state {
        return social.ErrInvalid.New()
    }
    if q.Get("error") != "" { // 用户在第三方页面取消了授权
        web.Redirect(w, r, "/login")
        return nil
    }
    tok, err := p.OAuth2.Exchange(ctx, q.Get("code"), oauth2.VerifierOption(verifier))
    if err != nil {
        return social.ErrInvalid.Wrap(err)
    }
    acc, err := p.Account(ctx, tok, nonce)
    if err != nil {
        return err
    }
    u, err := h.signIn.Handle(ctx, acc)
    if err != nil {
        return err
    }
    if err := h.pages.Login(ctx, identity.Subject{ID: u.ID, Type: identity.SubjectUser, Permissions: u.Permissions}); err != nil {
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
authweb.RegisterSocial(r, pages, sessions, providers, a.db)
```

这几个随机值各有用途：

| 值 | 作用 |
| --- | --- |
| state | 回调时比较，确认这次回调是本站发起的登录，防止攻击者把自己的授权码塞进用户的浏览器 |
| PKCE verifier | 跳转时只发送它的哈希，换取 token 时才发送原值，截获授权码的人无法使用它 |
| nonce | 写进 id_token，确认这个 id_token 属于这一次登录；GitHub 不使用它 |

它们用 `Pop` 读取，只能使用一次。回调是从第三方跳回本站的顶层 GET 请求，SameSite 为 `lax` 的会话 cookie 会随它发送，`pages.Routes` 的跨站请求防护也只拦截 `POST` 等非安全请求，所以会话中的值都能读到。

<a id="api-login"></a>
## 在单页应用和 App 中登录

单页应用和 App 不使用会话，由客户端自己完成跳转：

1. 客户端生成 state、nonce 和 PKCE verifier，自己保存，然后打开第三方的授权地址，`redirect_uri` 是它登记的回调地址；
2. 第三方带着授权码跳回客户端，客户端比较 state；
3. 客户端把授权码、verifier、nonce 和回调地址交给 API；
4. API 用 client secret 换取 token，读取账号，签发 [API 令牌](api-tokens.md)。

client secret 始终留在服务器上。下面是完整的 `internal/auth/authhttp/social.go`：

```go
package authhttp

import (
    "context"
    "net"
    "net/http"
    "slices"

    "github.com/danielgtaylor/huma/v2"
    "github.com/uptrace/bun"
    "golang.org/x/oauth2"

    "example.com/blog/internal/auth/social"
    "example.com/blog/internal/user"
    "github.com/ryan053857/mygoravel/apitoken"
    "github.com/ryan053857/mygoravel/httpx/rest"
    "github.com/ryan053857/mygoravel/identity"
)

type SocialTokenInput struct {
    Provider  string `path:"provider"`
    UserAgent string `header:"User-Agent"`
    Body      struct {
        Code         string `json:"code" minLength:"1"`
        CodeVerifier string `json:"code_verifier" minLength:"43" maxLength:"128"`
        Nonce        string `json:"nonce,omitempty"`
        RedirectURI  string `json:"redirect_uri" format:"uri"`
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
        Token string `json:"token"`
    }
}

// RegisterSocial 注册用第三方授权码换取 API 令牌的接口。
func RegisterSocial(api *rest.API, providers map[string]*social.Provider, cfg social.Config, db *bun.DB, tokens *apitoken.Store) {
    signIn := user.NewSignInWith(db)
    op := huma.Operation{OperationID: "auth-social-token", Method: http.MethodPost, Path: "/auth/{provider}/tokens"}
    rest.Public()(&op)
    rest.Register(api, op, func(ctx context.Context, in *SocialTokenInput) (*SocialTokenOutput, error) {
        p, ok := providers[in.Provider]
        if !ok {
            return nil, social.ErrUnknownProvider.New()
        }
        if !slices.Contains(cfg.ClientRedirectURLs, in.Body.RedirectURI) {
            return nil, social.ErrInvalid.New()
        }
        conf := *p.OAuth2
        conf.RedirectURL = in.Body.RedirectURI
        tok, err := conf.Exchange(ctx, in.Body.Code, oauth2.VerifierOption(in.Body.CodeVerifier))
        if err != nil {
            return nil, social.ErrInvalid.Wrap(err)
        }
        acc, err := p.Account(ctx, tok, in.Body.Nonce)
        if err != nil {
            return nil, err
        }
        u, err := signIn.Handle(ctx, acc)
        if err != nil {
            return nil, err
        }
        sub := identity.Subject{ID: u.ID, Type: identity.SubjectUser}
        plain, err := tokens.Login(ctx, sub, apitoken.Device{IP: in.ip, UserAgent: in.UserAgent})
        if err != nil {
            return nil, err
        }
        out := &SocialTokenOutput{}
        out.Body.Token = plain
        return out, nil
    })
}
```

换取 token 时的 `redirect_uri` 必须与客户端跳转时使用的一致，所以由客户端传来；但它必须在 `client_redirect_urls` 中，否则攻击者可以用发往其他地址的授权码来换取 token。客户端没有 client secret，PKCE 是它唯一的保护，所以 `code_verifier` 是必填的。

<a id="native-sign-in"></a>
### App 原生登录

App 也可以使用系统或第三方 SDK 提供的原生登录，例如 Google 登录和 Sign in with Apple。SDK 直接返回 id_token，不经过授权码。App 把 id_token 和它生成的 nonce 交给 API，API 用 `VerifyIDToken` 校验：

```go
google, err := oidc.NewProvider(ctx, "https://accounts.google.com")
if err != nil {
    return err
}
appVerifier := google.Verifier(&oidc.Config{ClientID: iosClientID})

// 在接口中：
acc, err := social.VerifyIDToken(ctx, appVerifier, "google", in.Body.IDToken, in.Body.Nonce)
```

`ClientID` 必须是 App 在第三方登记的 client ID，它通常与网页使用的不同。nonce 传 App 放进登录请求的那个值；有的 SDK 要求在请求中放 nonce 的哈希，这时传哈希值。之后与上面相同：`SignInWith`，然后 `tokens.Login`。

<a id="sign-in-with-apple"></a>
## Sign in with Apple

Apple 也是 OpenID Connect 提供方，与 Google 的写法基本相同，但有四点差别：

- **client secret 是你自己签名的 JWT**：用 Apple 下发的私钥（`.p8`）按 ES256 签名，有效期最长 6 个月，需要定期重新生成，见 [Apple 的文档](https://developer.apple.com/documentation/accountorganizationaldatasharing/creating-a-client-secret)。
- **不使用 PKCE**：Apple 的授权和 token 接口没有 PKCE 参数，依靠 state、nonce 和 client secret。
- **用户姓名只在第一次授权时返回**：它不在 id_token 中，而是回调表单中的 `user` 字段，第一次登录时就要保存下来。邮箱每次都在 id_token 中。
- **申请了姓名或邮箱时，回调是跨站 POST**：申请任何 scope 时 Apple 都要求 `response_mode=form_post`，浏览器从 `appleid.apple.com` 把授权码提交到回调地址。

最后一点影响[页面登录](#page-login)的写法：

1. `pages.Routes` 会拒绝跨站的 `POST`，需要把 Apple 加入 `web.Options.TrustedOrigins`：
   ```go
   TrustedOrigins: []string{"https://appleid.apple.com"},
   ```
2. 跨站 `POST` **不会**携带 SameSite 为 `lax` 的会话 cookie，回调中读不到会话里的 state。因此跳转时把 state 和 nonce 存进一个单独的短期 cookie，它必须是 `SameSite=None`：
   ```go
   http.SetCookie(w, &http.Cookie{
       Name: "apple_auth", Value: state + "." + nonce,
       Path: "/auth/apple/callback", MaxAge: 600,
       HttpOnly: true, Secure: true, SameSite: http.SameSiteNoneMode,
   })
   ```
   回调用 `public.Post("/auth/apple/callback", ...)` 注册，从这个 cookie 中取出 state 和 nonce 并删除 cookie，再用 `r.PostFormValue` 读取 `code`、`state` 和 `user`。其余步骤与 Google 相同。

回调中调用 `pages.Login` 不受影响：它为这个请求创建新的会话，浏览器接受顶层跳转响应中的会话 cookie。
