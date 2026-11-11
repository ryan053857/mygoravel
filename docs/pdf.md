# 生成 PDF

- [简介](#introduction)
- [启动 Gotenberg](#running-gotenberg)
- [编写模板](#writing-templates)
    - [纸张、边距与页码](#page-setup)
    - [样式和图片](#assets)
- [下载 PDF](#downloading)
- [作为邮件附件](#mail-attachments)
- [后台生成](#background-generation)
- [测试](#testing)
- [部署](#deployment)

<a id="introduction"></a>
## 简介

PDF 用写页面的方式编写：一个 templ 组件加一份 CSS，交给 Chromium 打印。发票、合同、报表都可以这样生成，模板在浏览器中打开就能预览。

`pdf` 包定义了文档和生成器：

```go
type Document struct {
    HTML   templ.Component // 整个页面
    Assets fs.FS           // 页面引用的样式、图片和字体
}

type Renderer interface {
    Render(ctx context.Context, doc Document) ([]byte, error)
}
```

`pdf/gotenberg` 实现 `Renderer`，调用 [Gotenberg](https://gotenberg.dev) 服务中的 Chromium。浏览器运行在独立的容器中，崩溃、内存占用、排队和定期重启都由 Gotenberg 负责；它的镜像带有 Noto CJK 字体，中文不需要额外配置。

本章为博客应用增加“下载文章 PDF”。

<a id="running-gotenberg"></a>
## 启动 Gotenberg

本地开发时用 Docker 运行：

```shell
docker run --rm -p 3000:3000 gotenberg/gotenberg:9-chromium
```

`9-chromium` 是只带 Chromium 的版本。不带后缀的完整镜像还包含用来转换 Office 文档的 LibreOffice，这里用不到。

把 `gotenberg.Config` 放进应用的根配置结构体：

```go
type Config struct {
    // ...
    Gotenberg gotenberg.Config `yaml:"gotenberg"`
}
```

```yaml
gotenberg:
  url: ${GOTENBERG_URL:-http://localhost:3000}
```

Gotenberg 用 `--api-enable-basic-auth` 开启了基本认证时，再填写 `username` 和 `password`。

在[入口](dependencies.md#assembling-the-application)的 `open` 中创建生成器，保存在 `App` 中。`gotenberg.New` 不访问网络：

```go
renderer, err := gotenberg.New(cfg.Gotenberg)
if err != nil {
    return nil, err
}
```

<a id="writing-templates"></a>
## 编写模板

PDF 的模板和用到的文件放在 `internal/article/articlepdf` 中。和[页面](directory-structure.md)一样，templ 组件要导入 `article` 包的类型，所以放在子包里：

```text
internal/article/articlepdf/
├── article.templ    PDF 的页面
├── assets/
│   ├── article.css
│   └── logo.svg
└── download.go      Download：生成并下载
```

`article.templ` 是一个完整的 HTML 页面，`<title>` 成为 PDF 的标题：

```templ
package articlepdf

templ Article(a *article.Article) {
	<!DOCTYPE html>
	<html lang="zh-CN">
		<head>
			<meta charset="utf-8"/>
			<title>{ a.Title }</title>
			<link rel="stylesheet" href="article.css"/>
		</head>
		<body>
			<header class="running"><img src="logo.svg" height="16"/> 示例博客</header>
			<h1>{ a.Title }</h1>
			if a.PublishedAt != nil {
				<p class="meta">发布于 { a.PublishedAt.Format("2006-01-02") }</p>
			}
			<div class="content">{ a.Content }</div>
		</body>
	</html>
}
```

<a id="page-setup"></a>
### 纸张、边距与页码

纸张、方向、边距和页码都在 CSS 的 `@page` 中设置，Go 代码中没有对应的选项。下面是 `assets/article.css`：

```css
@page {
  size: A4;
  margin: 25mm 20mm;

  @bottom-center {
    content: "第 " counter(page) " 页，共 " counter(pages) " 页";
    font-size: 9pt;
    color: #666;
  }
}

body { font-family: "Noto Sans CJK SC", sans-serif; font-size: 11pt; line-height: 1.7; }
.content { white-space: pre-wrap; }

/* 固定定位的元素在每一页重复，放在上边距中作为页眉 */
.running { position: fixed; top: -15mm; left: 0; font-size: 9pt; color: #666; }

h1, h2 { break-after: avoid; }
img, pre, tr { break-inside: avoid; }
thead { display: table-header-group; } /* 表格跨页时重复表头 */
```

- `size` 可以写 `A4`、`A5 landscape`、`210mm 297mm` 等。没有写 `size` 时使用 A4。
- `@bottom-center` 是页边距中的区域，类似的还有 `@top-right`、`@bottom-left` 等，`counter(page)` 是当前页码，`counter(pages)` 是总页数。
- 背景色和背景图默认会打印出来。
- Gotenberg 镜像中的中文字体是 Noto Sans CJK（黑体）。需要其他字体时，把字体文件放进 `Assets`，用 `@font-face` 引用。

模板在浏览器中打开后，用打印预览就能看到分页的效果。

<a id="assets"></a>
### 样式和图片

`Assets` 中的文件和页面一起上传，**HTML 中只写文件名**，例如 `href="article.css"`、`src="logo.svg"`。`Assets` 只能包含顶层文件，不能有子目录。把它们嵌入程序：

```go
//go:embed assets
var files embed.FS

var assets, _ = fs.Sub(files, "assets")
```

引用的文件加载失败（文件名写错、不在 `Assets` 中）时，`Render` 返回错误，而不是生成一份没有样式的 PDF。

其他图片的来源：

- [文件存储](filesystem.md)中的图片有公开地址，直接写在 `src` 中，Gotenberg 会去下载。
- 每份文档不同的图片，例如二维码，可以写成 data URL：`<img src={ templ.SafeURL("data:image/png;base64," + code) }/>`。

要使用 [Tailwind CSS](assets.md#tailwind)，把为 PDF 生成的样式表输出到 `assets` 目录中即可。

<a id="downloading"></a>
## 下载 PDF

`download.go` 中的 action 先用 `article.Get` 读取文章，同样只允许作者本人看到草稿，再生成 PDF：

```go
package articlepdf

type Download struct {
    get *article.Get
    pdf pdf.Renderer
}

func NewDownload(db *bun.DB, renderer pdf.Renderer) *Download {
    return &Download{get: article.NewGet(db), pdf: renderer}
}

// Handle 返回文章的 PDF 和文件名。
func (a *Download) Handle(ctx context.Context, subject identity.Subject, id uuid.UUID) ([]byte, string, error) {
    item, err := a.get.Handle(ctx, subject, id)
    if err != nil {
        return nil, "", err
    }
    data, err := a.pdf.Render(ctx, pdf.Document{HTML: Article(item), Assets: assets})
    if err != nil {
        return nil, "", errors.Wrapf(err, "render article %s", item.ID)
    }
    return data, item.Slug + ".pdf", nil
}

type DownloadInput struct {
    ID uuid.UUID `path:"id"`
}

type DownloadOutput struct {
    ContentType        string `header:"Content-Type"`
    ContentDisposition string `header:"Content-Disposition"`
    Body               []byte
}

// HTTP 处理 GET /articles/{id}/pdf。
func (a *Download) HTTP(ctx context.Context, in *DownloadInput) (*DownloadOutput, error) {
    subject, _ := identity.FromContext(ctx)
    data, filename, err := a.Handle(ctx, subject, in.ID)
    if err != nil {
        return nil, err
    }
    return &DownloadOutput{
        ContentType:        "application/pdf",
        ContentDisposition: mime.FormatMediaType("attachment", map[string]string{"filename": filename}),
        Body:               data,
    }, nil
}
```

想让浏览器直接打开而不是下载，把 `attachment` 换成 `inline`。路由的注册方式见[组织路由](routing.md#organizing-routes)。

生成失败时：

| 情况 | 错误 | 响应 |
| --- | --- | --- |
| 连不上 Gotenberg、排队已满或超时 | `pdf.ErrUnavailable` | 503，“PDF 生成服务暂时不可用” |
| 引用的文件加载失败、模板渲染出错等 | 带有原因的普通错误 | 500，记录日志 |

<a id="mail-attachments"></a>
## 作为邮件附件

把生成的内容放进 `mail.Attachment`：

```go
msg.Attachments = append(msg.Attachments, mail.Attachment{
    Filename:    filename,
    ContentType: "application/pdf",
    Content:     data,
})
```

和其他邮件一样，在事务提交之后或在队列任务中发送，见[邮件](mail.md#sending-mail)。

<a id="background-generation"></a>
## 后台生成

一份几页的 PDF 通常在一秒内生成，在请求中同步生成即可。页数很多，或者要一次生成一批时，交给[队列](queue.md)：任务中调用 `Render`，用[文件存储](filesystem.md#server-upload)的 `Upload` 保存结果，再用[通知](notifications.md)告诉用户下载地址。需要长期保留的文件，上传后用 `Confirm` 确认。

Gotenberg 暂时不可用时，任务返回错误，按队列的规则稍后重试。

<a id="testing"></a>
## 测试

`pdftest.Renderer` 渲染并记录 HTML，返回一页空白的 PDF，不需要 Gotenberg：

```go
func TestDownload(t *testing.T) {
    renderer := pdftest.New()
    download := articlepdf.NewDownload(db, renderer)

    _, filename, err := download.Handle(ctx, author, item.ID)
    require.NoError(t, err)
    assert.Equal(t, "hello-world.pdf", filename)

    html, _ := renderer.Last()
    assert.Contains(t, html, "<h1>Hello World</h1>")
}
```

`FailWith(pdf.ErrUnavailable.New())` 让之后的 `Render` 都失败，用来测试出错时的处理。

模板的排版用浏览器的打印预览检查。

<a id="deployment"></a>
## 部署

Gotenberg 和应用一起部署。在[部署](deployment.md#docker)的 compose 文件中加一个服务，应用通过 `GOTENBERG_URL` 访问它：

```yaml
x-app: &app
  environment:
    GOTENBERG_URL: http://gotenberg:3000
    # ...

services:
  gotenberg:
    image: gotenberg/gotenberg:9-chromium
    restart: unless-stopped
  # ...
```

使用 systemd 部署时，同样用 Docker 在同一台机器上运行 Gotenberg。几点注意：

- **不要把 Gotenberg 暴露到公网**，只让应用访问它。
- 默认最多同时生成 6 份，其余排队；单次请求的时限是 30 秒。分别用 `--chromium-max-concurrency`、`--api-timeout` 调整，见 [Gotenberg 文档](https://gotenberg.dev)。
- 模板中有用户填写的图片地址时，Chromium 会去访问这些地址。在服务中加上 `command: [gotenberg, --chromium-deny-private-ips]` 可以禁止它访问内网地址，被拒绝的图片不显示，PDF 照常生成。开启后，文件存储的公开地址也不能是内网地址。
