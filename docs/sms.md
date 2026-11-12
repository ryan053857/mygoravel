# 短信

- [简介](#introduction)
- [装配发送器](#assembling)
- [发送短信](#sending)
- [错误](#errors)
- [多个服务商](#failover)
- [测试](#testing)
- [使用阿里云](#aliyun)
- [使用腾讯云](#tencent)
- [自定义发送器](#custom-senders)

<a id="introduction"></a>
## 简介

Laravel 的短信通知通过 Vonage 发送，内容是任意一段文字。国内短信不一样：只能用审核通过的**签名**和**模板**发送，发送时传模板 ID 和模板变量，例如模板“您的验证码是 ${code}，15 分钟内有效”加上 `{"code": "123456"}`。

所以 `sms` 中的一条短信是收件号码、模板名和模板变量。模板名是应用自己起的，例如 `login_code`；签名和每家服务商的模板 ID 写在各自的配置中。同一条短信可以交给不同的服务商发送，业务代码不需要知道用的是哪一家。

| 包 | 用途 |
| --- | --- |
| `sms/aliyun` | 阿里云短信服务 |
| `sms/tencent` | 腾讯云短信 |
| `sms/logsms` | 只写日志不发送，用于开发 |
| `sms/smstest` | 在内存中记录，用于测试 |

本章的例子是[读者 App](account-examples.md#reader-api) 的验证码登录。

<a id="assembling"></a>
## 装配发送器

和[邮件](mail.md#assembling-mailer)一样，用哪个服务商由应用的配置决定：

```go
type SMSConfig struct {
    Driver  string         `yaml:"driver"` // log、aliyun 或 tencent
    Aliyun  aliyun.Config  `yaml:"aliyun"`
    Tencent tencent.Config `yaml:"tencent"`
}

func newTexts(cfg settings.SMSConfig) (sms.Sender, error) {
    switch cfg.Driver {
    case "aliyun":
        return aliyun.New(cfg.Aliyun, nil)
    case "tencent":
        return tencent.New(cfg.Tencent, nil)
    default:
        return logsms.New(nil), nil
    }
}
```

```yaml
sms:
  driver: aliyun
  aliyun:
    access_key_id: ${ALIYUN_ACCESS_KEY_ID}
    access_key_secret: ${ALIYUN_ACCESS_KEY_SECRET}
    sign_name: 博客
    templates:
      login_code: SMS_465000001
```

开发时用 `log`，`logsms` 把每条短信记录为一条 `sms sent` 日志，带着号码、模板和变量，验证码直接从日志里看。发送器不持有长连接，不需要登记到 `cleanup.Stack`。

<a id="sending"></a>
## 发送短信

读者 App 用手机号登录，[`login`](login.md#codes) 的 `Send` 把验证码交给短信发送器：

```go
Send: func(ctx context.Context, code login.Code) error {
    return texts.Send(ctx, sms.Message{
        To:       code.To.Value,
        Template: "login_code",
        Params:   map[string]string{"code": code.Code},
    })
},
```

| 字段 | 说明 |
| --- | --- |
| `To` | E.164 格式的号码，例如 `+8613800138000`。`login` 中的手机号已经是这个格式 |
| `Template` | 应用给模板起的名字，各服务商的配置把它对应到自己的模板 ID |
| `Params` | 模板变量，键是变量名 |

`Send` 是同步的，等服务商响应才返回。通知类的短信交给[队列](queue.md#publishing-jobs)发送，`sms.Message` 可以完整地序列化为 JSON，失败时按退避重试。

服务商接受了请求，`Send` 就返回成功，之后由运营商投递，送达与否在服务商的回执中查看。阿里云和腾讯云都没有幂等键，重试可能让用户收到两条。

<a id="errors"></a>
## 错误

| 错误 | 含义 | 返回给客户端 |
| --- | --- | --- |
| `sms.ErrInvalid` | 短信本身不合法：号码不是 E.164 格式、模板名为空，或者缺少腾讯云模板需要的变量 | `500`，是程序的问题 |
| `sms.ErrRejected` | 服务商拒绝向这个号码发送：号码格式不被接受、空号停机、在黑名单中、用户退订 | `422` |
| `sms.ErrLimited` | 触发了服务商对这个号码的频率限制，例如同一号码 30 秒内只能收一条 | `429` |
| 其他错误 | 服务商的问题：余额不足、签名或模板未审核、密钥错误、系统故障、网络错误 | `500` |

`ErrRejected` 和 `ErrLimited` 是 [业务错误](errors.md)，经 `login.SendCode` 原样返回时，客户端得到 `422` 或 `429` 和翻译后的提示。

<a id="failover"></a>
## 多个服务商

验证码是同步发送的，用户在等着收码。一家服务商出问题（故障、余额不足、签名被暂停），用户就登录不了。`sms.Failover` 把几家组合起来，前一家失败时换下一家：

```go
primary, err := aliyun.New(cfg.SMS.Aliyun, nil)
if err != nil {
    return err
}
backup, err := tencent.New(cfg.SMS.Tencent, nil)
if err != nil {
    return err
}
texts := sms.Failover([]sms.Named{
    {Name: "aliyun", Sender: primary},
    {Name: "tencent", Sender: backup},
}, sms.FailoverOptions{})
```

规则与[邮件的多个发送器](mail.md#failover)相同：按顺序尝试，失败的服务商在 `Cooldown`（默认 1 分钟）内排到最后，换用时记录 `Warn` 日志，恢复时记录 `Info`。区别在于什么时候**不**换下一家：

- `sms.ErrInvalid`：换哪家都一样。
- `sms.ErrRejected`：号码本身发不到，换哪家都一样。
- `sms.ErrLimited`：这是服务商对单个号码的防刷保护。换一家发就绕过了它，被人批量刷验证码时，费用会翻倍。
- `ctx` 已经取消或超时：调用方不再等待。

每家服务商都要申请好同名的模板，签名也都要审核通过。

<a id="testing"></a>
## 测试

`smstest.New()` 返回在内存中记录短信的发送器，`Sent`、`Last` 读出发送过的短信，`FailWith` 模拟服务商故障或拒绝：

```go
texts := smstest.New()
// ……用 texts 装配 login，调用 SendCode
msg, ok := texts.Last()
if !ok || msg.To != "+8613800138000" || msg.Params["code"] == "" {
    t.Fatalf("sms %+v", msg)
}

texts.FailWith(sms.ErrLimited.New())
// ……再发送，确认客户端收到 429
```

测试 `sms/aliyun` 和 `sms/tencent` 的配置时，把 [`outboundtest`](outbound.md#testing) 的客户端传给 `New`。

<a id="aliyun"></a>
## 使用阿里云

| 字段 | 说明 |
| --- | --- |
| `access_key_id`、`access_key_secret` | RAM 用户的 AccessKey，需要 `dysms:SendSms` 权限 |
| `sign_name` | 审核通过的签名，必填 |
| `templates` | 模板名到模板 Code 的对应，例如 `login_code: SMS_465000001` |
| `endpoint` | 接口域名，为空时是 `dysmsapi.aliyuncs.com` |

模板中的变量名与 `Params` 的键一致，例如模板写 `${code}`，就传 `{"code": "123456"}`。号码去掉 `+` 后交给阿里云，国内号码带 `86` 前缀也被接受。

国内短信的签名需要完成实名报备，运营商会拦截未报备签名的短信，报备约需一周。验证码模板的变量只能有一个，长度 4 到 6 位。

<a id="tencent"></a>
## 使用腾讯云

```yaml
sms:
  driver: tencent
  tencent:
    secret_id: ${TENCENT_SECRET_ID}
    secret_key: ${TENCENT_SECRET_KEY}
    app_id: "1400000001"
    sign_name: 博客
    templates:
      login_code: {id: "2000001", params: [code]}
```

| 字段 | 说明 |
| --- | --- |
| `secret_id`、`secret_key` | API 密钥 |
| `app_id` | 短信应用的 SdkAppId，必填 |
| `sign_name` | 签名内容（不是签名 ID）。国内短信必填，国际短信可以不填 |
| `templates` | 模板名到模板的对应：`id` 是模板 ID，`params` 是模板变量的顺序 |
| `region` | 地域，为空时是 `ap-guangzhou` |
| `endpoint` | 接口域名，为空时是 `sms.tencentcloudapi.com` |

腾讯云的模板变量是按顺序的 `{1}`、`{2}`，没有名字。`params` 写明第几个变量取 `Params` 中的哪个键，阿里云和腾讯云因此可以用同一条 `sms.Message`。`Params` 缺少其中的键时返回 `sms.ErrInvalid`，不调用接口。

<a id="custom-senders"></a>
## 自定义发送器

接入其他服务商（例如海外的 Twilio）时，实现 `sms.Sender` 即可。发送前调用 `Validate`，把服务商表示号码被拒绝、号码频率受限的错误码包装为 `sms.ErrRejected`、`sms.ErrLimited`，`Failover` 才能正确判断：

```go
func (s *TwilioSender) Send(ctx context.Context, msg sms.Message) error {
    if err := msg.Validate(); err != nil {
        return err
    }
    body, err := s.render(msg) // Twilio 发送文字：按模板名渲染出正文
    if err != nil {
        return err
    }
    // 调用 Twilio API；21211（号码无效）、21610（用户退订）包装为 sms.ErrRejected
}
```
