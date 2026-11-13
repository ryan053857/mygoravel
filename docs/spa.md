# 单页应用

- [简介](#introduction)
- [嵌入构建产物](#embedding)
- [挂载](#mounting)
- [缓存与前端路由](#caching-and-routing)
- [开发](#development)
- [构建与发布](#building)

<a id="introduction"></a>
## 简介

用 React、Vue 等写的单页应用，可以和 API 打进同一个二进制：Vite 的构建产物通过 `embed` 嵌入程序，由 `httpx.SPA` 提供。页面和 API 同源，不需要配置 CORS，部署时仍然只有一个二进制文件。

单页应用调用 [JSON API](routing.md)，请求带 Bearer token（access token 只在内存中，refresh token 在 HttpOnly cookie 中，见[单页应用的登录](api-tokens.md#single-page-applications)），类型由 OpenAPI 文档生成，见[客户端代码](clients.md)。后台和用户端都是单页应用；不需要登录的公开页面可以[在服务端渲染](views.md)，两者共用一个 Router。

本章给博客加上读者使用的前端：React 加 TanStack Router，放在应用根目录的 `frontend` 中，是一个普通的 Vite 项目。

<a id="embedding"></a>
## 嵌入构建产物

`npm run build` 把前端构建到 `frontend/dist`。在 `frontend` 目录中加一个 Go 文件，把它嵌入程序，下面是 `frontend/frontend.go`：

```go
package frontend

import (
    "embed"
    "io/fs"
)

//go:embed dist
var dist embed.FS

// Dist 是 Vite 的构建产物，包含 index.html 和 assets 目录。
var Dist, _ = fs.Sub(dist, "dist")
```

`dist` 不提交到版本库。没有构建前端时编译会失败（`pattern dist: no matching files found`），所以不会发布一个缺少页面的二进制，见[构建与发布](#building)。

<a id="mounting"></a>
## 挂载

在 `HTTP` 方法中创建 `SPA`，挂载到根路径。API 和其他路由照常注册，chi 优先匹配更具体的路径：

```go
spa, err := httpx.NewSPA(frontend.Dist, httpx.SPAOptions{DevServer: a.cfg.FrontendDevServer})
if err != nil {
    return err
}

router.Route("/api/v1", func(r chi.Router) {
    opts.Router, opts.BasePath = r, "/api/v1"
    // 注册接口，见路由一章
})
router.Handle("/realtime", rt.Handler())
router.Handle("/*", spa)
```

`NewSPA` 在创建时读取全部文件，没有 `index.html` 时返回错误。`DevServer` 见[开发](#development)。[routes 命令](commands.md#routes-command)把 `/*` 列为公开路由：页面本身不需要登录，前端打开后调用 API，API 收到 `401` 时前端跳转到登录页。

API 要放在子路由下。`rest` 给子路由设置了 JSON 格式的 `404`，写错的接口地址得到 `route not found`，不会落到前端。

前端不放在根路径时，把 Vite 的 `base` 和 `SPAOptions.Base` 设为同一个路径：

```go
spa, err := httpx.NewSPA(frontend.Dist, httpx.SPAOptions{Base: "/app/"})
router.Handle("/app/*", spa)
```

<a id="caching-and-routing"></a>
## 缓存与前端路由

| 请求的地址 | 响应 |
| --- | --- |
| `assets/` 下的文件，例如 `/assets/index-GpEN3EET.js` | Vite 生成的文件名带内容哈希，`Cache-Control: public, max-age=31536000, immutable`，永久缓存 |
| `/`、`/index.html`，以及 `public` 目录中的文件，例如 `/favicon.svg` | `Cache-Control: no-cache` 和 `ETag`，浏览器每次确认，没有变化时返回 `304` |
| 不存在的地址，浏览器打开页面（`Accept` 含 `text/html`） | 前端路由，例如 `/articles/42`：返回 `index.html`，由 TanStack Router 渲染 |
| 不存在的地址，其他请求 | `404`。缺失的脚本、图片和写错地址的 `fetch` 不会收到 HTML |
| `assets/` 下不存在的文件 | `404` |

HTML 文件带有 `Content-Security-Policy: frame-ancestors 'none'`，其他网站不能把应用嵌入框架、诱导用户点击。需要被嵌入时，在 `SPAOptions.FrameAncestors` 中列出允许的来源，例如 `[]string{"https://partner.example.com"}`。

`index.html` 不缓存，发布新版本后，用户下次打开页面就会加载新的脚本。已经打开的页面仍在运行旧版本，它的脚本在 `assets/` 中按需加载的分块，在新版本中已经不存在了，会得到 `404`。TanStack Router 按路由懒加载时，可以在路由的 `errorComponent` 中提示刷新页面。

<a id="development"></a>
## 开发

开发时前端由 Vite 的开发服务器提供，修改代码后浏览器立即更新。把 `SPAOptions.DevServer` 设为它的地址，`SPA` 就把收到的请求全部转发给它，包括热更新的 WebSocket。浏览器仍然打开应用的地址，页面和 API 与生产环境一样同源，Vite 不需要配置代理。

`DevServer` 取自应用配置。在 `settings.Config` 中加一个字段：

```go
type Config struct {
    // ……
    FrontendDevServer string `yaml:"frontend_dev_server"` // 非空时把前端请求转发给 Vite，只在开发时设置
}
```

只在本地的配置文件中设置它：

```yaml
# config/local.yaml
frontend_dev_server: http://localhost:5173
```

然后分别启动两者，打开 http://localhost:8080：

```bash
npm --prefix frontend run dev
go run ./cmd/api serve --config config/app.yaml,config/local.yaml
```

设置了 `DevServer` 时，`NewSPA` 不读取构建产物，但 `dist` 仍然要存在，程序才能编译：第一次开发前运行一次 `npm --prefix frontend run build`。Vite 没有启动时，页面返回 `502`，内容是连接错误。

<a id="building"></a>
## 构建与发布

先构建前端，再构建 Go：

```bash
npm --prefix frontend ci
npm --prefix frontend run build
go build -trimpath -o bin/api ./cmd/api
```

[Docker 镜像](deployment.md#docker)中加一个 Node 阶段，把它的产物复制进 Go 的构建阶段：

```dockerfile
FROM node:22 AS frontend
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend .
RUN npm run build

FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend /src/frontend/dist frontend/dist
RUN CGO_ENABLED=0 go build -trimpath -o /out/api ./cmd/api && mkdir /out/storage
```

后面的运行阶段不变。只运行 Go 测试和 lint 的 CI 任务不必构建前端，放一个空页面让程序能编译即可：

```bash
mkdir -p frontend/dist && touch frontend/dist/index.html
```
