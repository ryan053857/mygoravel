# 前端资源

- [简介](#introduction)
- [存放静态文件](#static-files)
- [提供静态文件](#serving)
- [在页面中引用](#referencing)
- [缓存](#caching)
- [开发模式](#development)
- [用 Tailwind CSS 编写样式](#tailwind)

<a id="introduction"></a>
## 简介

[界面组件](components.md)的样式表和脚本由框架内置，`ui.Assets()` 提供。应用自己的图片、字体、脚本，以及用 Tailwind 生成的样式表，放在应用中，通过 `embed` 打进二进制，由 `web.Static` 提供。部署时仍然只有一个二进制文件。

`web.Static` 按文件内容给地址加上指纹，例如 `/static/css/app.3f2a9c1d.css`。这样的地址可以让浏览器和 CDN 永久缓存：文件内容一变，指纹随之改变，页面引用的就是新地址，不需要手动加版本号，也不会读到旧缓存。

本章接着[视图](views.md)一章，给博客的公开页面加上应用自己的静态文件和样式。

<a id="static-files"></a>
## 存放静态文件

把静态文件放在页面布局旁边的 `internal/web/static` 目录中，并嵌入程序。下面是 `internal/web/static.go`：

```go
package web

import (
    "embed"
    "io/fs"
)

//go:embed static
var files embed.FS

// Static 是嵌入的静态文件，路径相对 static 目录，例如 css/app.css。
var Static, _ = fs.Sub(files, "static")
```

<a id="serving"></a>
## 提供静态文件

在 `HTTP` 方法中，用 `web.NewStatic` 创建 `Static`，挂载到 `web.StaticPath`（`/static/`），再把它交给 `web.New`：

```go
static, err := web.NewStatic(layout.Static, web.StaticOptions{DevDir: a.cfg.StaticDir})
if err != nil {
    return err
}
pages := web.New(web.Options{
    Static: static,
    // ErrorPage 等其余字段见视图一章
})

router.Handle(web.StaticPath+"*", static)
router.Handle(web.AssetsPath+"*", web.Assets())
```

`layout` 是应用的 `internal/web` 包，与框架的 `web` 包同名，导入时起了别名。`DevDir` 见[开发模式](#development)。`web.NewStatic` 在创建时读取全部文件、计算指纹。[routes 命令](commands.md#routes-command)把 `/static/*` 列为公开路由。

<a id="referencing"></a>
## 在页面中引用

在 templ 组件中用 `web.StaticURL` 取地址。例如在页面上显示 logo：

```templ
<img src={ web.StaticURL(ctx, "img/logo.svg") } alt="Blog"/>
```

文件名是相对 `static` 目录的路径。页面路由和错误页渲染时，`ctx` 中带有 `Options.Static`，返回的是带指纹的地址。不存在的文件不会报错，地址也不带指纹。可以访问 `/static/` 加文件名检查文件是否存在。

<a id="caching"></a>
## 缓存

| 请求的地址 | 响应 |
| --- | --- |
| 带指纹的地址，即 `StaticURL` 返回的地址 | `Cache-Control: public, max-age=31536000, immutable`，永久缓存 |
| 文件名已经带有内容哈希的地址，例如打包工具生成的 `bundle-86c5f2802d0bc25d.js` | 同样永久缓存，`StaticURL` 不会重复加指纹 |
| 原始地址，例如 `/static/css/app.css` | `Cache-Control: no-cache` 和 `ETag`。浏览器每次都会确认，文件没有变化时返回 `304` |
| 目录、不存在的文件 | `404` |

邮件、第三方页面等无法调用 `StaticURL` 的地方，可以使用原始地址。

<a id="development"></a>
## 开发模式

嵌入的文件只有重新编译后才会更新。开发时把 `StaticOptions.DevDir` 设为静态文件目录，`Static` 就改为直接读取磁盘：地址不带指纹，响应也不缓存，修改样式后刷新页面即可看到。

`DevDir` 取自应用配置。在 `settings.Config` 中加一个字段：

```go
type Config struct {
    // ……
    StaticDir string `yaml:"static_dir"` // 非空时从这个目录读取静态文件，只在开发时设置
}
```

只在本地的配置文件中设置它，生产环境的配置不写这个字段：

```yaml
# config/local.yaml
static_dir: internal/web/static
```

<a id="tailwind"></a>
## 用 Tailwind CSS 编写样式

内置的样式表包含组件用到的类名和一组常用的布局工具类，见[使用组件](components.md#using-components)。设计感强的页面，例如有自己品牌风格的官网、活动页，会用到更多的类名和自己的设计令牌，这时用 [Tailwind CSS](https://tailwindcss.com) 生成应用自己的样式表。它代替内置的样式表，组件、风格与应用的类名生成在同一份样式表中。

Tailwind 的 [standalone CLI](https://tailwindcss.com/blog/standalone-cli) 是一个独立的可执行文件，不需要 Node.js。从 [GitHub Releases](https://github.com/tailwindlabs/tailwindcss/releases) 下载对应平台的版本，放进 `PATH`。

样式的入口文件不放进 `static`，下面是 `internal/web/app.css`：

```css
@import "tailwindcss";
@import "./.web-ui/style-nova.css";

@theme {
  --color-brand-500: oklch(0.62 0.2 275);
  --font-display: "Iowan Old Style", "Songti SC", serif;
}
```

- 第二行导入组件和一套风格，风格与[主题](components.md#themes)的 `style` 一致。`.web-ui` 目录由[开发工具](tooling.md)从框架中复制过来，目录中带 `.gitignore`，不会被提交。
- `@theme` 定义应用自己的设计令牌，之后就可以在 class 中使用 `bg-brand-500`、`font-display` 这样的工具类，见 [Tailwind 文档](https://tailwindcss.com/docs/theme)。组件的颜色变量（`bg-primary`、`text-muted-foreground` 等）同样可以使用。

然后让主题使用生成的样式表：

```yaml
theme:
  style: nova
  stylesheet: css/app.css
```

在 `tenon.json` 中写上 Tailwind 的入口和输出，开发时运行 [tenon dev](tooling.md#dev)，打开 http://localhost:7331：

```json
{
  "tailwind": { "input": "internal/web/app.css", "output": "internal/web/static/css/app.css" },
  "templ": { "static": "internal/web/static" }
}
```

```bash
go tool tenon dev
```

修改 `.templ` 后，templ 重新生成代码、重启应用；Tailwind 随之重新生成 `internal/web/static/css/app.css`，浏览器自动刷新。停止 `dev` 时，它会生成压缩的样式表。生成的 `app.css` 和 `_templ.go` 一样要提交到版本库，这样构建和 CI 不需要安装 Tailwind。
