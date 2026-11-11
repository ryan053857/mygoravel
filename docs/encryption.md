# 加密与签名链接

- [简介](#introduction)
- [配置](#configuration)
- [加密数据](#encrypting-data)
- [签名链接](#signed-urls)
    - [校验链接](#validating-signed-urls)
    - [永不过期的链接](#permanent-urls)
- [轮换密钥](#rotating-keys)
- [测试](#testing)

<a id="introduction"></a>
## 简介

`crypt` 包用一个应用密钥做两件事：

- **加密**以后还要读出明文的值，例如租户填写的模型服务商 API Key、第三方平台的 refresh token。这类值不能像密码那样[哈希](hashing.md)，因为调用第三方接口时要用到明文。
- **签名链接**：给邮件里的链接加上过期时间和签名，用户点开时确认链接是本站生成的、没有被改过、没有过期。例如邮箱验证、退订通知。

加密使用 AES-256-GCM，签名使用 HMAC-SHA256，两者的子密钥都从应用密钥用 HKDF 派生，互不相同。

<a id="configuration"></a>
## 配置

把 `crypt.Config` 放进应用的根配置结构体，配置段为 `crypt`：

```go
type Config struct {
    // ...
    Crypt crypt.Config `yaml:"crypt"`
}
```

```yaml
crypt:
  key: ${APP_KEY}
```

应用密钥是 32 字节随机数的 base64，用下面的命令生成，和数据库密码一样保存在部署环境中：

```shell
openssl rand -base64 32
```

密钥一旦用来加密了数据，就不能丢失或随意更换：没有它，已经加密的值无法解开，已经发出的链接全部失效。更换密钥见[轮换密钥](#rotating-keys)。

在[入口](dependencies.md#assembling-the-application)的 `open` 中创建 `Crypt`，保存在 `App` 中，传给需要它的 action 和处理器。`Crypt` 可以并发使用，整个应用共用一个：

```go
c := crypt.New(cfg.Crypt)
```

<a id="encrypting-data"></a>
## 加密数据

`Encrypt` 返回 URL 安全的 base64 文本，存在普通的文本列中。每次加密使用新的随机数，同一个值两次的结果不同。下面是保存租户模型服务商的 action：

```go
type Provider struct {
    ID       uuid.UUID `bun:",pk"`
    TenantID string
    Model    string
    BaseURL  string
    APIKey   string // 密文
}

func (a *CreateProvider) Handle(ctx context.Context, sub identity.Subject, in ProviderInput) (*Provider, error) {
    p := &Provider{
        ID: uuid.New(), TenantID: sub.Tenant, Model: in.Model, BaseURL: in.BaseURL,
        APIKey: a.crypt.Encrypt(in.APIKey),
    }
    if _, err := a.db.NewInsert().Model(p).Exec(ctx); err != nil {
        return nil, errors.Wrap(err, "create provider")
    }
    return p, nil
}
```

用到时用 `Decrypt` 解开：

```go
key, err := a.crypt.Decrypt(p.APIKey)
if err != nil {
    return errors.Wrapf(err, "decrypt api key of provider %s", p.ID)
}
model := ai.NewModel(ai.ModelConfig{Model: p.Model, BaseURL: p.BaseURL, APIKey: key})
```

密文被改过，或者加密它的密钥已经不在配置中时，`Decrypt` 返回 `crypt.ErrDecrypt`。它不是业务错误，接口返回 500 并记录日志。

<a id="signed-urls"></a>
## 签名链接

用户修改邮箱后，博客给新邮箱发一封验证邮件。`SignURL` 在链接后加上 `expires` 和 `signature` 两个参数：

```go
link := a.crypt.SignURL("https://blog.example.com/email/verify/"+u.ID+"?email="+url.QueryEscape(u.Email), 24*time.Hour)
// https://blog.example.com/email/verify/42?email=a%40b.com&expires=1731400000&signature=Xy...

msg, err := a.emails.Render(ctx, "verify_email", map[string]any{"URL": link})
```

签名覆盖路径和其余全部查询参数，改了其中任何一处，签名就对不上；路径本身区分了链接的用途，验证邮箱的签名用不到别的地址上。签名**不覆盖**协议和域名：经过反向代理后，处理器看到的地址可能是 `http://10.0.0.5:8080/...`，校验结果不受影响。

签名只防篡改，不加密：链接中的参数谁都能看到。

<a id="validating-signed-urls"></a>
### 校验链接

在处理器中把 `r.URL` 交给 `VerifyURL`：

```go
func (h *Handlers) VerifyEmail(w http.ResponseWriter, r *http.Request) error {
    if err := h.crypt.VerifyURL(r.URL); err != nil {
        return err
    }
    err := h.verifyEmail.Handle(r.Context(), chi.URLParam(r, "id"), r.URL.Query().Get("email"))
    if err != nil {
        return err
    }
    web.Flash(r.Context(), web.Message{Type: "success", Title: "邮箱已验证"})
    web.Redirect(w, r, "/account")
    return nil
}
```

`VerifyURL` 只证明这个链接是本站发出的。链接发出之后数据可能变了，action 仍要按当前数据判断：上面的 `verifyEmail` 要确认用户当前的邮箱仍是链接中的 `email`，用户在此期间又改过邮箱，旧链接就不再生效。

签名不对时返回 `crypt.ErrInvalidSignature`，过期时返回 `crypt.ErrExpired`。两者都是 `errorx.PermissionDenied`，页面呈现 403 [错误页](views.md#error-pages)，提示“链接无效”或“链接已过期”。过期页可以提供重新发送的入口：

```go
if errors.Is(err, crypt.ErrExpired) {
    return web.Render(w, r, http.StatusForbidden, views.VerifyEmailExpired())
}
```

单页应用中，邮件里的链接指向前端页面，前端把整个地址交给 API，API 用 `url.Parse` 解析后同样交给 `VerifyURL`。

> [!WARNING]
> 有的邮件服务商会改写链接，追加 `utm_source` 等统计参数，签名随之失效。发送这类邮件时请在服务商处关闭链接追踪。

<a id="permanent-urls"></a>
### 永不过期的链接

`ttl` 为 0 时链接不过期，适合退订这类在旧邮件中也要能用的链接：

```go
link := a.crypt.SignURL("https://blog.example.com/unsubscribe/"+u.ID, 0)
```

需要只能使用一次的链接（例如重置密码）时，签名不够，要把令牌存在数据库中，用过就删除。

<a id="rotating-keys"></a>
## 轮换密钥

密钥可能泄露时要更换。把新密钥放进 `key`，旧密钥移到 `previous_keys`：

```yaml
crypt:
  key: ${APP_KEY}
  previous_keys:
    - ${APP_PREVIOUS_KEY}
```

之后新加密的数据和新发出的链接都使用新密钥；`Decrypt` 和 `VerifyURL` 依次尝试 `key` 和 `previous_keys`，旧数据和旧链接仍然有效。

旧密钥要一直保留，直到用它加密的数据都换成了新密钥。框架不提供重新加密的命令：需要时写一个一次性的[命令](commands.md#custom-commands)，逐行 `Decrypt` 再 `Encrypt`。密钥确实泄露时，换完数据后从 `previous_keys` 中删除旧密钥，旧链接也随之失效。

<a id="testing"></a>
## 测试

测试中用 `crypt.GenerateKey()` 生成密钥：

```go
c := crypt.New(crypt.Config{Key: crypt.GenerateKey()})
```

测试链接过期时，用 [testing/synctest](https://pkg.go.dev/testing/synctest) 推进时间：

```go
synctest.Test(t, func(t *testing.T) {
    link, _ := url.Parse(c.SignURL("https://blog.example.com/email/verify/42", time.Hour))
    time.Sleep(time.Hour)
    assert.ErrorIs(t, c.VerifyURL(link), crypt.ErrExpired)
})
```
