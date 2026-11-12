# 出站请求

- [简介](#introduction)
- [创建客户端](#creating-clients)
- [允许的地址与端口](#allowed-destinations)
    - [本地开发](#local-development)
- [处理被拒绝的请求](#handling-refusals)
- [重试](#retrying)
- [代理](#proxies)
- [在其他组件中使用](#using-with-other-components)
- [测试](#testing)

<a id="introduction"></a>
## 简介

有些请求的地址由用户填写：租户的 [webhook](webhooks.md) 端点、文章中链接的预览、AI 助手要抓取的网页。如果直接用 `http.DefaultClient` 去请求，有人可以填 `http://169.254.169.254/latest/meta-data/` 读取云服务器的元数据（其中可能有临时密钥），或者填 `http://10.0.0.5:6379/` 访问内网服务。这类攻击叫服务端请求伪造（SSRF）。

`outbound` 包创建只能访问公网地址的 `*http.Client`。地址在域名解析之后、建立连接之前检查，检查的是真正要连接的 IP，所以下面这些绕过方式都无效：

- 域名解析到内网地址，或者先解析到公网、请求时再改成内网（DNS 重绑定）；
- 公网地址返回重定向，跳到内网地址；
- `100.100.100.200`（阿里云的元数据接口）、`[::ffff:10.0.0.1]`、`[64:ff9b::a00:1]` 这类不常见的写法。

拒绝的地址取自 IANA 的特殊用途地址登记表，由 [code.dny.dev/ssrf](https://pkg.go.dev/code.dny.dev/ssrf) 维护，包括内网、回环、链路本地、运营商级 NAT、组播、保留地址，以及能内嵌 IPv4 地址的 IPv6 前缀。

<a id="creating-clients"></a>
## 创建客户端

博客为文章中的链接显示预览卡片：取出页面的标题和图片。链接是作者填的，用 `outbound` 的客户端请求它。客户端可以复用，在[入口](dependencies.md#assembling-the-application)中创建一次，传给 action：

```go
import "github.com/ryan053857/mygoravel/outbound"

previews := outbound.New(outbound.Options{Timeout: 5 * time.Second})
```

它就是普通的 `*http.Client`：

```go
func (a *Preview) fetch(ctx context.Context, link string) (*LinkPreview, error) {
    req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
    if err != nil {
        return nil, ErrInvalidLink.Wrap(err)
    }
    resp, err := a.client.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()
    return parsePreview(io.LimitReader(resp.Body, 1<<20)) // 最多读 1 MiB
}
```

`Timeout` 是整个请求的上限，包括连接、重定向和读取响应体，零值是 30 秒。客户端不限制响应的大小，请求用户填写的地址时，用 `io.LimitReader` 限制读取的字节数，防止有人让服务器下载一个巨大的文件。

<a id="allowed-destinations"></a>
## 允许的地址与端口

默认只能连接公网地址的 80 和 443 端口。其他选项：

| 选项 | 作用 |
| --- | --- |
| `Ports` | 允许的端口，替换默认的 80 和 443，例如 `[]uint16{443, 8443}` |
| `AnyPort` | 允许任何端口，地址检查仍然生效 |
| `Allow` | 额外允许的网段，先于内置的拒绝规则检查，用于有意访问的内部服务 |

客户端跟随重定向（最多 10 次），每一跳都经过同样的检查。不需要跟随时，设置返回的客户端的 `CheckRedirect`。

<a id="local-development"></a>
### 本地开发

开发时 webhook 的接收方往往运行在本机，用 `Allow` 放行回环地址：

```go
opts := outbound.Options{AnyPort: true}
if a.cfg.Env == "local" {
    opts.Allow = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("::1/128")}
}
client := outbound.New(opts)
```

<a id="handling-refusals"></a>
## 处理被拒绝的请求

被拒绝的请求返回的错误可以用 `errors.Is(err, outbound.ErrForbidden)` 识别。目标地址不会变成公网地址，重试也没有用，应当直接告诉用户：

```go
resp, err := a.client.Do(req)
if errors.Is(err, outbound.ErrForbidden) {
    return nil, ErrInvalidLink.Wrap(err) // 400：这个链接不能预览
}
```

拒绝发生在建立连接之前，不会向目标发出任何数据。

<a id="retrying"></a>
## 重试

对方偶尔返回 503 或者连接被重置时，预览就失败了。用 `outbound.Retry` 包装客户端的传输层，临时失败时自动重试：

```go
previews := outbound.New(outbound.Options{Timeout: 5 * time.Second})
previews.Transport = outbound.Retry(previews.Transport, outbound.RetryOptions{})
```

为了不把一次失败变成两次扣款，它只重试可以安全重复的请求：

- 方法是 HTTP 规范中幂等的 GET、HEAD、OPTIONS、TRACE、PUT、DELETE，或者请求带有 `Idempotency-Key`（`X-Idempotency-Key`）请求头，并且请求体可以重新读取：用 `bytes`、`strings` 创建的请求体可以，直接传入的流不行；
- 连接失败，或者响应状态码是 429、502、503、504。`ErrForbidden`、域名不存在和证书错误不会因为重试而成功，直接返回。

| 选项 | 作用 |
| --- | --- |
| `Attempts` | 包括第一次在内的最多尝试次数，默认 3 |
| `Backoff` | 第一次重试前的等待时间，之后每次翻倍并加上随机抖动，默认 200ms |
| `MaxWait` | 两次尝试之间最长的等待时间，默认 5s；响应的 `Retry-After` 超过它时不重试，直接返回这个响应 |

客户端的 `Timeout` 和请求的 context 限制包括重试在内的总时长，剩余时间不够等到下一次时，直接返回最后一次的结果。

重试让用户多等，只适合当前请求就需要结果的场景。要保证送达的请求，例如通知第三方订单已支付，放进[队列](queue.md)的任务中：任务失败后由队列重试，进程重启也不会丢失。[Webhook](webhooks.md) 的投递就是这样做的。

<a id="proxies"></a>
## 代理

客户端不使用 `HTTP_PROXY`、`HTTPS_PROXY` 环境变量中的代理：经过代理时，服务器连接的是代理，真正的目标由代理去连接，检查就失去了作用。部署要求经过代理访问外网时，请在代理上做同样的限制，例如使用 [Smokescreen](https://github.com/stripe/smokescreen)。

<a id="using-with-other-components"></a>
## 在其他组件中使用

- [Webhook](webhooks.md#private-addresses) 的投递默认使用 `outbound` 的客户端，端口不限。
- [AI 助手](ai.md)的模型地址来自应用自己的配置或供应商表，默认不做限制：接入内网自建的模型服务是正常的需求。允许租户填写模型地址时，把客户端传给模型：

  ```go
  client := outbound.New(outbound.Options{AnyPort: true, Timeout: -1}) // 流式响应不能有总超时
  llm := chat.NewModel(cfg, openai.WithOpenAIOptions(option.WithHTTPClient(client)))
  media := aiopenai.New(cfg, aiopenai.WithHTTPClient(client))
  ```

  `openai` 是 trpc-agent-go 的 `model/openai`，`option` 是 openai-go 的 `option` 包，`aiopenai` 是框架的 `ai/openai`。
- 其他需要 `*http.Client` 的库，例如 SDK 的 `WithHTTPClient` 选项，同样可以传入它。

<a id="testing"></a>
## 测试

`outbound/outboundtest` 提供不连接网络的 `http.RoundTripper`：按方法和地址登记响应，记录发出的每个请求。把它的 `Client()` 交给 action，就可以测试链接预览，不需要真的去请求 `example.com`：

```go
func TestPreview(t *testing.T) {
    fake := outboundtest.New(t)
    fake.On("GET https://example.com/post").Reply(
        outboundtest.Text(200, `<title>Hello</title><meta property="og:image" content="/cover.png">`).
            Header("Content-Type", "text/html"),
    )
    preview := &Preview{client: fake.Client()}

    got, err := preview.fetch(t.Context(), "https://example.com/post")
    require.NoError(t, err)
    assert.Equal(t, "Hello", got.Title)
    assert.Len(t, fake.Sent("GET https://example.com/post"), 1)
}
```

`On` 的参数是可选的方法加地址，省略方法时匹配任何方法。地址中的 `*` 匹配任意字符，例如 `https://api.github.com/users/*`；省略协议时 http 和 https 都匹配；不写 `?` 时忽略查询参数。同一个请求匹配多个登记时，后登记的优先，测试可以覆盖公共辅助函数中登记的响应。

`Reply` 接收一个或多个响应：第 n 个请求得到第 n 个响应，用完之后一直返回最后一个。

| 响应 | 作用 |
| --- | --- |
| `JSON(status, v)` | JSON 响应 |
| `Text(status, body)` | 纯文本响应 |
| `Status(status)` | 没有响应体的响应，没有调用 `Reply` 时是 `Status(200)` |
| `Fail(err)` | 请求返回 `err`，模拟连接失败、超时（`context.DeadlineExceeded`） |
| `Handler(h)`、`HandlerFunc(f)` | 用 `http.Handler` 生成响应，可以检查请求，或者等待 `r.Context()` 结束模拟慢响应 |
| `.Header(key, value)` | 设置响应头，例如 `Status(429).Header("Retry-After", "1")` |

它替换的是传输层，`outbound` 的地址检查不会执行。测试被拒绝的链接时，让请求返回 `ErrForbidden`：

```go
fake.On("https://internal.example.com/*").Reply(outboundtest.Fail(outbound.ErrForbidden))

_, err := preview.fetch(t.Context(), "https://internal.example.com/admin")
assert.ErrorIs(t, err, ErrInvalidLink)
```

`Sent(pattern)` 返回匹配的请求，`Requests()` 返回全部请求。记录的请求带有完整的请求体，用 `JSON(&v)` 或 `Form()` 解析。没有登记的请求返回错误，并且在测试结束时让测试失败，代码不会在测试中悄悄访问外网。

第三方 SDK 通常有传入 `*http.Client` 的选项，同样可以用它测试，不需要 SDK 支持修改服务地址：

```go
fake.On("POST https://api.resend.com/emails").Reply(outboundtest.JSON(200, map[string]string{"id": "em_1"}))
client := resendsdk.NewCustomClient(fake.Client(), "re_test") // github.com/resend/resend-go/v3
mailer, err := resend.NewWithClient(client, from)
```
