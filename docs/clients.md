# 客户端代码

- [简介](#introduction)
- [生成的名称](#generated-names)
- [TypeScript](#typescript)
    - [生成客户端](#generating-types)
    - [创建客户端](#creating-the-client)
    - [调用接口](#calling-endpoints)
    - [错误](#ts-errors)
    - [事件流](#event-streams)
    - [上传文件](#uploading-files)
    - [下载文件](#downloading-files)
    - [TanStack Query](#tanstack)
    - [其他工具](#other-ts-tools)
- [Go 客户端](#go)
    - [生成客户端](#generating-go-client)
    - [调用接口](#calling-go-client)
    - [错误](#go-client-errors)
    - [认证](#go-client-auth)
    - [事件流](#go-client-events)
    - [上传文件](#go-client-uploads)
    - [复制的类型](#copied-types)
- [其他语言](#other-languages)
- [App 版本](#app-version)
- [检查接口变更](#checking-changes)

<a id="introduction"></a>
## 简介

前端和 App 调用 JSON API 时，请求和响应的类型不需要手写。[openapi 命令](commands.md#openapi-command)导出的 OpenAPI 文档描述了每个接口的路径、参数、请求体、响应和错误，各语言的生成器据此生成类型或客户端代码。文档和服务端出自同一份路由声明，任何支持 OpenAPI 的生成器都可以使用。TypeScript 和 Go 的调用方用 `client` 命令直接从路由生成客户端：[TypeScript](#typescript) 的类型取自同一份 OpenAPI 文档，[Go](#go) 的类型从源码复制。

```bash
./bin/api openapi > openapi.json
```

把 `openapi.json` 提交到仓库，接口的每次变化都会出现在代码评审中，CI 还可以[检查不兼容的变更](#checking-changes)。

<a id="generated-names"></a>
## 生成的名称

生成器用 OpenAPI 中的名称命名生成的代码，这些名称来自 Go 代码：

| OpenAPI | 来源 | 例子 |
| --- | --- | --- |
| operationId | 路由的 `OperationID`，必须是 kebab-case | `article-publish`，生成函数 `articlePublish` |
| schema 名 | 包名加类型名，类型名已以包名开头时不重复 | `article.Changes` 为 `ArticleChanges`，`article.ArticleDTO` 为 `ArticleDTO` |
| 错误响应 | 所有接口共用 | `ErrorModel` |

规则见[共享响应结构](routing.md#shared-responses)。改名会改变生成的代码，和修改接口一样需要通知调用方。

<a id="typescript"></a>
## TypeScript

<a id="generating-types"></a>
### 生成客户端

`client` 命令的 `--ts` 从路由生成 TypeScript 客户端，与 [Go 客户端](#generating-go-client)是同一个命令，在服务端的源码目录中运行：

```bash
go run ./cmd/api client --ts frontend/src/api
```

| 文件 | 内容 |
| --- | --- |
| `api.ts` | OpenAPI 中的全部类型、每个接口的输入类型、事件流的事件类型，以及 `createClient` |
| `errors.ts` | 服务端用 [`errorx.Define`](errors.md#defining-errors) 定义的错误码，按包分组，例如 `ArticleErrors.SlugTaken` |
| `runtime.ts` | 发送请求、刷新 access token、`ApiError`、事件流和下载 |

生成的代码不依赖任何 npm 包，也不需要 Node 或 Bun 参与生成。重新生成时只删除带生成标记的文件，目录中手写的文件保持不变。把它们提交到仓库，CI 中重新生成后用 `git diff --exit-code` 检查是否忘了更新。

开发时交给 [dev 命令](commands.md#dev-command)，改了 Go 接口，前端的类型随之更新：

```go
devcmd.New(devcmd.Options{
    Main:     "./cmd/api",
    Tools:    []devcmd.Tool{devcmd.Vite("frontend")},
    Generate: []string{"client", "--ts", "frontend/src/api"},
}, httpAddr)
```

<a id="creating-the-client"></a>
### 创建客户端

博客的单页应用用 [JWT 模式](api-tokens.md#jwt)登录，refresh token 保存在 [HttpOnly cookie](api-tokens.md#single-page-applications) 中。access token 只放在内存里，由客户端保管：请求自动带上它；受保护的接口返回 `401` 时调用一次 `auth.refresh` 换新的，再重试请求。下面是 `frontend/src/client.ts`：

```ts
import { createClient } from "./api/api"
import { router } from "./router"

// 刷新接口靠 cookie 认证，不带 access token。单页应用与 API 在不同的子域名时需要 credentials: "include"。
const plain = createClient({ baseURL: "/api", credentials: "include" })

export const api = createClient({
  baseURL: "/api",
  credentials: "include",
  auth: {
    refresh: async () => (await plain.auth.refresh({})).access_token,
    onSignedOut: () => router.navigate({ to: "/login" }),
  },
  // 403 auth.confirm_required（rest.Confirmed）：弹出密码框，确认成功后重试原请求
  onConfirmRequired: () => confirmDialog.open(),
})
```

- 打开页面（包括刷新页面、新标签页）时先调用一次 `await api.refresh()`，返回 `true` 表示已经登录，决定显示登录页还是应用。
- 登录接口返回后调用 `api.setAccessToken(out.access_token)`；登出调用 `DELETE /auth/session`，服务端撤销这次登录并清除 cookie，再调用 `api.setAccessToken("")`。
- 几个请求同时收到 `401` 时，只调用一次 `refresh`，它们都等这一次的结果。刷新失败（返回空值或抛出错误）时调用 `onSignedOut`，原来的 `401` 抛给调用方。
- 公开的接口（[`rest.Public`](routing.md#access-policies)）从不带 token，`401` 时也不刷新。
- 请求带上 `Accept-Language`（默认是浏览器的语言，`language` 可以覆盖），错误的 `detail` 按它翻译。App 的 WebView 设置 `appVersion`，作为 [`X-App-Version`](#app-version) 发送。

`/auth/login`、`/auth/refresh` 是博客自己注册的公开接口，写法见[单页应用](api-tokens.md#single-page-applications)。App、桌面端（Electron、Tauri）和小程序不用 cookie：登录时不传 `cookie`，refresh token 在响应体中，保存在系统的钥匙串中，`refresh` 把它放进请求体。

<a id="calling-endpoints"></a>
### 调用接口

接口按 operationId 分组，与 Go 客户端的规则相同：`article-publish` 生成 `api.article.publish`，只有一段的 `ping` 生成 `api.ping`。

```ts
const article = await api.article.get(id) // ArticleDTO
const list = await api.article.list({ q: "go", page: 2 }) // 查询参数，类型是 ArticleListInput
await api.article.publish(id, { idempotencyKey: crypto.randomUUID() }) // 请求头
const created = await api.article.create({ title: "Hello", slug: "hello" })
created.body.id
created.headers.location // 有响应头的接口返回 { body, headers }
```

| 服务端 | 客户端 |
| --- | --- |
| 路径参数 | 按在路径中出现的顺序作为参数 |
| 只有请求体 | 请求体的类型作为最后一个参数 |
| 有查询参数或请求头 | 最后一个参数是 `<OperationId>Input`，查询参数用原名，请求头转为 camelCase，请求体在 `body` 中 |
| 响应体 | 返回它的类型；`204` 返回 `void` |
| 响应头 | 返回 `{ body, headers }` |

方法的注释来自路由的 `Summary` 和 `Description`，类型的注释来自字段的 `doc` 标签和 Go 注释，在编辑器中悬停即可看到。

<a id="ts-errors"></a>
### 错误

非 2xx 的响应抛出 `ApiError`：`status` 是状态码，`code` 是业务错误码，`detail` 是可以展示给用户的说明，`fields` 是 `422` 时每个字段的问题（[翻译后的提示和规则 `code`](validation.md#validation-error-response)），`requestId` 用来报告问题。业务错误码用 `errors.ts` 中的常量判断：

```ts
import { ArticleErrors, isApiError } from "./api/api"

try {
  await api.article.create(changes)
} catch (err) {
  if (isApiError(err, ArticleErrors.SlugTaken)) return form.setError("slug", err.detail)
  if (isApiError(err) && err.status === 422) {
    for (const f of err.fields) form.setError(f.location?.replace(/^body\./, "") ?? "", f.message)
    return
  }
  throw err
}
```

框架自带的错误码同样会生成，例如 `IdentityErrors.ConfirmRequired`、`RatelimitErrors.Exceeded`。

<a id="event-streams"></a>
### 事件流

[`rest.RegisterSSE`](sse.md) 注册的接口返回异步迭代器。事件是按 `event` 区分的联合类型，`data` 的类型随之确定：

```ts
for await (const ev of api.article.publishBatch({ ids })) {
  if (ev.event === "progress") showProgress(ev.data) // ev.data: PublishProgress
}
```

迭代器建立在 `fetch` 上，所以能带 Bearer token，`401` 时同样刷新。推送开始之前的错误照常抛出 `ApiError`。服务端新增的、客户端还不认识的事件被跳过。最后一个参数可以传 `signal` 停止接收，以及 `lastEventId` 接着上次的位置继续：

```ts
const stop = new AbortController()
for await (const ev of api.article.events({}, { signal: stop.signal, lastEventId })) {
  lastEventId = ev.id ?? lastEventId
}
```

<a id="uploading-files"></a>
### 上传文件

[服务端上传接口](filesystem.md#rest-upload)的表单字段生成为 `<OperationId>Form`，文件字段的类型是 `Blob`，`File` 可以直接传入：

```ts
const uploaded = await api.file.upload({ file: input.files[0] })
```

<a id="downloading-files"></a>
### 下载文件

[文件存储](filesystem.md)中的文件有公开地址，`<a href>`、`<img src>` 直接使用。返回 `huma.StreamResponse` 的接口生成为下载，例如[导入失败的行](import-export.md)，返回 `{ blob, filename }`，`saveBlob` 让浏览器保存它：

```ts
import { saveBlob } from "./api/api"

const { blob, filename } = await api.article.failures(id, { format: "xlsx" })
saveBlob(blob, filename || "failures.xlsx")
```

<a id="tanstack"></a>
### TanStack Query

客户端的方法直接作为 `queryFn`、`mutationFn`，错误抛出 `ApiError`，TanStack Query 照常处理。修改数据后按前缀让相关的查询失效：

```tsx
function Article({ id }: { id: string }) {
  const { data, error } = useQuery({ queryKey: ["article", id], queryFn: () => api.article.get(id) })
  const publish = useMutation({
    mutationFn: () => api.article.publish(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["article"] }), // 文章列表和所有文章详情
  })
  // ……
}
```

[实时频道](realtime.md#subscribing)收到消息后，让对应的查询失效，由 TanStack Query 重新获取，不要把消息内容合并进缓存：

```ts
articles.on("publication", () => queryClient.invalidateQueries({ queryKey: ["article"] }))
```

[固定分页](responses.md#offset-pagination)的列表由服务端分页。TanStack Table 的 `pageIndex` 从 0 开始，`page` 从 1 开始；把 `items` 和 `total` 交给表格，并开启 `manualPagination`：

```ts
const { data } = useQuery({
  queryKey: ["article", "list", pagination],
  queryFn: () => api.article.list({ page: pagination.pageIndex + 1, per_page: pagination.pageSize }),
  placeholderData: keepPreviousData, // 翻页时保留上一页，表格不闪烁
})
// 表格：data = data?.items ?? []，rowCount = data?.total，manualPagination = true
```

列表的查询参数放进路由的 search params 时，用生成的 `ArticleListInput` 作为 `validateSearch` 的类型，地址和接口的参数是同一个类型。

<a id="other-ts-tools"></a>
### 其他工具

`openapi.json` 仍然是公开的契约。已经在用 [openapi-typescript](https://openapi-ts.dev) 加 openapi-fetch、或者其他基于 OpenAPI 的工具时，照常从它生成：

```bash
bunx openapi-typescript openapi.json -o src/api.d.ts --root-types --root-types-no-schema-prefix --root-types-keep-casing
```

<a id="go"></a>
## Go 客户端

用 Go 写的调用方，例如 [mygo](https://github.com/egoist/mygo) 写的桌面和移动 App、其他 Go 服务、命令行工具和端到端测试，使用 `clientgen` 直接从路由声明生成的客户端：

```go
a, err := c.Article.Update(ctx, id, article.Changes{Title: "Hello", Slug: "hello"})
if errors.Is(err, article.ErrSlugTaken) {
    // ...
}
```

接口还是同一套 JSON API，Go 客户端只是又一个调用方。和 OpenAPI 生成的代码相比，类型与服务端同名、同包（`article.Changes` 而不是 `ArticleChanges`），业务错误码是可以 `errors.Is` 的常量。

<a id="generating-go-client"></a>
### 生成客户端

把 `client` 命令加入命令行，参数和 [openapi 命令](commands.md#openapi-command)相同：

```go
import "github.com/go-tenon/tenon/httpx/rest/clientgen"

root.AddCommand(clientgen.Command(bootstrap.API, bootstrap.DocRoutes))
```

在服务端的源码目录中运行，`--out` 是输出目录：

```bash
go run ./cmd/api client --out ../api
```

类型声明和错误码从源码复制，所以命令要在源码目录中运行；它以文档模式注册路由，不读取配置，也不连接数据库。输出目录通常是 [go.work](https://go.dev/ref/mod#workspaces) 中的另一个模块，调用方只依赖它，不会把服务端的依赖带进 App：

```text
blog/
├── go.work
├── server/          服务端
├── api/             生成的客户端，go.mod 只需要 github.com/google/uuid 这类小库
│   ├── client.go    Client 和 Options
│   ├── article.go   Client.Article 的方法
│   ├── article/     服务端 internal/article 中接口用到的类型和错误码
│   ├── page/        框架的分页类型
│   └── wire/        发送请求、刷新凭据、解码错误的运行时
└── app/             调用方，例如 mygo App
```

目录名就是根包的包名。重新生成时，不再生成的文件会被删除，你自己放进去的文件保持不变。把生成的代码提交到仓库，CI 中重新生成后用 `git diff --exit-code` 检查是否忘了更新，与 [openapi.json](#checking-changes) 的做法相同。

返回原始字节或非 SSE 流的接口、`RawBody` 不是 `huma.MultipartFormFiles` 的接口不生成，命令把它们列在标准错误中。

<a id="calling-go-client"></a>
### 调用接口

```go
c := api.New(api.Options{
    BaseURL:    "https://blog.example.com",
    Auth:       tokens,                           // 见下文“认证”
    AppVersion: "macos/1.4.2",                    // 见中间件的“App 最低版本”
    Language:   func() string { return "zh-CN" }, // 错误的 Detail 按它翻译
})

created, err := c.Article.Create(ctx, article.Changes{Title: "Hello", Slug: "hello"})
list, err := c.Article.List(ctx, article.ListInput{Status: article.StatusPublished})
for _, a := range list.Items { // page.Offset[article.ArticleDTO]
    // ...
}
```

方法按以下规则生成：

| 服务端 | 客户端 |
| --- | --- |
| operationId `article-publish` | `c.Article.Publish`；只有一段的 `ping` 是 `c.Ping` |
| 路径参数 | 按在路径中出现的顺序作为参数 |
| 只有 `Body` | `Body` 的类型作为最后一个参数 |
| 有查询参数或请求头 | 输入类型去掉路径参数后作为最后一个参数 |
| 输出只有 `Body` | 返回 `Body` 的类型 |
| 输出有响应头或 `Status` | 返回输出类型，例如 `created.Location`、`created.Body` |

`Body` 是匿名结构体时，生成的类型以输入或输出类型的名字加 `Body` 命名，例如 `RefreshInput` 的请求体是 `RefreshInputBody`。查询参数和请求头为零值时不发送，服务端使用默认值，所以 `article.ListInput{}` 取第一页、每页 20 条。

<a id="go-client-errors"></a>
### 错误

非 2xx 的响应返回 `*wire.Error`：`Status` 是状态码，`Code` 是业务错误码，`Detail` 是可以展示给用户的说明，`Fields` 是 `422` 时每个字段的问题（[翻译后的提示和规则 `code`](validation.md#validation-error-response)），`RequestID` 用来报告问题。

服务端用 [`errorx.Define`](errors.md#defining-errors) 定义的错误码，在对应的包中生成为同名常量，用 `errors.Is` 判断：

```go
switch {
case errors.Is(err, article.ErrSlugTaken):
    form.SetError("slug", "已被使用")
case errors.Is(err, identity.ErrUnauthenticated):
    showLogin()
case errors.Is(err, wire.ErrUpgradeRequired):
    showUpgrade()
}
```

框架自带的错误码同样会生成，例如 `identity.ErrUnauthenticated`、`ratelimit.ErrExceeded`、`page.ErrInvalidCursor`。

<a id="go-client-auth"></a>
### 认证

`Options.Auth` 提供受保护接口的 Bearer token；公开的接口从不带 token。不透明的 [API token](api-tokens.md) 用 `wire.StaticToken(token)`。[JWT 模式](api-tokens.md#jwt)实现 `wire.Auth` 的两个方法，下面是[读者 App](account-examples.md) 的实现：

```go
type tokens struct {
    mu      sync.Mutex
    access  string
    refresh string
    public  *api.Client // 不带 Auth，调用公开的刷新接口
}

func (t *tokens) Token(context.Context) (string, error) {
    t.mu.Lock()
    defer t.mu.Unlock()
    return t.access, nil
}

func (t *tokens) Refresh(ctx context.Context) error {
    t.mu.Lock()
    refresh := t.refresh
    t.mu.Unlock()
    s, err := t.public.Reader.Refresh(ctx, readerapi.RefreshInputBody{RefreshToken: refresh})
    if err != nil {
        return err
    }
    t.mu.Lock()
    defer t.mu.Unlock()
    t.access = s.AccessToken
    if s.RefreshToken != "" {
        t.refresh = s.RefreshToken
    }
    return nil // 同时保存到系统的钥匙串，例如 mygo 的 Secrets
}
```

受保护的接口返回 `401` 时，客户端调用一次 `Refresh`，再用新 token 重试一次。几个请求同时收到 `401` 时只刷新一次；`Refresh` 返回错误时，调用方收到原来的 `401`，这时让用户重新登录。

<a id="go-client-events"></a>
### 事件流

用 [`rest.RegisterSSE`](sse.md) 登记的接口返回事件的迭代器。每个接口生成一个事件结构体，每种事件一个指针字段，一次只有一个不为 `nil`；默认的 `message` 事件是 `Message` 字段：

```go
for ev, err := range c.Article.Events(ctx, article.EventsInput{}) {
    if err != nil {
        return err // 建立连接前的错误（例如 401）也从这里返回
    }
    switch {
    case ev.Changed != nil:
        refresh(*ev.Changed)
    case ev.Deleted != nil:
        remove(*ev.Deleted)
    }
}
```

服务端结束响应、收到 `data: [DONE]`、`ctx` 取消或者你跳出循环时，迭代结束。服务端新增的、客户端还不认识的事件被跳过，旧版本的 App 不受影响。连接中断时不会自动重连：需要接着上次的位置继续时，把最后一个事件的 `ID` 用 `wire.WithLastEventID` 放进 `ctx`，请求会带上 `Last-Event-ID`：

```go
events := c.Article.Events(wire.WithLastEventID(ctx, lastID), article.EventsInput{})
```

<a id="go-client-uploads"></a>
### 上传文件

`RawBody` 为 `huma.MultipartFormFiles[T]` 的接口（见[服务端上传接口](filesystem.md#rest-upload)），表单 `T` 作为最后一个参数；`huma.FormFile` 字段是 `wire.File`，其余字段原样保留。表单是匿名结构体时，类型以输入类型的名字加 `Form` 命名：

```go
f, err := os.Open("cover.png")
if err != nil {
    return err
}
defer f.Close()
uploaded, err := c.File.Upload(ctx, file.UploadInputForm{
    File: wire.File{Name: "cover.png", ContentType: "image/png", Body: f},
})
```

请求体边读边发送，不会把文件整个读进内存。`Body` 为 `nil` 的文件不发送。受保护的接口返回 `401` 时，文件内容实现了 `io.Seeker`（例如 `*os.File`、`bytes.Reader`）就在刷新凭据后从头重传；否则只刷新凭据，调用方收到 `401`，再调用一次即可。

<a id="copied-types"></a>
### 复制的类型

接口用到的类型复制到客户端：字段、tag、注释、泛型参数和同一个包中这个类型的常量都会保留，方法不复制，`json:"-"` 和未导出的字段去掉。只有应用模块和 Tenon 的类型会复制；标准库和 `github.com/google/uuid` 的类型直接导入。

| 情况 | 结果 |
| --- | --- |
| 类型实现了 `MarshalJSON`、`UnmarshalText` 等自定义编码 | 生成失败：复制字段得不到相同的 JSON。把它放进客户端可以导入的包，或者改用普通字段 |
| 用到了其他第三方包的类型 | 生成失败。确认那个包适合在客户端使用后，用 `--import 包路径` 允许导入，子包一并允许 |
| 两个包同名，例如应用的 `settings` 和框架的 `settings` | 生成失败。用 `--rename 包路径=新名字` 给其中一个改名 |

<a id="other-languages"></a>
## 其他语言

各语言的生成器对 OpenAPI 3.1 的支持不一。生成器报错或丢失字段时，改用 `openapi --spec 3.0` 导出的文档：

```bash
./bin/api openapi --spec 3.0 > openapi-3.0.json
```

| 调用方 | 生成器 |
| --- | --- |
| Flutter | [openapi-generator](https://openapi-generator.tech/docs/generators/dart-dio) 的 `dart-dio`，或 [swagger_parser](https://pub.dev/packages/swagger_parser) |
| iOS | [swift-openapi-generator](https://github.com/apple/swift-openapi-generator) |
| Android | openapi-generator 的 `kotlin`，或 [Kiota](https://learn.microsoft.com/openapi/kiota/) |
| Python 脚本 | [openapi-python-client](https://github.com/openapi-generators/openapi-python-client) |

生成的客户端同样需要处理认证：请求带 `Authorization: Bearer`，JWT 模式按[客户端的规则](api-tokens.md#jwt)刷新，错误按 `code` 区分。

<a id="app-version"></a>
## App 版本

安装在手机和电脑上的 App，每个请求都带上平台和版本，服务端才能在需要时[停止支持旧版本](middleware.md#app-version)：

```http
X-App-Version: android/2.3.1
```

从第一个发布的版本就开始发送。没有带它的版本，服务端无法拦截，只能等它们自然消失。平台名自己决定，常用 `ios`、`android`、`macos`、`windows`、`linux`；版本就是商店中显示的版本号，例如 `2.3.1`。浏览器中的单页应用总是最新的，不用发送。

收到 `426`、`code` 为 `app.upgrade_required` 时，App 停在升级页面，打开应用商店或开始自动更新，不再重试请求。`detail` 是可以展示给用户的提示。

<a id="checking-changes"></a>
## 检查接口变更

已经上线的接口，删掉响应中的字段、新增必填参数，都会让旧版本的 App 出错。在 PR 的 CI 中用 [oasdiff](https://github.com/oasdiff/oasdiff) 把导出的文档与主分支上的比较，发现不兼容的变更时失败：

```bash
go install github.com/oasdiff/oasdiff@v1.37.0

./bin/api openapi > openapi.json
git diff --exit-code openapi.json   # 改了接口，却没有重新导出、提交

git show origin/main:openapi.json > base.json
# 接口：旧文档在前，新文档在后
oasdiff breaking <(jq 'del(.webhooks)' base.json) <(jq 'del(.webhooks)' openapi.json) --fail-on ERR
# webhook：新旧交换位置，--unmatch-path 排除全部接口
oasdiff breaking openapi.json base.json --unmatch-path '.*' --fail-on ERR
```

[webhook](webhooks.md#documenting) 要单独比较。oasdiff 把 webhook 当作别人发给你的请求，而实际上你是发送方，判断方向正好相反：给事件内容加一个字段，它报不兼容；删掉一个字段，它只给出警告。交换新旧文档的位置，结论就对了。

确实要做不兼容的变更时，例如接口还没有调用方，把 oasdiff 输出中的那一行写进一个文件，用 `--err-ignore` 传入：

```text
PUT /articles/{id} added the new required request property `slug`
```

oasdiff 只比较请求和响应的结构。schema 改名不改变结构，它不会报告，但生成的类型名会变，前端重新生成类型后由编译器发现。
