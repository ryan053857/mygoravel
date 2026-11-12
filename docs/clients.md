# 客户端代码

- [简介](#introduction)
- [生成的名称](#generated-names)
- [TypeScript](#typescript)
    - [生成类型](#generating-types)
    - [创建客户端](#creating-the-client)
    - [调用接口](#calling-endpoints)
    - [事件流](#event-streams)
    - [上传文件](#uploading-files)
    - [TanStack Query](#tanstack)
- [其他语言](#other-languages)
- [App 版本](#app-version)
- [检查接口变更](#checking-changes)

<a id="introduction"></a>
## 简介

前端和 App 调用 JSON API 时，请求和响应的类型不需要手写。[openapi 命令](commands.md#openapi-command)导出的 OpenAPI 文档描述了每个接口的路径、参数、请求体、响应和错误，各语言的生成器据此生成类型或客户端代码。mygoravel 不自带生成器，也不发布 SDK：文档和服务端出自同一份路由声明，任何支持 OpenAPI 的生成器都可以使用。

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

推荐 [openapi-typescript](https://openapi-ts.dev/) 加 [openapi-fetch](https://openapi-ts.dev/openapi-fetch/)：前者只生成类型，后者是几 KB 的 `fetch` 封装，按路径调用接口，参数、请求体、响应和错误都有类型。重新生成时只有类型文件变化，没有生成的函数需要维护。

<a id="generating-types"></a>
### 生成类型

```bash
npm i openapi-fetch
npm i -D openapi-typescript typescript
npx openapi-typescript openapi.json -o src/api.d.ts \
    --root-types --root-types-no-schema-prefix --root-types-keep-casing
```

后三个选项把 schema 导出为同名类型，可以直接 `import type { ArticleDTO } from "./api"`。CI 中加上 `--check`，`api.d.ts` 与 `openapi.json` 不一致时失败。

<a id="creating-the-client"></a>
### 创建客户端

博客的用户端用 [JWT 模式](api-tokens.md#jwt)登录：请求带 access token，收到 `401` 时用 refresh token 换一对新的，再重试请求。这个逻辑放在传给 openapi-fetch 的 `fetch` 中，所有接口都会经过它：

```ts
import createClient from "openapi-fetch"
import type { paths } from "./api"

const token = (name: string) => localStorage.getItem(name) ?? ""

export function saveTokens(access: string, refresh?: string) {
  localStorage.setItem("access_token", access)
  if (refresh) localStorage.setItem("refresh_token", refresh) // 为空时继续使用已保存的
}

const plain = createClient<paths>({ baseUrl: "/api" })
let refreshing: Promise<boolean> | null = null

async function refresh(): Promise<boolean> {
  const { data } = await plain.POST("/auth/refresh", { body: { refresh_token: token("refresh_token") } })
  if (!data) return false // 刷新失败：让用户重新登录
  saveTokens(data.access_token, data.refresh_token)
  return true
}

async function authFetch(request: Request): Promise<Response> {
  const retry = request.clone()
  const res = await fetch(withToken(request))
  if (res.status !== 401 || !token("refresh_token")) return res
  refreshing ??= refresh().finally(() => (refreshing = null))
  return (await refreshing) ? fetch(withToken(retry)) : res
}

function withToken(request: Request): Request {
  request.headers.set("Authorization", `Bearer ${token("access_token")}`)
  return request
}

export const api = createClient<paths>({ baseUrl: "/api", fetch: authFetch })
```

`/auth/refresh` 是博客自己注册的公开接口，调用 `JWT.Refresh`。几个请求同时收到 `401` 时，`refreshing` 让它们等待同一次刷新。token 每次使用时从 `localStorage` 读取，不要缓存在变量中：refresh token 每次刷新都会更换，另一个标签页如果还拿着换掉的那个，过了[宽限期](api-tokens.md#jwt)再用它刷新，服务端会当作被盗，撤销这次登录。

不透明 token 没有刷新：去掉 `refresh`，`authFetch` 只设置请求头，收到 `401` 时跳转登录。

<a id="calling-endpoints"></a>
### 调用接口

```ts
import { api } from "./client"

const { data, error } = await api.POST("/articles/{id}/publish", {
  params: { path: { id } },
})
if (error) {
  if (error.code === "article.forbidden") return showReadOnly()
  return toast(error.detail ?? "操作失败")
}
render(data) // ArticleDTO
```

路径参数写错、缺少必填字段，`tsc` 都会报错。`error` 是 `ErrorModel`：[业务错误](errors.md)带 `code`，按它决定怎么处理；`detail` 是可以展示给用户的文案；输入格式错误时没有 `code`，`errors` 中是每个字段的错误。

<a id="event-streams"></a>
### 事件流

[`rest.RegisterSSE`](sse.md#json-api) 注册的接口在文档中列出了每种事件的数据类型。浏览器的 `EventSource` 不能带 `Authorization` 请求头，所以用 `parseAs: "stream"` 取得响应流，交给 [eventsource-parser](https://github.com/rexxars/eventsource-parser) 解析：

```ts
import { EventSourceParserStream } from "eventsource-parser/stream"
import type { ArticlePublishProgress } from "./api"

const { response, error } = await api.POST("/articles/publish", {
  body: { ids },
  parseAs: "stream",
})
if (error || !response.body) return

const events = response.body
  .pipeThrough(new TextDecoderStream())
  .pipeThrough(new EventSourceParserStream())
  .getReader()
for (;;) {
  const { done, value } = await events.read()
  if (done) break
  if (value.event === "progress") {
    const p: ArticlePublishProgress = JSON.parse(value.data)
    showProgress(p)
  }
}
```

推送开始之前的错误照常是 `ErrorModel`，在 `error` 中。

<a id="uploading-files"></a>
### 上传文件

[服务端上传接口](filesystem.md#rest-upload)接收 `multipart/form-data`。直接传 `FormData`，openapi-fetch 会原样发送：

```ts
const form = new FormData()
form.append("file", file)
const { data } = await api.POST("/files", {
  body: form as unknown as { file: string }, // 生成的类型把文件写作 string
})
```

<a id="tanstack"></a>
### TanStack Query

React 应用用 [TanStack Query](https://tanstack.com/query) 管理接口数据时，用 [openapi-react-query](https://openapi-ts.dev/openapi-react-query/) 把上面的 `api` 包装成 hooks。它按方法、路径和参数生成 query key，类型照旧来自 `api.d.ts`：

```bash
npm i @tanstack/react-query openapi-react-query
```

```ts
import { QueryClient } from "@tanstack/react-query"
import createQueryClient from "openapi-react-query"
import { api } from "./client"

export const $api = createQueryClient(api)
export const queryClient = new QueryClient()
```

query key 是 `[方法, 路径, 参数]`，例如 `["get", "/articles/{id}", { params: { path: { id } } }]`。修改数据后按前缀让相关的查询失效：

```tsx
function Article({ id }: { id: string }) {
  const { data, error } = $api.useQuery("get", "/articles/{id}", { params: { path: { id } } })
  const publish = $api.useMutation("post", "/articles/{id}/publish", {
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["get", "/articles"] })      // 文章列表
      queryClient.invalidateQueries({ queryKey: ["get", "/articles/{id}"] }) // 所有文章详情
    },
  })
  // ……
}
```

`error` 同样是 `ErrorModel`，按 `code` 处理。

[实时频道](realtime.md#subscribing)收到消息后，让对应的查询失效，由 TanStack Query 重新获取，不要把消息内容合并进缓存：

```ts
articles.on("publication", () => queryClient.invalidateQueries({ queryKey: ["get", "/articles"] }))
```

[固定分页](responses.md#offset-pagination)的列表由服务端分页。TanStack Table 的 `pageIndex` 从 0 开始，`page` 从 1 开始；把 `items` 和 `total` 交给表格，并开启 `manualPagination`：

```ts
const { data } = $api.useQuery(
  "get", "/articles",
  { params: { query: { page: pagination.pageIndex + 1, per_page: pagination.pageSize } } },
  { placeholderData: keepPreviousData }, // 翻页时保留上一页，表格不闪烁
)
// 表格：data = data?.items ?? []，rowCount = data?.total，manualPagination = true
```

[游标分页](responses.md#cursor-pagination)的列表用 `$api.useInfiniteQuery`：`pageParamName` 为 `"cursor"`，`initialPageParam` 为 `""`，`getNextPageParam` 返回上一页的 `next_cursor`。

[验证错误](validation.md#validation-error-response)的 `errors[].location` 去掉 `body.` 就是字段名。TanStack Form 在 `validators.onSubmitAsync` 中提交，把它们作为字段错误返回：

```ts
const { error } = await api.POST("/articles", { body: value })
if (error?.errors) {
  return { fields: Object.fromEntries(error.errors.map((e) => [e.location?.replace(/^body\./, ""), e.message])) }
}
```

`message` 是英文，中文界面按字段和约束自己写提示。

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
| 其他 Go 服务 | [oapi-codegen](https://github.com/oapi-codegen/oapi-codegen)，只支持 3.0 |
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
