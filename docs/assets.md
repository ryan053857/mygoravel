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

页面用到的样式表、脚本、字体和图片放在应用中，通过 `embed` 打进二进制，由 `web.Static` 提供。部署时仍然只有一个二进制文件。

`web.Static` 按文件内容给地址加上指纹，例如 `/static/css/app.3f2a9c1d.css`。这样的地址可以让浏览器和 CDN 永久缓存：文件内容一变，指纹随之改变，页面引用的就是新地址，不需要手动加版本号，也不会读到旧缓存。

本章接着[视图](views.md)一章，给博客的编辑后台加上样式表。

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
    Sessions: sessions,
    Static:   static,
    // Login、ErrorPage 等其余字段见视图一章
})

router.Handle(web.StaticPath+"*", static)
router.Handle(web.AssetsPath+"*", web.Assets())
```

`layout` 是应用的 `internal/web` 包，与框架的 `web` 包同名，导入时起了别名。`DevDir` 见[开发模式](#development)。`web.NewStatic` 在创建时读取全部文件、计算指纹。[routes 命令](commands.md#routes-command)把 `/static/*` 列为公开路由。

<a id="referencing"></a>
## 在页面中引用

在 templ 组件中用 `web.StaticURL` 取地址。例如在布局的 `<head>` 中加载样式表：

```templ
<head>
	<meta charset="utf-8"/>
	<title>{ title }</title>
	<link rel="stylesheet" href={ web.StaticURL(ctx, "css/app.css") }/>
	@web.Scripts()
</head>
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

[Tailwind CSS](https://tailwindcss.com) 直接在 HTML 的 `class` 中组合样式，适合与 templ 一起使用。它的 [standalone CLI](https://tailwindcss.com/blog/standalone-cli) 是一个独立的可执行文件，不需要 Node.js。从 [GitHub Releases](https://github.com/tailwindlabs/tailwindcss/releases) 下载对应平台的版本，放进 `PATH`。

样式的入口文件不放进 `static`，下面是 `internal/web/app.css`：

```css
@import "tailwindcss";

@theme {
  --color-brand-500: oklch(0.62 0.2 275);
  --font-display: "Iowan Old Style", "Songti SC", serif;
}
```

`@theme` 定义设计令牌，即应用自己的颜色、字体等。之后就可以在 class 中使用 `bg-brand-500`、`font-display` 这样的工具类，见 [Tailwind 文档](https://tailwindcss.com/docs/theme)。

生成样式表：

```bash
tailwindcss -i internal/web/app.css -o internal/web/static/css/app.css --minify
```

Tailwind 会扫描项目中的文件，包括 `.templ` 和生成的 `_templ.go`，只输出实际用到的 class。生成的 `app.css` 和 `_templ.go` 一样，要提交到版本库，这样构建和 CI 不需要安装 Tailwind。

开发时运行 [dev 命令](commands.md#dev-command)，打开 http://localhost:7331：

```bash
go run ./cmd/api dev --config config/app.yaml,config/local.yaml
```

修改 `.templ` 后，templ 重新生成代码、重启应用；Tailwind 随之重新生成样式表，浏览器自动刷新。停止 `dev` 时，它会生成压缩的样式表，可以直接提交。
