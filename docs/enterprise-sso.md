# 企业单点登录

- [简介](#introduction)
- [连接与域名](#connections)
- [验证域名](#verifying-domains)
- [运行时创建提供方](#providers)
- [登录](#signing-in)
- [同步角色](#syncing-roles)
- [要求用 SSO 登录](#requiring-sso)
- [不支持的](#not-supported)

<a id="introduction"></a>
## 简介

B2B 产品的企业客户通常要求员工用公司自己的身份系统登录，例如 Okta、Microsoft Entra ID、Google Workspace。一个完整的例子：

- 租户在后台填写自己的 OpenID Connect 应用，并验证邮箱域名 `acme.com`；
- 员工在登录页输入 `alice@acme.com`，被带到 Acme 的 IdP 登录，第一次登录自动开通账号；
- IdP 中的分组同步为租户中的角色；
- 租户可以要求成员只能用 SSO 登录，并要求 IdP 做过多因素认证。

框架提供其中的通用部分：[`login/oauth`](social-login.md) 完成 OpenID Connect 流程，[`login.Realm`](login.md) 管理登录标识和[按租户的登录策略](login.md#policies)，[`authz`](authorization.md) 管理角色。连接配置、域名验证和登录页由应用编写，本章给出一套写法。

本章的成员账号按租户登录（`PerTenant: true`），租户取自子域名，例如 `acme.app.example.com`；同一个人在两个租户中是两个账号。单页应用的登录接口写法与[第三方登录](social-login.md#spa-login)相同。

<a id="connections"></a>
## 连接与域名

每个租户的 SSO 配置是应用自己的两张表：

```sql
CREATE TABLE sso_connections (
    tenant text PRIMARY KEY,                 -- 每个租户一个连接
    issuer text NOT NULL,                    -- 例如 https://acme.okta.com
    client_id text NOT NULL,
    client_secret text NOT NULL,             -- crypt.Encrypt 加密后保存
    require_sso boolean NOT NULL DEFAULT false,
    updated_at timestamptz NOT NULL
);

CREATE TABLE sso_domains (
    domain text PRIMARY KEY,                 -- 例如 acme.com；一个域名只能属于一个租户
    tenant text NOT NULL,
    token text NOT NULL,                     -- 写进 DNS TXT 记录的随机值
    verified_at timestamptz
);
```

第三方账号在 `login` 中的提供方名称（标识的 `Kind`）统一用 `sso`。标识按租户隔离，所以每个租户各有一个 `sso` 不会冲突。

<a id="verifying-domains"></a>
## 验证域名

域名决定两件事：输入哪个邮箱时把用户带到这个租户的 IdP，以及能否把 IdP 断言的邮箱关联到已有账号。不验证的话，一个租户可以声称拥有 `gmail.com`，借此接管别人的账号。验证方式是让租户在 DNS 中加一条 TXT 记录：

```go
func (a *VerifyDomain) Handle(ctx context.Context, sub identity.Subject, domain string) error {
    d, err := a.find(ctx, sub.Tenant, domain)
    if err != nil {
        return err
    }
    records, err := net.DefaultResolver.LookupTXT(ctx, "_app-verify."+d.Domain)
    if err != nil || !slices.Contains(records, "app-verify="+d.Token) {
        return ErrDomainNotVerified.New()
    }
    _, err = a.db.NewUpdate().Model(d).Set("verified_at = ?", time.Now().UTC()).WherePK().Exec(ctx)
    return err
}
```

`gmail.com`、`outlook.com` 这类公共邮箱域名在添加时就拒绝。

<a id="providers"></a>
## 运行时创建提供方

连接由租户在运行时修改，提供方不能像 Google 那样在启动时创建。按租户取出连接，用 `oauth.Discover` 创建，并按 `(租户, updated_at)` 缓存在进程中：配置改了，键也随之改变，旧的提供方不再被使用。issuer 是租户填写的，发现文档中的地址也不可信，所以全部请求走 [`outbound`](outbound.md) 的客户端：

```go
type Providers struct {
    db     *bun.DB
    crypt  *crypt.Crypt
    client *http.Client // outbound.New(outbound.Options{Timeout: 10 * time.Second})
    group  singleflight.Group
    cache  sync.Map // "tenant@updated_at" → *oauth.Provider
}

func (p *Providers) For(ctx context.Context, tenant string) (*oauth.Provider, error) {
    var c Connection
    if err := p.db.NewSelect().Model(&c).Where("tenant = ?", tenant).Scan(ctx); err != nil {
        return nil, err // 没有连接：sql.ErrNoRows
    }
    key := tenant + "@" + c.UpdatedAt.String()
    if v, ok := p.cache.Load(key); ok {
        return v.(*oauth.Provider), nil
    }
    v, err, _ := p.group.Do(key, func() (any, error) {
        secret, err := p.crypt.Decrypt(c.ClientSecret)
        if err != nil {
            return nil, err
        }
        provider, err := oauth.Discover(ctx, oauth.Config{
            Provider: "sso", Issuer: c.Issuer, ClientID: c.ClientID, ClientSecret: secret,
            RedirectURL: "https://" + tenant + ".app.example.com/auth/sso/callback",
            Scopes:      []string{"openid", "email", "profile", "groups"},
            Client:      p.client,
        })
        if err == nil {
            p.cache.Store(key, provider)
        }
        return provider, err
    })
    if err != nil {
        return nil, err
    }
    return v.(*oauth.Provider), nil
}
```

缓存只保存由配置推导出来的对象，配置本身仍然每次从数据库读取，多个实例之间不会不一致。

<a id="signing-in"></a>
## 登录

登录页先让用户输入邮箱：

1. `POST /auth/sso/start`，请求体是邮箱。按域名在 `sso_domains` 中找到已验证的租户，取出提供方，`Start` 返回跳转地址和 `flow`。域名没有配置 SSO 时返回空，登录页显示密码框；
2. 用户在 IdP 登录后回到单页应用的回调页，回调页把查询参数和保存的 `flow` 交给 `POST /auth/sso/finish`；
3. `Finish` 得到 `login.External`，交给租户的 `Realm`。

```go
func (h *handlers) finish(ctx context.Context, in *FinishInput) (*SessionOutput, error) {
    tenant := identity.TenantFromContext(ctx)
    provider, err := h.providers.For(ctx, tenant)
    if err != nil {
        return nil, err
    }
    flow, err := oauth.ParseFlow(in.Body.Flow)
    if err != nil {
        return nil, err
    }
    params, err := url.ParseQuery(in.Body.Callback)
    if err != nil {
        return nil, oauth.ErrInvalid.Wrap(err)
    }
    ext, err := provider.Finish(ctx, flow, params)
    if err != nil {
        return nil, err
    }
    // 这是租户自己的 IdP：它管理的域名下的邮箱可信，它做过的多因素认证也可信。
    ext.TrustEmail = h.domains.Verified(ctx, tenant, ext.Email)
    ext.TwoFactor = idpMFA(ext.Claims)

    out, err := h.members.External(ctx, ext)
    if err != nil {
        return nil, err
    }
    if out.Proof != "" { // 第一次登录：直接开通账号
        if out, err = h.signUp.Handle(ctx, out.Proof, ext.Name); err != nil {
            return nil, err
        }
    }
    if out.Subject.ID != "" {
        if err := h.syncRoles(ctx, out.Subject.Ref(), tenant, ext.Claims); err != nil {
            return nil, err
        }
    }
    return h.session(ctx, out, in.UserAgent) // 签发 token，或者返回 pending / enroll
}

// idpMFA 按 ID token 的 amr 判断 IdP 是否做过多因素认证。各家写法不同，按租户实际使用的 IdP 调整。
func idpMFA(claims map[string]any) bool {
    amr, _ := claims["amr"].([]any)
    return slices.ContainsFunc(amr, func(v any) bool { return v == "mfa" || v == "otp" || v == "hwk" })
}
```

- **`TrustEmail`**：邮箱在这个租户中已经有账号时（例如公司改用 SSO 之前员工用密码注册过），`External` 直接把 SSO 账号绑定给它并登录。只有在租户验证过这个邮箱的域名时才设置，`Verified` 检查 `ext.Email` 的域名在 `sso_domains` 中属于这个租户并且 `verified_at` 不为空。
- **自动开通**：`out.Proof` 表示还没有账号。`signUp` 是应用的注册 action，在事务中创建成员行并调用 `Register`，与[验证码注册](login.md#codes)相同，返回的也是 `login.Outcome`。
- **`TwoFactor`**：IdP 做过多因素认证时，这次登录记为 `[sso, idp_mfa]`，计入二次验证；租户要求二次验证时不再要求用户在本应用中另设验证器。

<a id="syncing-roles"></a>
## 同步角色

IdP 的分组在 ID token 的 `groups` 中（Okta、Entra ID 需要在 IdP 中配置声明）。每次登录按租户配置的映射同步一次，用户在 IdP 中被移出分组后，下次登录就失去对应的角色：

```go
func (h *handlers) syncRoles(ctx context.Context, ref identity.Ref, tenant string, claims map[string]any) error {
    groups, _ := claims["groups"].([]any)
    mapping, err := h.groupRoles(ctx, tenant) // 租户配置的 分组 → 角色 ID
    if err != nil {
        return err
    }
    for group, role := range mapping {
        if slices.Contains(groups, any(group)) {
            err = h.roles.Assign(ctx, ref, tenant, role)
        } else {
            err = h.roles.Unassign(ctx, ref, tenant, role)
        }
        if err != nil {
            return err
        }
    }
    return nil
}
```

只同步映射中出现的角色，租户管理员手动分配的其他角色不受影响。成员在 IdP 中被停用后，在下次登录之前仍然持有之前签发的 token；需要立即生效时，由 IdP 的事件或租户管理员在应用中停用成员，并[撤销令牌](api-tokens.md#revoking-tokens)。

<a id="requiring-sso"></a>
## 要求用 SSO 登录

租户开启 `require_sso` 后，成员只能用 SSO 登录，密码和验证码都不能用。在 `PolicyFor` 中按连接的设置返回 `Methods`：

```go
PolicyFor: func(ctx context.Context) (login.Policy, error) {
    c, err := connections.Get(ctx, identity.TenantFromContext(ctx))
    if err != nil {
        return login.Policy{}, err
    }
    p := login.Policy{RequireTwoFactor: c.RequireTwoFactor}
    if c.RequireSSO {
        p.Methods = []string{"sso"}
    }
    return p, nil
},
```

之后 `Password`、`SendCode` 在这个租户中返回 `403 login.method_not_allowed`，登录页据此只显示 SSO 按钮。账号在多个租户中通用、登录时不知道租户的产品，改由租户的 Resolver 检查 `sub.Auth.Methods`，返回 `identity.ErrMethodRequired`，见[按租户的登录策略](login.md#policies)。

<a id="not-supported"></a>
## 不支持的

- **SAML**：Okta、Entra ID、Google Workspace、Ping 都支持 OpenID Connect，先用它。确实需要 SAML 的客户再单独接入。
- **SCIM**：IdP 主动推送账号的创建和停用。本章在登录时同步，加上应用中的停用和撤销，能覆盖大部分情况。
- **IdP 发起的登录**：在 IdP 的应用列表中点图标时，IdP 跳到应用的登录地址，应用照常发起上面的流程即可。
