# 开发工具

- [简介](#introduction)
- [安装](#installation)
- [tenon init](#init)
- [tenon dev](#dev)
- [tenon build](#build)
- [tenon generate](#generate)
- [tenon.json](#configuration)
- [应用这一侧](#app-side)

<a id="introduction"></a>
## 简介

`tenon` 是开发 Tenon 应用的命令行工具，作用类似 Laravel 的 Vite 插件加 Sail，或者 [mygo](https://github.com/egoist/mygo) 的 CLI：

| 命令 | 作用 |
| --- | --- |
| `tenon dev` | 一起运行 Vite 和应用。修改 Go 代码后重新编译、平滑重启，并重新生成客户端；修改前端代码后由 Vite 热更新 |
| `tenon build` | 先构建前端，再编译应用，前端的构建产物嵌入二进制 |
| `tenon generate` | 生成 TypeScript 和 Go 客户端 |

它只负责开发和构建。`serve`、`work`、`migrate`、`routes`、`openapi` 这些运行时的命令仍然在应用自己的二进制中，部署后照样可用，见[命令行](commands.md)。

`tenon` 读取项目根目录的 `tenon.json`。所有字段都有约定的默认值，按[目录结构](directory-structure.md)组织的项目可以没有这个文件。

<a id="installation"></a>
## 安装

用 [tenon init](#init) 生成的项目已经装好了。已有的项目把 `tenon` 加入 `go.mod` 的工具，版本随框架一起固定，不需要另外安装：

```bash
go get -tool github.com/go-tenon/tenon/cmd/tenon
```

然后用 `go tool tenon` 运行。前端的依赖和脚本用 [Bun](https://bun.sh) 管理，在项目根目录的 `package.json` 中写好脚本，开发者只需要 `bun run dev`：

```json
{
  "scripts": {
    "dev": "go tool tenon dev",
    "build": "go tool tenon build",
    "generate": "go tool tenon generate"
  }
}
```

`package.json`、`vite.config.ts` 放在项目根目录，Vite 的 `root` 指向前端目录：

```ts
import { defineConfig } from "vite"
import react from "@vitejs/plugin-react"

export default defineConfig({
  root: "frontend",
  plugins: [react()],
})
```

构建产物默认在 `frontend/dist`，由 `frontend/frontend.go` 嵌入程序，见[单页应用](spa.md#embedding)。

<a id="init"></a>
## tenon init

```bash
go run github.com/go-tenon/tenon/cmd/tenon@main init todo
```

在 `todo` 目录生成一个能直接运行的项目：Go 应用加 React 单页应用，带一个待办事项的示例资源，数据保存在内存中，不依赖任何外部服务。目录结构见[安装](installation.md#creating-a-project)。

生成后它运行 `go mod tidy`、生成 TypeScript 客户端，装了 Bun 时再运行 `bun install`。`--module` 是 Go 模块路径，`--name` 是应用的名称，默认都是目录名。目录必须不存在或为空。

`go.mod` 要求的框架版本就是运行 `init` 的这个版本：用 `@main` 运行时是 main 分支当前的提交，用 `@v1.2.0` 时是这个版本。开发框架本身时用 `--tenon <框架目录>`，`go.mod` 用 `replace` 指向本地的副本。

<a id="dev"></a>
## tenon dev

```bash
bun run dev   # 即 go tool tenon dev
```

它同时运行：

- **Vite**：在 `package.json` 所在的目录运行 `bun x vite`（不经过 `package.json` 的 `dev` 脚本，那个名字留给 `tenon dev` 自己）。启动前没有 `node_modules` 时先 `bun install`；没有 `dist` 时写一个占位的 `index.html`，`go:embed` 才能编译通过，开发时页面来自 Vite，用不到它。
- **应用**：等 Vite 能访问后，把 `main` 编译到临时目录，用 `serve` 中的参数运行。应用的 `httpx.SPA` 把页面请求转给 Vite（`config/local.yaml` 中的 `frontend_dev_server`），浏览器打开应用的地址，页面和 API 同源，热更新照常工作。
- **重新编译**：模块中的 `.go` 文件（测试除外）、`go.mod`、`go.sum` 变化后重新编译。成功时平滑停止旧进程（`SIGTERM`），启动新的，再运行一次 [`generate`](#generate)，前端的类型随接口一起更新；失败时打印编译错误，旧进程继续运行。跳过隐藏目录、`node_modules`、`vendor`、`testdata` 和 `dist`。
- **服务端渲染的页面**（模块中有 `.templ` 文件时）：`go tool templ generate --watch`，在 `templ.proxyPort`（默认 7331）代理应用并注入自动刷新的脚本，这时打开 http://localhost:7331。只改了文字时不必重新编译。`templ.static` 目录中的文件变化时也刷新浏览器。
- **Tailwind 和其他工具**：见 [tenon.json](#configuration)。

启动前检查用到的命令是否已安装：Go、Bun、templ（`go get -tool github.com/a-h/templ/cmd/templ`）、Tailwind。按 `Ctrl-C` 停止后，它平滑停止应用，再运行 `templ generate`、`vite build`、`tailwindcss --minify` 和各工具的 `build`，把监听模式的输出恢复为正式版本。

<a id="build"></a>
## tenon build

```bash
bun run build   # 即 go tool tenon build
```

有前端时先运行 `bun install --frozen-lockfile` 和 `bun x vite build`，再 `go build -trimpath -o bin/api ./cmd/api`。`-o` 指定输出的二进制，`GOOS`、`GOARCH` 等环境变量照常生效。`--frozen-lockfile` 要求 `bun.lock` 与 `package.json` 一致，CI 中安装的依赖与本地完全相同。Docker 镜像的写法见[构建与发布](spa.md#building)。

<a id="generate"></a>
## tenon generate

编译应用（没有 `dist` 时同样先写占位页），用它的 `client` 命令生成客户端，写到 `generate` 指定的目录：TypeScript 客户端见[客户端代码](clients.md#typescript)，Go 客户端见 [Go 客户端](clients.md#go)。`tenon dev` 每次重新编译成功后会自动运行它。把生成的代码提交到仓库，CI 中重新生成后用 `git diff --exit-code` 检查是否忘了更新。

<a id="configuration"></a>
## tenon.json

```json
{
  "$schema": "https://raw.githubusercontent.com/go-tenon/tenon/main/cmd/tenon/schema.json",
  "generate": { "go": "../api" }
}
```

`$schema` 让编辑器补全字段并显示说明。没有写的字段使用默认值：

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `main` | `./cmd/api` | 应用入口的包 |
| `serve` | `["serve", "--config", "config/app.yaml,config/local.yaml"]`，`local.yaml` 不存在时去掉它 | 开发时运行应用的参数 |
| `url` | `http://localhost:8080` | 应用的地址：`dev` 打印它，templ 的代理访问它 |
| `frontend.dir` | 有 `frontend/index.html` 时为 `frontend` | 前端源码的目录 |
| `frontend.devUrl` | `http://localhost:5173` | Vite 的地址，`dev` 等它能访问后再启动应用 |
| `generate.ts` | 有前端时为 `<frontend.dir>/src/api` | TypeScript 客户端的目录，`"-"` 表示不生成 |
| `generate.go` | 不生成 | Go 客户端的目录 |
| `templ.proxyPort` | `7331`，有 `.templ` 文件时启用 | templ 代理的端口 |
| `templ.static` | 不监视 | 文件变化时刷新浏览器的目录 |
| `tailwind.input`、`tailwind.output` | 不使用 | Tailwind 的入口和生成的样式表，见[用 Tailwind 编写样式](assets.md#tailwind) |
| `tools` | 空 | 其他工具，`watch` 是开发时的监听命令，`build` 是停止后的生成命令 |

例如用 esbuild 打包公开页面自己的脚本：

```json
{
  "tools": [{
    "watch": ["esbuild", "internal/web/js/app.js", "--bundle", "--outdir=internal/web/static/js", "--watch=forever"],
    "build": ["esbuild", "internal/web/js/app.js", "--bundle", "--outdir=internal/web/static/js", "--minify"]
  }]
}
```

<a id="app-side"></a>
## 应用这一侧

路由是运行时注册的，`tenon generate` 要借助应用自己的 `client` 命令。应用在根命令中加入 `httpcmd.Commands`，它返回 `openapi`、`routes`、`client` 三个命令，见[命令行](commands.md#declaring-the-cli)：

```go
root.AddCommand(httpcmd.Commands(bootstrap.API, bootstrap.DocRoutes, bootstrap.DocRouter)...)
```

`config/local.yaml` 设置 `frontend_dev_server: http://localhost:5173`，开发时 `httpx.SPA` 把页面请求转给 Vite，见[单页应用的开发](spa.md#development)。
