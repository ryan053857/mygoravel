# 客户端代码

- [简介](#introduction)
- [生成的名称](#generated-names)
- [TypeScript](#typescript)
    - [生成类型](#generating-types)
    - [创建客户端](#creating-the-client)
    - [调用接口](#calling-endpoints)
    - [事件流](#event-streams)
    - [上传文件](#uploading-files)
- [其他语言](#other-languages)

<a id="introduction"></a>
## 简介

前端和 App 调用 JSON API 时，请求和响应的类型不需要手写。[openapi 命令](commands.md#openapi-command)导出的 OpenAPI 文档描述了每个接口的路径、参数、请求体、响应和错误，各语言的生成器据此生成类型或客户端代码。mygoravel 不自带生成器，也不发布 SDK：文档和服务端出自同一份路由声明，任何支持 OpenAPI 的生成器都可以使用。

```bash
./bin/api openapi > openapi.json
```

把 `openapi.json` 提交到仓库，接口的每次变化都会出现在代码评审中。

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
