# 邮件

- [简介](#introduction)
- [装配发送器](#assembling-mailer)
- [编写邮件模板](#writing-templates)
    - [布局](#layouts)
    - [翻译](#translations)
- [发送邮件](#sending-mail)
- [邮件消息](#messages)
- [测试](#testing)
- [使用 SMTP](#smtp)
- [使用 Resend](#resend)
- [自定义发送器](#custom-mailers)

<a id="introduction"></a>
## 简介

`mail` 包定义了邮件消息 `mail.Message`，以及只有一个方法的发送接口：

```go
type Mailer interface {
    Send(ctx context.Context, msg Message) error
}
```

具体怎么发送由驱动决定：

| 包 | 用途 |
| --- | --- |
| `mail/logmail` | 开发时把邮件写进日志，不真正发送 |
| `mail/smtp` | 通过 SMTP 服务器发送，基于 [go-mail](https://github.com/wneessen/go-mail) |
| `mail/resend` | 通过 [Resend](https://resend.com) 发送，基于官方 SDK [resend-go](https://github.com/resend/resend-go) |
| `mail/mailtest` | 测试中记录发出的邮件 |

业务代码只依赖 `mail.Mailer`，换驱动只需要改入口。

本章为博客应用加一个功能：文章发布后，给编辑部发一封通知邮件。

<a id="assembling-mailer"></a>
## 装配发送器

在应用配置中加入邮件段。选哪个驱动由应用自己决定，这里用一个 `driver` 字段区分：

```go
import (
    "github.com/ryan053857/mygoravel/mail"
    "github.com/ryan053857/mygoravel/mail/logmail"
    "github.com/ryan053857/mygoravel/mail/resend"
    "github.com/ryan053857/mygoravel/mail/smtp"
)

type MailConfig struct {
    Driver  string        `yaml:"driver"`  // log、smtp 或 resend
    Editors mail.Address  `yaml:"editors"` // 编辑部邮箱
    SMTP    smtp.Config   `yaml:"smtp"`
    Resend  resend.Config `yaml:"resend"`
}

type Config struct {
    // ...
    Mail MailConfig `yaml:"mail"`
}
```

```yaml
mail:
  driver: log
  editors: 编辑部 <editors@example.com>
```

`mail.Address` 在配置中写成一个字符串，`"名字 <邮箱>"` 或只写邮箱都可以。

然后在[入口](dependencies.md#assembling-the-application)中创建发送器，传给发布文章的 action：

```go
func newMailer(cfg settings.MailConfig) (mail.Mailer, error) {
    switch cfg.Driver {
    case "smtp":
        return smtp.New(cfg.SMTP)
    case "resend":
        return resend.New(cfg.Resend)
    default:
        return logmail.New(nil), nil
    }
}
```

```go
mailer, err := newMailer(cfg.Mail)
if err != nil {
    return err
}
publish := article.NewPublish(db, mailer, emails, cfg.Mail.Editors)
```

[发布文章](actions.md#writing-actions)的 `Publish` 多了三个依赖，`article.Register` 构造它的地方也要把它们传进去。

`emails` 是下一节加载的邮件模板。`logmail.New(nil)` 使用 `slog.Default()`，每封邮件记录为一条 `mail sent` 日志，包含收件人、主题和正文，开发时可以直接从日志里复制邮件中的链接。

发送器不持有长连接，不需要登记到 `cleanup.Stack`。

<a id="writing-templates"></a>
## 编写邮件模板

邮件模板用 Go 标准库的 [html/template](https://pkg.go.dev/html/template) 和 [text/template](https://pkg.go.dev/text/template) 编写。一封名为 `article_published` 的邮件由三个文件组成：

```
internal/emails/
├── embed.go
├── _layout.html
├── article_published.subject.txt
├── article_published.html
└── article_published.txt
```

`.subject.txt` 是主题，必须提供；`.html` 和 `.txt` 是正文，至少提供一个，两个都有时收件人的邮件客户端会选择合适的版本显示。邮件可以放在子目录中，名称就是相对路径，例如 `auth/reset_password`。

```
{{/* article_published.subject.txt */}}
新文章：{{.Title}}
```

```
{{/* article_published.txt */}}
《{{.Title}}》已经发布：{{.URL}}
```

用 `embed` 把模板嵌入二进制，在入口中加载一次：

```go
package emails

import "embed"

//go:embed *.html *.txt
var Files embed.FS
```

```go
emails, err := mail.NewTemplates(emails.Files)
if err != nil {
    return err
}
```

`NewTemplates` 在启动时解析全部模板，缺少主题、缺少正文或语法错误都会在这里报错，而不是等到发送时。返回的 `*mail.Templates` 可以被多个请求同时使用。

<a id="layouts"></a>
### 布局

文件名以 `_` 开头的 `.html` 或 `.txt` 文件是公共片段，会被加入每封邮件的同类模板。用它写布局：布局通过 `{{template "content" .}}` 引用邮件内容，邮件先定义 `content`，再调用布局：

```html
{{/* _layout.html */}}
<!doctype html>
<html>
<body style="font-family: sans-serif">
  {{template "content" .}}
  <p style="color: #888">博客编辑部</p>
</body>
</html>
```

```html
{{/* article_published.html */}}
{{define "content"}}
<p>《{{.Title}}》已经发布。</p>
<p><a href="{{.URL}}">查看文章</a></p>
{{end}}
{{template "_layout.html" .}}
```

`.html` 模板会按 HTML 规则转义数据，文章标题中的 `<` 不会破坏邮件结构。

<a id="translations"></a>
### 翻译

所有模板都可以调用 `t` 翻译消息，它就是 [i18n.T](localization.md)，语言取自渲染时的 `ctx`：

```
{{/* article_published.subject.txt */}}
{{t "mail.article_published.subject" .}}
```

```yaml
# locales/zh-CN.yaml
mail.article_published.subject: "新文章：{{.Title}}"
```

在 HTTP 请求中渲染时，`ctx` 已经带着请求语言。在请求之外，用 `i18n.WithLocale` 指定收件人的语言。

<a id="sending-mail"></a>
## 发送邮件

`Render` 渲染一封邮件，返回已经填好 `Subject`、`HTML` 和 `Text` 的 `mail.Message`，再补上收件人交给发送器：

```go
type Publish struct {
    db      *bun.DB
    mailer  mail.Mailer
    emails  *mail.Templates
    editors mail.Address
}

func (a *Publish) Handle(ctx context.Context, subject identity.Subject, id uuid.UUID) (*Article, error) {
    item := new(Article)
    err := a.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
        // ...
    })
    if err != nil {
        return nil, err
    }
    a.notifyEditors(ctx, item)
    return item, nil
}

func (a *Publish) notifyEditors(ctx context.Context, item *Article) {
    msg, err := a.emails.Render(ctx, "article_published", map[string]any{
        "Title": item.Title,
        "URL":   "https://blog.example.com/" + item.Slug,
    })
    if err == nil {
        msg.To = []mail.Address{a.editors}
        err = a.mailer.Send(ctx, msg)
    }
    if err != nil {
        slog.ErrorContext(ctx, "notify editors failed", "article_id", item.ID, "error", err)
    }
}
```

`Publish` 的事务部分见[授权](authorization.md#checking-in-transactions)。这里有两点要注意：

- **在事务提交之后发送**。在事务里发送，事务回滚时邮件已经发出去了。
- **发送失败不影响发布**。文章已经发布成功，通知没发出去只记录日志，不让接口返回错误。

> [!NOTE]
> `Send` 是同步的：接口要等邮件服务器响应才返回，发送失败也不会重试。需要可靠送达的邮件可以交给[队列](queue.md#publishing-jobs)发送，`mail.Message` 可以完整地序列化为 JSON。

<a id="messages"></a>
## 邮件消息

除了模板填好的字段，`mail.Message` 还可以设置：

```go
msg.From = mail.Address{Name: "博客", Email: "news@example.com"} // 为空时使用驱动配置的发件人
msg.Cc = []mail.Address{{Email: "chief@example.com"}}
msg.Bcc = []mail.Address{{Email: "archive@example.com"}}
msg.ReplyTo = mail.Address{Email: "support@example.com"}
msg.Attachments = []mail.Attachment{
    {Filename: "report.pdf", ContentType: "application/pdf", Content: pdf},
}
msg.Headers = map[string]string{"X-Entity-Ref-ID": item.ID.String()}
```

每个驱动在发送前都会调用 `msg.Validate()`：至少一个 `To` 收件人、主题非空且只有一行、正文至少有一种、地址合法、头部不含换行。不满足时返回的错误可以用 `errors.Is(err, mail.ErrInvalid)` 判断。

附件会整个放进内存。大文件更适合先用[文件存储](filesystem.md)上传，在邮件中放公开链接。

<a id="testing"></a>
## 测试

测试中用 `mailtest.New()` 代替真实的发送器，它记录每封通过校验的邮件：

```go
func TestPublishNotifiesEditors(t *testing.T) {
    mailer := mailtest.New()
    emails, err := mail.NewTemplates(emails.Files)
    if err != nil {
        t.Fatal(err)
    }
    editors := mail.Address{Email: "editors@example.com"}
    publish := article.NewPublish(db, mailer, emails, editors)

    // ... 创建并发布一篇文章

    msg, ok := mailer.Last()
    if !ok || msg.To[0] != editors || !strings.Contains(msg.Subject, "Hello") {
        t.Fatalf("sent = %+v", mailer.Sent())
    }
}
```

`mailer.FailWith(err)` 让之后的发送都返回 `err`，用来测试发送失败时的行为；`Reset` 清空记录。

<a id="smtp"></a>
## 使用 SMTP

```yaml
mail:
  driver: smtp
  smtp:
    from: 博客 <no-reply@example.com>
    host: smtp.example.com
    port: 587
    username: ${SMTP_USERNAME}
    password: ${SMTP_PASSWORD}
    tls: starttls
```

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `from` | | 默认发件人，必填 |
| `host` | | 服务器地址，必填 |
| `port` | `587` | 端口 |
| `username`、`password` | | 为空时不认证 |
| `tls` | `starttls` | `starttls`：连接后必须升级到 TLS；`tls`：直接建立 TLS 连接，通常用于 465 端口；`none`：不加密 |
| `timeout` | `10s` | 连接和每次读写的超时 |

每次 `Send` 都会建立一个新连接。设置了 `username` 时必须使用 `starttls` 或 `tls`，go-mail 不会在未加密的连接上发送密码。

本地开发可以用 [Mailpit](https://mailpit.axllent.org) 在浏览器里查看发出的邮件，配置为 `host: localhost`、`port: 1025`、`tls: none`。

<a id="resend"></a>
## 使用 Resend

```yaml
mail:
  driver: resend
  resend:
    from: 博客 <no-reply@example.com>
    api_key: ${RESEND_API_KEY}
```

发件人的域名需要先在 Resend 中验证。

Resend 支持幂等键：在同一个 key 下重复发送，Resend 只会发出一封。重试可能重复的发送时，用业务 ID 作为 key：

```go
ctx = resend.WithIdempotencyKey(ctx, "article-published/"+item.ID.String())
err = mailer.Send(ctx, msg)
```

需要 Resend 特有的功能（批量发送、定时发送、标签）时，`Client()` 返回底层的 SDK 客户端。需要自定义 `http.Client` 时，自己用 SDK 创建客户端，再交给 `resend.NewWithClient`：

```go
client := resendsdk.NewCustomClient(httpClient, cfg.Mail.Resend.APIKey)
mailer, err := resend.NewWithClient(client, cfg.Mail.Resend.From)
```

<a id="custom-mailers"></a>
## 自定义发送器

接入其他邮件服务时，实现 `mail.Mailer` 即可。发送前用 `WithDefaultFrom` 补上默认发件人，再调用 `Validate`：

```go
func (m *SESMailer) Send(ctx context.Context, msg mail.Message) error {
    msg = msg.WithDefaultFrom(m.from)
    if err := msg.Validate(); err != nil {
        return err
    }
    // 调用 SES API
}
```
