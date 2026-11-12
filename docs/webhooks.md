# Webhook

- [简介](#introduction)
- [安装](#installation)
- [发送事件](#sending)
    - [请求的格式](#request-format)
    - [写入接口文档](#documenting)
- [管理端点](#endpoints)
    - [轮换密钥](#rotating-secrets)
    - [投递记录与重发](#deliveries)
- [运行投递](#delivering)
    - [重试](#retries)
    - [拒绝内网地址](#private-addresses)
- [接收 webhook](#receiving)
- [测试](#testing)
- [保留期限](#retention)

<a id="introduction"></a>
## 简介

SaaS 的租户常常要把你的应用接进他们自己的系统：文章发布后同步到公司官网，订单付款后通知 ERP。webhook 就是在事件发生时，向租户配置的地址发一个 HTTP 请求。

`webhook` 包负责发送这一端：租户管理自己的**端点**（接收地址和订阅的事件），业务代码发出事件，worker 签名并投递，失败时重试，每次投递都留下**投递记录**供租户排查。签名遵循 [Standard Webhooks](https://www.standardwebhooks.com/) 规范，租户可以直接用 Svix 等现成的库校验。它也提供 `Verify`，用来校验别人按这个规范发给你的 webhook。

支持 PostgreSQL、SQLite 和 MySQL。本章的例子：文章发布后，通知租户配置的端点。

<a id="installation"></a>
## 安装

webhook 的投递任务使用[数据库队列](queue.md)，把两者的迁移都加入[发布命令](migrations.md#publishing-framework-migrations)，发布后执行 `migrate up`：

```go
migratecmd.Publish(postgres.Driver,
    migrate.Source{Name: "queue", FS: queue.Migrations()},
    migrate.Source{Name: "webhook", FS: webhook.Migrations()},
)
```

<a id="sending"></a>
## 发送事件

事件先声明再发送。声明写明事件的名称和内容的类型，放在 `internal/article/webhooks.go` 中：

```go
package article

// PublishedHook 是 article.published 事件的内容。它是给租户系统的公开接口，字段一旦发布就不要随意修改。
type PublishedHook struct {
    ID          uuid.UUID `json:"id"`
    Title       string    `json:"title"`
    URL         string    `json:"url"`
    PublishedAt time.Time `json:"published_at"`
}

var Published = webhook.Event[PublishedHook]{Name: "article.published", Summary: "文章发布后推送"}
```

在[发布文章](actions.md#writing-actions)的事务中调用 `Send`：

```go
err := database.RunInTx(ctx, a.db, nil, func(ctx context.Context, tx database.Tx) error {
    // ……读取、检查并更新文章，与原来相同
    return Published.Send(ctx, tx, subject, PublishedHook{
        ID: item.ID, Title: item.Title, URL: a.baseURL + "/articles/" + item.Slug, PublishedAt: *item.PublishedAt,
    })
})
```

`Send` 找出 `subject.Tenant` 中订阅了这个事件的端点，为每个端点写入一条投递记录，并在同一个事务中发布投递任务：事务提交后才会投递，回滚时一起取消。租户没有配置端点时什么也不写，所以可以放心地在每个事件上调用它。

事件内容用专门的结构体，不要直接传入数据库模型：模型以后加的字段会悄悄发给所有租户，改名也会破坏他们的集成。

<a id="request-format"></a>
### 请求的格式

每个端点收到一个 `POST` 请求（签名为示意）：

```http
POST /hooks HTTP/1.1
Content-Type: application/json
webhook-id: 0193a5c2-7d1e-7b3a-9f61-2c4e8d0a1b2c
webhook-timestamp: 1767225600
webhook-signature: v1,K5oZfzN95Z9UVu1EsfQmfVNQhnkZ2pj9o9NDN/H/pI4=

{"type":"article.published","timestamp":"2026-01-01T00:00:00Z","data":{"id":"…","title":"…","url":"…","published_at":"…"}}
```

- `webhook-id` 是事件 ID：同一个事件发给不同的端点、重试多少次都相同，接收方用它去重。
- `webhook-signature` 是用端点密钥对 `id.timestamp.body` 计算的 HMAC-SHA256，接收方据此确认请求来自你、内容没有被改过。
- 投递**至少一次**，不保证顺序。接收方应当按 `webhook-id` 去重，按 `timestamp` 判断先后。

<a id="documenting"></a>
### 写入接口文档

在注册全部路由的 `bootstrap.Routes`（见[依赖装配](dependencies.md#assembling-the-application)）中用 `webhook.Document` 列出全部事件，[openapi 命令](commands.md#openapi-command)导出的文档就会在 `webhooks` 中描述它们：

```go
func Routes(api *rest.API, validate rest.TokenValidator, db *bun.DB) {
    article.Register(api, rest.RequireBearer(api, validate, "articles"), db)
    webhook.Document(api.OpenAPI(), article.Published)
}
```

新增事件时记得加到这里；事件名称重复时 `Document` 会 panic。事件内容一旦发布就是租户依赖的接口，修改时的检查见[检查接口变更](clients.md#checking-changes)。

每个事件描述为一个 `POST` 请求：请求体是 `{type, timestamp, data}`，`data` 的 schema 由事件内容的类型生成，名称规则与[接口的类型](routing.md#shared-responses)相同（`article.PublishedHook` 为 `ArticlePublishedHook`）；请求头是上面的三个签名请求头。`openapi --spec 3.0` 导出的文档和 `/openapi-3.0.json`、`/openapi-3.0.yaml` 端点都不含 `webhooks`，3.0 没有这一项。

租户可以用这份文档生成事件内容的类型。例如用 TypeScript 接收时，[openapi-typescript](clients.md#generating-types) 生成的 `webhooks` 类型按事件名列出请求体，配合 Standard Webhooks 的官方库 [standardwebhooks](https://www.npmjs.com/package/standardwebhooks) 校验签名：

```ts
import { Webhook } from "standardwebhooks"
import type { webhooks } from "./blog-api"

type BlogEvent = {
  [K in keyof webhooks]: webhooks[K]["post"]["requestBody"]["content"]["application/json"]
}[keyof webhooks]

const wh = new Webhook(secret) // 端点的 whsec_ 密钥

export async function POST(req: Request): Promise<Response> {
  const event = wh.verify(await req.text(), Object.fromEntries(req.headers)) as BlogEvent
  switch (event.type) {
    case "article.published":
      await syncArticle(event.data.url) // data 是 ArticlePublishedHook
      break
  }
  return new Response(null, { status: 204 })
}
```

<a id="endpoints"></a>
## 管理端点

端点属于租户，由租户管理员在设置页中维护，路由用 `webhooks:manage` 这类权限保护。所有函数都接收租户，其他租户的端点 ID 返回 `webhook.ErrEndpointNotFound`：

| 操作 | 调用 |
| --- | --- |
| 列出端点 | `webhook.ListEndpoints(ctx, db, sub.Tenant)` |
| 创建端点 | `webhook.CreateEndpoint(ctx, db, sub.Tenant, webhook.EndpointInput{URL, Events, Description})` |
| 查看、修改、停用 | `webhook.GetEndpoint`、`webhook.UpdateEndpoint`（`Disabled: true` 停用） |
| 删除端点和它的投递记录 | `webhook.DeleteEndpoint(ctx, db, sub.Tenant, id)` |

和[成员管理](tenancy.md#managing-members)一样，**租户只能取自 `sub.Tenant`**，不能来自表单。

`Events` 是订阅的事件：完整的类型如 `article.published`，以 `.*` 结尾的前缀如 `article.*`，或者 `*` 表示全部。可订阅的事件由应用决定，在模块中列出来，订阅表单用它显示选项，提交时用它校验：

```go
// WebhookEvents 是租户可以订阅的事件。
var WebhookEvents = []string{"article.published", "article.deleted"}
```

地址不是 http 或 https 的绝对地址时返回 `webhook.ErrInvalidURL`，`Events` 为空或写法不合法时返回 `webhook.ErrInvalidEvents`，它们都是[业务错误](errors.md)，API 返回 400。

创建时生成的 `Secret`（`whsec_` 开头）在端点详情页展示给租户，他们用它校验签名。

<a id="rotating-secrets"></a>
### 轮换密钥

密钥泄露或定期更换时，调用 `webhook.RotateSecret`。之后 24 小时内，每个请求同时带有新旧两个密钥的签名，租户先换上新密钥，旧密钥到期后自动失效，中间不会有请求校验失败。

<a id="deliveries"></a>
### 投递记录与重发

端点详情页列出最近的投递，帮助租户排查他们的接收程序：

```go
list, err := webhook.ListDeliveries(ctx, a.db, webhook.DeliveryQuery{Tenant: sub.Tenant, EndpointID: id, Before: before})
```

每条记录有状态（`pending` 等待或重试中、`succeeded`、`failed`）、已发送的次数、最后一次的 HTTP 状态码，以及失败原因（包括响应体的开头）。`DeliveryQuery` 还可以按 `EventID`、`Status` 筛选，翻页方式与[通知](notifications.md#listing)相同。

租户修好接收程序后，可以在页面上重发失败的投递：

```go
d, err := webhook.Resend(ctx, a.db, sub.Tenant, deliveryID)
```

`Resend` 以相同的事件 ID 和请求体写入一条新记录并立即投递，原记录保持不变。

<a id="delivering"></a>
## 运行投递

在 [worker](queue.md#running-workers) 中注册：

```go
webhook.NewDeliverer(a.db, webhook.Options{}).Handle(w)
```

`webhook.Options` 的零值即可：每次请求 10 秒超时，不跟随重定向，每次投递最多请求 16 次，最后一次重试约在 2 天后。重试次数只作用于投递任务，不受 `queue` 段 `max_attempts` 的影响，需要时用 `Options.MaxAttempts` 修改。

投递任务总是在数据库队列中。应用的其他任务使用 Redis 或 NATS 时，再创建一个数据库队列的 worker，和原来的一起运行：

```go
hooks := queue.NewWorker(queue.Database(a.db), a.cfg.Queue, nil)
webhook.NewDeliverer(a.db, webhook.Options{}).Handle(hooks)
return func(ctx context.Context) error { return command.Concurrently(ctx, w.Run, hooks.Run) }, nil
```

<a id="retries"></a>
### 重试

| 端点的响应 | 结果 |
| --- | --- |
| 2xx | 投递成功 |
| 其他状态码（包括 3xx）、超时、连接失败 | 按队列的[退避时间](queue.md#failures-and-retries)重试，第 n 次失败后约等 n⁴ 秒 |
| 429、503 带有 `Retry-After` | 按它的要求等待后重试 |
| 最后一次仍然失败 | 投递记录标记为 `failed` |
| 端点已停用 | 不再发送，投递记录标记为 `failed` |

最后一次失败的投递**不进入**[失败任务表](queue.md#failed-jobs)：接收方出错不是你的系统故障，不应该触发你的告警。投递记录就是它的结果，租户在页面上看到并重发。

<a id="private-addresses"></a>
### 拒绝内网地址

端点地址由租户填写。部署在云上时，有人可能填 `http://169.254.169.254/` 或内网服务的地址，借你的 worker 访问它们。`Deliverer` 默认使用 [outbound](outbound.md) 的客户端：内网、回环等地址在建立连接之前就被拒绝，端口不限。被拒绝的投递不再重试，直接标记为失败，`last_error` 中写明原因。

本地开发时接收方往往运行在本机，传入放行回环地址的客户端：

```go
client := outbound.New(outbound.Options{
    AnyPort: true,
    Allow:   []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("::1/128")},
})
webhook.NewDeliverer(a.db, webhook.Options{Client: client}).Handle(w)
```

无论传入什么客户端，投递都不跟随重定向，`3xx` 视为失败。

<a id="receiving"></a>
## 接收 webhook

越来越多的服务按 Standard Webhooks 发送 webhook，例如 Resend。用 `webhook.Verify` 校验，校验通过后把内容交给队列处理，立即返回 2xx：

```go
// EmailEvent 是收到的邮件事件，由任务处理。
var EmailEvent = queue.Topic[Inbound]("resend.event")

type Inbound struct {
    ID   string          `json:"id"` // webhook-id，任务按它去重
    Body json.RawMessage `json:"body"`
}

func (h *Handlers) ResendWebhook(w http.ResponseWriter, r *http.Request) {
    body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
    if err != nil {
        http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
        return
    }
    if err := webhook.Verify(h.secret, r.Header, body); err != nil {
        http.Error(w, "invalid signature", http.StatusUnauthorized)
        return
    }
    in := Inbound{ID: r.Header.Get(webhook.HeaderID), Body: body}
    if err := EmailEvent.Publish(r.Context(), queue.Database(h.db), in); err != nil {
        http.Error(w, "try again", http.StatusServiceUnavailable) // 对方会重发
        return
    }
    w.WriteHeader(http.StatusNoContent)
}
```

- 签名针对原始请求体计算，必须先读出原始字节再校验，校验之后再解析 JSON。
- 对方会重发，同一个事件可能收到多次。任务处理前按 `webhook-id` 去重，例如插入一张以它为主键的表，冲突时跳过。
- 处理器不做业务逻辑，尽快返回，对方等待超时也会重发。
- webhook 路由挂在页面的会话和跨站请求防护之外，与[接收 Telegram 的消息](notifications.md#telegram)相同。

不按这个规范签名的服务，用它们自己 SDK 中的校验函数，接收的流程不变：Stripe 是 `stripe-go` 的 `webhook.ConstructEvent`（包名与本包相同，导入时起个别名），GitHub 是 `go-github` 的 `github.ValidatePayloadFromBody`。

<a id="testing"></a>
## 测试

测试发送端时，用 `httptest.Server` 作为端点，用 `webhook.Verify` 检查收到的请求。测试接收端的处理器时，用 `webhook.Sign` 构造签名：

```go
body := `{"type":"email.delivered","timestamp":"2026-01-01T00:00:00Z","data":{}}`
now := time.Now()
sig, err := webhook.Sign(secret, "msg_1", now, []byte(body))
if err != nil {
    t.Fatal(err)
}
req := httptest.NewRequest(http.MethodPost, "/hooks/resend", strings.NewReader(body))
req.Header.Set(webhook.HeaderID, "msg_1")
req.Header.Set(webhook.HeaderTimestamp, strconv.FormatInt(now.Unix(), 10))
req.Header.Set(webhook.HeaderSignature, sig)
```

<a id="retention"></a>
## 保留期限

`webhook.Cleanup` 返回删除保留期以前的投递记录的清理函数，交给[定时任务](scheduling.md#framework-cleanups)：

```go
sched.Add("webhooks:cleanup", "0 4 * * *", schedule.Cleanup(webhook.Cleanup(a.db, 30*24*time.Hour), 1000))
```
