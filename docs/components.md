# 界面组件

- [简介](#introduction)
- [主题](#themes)
- [布局](#layouts)
- [使用组件](#using-components)
- [确认对话框](#confirming)
- [请求进行中](#busy-buttons)
- [按需加载的组件](#extra-components)
- [不同区域的界面](#areas)
- [升级组件](#upgrading)
- [注意事项](#caveats)

<a id="introduction"></a>
## 简介

按钮、表格、下拉菜单、对话框这些界面组件来自 [shadcn-templ](https://github.com/axadrn/shadcn-templ)。它是 [shadcn/ui](https://ui.shadcn.com) 的 templ 移植版，交互（焦点锁定、键盘导航、弹层定位）来自 Base UI 的移植。

框架在 go.mod 中固定了 shadcn-templ 的版本，`web/ui` 包内置了这个版本的样式表和组件脚本。应用直接 import 组件，不需要安装 shadcn-templ 的命令行，也不需要 Tailwind。`web/ui` 负责把组件接到[页面](views.md)上：

- 按主题加载样式表和脚本；
- 局部请求的错误显示为 toast；
- `hx-confirm` 弹出确认对话框；
- 表单字段、分页、排序表头、空状态这几个与 `web` 配合的组件。

这些衔接都可以不用，换成应用自己的写法，见[不同区域的界面](#areas)。

本章给博客的公开页面加上组件。组件中的文字（分页的「上一页」、确认框的「取消」等）按请求的语言显示，见[本地化](localization.md#built-in-messages)。

<a id="themes"></a>
## 主题

页面的外观由 `web.Options.Theme` 决定，它的字段都可以写在配置文件中。在 `settings.Config` 中加一个字段：

```go
type Config struct {
    // ……
    Theme web.Theme `yaml:"theme"`
}
```

```yaml
# config/app.yaml
theme:
  style: nova
  radius: 0.5rem
  vars:
    primary: "oklch(0.55 0.22 275)"
    primary-foreground: "oklch(0.98 0 0)"
```

| 字段 | 说明 |
| --- | --- |
| `style` | 组件风格，shadcn 的 8 套之一：`vega`、`nova`（默认）、`lyra`、`maia`、`mira`、`luma`、`sera`、`rhea`。同样的组件，圆角、间距、阴影各不相同，可以在 [shadcn 的主题页](https://ui.shadcn.com/themes)预览 |
| `mode` | `system`（默认）跟随系统，用户切换后记在浏览器中；`light`、`dark` 固定 |
| `radius` | 圆角的基准 |
| `font` | 正文字体，CSS `font-family` 的值。字体文件作为[静态文件](assets.md)提供 |
| `vars`、`dark_vars` | 覆盖亮色、暗色主题的 CSS 变量，例如 `primary`、`ring`、`sidebar`，完整的变量见 [shadcn 的主题文档](https://ui.shadcn.com/docs/theming) |
| `stylesheet` | 用 Tailwind 生成的样式表，见[用 Tailwind 编写样式](assets.md#tailwind) |

在配置的 `Validate` 中调用 `cfg.Theme.Validate()`，配置写错时启动就会报错。然后把它交给 `web.New`：

```go
pages := web.New(web.Options{
    ErrorPage: ui.ErrorPage,
    Theme:     a.cfg.Theme,
})
router.Handle(web.AssetsPath+"*", web.Assets())
router.Handle(ui.AssetsPath+"*", ui.Assets())
```

`ui` 是 `github.com/ryan053857/mygoravel/web/ui`。`ui.ErrorPage` 用主题渲染错误页，也可以换成自己的组件。`ui.ThemeToggle()` 是暗色模式的开关按钮，页面上任何带 `data-web-ui-theme-toggle` 属性的元素也都是开关。

<a id="layouts"></a>
## 布局

[视图](views.md#layouts)一章的布局用 `ui.Page` 生成 HTML 文档，再放上确认对话框和提示：

```templ
package web

import "github.com/ryan053857/mygoravel/web/ui"

templ Layout(title string) {
	@ui.Page(ui.PageProps{Title: title}) {
		<header class="flex justify-end p-4">
			@ui.ThemeToggle()
		</header>
		<main class="mx-auto max-w-3xl p-6">
			{ children... }
		</main>
		@ui.Confirm()
		@ui.Toaster()
	}
}
```

- `ui.Page` 给 `<html>` 加上风格的 class，在 `<head>` 中输出 `ui.Head()`：样式表、主题变量、htmx、Alpine 和组件脚本。需要额外的 `<head>` 内容时放进 `PageProps.Head`。
- `ui.Confirm()` 是 `hx-confirm` 的确认对话框，见[确认对话框](#confirming)。
- `ui.Toaster()` 把局部请求的错误显示为 toast，页面内容保持不变，见[错误页](views.md#error-pages)。

需要完全控制文档结构时，不用 `ui.Page`，自己写 `<html class={ ui.Class(ctx) }>`，在 `<head>` 中调用 `@ui.Head()`。

<a id="using-components"></a>
## 使用组件

组件是带参数结构体的 templ 组件，直接 import 使用，子组件组合成完整的结构。`Attributes` 可以传入任意属性，包括 htmx 的属性：

```templ
import "github.com/axadrn/shadcn-templ/v2/components/button"

@button.Button(button.Props{
	Variant:    button.VariantOutline,
	Attributes: templ.Attributes{"hx-get": "/articles/" + item.Slug + "/related", "hx-target": "#related"},
}) {
	相关文章
}
```

每个组件的参数和示例见 [shadcn-templ 文档](https://shadcn-templ.com/docs)。`Class` 中的工具类总能覆盖风格的样式，例如 `Class: "rounded-none"` 得到方角按钮。

表单使用 `ui.TextField`、`ui.SelectField` 等字段组件，它们读取 `web.Form` 的输入和错误，见[渲染表单](forms.md#rendering-forms)。自己组合 `field`、`input` 等组件时注意：组件的样式按 `data-invalid="true"`、`aria-invalid="true"` 切换，属性的值必须是 `"true"`，没有错误时不写这个属性。

不用 Tailwind 时，页面能用的工具类是组件用到的类名，加上一组常用的布局工具类：间距、flex、grid、宽高、文字大小和对齐、主题颜色，以及它们在 `sm`、`md`、`lg` 断点上的变体，完整列表见框架的 `web/ui/css/safelist.css`。用到其他类名时，用 [Tailwind](assets.md#tailwind) 生成样式表。

<a id="confirming"></a>
## 确认对话框

布局中有 `ui.Confirm()` 时，按钮上的 `hx-confirm` 会弹出确认对话框，点击确认后才发出请求。例如订阅邮件中的退订页：

```templ
@button.Button(button.Props{
	Variant: button.VariantGhost,
	Attributes: templ.Attributes{
		"hx-post":                  "/unsubscribe?token=" + token,
		"hx-confirm":               "退订邮件？",
		"data-confirm-description": "之后不再收到新文章的通知。",
		"data-confirm-action":      "退订",
		"data-confirm-variant":     "destructive",
	},
}) {
	退订
}
```

`hx-confirm` 是对话框的标题。`data-confirm-description` 是说明，`data-confirm-action` 是确认按钮的文字，默认按请求的语言显示「确定」。`data-confirm-variant="destructive"` 让确认按钮显示为危险操作。取消、按 Esc 或点击遮罩时不发请求。

<a id="busy-buttons"></a>
## 请求进行中

按钮发出的 htmx 请求、以及提交按钮提交的表单，在请求结束之前，按钮会被禁用并带上 `aria-busy="true"`，图标换成旋转的圈。用户不会重复提交，应用也不用写任何代码。由输入自动触发的请求（例如列表页的搜索框）不受影响。

元素上写了 htmx 的 `hx-disable` 时，按它指定的元素禁用，不再自动处理。

<a id="extra-components"></a>
## 按需加载的组件

大部分组件的脚本在一个文件中，随页面加载。体积大且不常用的几个组件（`calendar`、`carousel`、`chart`、`drawer`、`input-otp`、`message-scroller`、`questionnaire`、`resizable`）单独成文件，页面上出现这些组件时才加载，包括 htmx 后来放进页面的。使用它们不需要任何设置。

<a id="areas"></a>
## 不同区域的界面

同一个应用常有几块公开页面，例如官网和帮助中心。它们各用一个 `web.Pages`，各有自己的主题和错误呈现，见[多组页面](views.md#multiple-areas)：

```yaml
site:
  theme:
    style: maia
    radius: 1rem
    stylesheet: css/app.css
    vars:
      primary: "oklch(0.55 0.22 275)"
help:
  theme:
    style: nova
```

`web/ui` 的每一项衔接都可以换掉：

- 不用 `ui.Toaster()`，自己监听 `web:flash` 事件显示局部请求的错误，见[错误页](views.md#error-pages)；
- 设置 `web.Options.ErrorFragment`，局部请求的错误换成自己的片段；
- 不用 `ui.Page`，自己写 `<html>`，见[布局](#layouts)；
- 用 [Tailwind](assets.md#tailwind) 给设计感强的页面写自己的样式，与组件共用一套主题变量。

<a id="upgrading"></a>
## 升级组件

shadcn-templ 的版本随框架升级，`ui.ShadcnTemplVersion` 是当前版本。不要在应用的 go.mod 中单独升级 shadcn-templ：组件的 HTML 与内置的样式表、脚本必须配套，版本不一致时 `ui.Assets()` 会 panic。

需要改动某个组件时，把它的源码复制进应用，修改后 import 应用自己的版本。组件的外观来自风格样式表中的 `cn-*` 类名，复制出来的组件仍然使用内置的样式。

<a id="caveats"></a>
## 注意事项

- **给对话框固定的 `ID`。** 组件没有 `ID` 时会生成随机值，同一个对话框重新渲染后就对不上了。
- **对话框不要放在 `<table>` 里。** 对话框渲染的是 `<div>`，浏览器解析 HTML 时会把 `<tbody>` 中的 `<div>` 移到表格外面。
- **templ 会拦截字符串形式的事件处理器。** `templ.Attributes{"onclick": "…"}` 渲染出来是一段注释，不会执行。在元素上写 `data-*` 属性，在应用的脚本中用事件委托处理，这样也符合[内容安全策略](views.md#content-security-policy)。
