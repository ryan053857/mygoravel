# 登录与注册

- [简介](#introduction)
- [装配](#assembling)
- [登录标识](#identifiers)
- [密码登录](#password)
- [验证码登录与注册](#codes)
- [找回密码](#reset)
- [管理登录方式](#managing)
- [二次验证](#two-factor)
- [Passkey](#passkeys)
- [确认身份](#confirming)
- [后台创建账号](#admin-accounts)
- [注销账号](#forget)
- [限流与防探测](#limits)
    - [人机验证](#captcha)
- [清理](#cleanup)

<a id="introduction"></a>
## 简介

`login` 包回答“这个人是谁”：用登录标识和密码登录、用验证码登录或注册、找回密码、绑定和更换邮箱或手机号。得到 `identity.Subject` 之后，由入口签发凭据：页面调用 [`pages.Login`](views.md#login)，API 调用 [`tokens.Login`](api-tokens.md#login-and-logout) 或 [`JWT.Login`](api-tokens.md#jwt)。

框架不拥有账号表。博客的 `users` 表只保存业务字段，不需要 `email`、`password_hash` 这类列；登录标识、密码哈希和验证码保存在框架的 `auth_*` 表中，按 [`identity.Ref`](authentication.md#key-concepts) 引用账号。一个账号可以有多个邮箱、手机号，也可以没有密码只用验证码登录。

每套[账号体系](authentication.md#account-systems)各用一个 `login.Realm`。两套账号从建表到接口的完整代码见[完整示例](account-examples.md)。

<a id="assembling"></a>
## 装配

先发布 `login` 的迁移；默认的限流器存在数据库中，同时发布 `ratelimit` 的迁移，见[发布框架迁移](migrations.md#publishing-framework-migrations)：

```go
migrationCmd.AddCommand(migratecmd.Publish(postgres.Driver,
    migrate.Source{Name: "login", FS: login.Migrations()},
    migrate.Source{Name: "ratelimit", FS: ratelimit.Migrations()},
))
```

在[入口](dependencies.md#assembling-the-application)中为博客用户创建 `Realm`，[`Hasher`](hashing.md) 哈希密码，[`Crypt`](encryption.md) 派生验证码的密钥：

```go
users := login.New(a.db, login.Options{
    Type:     user.Type,
    Register: true,
    Hasher:   hashing.New(a.cfg.Hashing),
    Crypt:    a.crypt,
    Send:     sendCode(mailer, emails),
    Resolve:  user.Resolve,
})
```

| 选项 | 含义 |
| --- | --- |
| `Type` | 这套账号体系，`Realm` 只管理这个类型的主体 |
| `Register` | 是否开放自助注册：为 `true` 时验证码也发给未注册的邮箱或手机号，验证后可以注册 |
| `Send` | 发送验证码，邮件和短信的内容、通道由应用决定 |
| `Resolve` | 登录成功、签发凭据之前调用一次，通常就是入口的 [Resolver](authentication.md#resolving-subjects)：账号已停用时返回错误，登录失败 |
| `PerTenant` | 登录标识在租户内唯一，见[登录标识](#identifiers) |

验证码默认 6 位数字、15 分钟有效、最多试 3 次，都可以在 `Options` 中修改。`Send` 收到的 `login.Code` 带有用途、接收的标识和验证码，下面用[邮件模板](mail.md#writing-templates)发送：

```go
func sendCode(mailer mail.Mailer, emails *mail.Templates) func(context.Context, login.Code) error {
    return func(ctx context.Context, c login.Code) error {
        msg, err := emails.Render(ctx, "code_"+string(c.Purpose), c)
        if err != nil {
            return err
        }
        msg.To = []mail.Address{{Email: c.To.Value}}
        return mailer.Send(ctx, msg)
    }
}
```

`c.Purpose` 是 `sign_in`、`reset`、`verify` 或 `confirm`（[确认身份](#confirming)），对应四个模板。`c.Registered` 表示邮箱已经注册，模板可以据此写“登录”还是“注册”。用短信时按 `c.To.Kind` 区分 `email` 和 `phone`。

<a id="identifiers"></a>
## 登录标识

登录标识是“用什么找到这个账号”：

| 构造 | 规范化 |
| --- | --- |
| `login.Email(s)` | 去掉首尾空白，转为小写 |
| `login.Phone(s)` | 必须是 E.164 格式，例如 `+8613800138000`，由应用把用户输入的号码转换成这个格式 |
| `login.Username(s)` | 去掉首尾空白，转为小写，不能包含空白 |

格式不正确时返回 `login.ErrInvalidIdentifier`。[第三方登录](social-login.md)的账号也是一种标识，类型是提供方的名称。

同一个标识在一套账号体系中只能属于一个账号。`PerTenant` 为 `true` 时只在租户内唯一：同一个邮箱可以在两个店铺各注册一个客户账号。这时查找和写入都使用 `identity.TenantFromContext(ctx)`，即入口的 [`Options.Tenant`](tenancy.md#identifying-tenants) 取出的租户；登录得到的主体带有 `Tenant`，签发的凭据只能在这个租户使用。验证码和完成凭证也只能在发送时的租户中使用。

<a id="password"></a>
## 密码登录

`Password` 用标识和密码登录，成功后把 `out.Subject` 交给入口签发凭据：

```go
out, err := users.Password(ctx, login.Email(in.Body.Email), in.Body.Password)
if err != nil {
    return nil, err
}
plain, err := tokens.Login(ctx, out.Subject, device)
```

完整的接口见 [API 令牌的登录与登出](api-tokens.md#login-and-logout)，页面中的写法见[页面登录](views.md#login)。邮箱不存在、没有设置密码、密码错误都返回 `login.ErrInvalidCredentials`，响应 `401`。连续失败太多次时返回 `ratelimit.ErrExceeded`，响应 `429`，见[限流与防探测](#limits)。

允许用哪些标识配合密码登录由 `Options.PasswordKinds` 决定，默认是邮箱、手机号和用户名。登录成功时，如果密码哈希是用旧的算法或参数生成的，会按当前配置[重新哈希](hashing.md#rehashing)。

<a id="codes"></a>
## 验证码登录与注册

验证码分两步：`SendCode` 发出验证码，返回请求 ID；客户端把请求 ID 和用户输入的验证码交给 `VerifyCode`。

```go
requestID, err := users.SendCode(ctx, login.SignIn, login.Email(in.Body.Email))
```

```go
out, err := users.VerifyCode(ctx, in.Body.RequestID, in.Body.Code)
if err != nil {
    return nil, err // 验证码不正确或已失效：login.ErrInvalidCode
}
if out.Proof != "" {
    // 邮箱还没有注册：把完成凭证交给客户端，进入注册页
    resp.Body.Proof = out.Proof
    return resp, nil
}
plain, err := tokens.Login(ctx, out.Subject, device)
```

邮箱已注册时，`VerifyCode` 直接登录，返回 `out.Subject`。还没有注册时返回 `out.Proof`：一个一次性的完成凭证，15 分钟内有效，证明客户端确实收到了验证码。

框架不替应用创建账号。博客的注册 action 在自己的事务中插入 `users` 行，再调用 `Register`，把已验证的邮箱挂到这个账号上：

```go
func (a *SignUp) Handle(ctx context.Context, in SignUpInput) (identity.Subject, error) {
    var sub identity.Subject
    err := database.RunInTx(ctx, a.db, nil, func(ctx context.Context, tx database.Tx) error {
        u := &User{ID: uuid.Must(uuid.NewV7()).String(), Name: in.Name}
        if _, err := tx.NewInsert().Model(u).Exec(ctx); err != nil {
            return err
        }
        var err error
        sub, err = a.users.Register(ctx, tx, in.Proof, u.Ref(), login.WithPassword(in.Password))
        return err
    })
    return sub, err
}
```

[第三方登录](social-login.md#signing-in)得到的注册凭证也交给同一个注册 action。注册需要什么资料、要不要同意协议都由应用决定。任何一步失败，账号行和凭证的使用一起回滚，客户端可以修正后用同一个凭证重试。两个人同时用同一个邮箱注册时，后完成的得到 `login.ErrTaken`。`WithPassword` 可以省略，只用验证码登录的账号没有密码。

`Register` 为 `false` 时不开放自助注册：未注册的邮箱收不到验证码。为了不暴露哪些邮箱注册过，`SendCode` 照样返回请求 ID，只是用它验证必然失败。

验证码也可以放进链接：模板中的链接同时带上 `RequestID` 和 `Code`，链接打开的页面把两者交给 `VerifyCode`。

<a id="reset"></a>
## 找回密码

找回密码用 `login.Reset`：

```go
requestID, err := users.SendCode(ctx, login.Reset, login.Email(in.Body.Email))
```

验证码只发给已注册的邮箱，未注册的同样返回一个请求 ID。`VerifyCode` 通过后得到 `out.Proof`，客户端连同新密码交给 `ResetPassword`：

```go
who, err := users.ResetPassword(ctx, in.Body.Proof, in.Body.Password)
if err != nil {
    return nil, err
}
if _, err := tokens.RevokeSubject(ctx, who); err != nil {
    return nil, err
}
```

新密码按 `Options.PasswordPolicy` 检查，默认要求 8 到 128 个字符、首尾没有空白，不符合时返回 `login.ErrWeakPassword`。接收验证码的邮箱同时标为已验证，它的登录失败计数清零。找回密码后通常撤销这个账号的全部会话和 token，见[撤销令牌](api-tokens.md#revoking-tokens)。`ResetPassword` 不登录，用户接着用新密码登录，启用了[二次验证](#two-factor)的账号仍然要通过二次验证，所以只拿到邮箱不能绕过它。没有设置过密码的账号同样可以用这个流程设置密码。

<a id="managing"></a>
## 管理登录方式

账号设置页面中，`Identifiers` 列出账号的全部标识和是否已验证，`HasPassword` 决定显示“设置密码”还是“修改密码”。

添加或更换邮箱、手机号时，先用 `login.Verify` 向新的标识发验证码，验证后把完成凭证交给 `Link`。更换时在同一个事务中用 `Detach` 移除旧的：

```go
err := database.RunInTx(ctx, a.db, nil, func(ctx context.Context, tx database.Tx) error {
    if err := a.users.Link(ctx, tx, sub.Ref(), in.Proof); err != nil {
        return err // 已被其他账号使用：login.ErrTaken
    }
    return a.users.Detach(ctx, tx, sub.Ref(), login.Email(in.OldEmail))
})
```

`Detach` 不允许移除最后一个标识，否则账号将无法登录，返回 `login.ErrLastIdentifier`。

| 方法 | 用途 |
| --- | --- |
| `SetPassword(ctx, db, ref, password)` | 设置或修改密码，按密码策略检查 |
| `RemovePassword(ctx, db, ref)` | 删除密码，之后只能用验证码等方式登录 |
| `Lookup(ctx, id)` | 管理后台按邮箱或手机号查找账号 |

修改密码后，用 `RevokeOthers` 让其他设备下线，见[撤销令牌](api-tokens.md#revoking-tokens)。

<a id="two-factor"></a>
## 二次验证

账号可以启用 TOTP 二次验证：用户在 Google Authenticator、1Password 等验证器 App 中添加账号，登录时除了密码还要输入验证器上的 6 位数字。`Options.Issuer` 是验证器中显示的应用名称。

启用分两步。`SetupTOTP` 生成密钥，`setup.URI` 渲染成二维码让用户扫描，`setup.Secret` 供无法扫码时手动输入：

```go
setup, err := users.SetupTOTP(ctx, a.db, sub.Ref(), email)
```

用户输入验证器上的数字后，`ConfirmTOTP` 校验并启用，返回 8 个恢复码：

```go
codes, err := users.ConfirmTOTP(ctx, a.db, sub.Ref(), in.Code)
```

恢复码在手机丢失时代替验证器，每个只能用一次。它们只在这里和 `RegenerateRecoveryCodes` 返回明文，页面要提示用户保存下来；`RecoveryCodesLeft` 返回剩余个数，用完之前提醒用户重新生成。

启用后，`Password` 和验证码登录不再直接返回 `out.Subject`，而是返回 `out.Pending`：第一步已经通过，等待二次验证。客户端带着它和验证器上的数字（或一个恢复码）调用登录的第二步：

```go
out, err := users.Password(ctx, login.Email(in.Body.Email), in.Body.Password)
if err != nil {
    return nil, err
}
if out.Pending != "" {
    resp.Body.Pending = out.Pending // 客户端显示输入验证码的界面
    return resp, nil
}
// 未启用二次验证：直接签发 token
```

```go
out, err := users.TwoFactor(ctx, in.Body.Pending, in.Body.Code)
if err != nil {
    return nil, err // 验证码不正确：login.ErrInvalidCode
}
plain, err := tokens.Login(ctx, out.Subject, device)
```

`Pending` 15 分钟内有效，最多输错 5 次，之后要重新登录。同一个验证码不能用两次；验证器与服务器的时钟允许相差 30 秒。`DisableTwoFactor` 关闭二次验证，之前先[确认身份](#confirming)。

运营后台通常要求员工都启用二次验证。这由应用决定：员工登录后，`TwoFactorEnabled` 为 `false` 时只允许访问启用二次验证的页面。

<a id="passkeys"></a>
## Passkey

Passkey（通行密钥，WebAuthn）用手机或电脑的指纹、面容、设备密码登录：不需要密码，私钥不离开设备，也不会被钓鱼网站骗走。`login/passkey` 把每个 passkey 作为一个登录标识（`Kind` 为 `passkey`）加到 `Realm` 上，所以它与邮箱、第三方账号一样可以绑定、解除、按租户隔离；用 passkey 登录不再要求[二次验证](#two-factor)，它本身就同时验证了设备和使用者。

发布 `passkey` 的迁移（`passkey.Migrations()`），在入口中为博客用户创建：

```go
passkeys, err := passkey.New(a.db, users, a.crypt, passkey.Config{
    RPID:    "blog.example.com",
    RPName:  "Blog",
    Origins: []string{"https://blog.example.com"},
})
```

`RPID` 是 passkey 绑定的域名，之后不能更改，否则已经创建的 passkey 全部失效。`Origins` 是允许发起登录的网页地址；Android App 加上 `android:apk-key-hash:<签名证书的哈希>`，iOS App 通过关联域名使用网站的地址。

每个操作分两步：`Begin*` 返回交给浏览器或 App 的 `Options` 和一个 `Token`，客户端完成后把结果连同 `Token` 交给 `Finish*`。`Token` 是用应用密钥加密的这次操作的状态，5 分钟内有效，服务器不需要保存它。

**登录**不需要先输入账号，浏览器列出这个网站的 passkey 让用户选择：

```go
c, err := passkeys.BeginLogin(ctx) // 返回 c 给客户端
```

```go
out, err := passkeys.FinishLogin(ctx, in.Body.Token, in.Body.Credential) // Credential 是 json.RawMessage
if err != nil {
    return nil, err // passkey.ErrInvalid
}
plain, err := tokens.Login(ctx, out.Subject, device)
```

浏览器中用 `navigator.credentials.get` 完成中间一步：

```js
const { options, token } = await api.post("/auth/passkey/begin")
const credential = await navigator.credentials.get({
  publicKey: PublicKeyCredential.parseRequestOptionsFromJSON(options.publicKey),
})
await api.post("/auth/passkey/finish", { token, credential: credential.toJSON() })
```

**创建**也是两步，客户端用 `navigator.credentials.create` 和 `parseCreationOptionsFromJSON`。`FinishRegistration` 返回完成凭证：已登录的用户添加 passkey 时交给 `Link`，用 passkey 注册新账号时交给注册 action 中的 `Register`，与[验证码注册](#codes)相同：

```go
c, err := passkeys.BeginRegistration(ctx, sub.Ref(), email) // 注册新账号时 ref 传零值
proof, err := passkeys.FinishRegistration(ctx, in.Body.Token, in.Body.Credential, in.Body.Name)
err = users.Link(ctx, a.db, sub.Ref(), proof)
```

`email` 是系统在保存 passkey 时显示的账号名，`in.Body.Name` 是给这个 passkey 起的名称，例如“iPhone”。同一个账号已有的 passkey 不会在同一个设备上重复创建。

账号设置中，`List` 列出账号的 passkey（名称、是否在设备之间同步、最近使用时间），`Rename` 改名，`Remove` 移除；不能移除最后一种登录方式。签名计数倒退（疑似 passkey 被复制）时登录失败。创建后没有绑定到账号的 passkey，以及账号[注销](#forget)后留下的公钥，由 `passkeys.Cleanup` 定时删除。

<a id="confirming"></a>
## 确认身份

修改邮箱、关闭二次验证、删除账号之前，再次确认操作的就是账号本人。按账号有的登录方式选一种：

| 方式 | 调用 |
| --- | --- |
| 密码 | `Confirm(ctx, ref, password)` |
| 验证码 | `SendConfirmCode(ctx, ref, id)` 发给账号自己的邮箱或手机号，`ConfirmCode(ctx, ref, requestID, code)` 校验 |
| 二次验证 | `CheckTwoFactor(ctx, ref, code)` 校验验证器上的数字或恢复码 |
| passkey、第三方账号 | 再登录一次，`out.Subject.Ref()` 等于 `ref` 即可 |

失败次数都有限制。`HasPassword` 决定显示密码框还是“发送验证码”。确认用的验证码只发给 `ref` 自己的标识，其他标识返回 `ErrNotFound`；它不能用来登录，`VerifyCode` 不接受它，所以不会绕过二次验证。

没有密码的账号用验证码确认：

```go
requestID, err := users.SendConfirmCode(ctx, sub.Ref(), login.Phone(phone))
// 用户收到验证码后
err = users.ConfirmCode(ctx, sub.Ref(), in.Body.RequestID, in.Body.Code)
```

JSON API 让客户端在这类请求中带上密码或验证码，处理器先确认，完整的写法见[读者注销账号](account-examples.md#reader-api)。页面则把这类路由声明为 `Confirmed`，在一段时间内确认过身份的会话直接放行：

```go
pages := web.New(web.Options{
    Login:   "/login",
    Confirm: "/confirm",
    // ...
})
pages.Routes(r, web.Require().Confirmed(15*time.Minute)).Post("/account/delete", h.Delete)
```

登录本身算一次确认。超过 15 分钟时，请求被带到 `Options.Confirm`，`next` 参数带上原地址。确认页校验密码（或验证码）后调用 `pages.Confirm`，再跳回原地址：

```go
func (h *Handlers) Confirm(w http.ResponseWriter, r *http.Request) error {
    sub, _ := identity.FromContext(r.Context())
    if err := h.users.Confirm(r.Context(), sub.Ref(), r.PostFormValue("password")); err != nil {
        return err
    }
    h.pages.Confirm(r.Context())
    web.Redirect(w, r, web.LocalPath(r.URL.Query().Get("next"), "/account"))
    return nil
}
```

确认页本身用 `web.Require()` 保护。

<a id="admin-accounts"></a>
## 后台创建账号

运营后台的员工账号通常由管理员创建，不开放注册。管理员创建 `staff` 行后，用 `Attach` 直接写入邮箱，再发一个设置密码的验证码，也就是邀请：

```go
if err := a.staff.Attach(ctx, tx, member.Ref(), login.Email(in.Email), false); err != nil {
    return err
}
// 事务提交后发送
_, err := a.staff.SendCode(ctx, login.Reset, login.Email(in.Email))
```

员工通过邀请邮件中的链接设置密码，邮箱随之标为已验证。`Attach` 不经过验证码，未验证的标识同样占用唯一性，所以只用于管理员建号这类可信的场景；自助注册一律通过验证码。

<a id="forget"></a>
## 注销账号

`Forget` 删除账号的全部登录标识、密码、二次验证和验证码。在注销账号的事务中与删除账号行一起调用，再撤销会话和 token：

```go
err := database.RunInTx(ctx, a.db, nil, func(ctx context.Context, tx database.Tx) error {
    if _, err := tx.NewDelete().Model((*User)(nil)).Where("id = ?", sub.ID).Exec(ctx); err != nil {
        return err
    }
    return a.users.Forget(ctx, tx, sub.Ref())
})
```

注销后，这个邮箱可以重新注册。

<a id="limits"></a>
## 限流与防探测

| 限制 | 默认 | 超出时 |
| --- | --- | --- |
| 同一个标识的密码登录，`Options.Attempts` | 15 分钟内连续失败 5 次，登录成功或找回密码后清零 | `ratelimit.ErrExceeded`，`429` |
| 向同一个标识发送验证码，`Options.Sends` | 15 分钟 5 次 | `ratelimit.ErrExceeded`，`429` |
| 同一个验证码的尝试次数，`Options.CodeAttempts` | 3 次 | 验证码作废，`login.ErrInvalidCode` |

默认限流器存在数据库中，多个实例共享计数。已经在用 Redis 时可以换成 [`redislimit`](rate-limiting.md#redis-limiter)。按 IP 的限流不在 `login` 中，需要时在登录接口上安装 [`httpx.RateLimit`](rate-limiting.md#http-middleware)。

为了不暴露哪些邮箱注册过：密码登录对“邮箱不存在”和“密码错误”返回同一个错误，耗时相同；找回密码和不开放注册时的验证码不发给未注册的邮箱，但同样返回请求 ID。验证码在库中只保存由应用密钥派生的 HMAC，数据库泄露也不能在有效期内穷举出验证码；完成凭证只保存 SHA-256。

<a id="captcha"></a>
### 人机验证

`Sends` 按标识计数。机器人每次换一个邮箱或手机号，这个限制就不起作用；换用代理池，按 IP 的限流也挡不住。验证码用短信发送时，被批量刷一晚就是一笔短信费。开放自助注册时，在发送验证码的接口前加上人机验证。`captcha` 包在服务端校验 [Cloudflare Turnstile](https://developers.cloudflare.com/turnstile/) 的结果：

```yaml
captcha:
  secret: ${TURNSTILE_SECRET}
  hostnames: [blog.example.com]
```

```go
bots, err := captcha.New(cfg.Captcha, nil)
```

前端用 site key 渲染 Turnstile 组件，`data-action` 设为 `signup`，把组件得到的 token 和邮箱一起提交。接口先校验 token，再发送验证码：

```go
type RequestCodeInput struct {
    Body struct {
        Email   string `json:"email" format:"email"`
        Captcha string `json:"captcha" doc:"Turnstile 组件得到的 token"`
    }
    ip string
}

// Resolve 记录客户端地址，见 Huma 的 Resolver。
func (in *RequestCodeInput) Resolve(ctx huma.Context) []error {
    in.ip, _, _ = net.SplitHostPort(ctx.RemoteAddr())
    return nil
}

func requestCode(users *login.Realm, bots *captcha.Verifier) func(context.Context, *RequestCodeInput) (*RequestCodeOutput, error) {
    return func(ctx context.Context, in *RequestCodeInput) (*RequestCodeOutput, error) {
        if err := bots.Verify(ctx, in.Body.Captcha, "signup", in.ip); err != nil {
            return nil, err
        }
        requestID, err := users.SendCode(ctx, login.SignIn, login.Email(in.Body.Email))
        // ……
    }
}
```

没有通过时返回 `captcha.ErrFailed`（`422`），前端重置组件，让用户重新验证。token 只能校验一次，重复提交同一个 token 也会得到这个错误。Turnstile 连不上时返回 `captcha.ErrUnavailable`（`503`），请求不会被放行。

`hostnames` 和 action 保证 token 来自自己的站点和这张表单：在注册页得到的 token，不能拿来提交联系表单。评论、联系表单等其他公开表单的做法相同，只是各用自己的 action。密码登录已经按账号限流，不需要人机验证。

使用 hCaptcha 时把 `endpoint` 设为 `https://api.hcaptcha.com/siteverify`，action 传空字符串，因为 hCaptcha 不返回它。本地开发可以使用 Cloudflare 提供的[测试密钥](https://developers.cloudflare.com/turnstile/troubleshooting/testing/)。测试中把 [`outboundtest`](outbound.md#testing) 的客户端传给 `captcha.New`，按需登记 `captcha.Turnstile` 地址的响应：

```go
fake := outboundtest.New(t)
fake.On("POST " + captcha.Turnstile).Reply(outboundtest.JSON(200, map[string]any{
    "success": true, "hostname": "blog.example.com", "action": "signup",
}))
bots, err := captcha.New(captcha.Config{Secret: "test"}, fake.Client())
```

<a id="cleanup"></a>
## 清理

过期和已用过的验证码不会再通过验证，但行仍然留在表中。`Cleanup` 每次最多删除 `limit` 条，可以注册为[定时任务](scheduling.md)：

```go
n, err := users.Cleanup(ctx, 1000)
```
