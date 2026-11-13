# 文档页

- [简介](#introduction)
- [编写文档](#writing)
    - [排序与标题](#ordering)
- [挂载到路由](#mounting)
- [Markdown 写法](#markdown)
    - [链接与图片](#links)
    - [提示块](#alerts)
- [搜索](#search)
- [开发时预览](#development)
- [注意事项](#caveats)

<a id="introduction"></a>
## 简介

产品的帮助中心、团队内部的操作手册，内容都是一篇篇说明文字。`web/docs` 把一个目录中的 Markdown 文件变成带导航的文档页：

- 左侧是按目录生成的侧边栏，正文右侧是本页目录；
- 顶栏有站内搜索；
- 切换页面时，htmx 只替换正文区域。

页面用当前区域的[主题](components.md#themes)渲染，不需要写模板。

本章给博客加上面向读者的帮助中心，地址是 `/help`。

<a id="writing"></a>
## 编写文档

文档放在 `internal/help/content` 中。目录结构就是侧边栏：

```
internal/help/content/
  _index.md               /help，首页
  intro.md                /help/intro
  writing/
    _index.md             侧边栏的分组「写作」；有正文时也是一页 /help/writing
    drafts.md             /help/writing/drafts
    images/editor.png     正文中用相对路径引用
  account/
    _index.md
    password.md
    security/             分组中可以展开的一项
      _index.md
      two-factor.md
```

第一层目录是侧边栏的分组，第二层目录是分组中可以展开的一项，不能再深。`_index.md` 是目录的首页。它没有正文时，访问目录显示其中的页面列表。只放图片的目录不出现在侧边栏中，以 `.` 开头的文件和目录被忽略。

<a id="ordering"></a>
### 排序与标题

文件开头可以写 front matter，三个字段都可以省略：

```markdown
---
title: 保存草稿
description: 草稿自动保存，发布前只有自己能看到
order: 2
---

编辑器每 30 秒保存一次……
```

| 字段 | 说明 |
| --- | --- |
| `title` | 页面和侧边栏中的标题。省略时取正文开头的一级标题，再没有时取文件名。正文开头的一级标题不会重复显示 |
| `description` | 显示在标题下方，也显示在目录的页面列表中 |
| `order` | 在目录中的位置。写了的按从小到大排在前面，没写的排在后面，再按文件名排序 |

写错字段名（例如 Hugo 的 `weight`）时启动报错，不会被静默忽略。

<a id="mounting"></a>
## 挂载到路由

把内容嵌入程序。下面是 `internal/help/help.go`：

```go
package help

import (
    "embed"
    "io/fs"
)

//go:embed content
var files embed.FS

// Content 是帮助中心的文档，路径相对 content 目录。
var Content, _ = fs.Sub(files, "content")
```

在 `HTTP` 方法中创建文档，注册到[页面路由](views.md#page-routes)上：

```go
site, err := docs.New(docs.Options{
    Content: help.Content,
    Prefix:  "/help",
    Title:   "Blog 帮助中心",
    Actions: helpActions(), // 顶栏右侧的内容，例如「返回首页」的链接
})
if err != nil {
    return err
}
site.Register(pages.Routes(router))
```

`docs.New` 读取并渲染全部文档。front matter 不合法、目录超过两层、两篇文档的地址相同、链接指向不存在的文件时返回错误，应用不会带着坏链接启动。之后每个请求只是套用页面模板。

文档页用这组 `Pages` 的主题和错误页。帮助中心是公开的；只给内部人员看的操作手册，把认证中间件装在传入的路由上，例如 `site.Register(pages.Routes(router.With(httpx.BasicAuth("staff", verifyStaff))))`，见 [HTTP Basic 认证](middleware.md#basic-auth)。

<a id="markdown"></a>
## Markdown 写法

支持 [GFM](https://github.github.com/gfm/)：表格、任务列表、删除线和自动链接。代码块写明语言时高亮，右上角有复制按钮。

二、三级标题会列入右侧的本页目录。标题 ID 按标题文字生成，中文保留，例如 `## 保存草稿` 的 ID 是 `保存草稿`。需要固定的 ID 时写在标题末尾：`## 保存草稿 {#drafts}`。

Markdown 中的 HTML 默认去掉。文档只由可信的人编写时，可以设置 `Options.HTML` 原样输出。

<a id="links"></a>
### 链接与图片

在文档之间用相对路径链接 `.md` 文件，在编辑器和 GitHub 中也能点开：

```markdown
草稿的保存规则见[保存草稿](../writing/drafts.md#自动保存)。

![编辑器](images/editor.png)
```

渲染时，`.md` 文件和目录改写为页面地址，例如 `/help/writing/drafts#自动保存`，点击后 htmx 只替换正文区域。图片等文件由文档路由提供，带 `ETag`。以 `/` 开头的地址和带协议的地址保持不变。

<a id="alerts"></a>
### 提示块

引用块的第一行写 `[!NOTE]`、`[!TIP]`、`[!IMPORTANT]`、`[!WARNING]` 或 `[!CAUTION]`，就显示为对应颜色的提示块，与 GitHub 的写法相同：

```markdown
> [!WARNING]
> 删除的文章 30 天后无法恢复。
```

提示块的标题（「警告」等）按请求的语言显示，见[本地化](localization.md)。

<a id="search"></a>
## 搜索

顶栏的搜索框边输入边搜索，结果显示在下方的弹层中，按回车打开搜索页。手机上点顶栏的搜索图标进入搜索页。搜索页的地址是 `/help/_search`，所以文档不能使用 `_search` 这个地址。

索引在 `docs.New` 时建立在内存中，每个二、三级标题下的正文是一条结果。查询按空白分成词，每个词都出现在页面标题、小节标题或正文中才算匹配；排序时页面标题优先，其次是小节标题，最后是正文。匹配不区分大小写，按子串查找，中文不需要分词。

这适合几百篇以内的文档。文档更多、需要相关度排序时，用[全文搜索](search.md)建立索引，在 `Actions` 中放自己的搜索框。

<a id="development"></a>
## 开发时预览

嵌入的文档只有重新编译后才会更新。开发时改为读取磁盘，并设置 `Reload`，每次请求都重新读取：

```go
type Config struct {
    // ……
    HelpDir string `yaml:"help_dir"` // 非空时从这个目录读取帮助中心的文档，只在开发时设置
}
```

```go
opts := docs.Options{Content: help.Content, Prefix: "/help", Title: "Blog 帮助中心"}
if a.cfg.HelpDir != "" {
    opts.Content, opts.Reload = os.DirFS(a.cfg.HelpDir), true
}
site, err := docs.New(opts)
```

修改文档后刷新页面即可看到。文档有错误时页面返回 500，错误写入日志。

<a id="caveats"></a>
## 注意事项

- 多种语言的文档各自是一个 `docs.Site`，例如 `/help` 和 `/en/help`。
- 侧边栏和本页目录的文字（「本页目录」「上一页」等）随请求的语言变化，正文不变。
