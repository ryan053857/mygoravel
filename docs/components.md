# 界面组件

- [简介](#introduction)
- [主题](#themes)
- [布局](#layouts)
- [后台外壳](#app-shell)
- [登录页](#auth-pages)
- [使用组件](#using-components)
- [提示消息](#toasts)
- [对话框](#dialogs)
    - [打开对话框](#opening-dialogs)
    - [提交与关闭](#submitting-dialogs)
    - [确认删除](#confirming)
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
- [提示消息](views.md#flash-messages)显示为 toast；
- 服务端打开和关闭对话框。

这些衔接都可以不用，换成应用自己的写法，见[不同区域的界面](#areas)。

本章给博客的编辑后台加上组件：在对话框中编辑文章、确认删除，保存后弹出提示。组件中的文字（分页的「上一页」、确认框的「取消」等）按请求的语言显示，见[本地化](localization.md#built-in-messages)。

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
    Sessions:  sessions,
    Login:     "/login",
    ErrorPage: ui.ErrorPage,
    Theme:     a.cfg.Theme,
})
router.Handle(web.AssetsPath+"*", web.Assets())
router.Handle(ui.AssetsPath+"*", ui.Assets())
```

`ui` 是 `github.com/ryan053857/mygoravel/web/ui`。`ui.ErrorPage` 用主题渲染错误页，也可以换成自己的组件。页面上任何带 `data-web-ui-theme-toggle` 属性的元素都是暗色模式的开关。

<a id="layouts"></a>
## 布局

[视图](views.md#layouts)一章的布局用 `ui.Page` 生成 HTML 文档，再放上对话框的位置和提示：

```templ
package web

import "github.com/ryan053857/mygoravel/web/ui"

templ Layout(title string) {
	@ui.Page(ui.PageProps{Title: title}) {
		<main class="mx-auto max-w-5xl p-6">
			{ children... }
		</main>
		@ui.Modal()
		@ui.Confirm()
		@ui.Toaster()
	}
}
```

- `ui.Page` 给 `<html>` 加上风格的 class，在 `<head>` 中输出 `ui.Head()`：样式表、主题变量、htmx、Alpine 和组件脚本。需要额外的 `<head>` 内容时放进 `PageProps.Head`。
- `ui.Modal()` 是放置对话框的位置，见[打开对话框](#opening-dialogs)；`ui.Confirm()` 是 `hx-confirm` 的确认对话框，见[确认删除](#confirming)。
- `ui.Toaster()` 显示[提示消息](#toasts)。

需要完全控制文档结构时，不用 `ui.Page`，自己写 `<html class={ ui.Class(ctx) }>`，在 `<head>` 中调用 `@ui.Head()`。

<a id="app-shell"></a>
## 后台外壳

运营后台常用的外壳是左侧的侧边栏加上右侧的顶栏和正文，`ui.AppShell` 直接提供它。导航是一个切片，`internal/web/nav.go`：

```go
var AdminNav = []ui.NavItem{
    {Label: "概览", Href: "/admin", Exact: true, Icon: icon.House},
    {Label: "文章", Href: "/admin/articles", Icon: icon.FileText, Group: "内容", Permission: "articles"},
    {Label: "评论", Href: "/admin/comments", Icon: icon.MessageSquare, Group: "内容", Permission: "comments"},
    {Label: "设置", Icon: icon.Settings, Group: "系统", Items: []ui.NavItem{
        {Label: "用户", Href: "/admin/users", Permission: "users"},
        {Label: "角色", Href: "/admin/roles", Permission: "users"},
    }},
}
```

布局把页面放进外壳：

```templ
templ AdminLayout(title string) {
	@ui.Page(ui.PageProps{Title: title}) {
		@ui.AppShell(ui.AppShellProps{
			Brand:   "Blog 后台",
			Logo:    icon.Newspaper,
			Home:    "/admin",
			Nav:     AdminNav,
			Title:   title,
			User:    currentUser(ctx),
			Actions: ui.ThemeToggle(),
		}) {
			{ children... }
		}
		@ui.Modal()
		@ui.Confirm()
		@ui.Toaster()
	}
}
```

- 导航按当前主体过滤：带 `Permission` 的项只有 `sub.Can` 时才显示，二级导航全部隐藏时整项隐藏。权限与 `web.Require` 使用同一套，见[授权](authorization.md)。
- 当前页面（`web.CurrentPath`）等于 `Href` 或在它之下时高亮，`Exact` 只比较相等；当前页面在二级导航中时，这一组默认展开。连续的、`Group` 相同的项显示在同一组下，`Badge` 是右侧的小标记。
- 侧边栏可以收起成图标，用户的选择记在 cookie 中，之后的页面保持收起；`Collapsed` 让它默认收起。手机宽度下侧边栏变成抽屉。
- `User` 显示在侧边栏底部，点击弹出 `Menu`。退出登录这类改变状态的项写 `Post: true`，发出 `hx-post`：

```go
func currentUser(ctx context.Context) *ui.ShellUser {
    sub, _ := identity.FromContext(ctx)
    return &ui.ShellUser{Name: sub.ID, Menu: []ui.MenuItem{
        {Label: "个人设置", Href: "/admin/profile", Icon: icon.User},
        {Label: "退出登录", Href: "/logout", Post: true, Icon: icon.LogOut},
    }}
}
```

`ui.ThemeToggle()` 是暗色模式的开关按钮。外壳不适合时，直接组合 shadcn-templ 的 `sidebar` 组件，或者完全自己写布局。

<a id="auth-pages"></a>
## 登录页

登录、注册、找回密码这些页面常用居中的卡片。`ui.AuthCard` 只负责布局，表单由应用自己放进去：

```templ
templ Login(f *web.Form[LoginInput]) {
	@ui.Page(ui.PageProps{Title: "登录"}) {
		@ui.AuthCard(ui.AuthCardProps{Title: "登录 Blog 后台", Description: "使用邮箱和密码登录", Brand: "Blog", Logo: icon.Newspaper}) {
			<form method="post" class="flex flex-col gap-4">
				@ui.TextField(f, "email", "邮箱", ui.FieldProps{Type: "email", Attributes: templ.Attributes{"autocomplete": "email"}})
				@ui.TextField(f, "password", "密码", ui.FieldProps{Type: "password", Attributes: templ.Attributes{"autocomplete": "current-password"}})
				@button.Button(button.Props{Type: button.TypeSubmit, Class: "w-full"}) {
					登录
				}
			</form>
		}
		@ui.Toaster()
	}
}
```

`Footer` 是卡片下方的内容，例如「还没有账号？注册」。登录的处理器见[登录](views.md#login)。

<a id="using-components"></a>
## 使用组件

组件是带参数结构体的 templ 组件，直接 import 使用，子组件组合成完整的结构。`Attributes` 可以传入任意属性，包括 htmx 的属性：

```templ
import "github.com/axadrn/shadcn-templ/v2/components/button"

@button.Button(button.Props{
	Variant:    button.VariantOutline,
	Attributes: templ.Attributes{"hx-get": "/admin/articles/" + item.ID.String() + "/edit", "hx-target": ui.ModalTarget},
}) {
	编辑
}
```

每个组件的参数和示例见 [shadcn-templ 文档](https://shadcn-templ.com/docs)。`Class` 中的工具类总能覆盖风格的样式，例如 `Class: "rounded-none"` 得到方角按钮。

表单使用 `ui.TextField`、`ui.SelectField` 等字段组件，它们读取 `web.Form` 的输入和错误，见[渲染表单](forms.md#rendering-forms)。自己组合 `field`、`input` 等组件时注意：组件的样式按 `data-invalid="true"`、`aria-invalid="true"` 切换，属性的值必须是 `"true"`，没有错误时不写这个属性。

不用 Tailwind 时，页面能用的工具类是组件用到的类名，加上一组常用的布局工具类：间距、flex、grid、宽高、文字大小和对齐、主题颜色，以及它们在 `sm`、`md`、`lg` 断点上的变体，完整列表见框架的 `web/ui/css/safelist.css`。用到其他类名时，用 [Tailwind](assets.md#tailwind) 生成样式表。

<a id="toasts"></a>
## 提示消息

布局中有 `ui.Toaster()` 时，`web.Flash` 存入的提示总会以 toast 弹出，不论之后的响应是什么：跳转后的整页、htmx 请求的片段、`204`。局部请求出错时，错误也以 toast 弹出，页面内容保持不变，见[错误页](views.md#error-pages)。完整规则见[提示消息](views.md#flash-messages)。

<a id="dialogs"></a>
## 对话框

对话框的开关状态在浏览器中，内容和数据在服务端。博客的约定是：由服务端渲染一个打开着的对话框，放进 `ui.Modal()`；保存成功后，由服务端通知浏览器关闭它。

<a id="opening-dialogs"></a>
### 打开对话框

列表中的「编辑」按钮用 `hx-get` 请求对话框，`hx-target` 是 `ui.ModalTarget`（见[使用组件](#using-components)）。处理器渲染对话框，表单沿用[表单](forms.md#rendering-forms)一章的 `ArticleForm`：

```go
func (h *Handlers) EditDialog(w http.ResponseWriter, r *http.Request) error {
    item, err := h.find(r)
    if err != nil {
        return err
    }
    f := web.NewForm(article.Changes{Title: item.Title, Slug: item.Slug, Content: item.Content})
    return web.Render(w, r, http.StatusOK, views.EditDialog(item.ID, f))
}
```

```templ
templ EditDialog(id uuid.UUID, f *web.Form[article.Changes]) {
	@dialog.Dialog(dialog.Props{ID: "article-dialog", DefaultOpen: true}) {
		@dialog.Content() {
			@dialog.Header() {
				@dialog.Title() {
					编辑文章
				}
			}
			@ArticleForm(id, f)
		}
	}
}
```

`h.find` 按路径参数查询文章。

`DefaultOpen` 让对话框加载后立即打开，焦点进入对话框，按 Esc 或点击遮罩关闭。下次点击「编辑」时，新的对话框替换 `ui.Modal()` 的内容，旧的随之销毁。

<a id="submitting-dialogs"></a>
### 提交与关闭

`ArticleForm` 带 `hx-swap="outerMorph"`。验证失败时，处理器返回 `422` 和重新渲染的表单，只替换表单，对话框保持打开，见[验证失败后重新显示](forms.md#redisplaying-forms)。保存成功后，处理器不再跳转，而是：

```go
web.CloseDialog(w, "article-dialog")
web.Trigger(w, "articles:changed", nil)
web.Flash(r.Context(), web.Message{Type: "success", Title: "文章已保存"})
w.WriteHeader(http.StatusNoContent)
return nil
```

- `web.CloseDialog` 关闭对话框，带着关闭动画收起；
- `articles:changed` 让列表区域按当前的筛选和页码刷新：列表区域上写 `hx-trigger="articles:changed from:body"`，`hx-get` 指向自己的地址，见[列表页](views.md#list-pages)；
- 提示随 `204` 响应送达，弹出 toast。

<a id="confirming"></a>
### 确认删除

只需要一句确认时，不必渲染对话框。布局中有 `ui.Confirm()` 时，按钮上的 `hx-confirm` 会弹出确认对话框，点击确认后才发出请求：

```templ
@button.Button(button.Props{
	Variant: button.VariantGhost,
	Attributes: templ.Attributes{
		"hx-delete":                "/admin/articles/" + item.ID.String(),
		"hx-confirm":               "删除「" + item.Title + "」？",
		"data-confirm-description": "删除后无法恢复。",
		"data-confirm-action":      "删除",
		"data-confirm-variant":     "destructive",
	},
}) {
	删除
}
```

`hx-confirm` 是对话框的标题。`data-confirm-description` 是说明，`data-confirm-action` 是确认按钮的文字，默认按请求的语言显示「确定」。`data-confirm-variant="destructive"` 让确认按钮显示为危险操作。取消、按 Esc 或点击遮罩时不发请求。

删除的处理器与保存相同，只是没有对话框要关闭：触发 `articles:changed`，存入提示，返回 `204`。

确认时需要显示服务端的数据（例如「这篇文章有 12 条评论」）或填写内容时，像[打开对话框](#opening-dialogs)一样，用 `hx-get` 把 `alert-dialog` 加载进 `ui.Modal()`。

<a id="busy-buttons"></a>
## 请求进行中

按钮发出的 htmx 请求、以及提交按钮提交的表单，在请求结束之前，按钮会被禁用并带上 `aria-busy="true"`，图标换成旋转的圈。用户不会重复提交，应用也不用写任何代码。由输入自动触发的请求（例如列表页的搜索框）不受影响。

元素上写了 htmx 的 `hx-disable` 时，按它指定的元素禁用，不再自动处理。

<a id="extra-components"></a>
## 按需加载的组件

大部分组件的脚本在一个文件中，随页面加载。体积大且不常用的几个组件（`calendar`、`carousel`、`chart`、`drawer`、`input-otp`、`message-scroller`、`questionnaire`、`resizable`）单独成文件，页面上出现这些组件时才加载，包括 htmx 后来放进页面的。使用它们不需要任何设置。

<a id="areas"></a>
## 不同区域的界面

同一个应用常有几块后台界面，例如运营后台和代理后台。它们各用一个 `web.Pages`，各有自己的主题、登录页、会话 cookie 和错误呈现，见[多组页面](views.md#multiple-areas)：

```yaml
ops:
  theme:
    style: nova
partners:
  theme:
    style: maia
    radius: 1rem
    stylesheet: css/app.css
    vars:
      primary: "oklch(0.55 0.22 275)"
```

`web/ui` 的每一项衔接都可以换掉：

- 不用 `ui.Toaster()`，自己监听 `web:flash` 事件显示提示，见[提示消息](views.md#flash-messages)；
- 设置 `web.Options.ErrorFragment`，局部请求的错误换成自己的片段；
- 不用 `ui.Page`，自己写 `<html>`，见[布局](#layouts)；
- 用 [Tailwind](assets.md#tailwind) 给设计感强的页面写自己的样式，与组件共用一套主题变量。

<a id="upgrading"></a>
## 升级组件

shadcn-templ 的版本随框架升级，`ui.ShadcnTemplVersion` 是当前版本。不要在应用的 go.mod 中单独升级 shadcn-templ：组件的 HTML 与内置的样式表、脚本必须配套，版本不一致时 `ui.Assets()` 会 panic。

需要改动某个组件时，把它的源码复制进应用，修改后 import 应用自己的版本。组件的外观来自风格样式表中的 `cn-*` 类名，复制出来的组件仍然使用内置的样式。

<a id="caveats"></a>
## 注意事项

- **给对话框固定的 `ID`。** 组件没有 `ID` 时会生成随机值，同一个对话框重新渲染后就对不上了；`web.CloseDialog` 也要靠 `ID` 找到它。
- **对话框不要放在 `<table>` 里。** 对话框渲染的是 `<div>`，浏览器解析 HTML 时会把 `<tbody>` 中的 `<div>` 移到表格外面。表格行里的按钮只负责请求，对话框放进 `ui.Modal()`。下拉菜单的内容可以放在单元格中，菜单项可以直接带 `hx-get`。
- **templ 会拦截字符串形式的事件处理器。** `templ.Attributes{"onclick": "…"}` 渲染出来是一段注释，不会执行。在元素上写 `data-*` 属性，在应用的脚本中用事件委托处理，这样也符合[内容安全策略](views.md#content-security-policy)。
- **对话框关闭后，焦点回到页面开头。** 对话框是 `hx-get` 加载的，没有关联的触发按钮。需要时可以在应用的脚本中把焦点放回合适的元素。
