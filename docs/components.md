# 界面组件

- [简介](#introduction)
- [安装](#installation)
- [在布局中加载](#loading)
- [使用组件](#using-components)
- [提示消息](#toasts)
- [对话框](#dialogs)
    - [打开对话框](#opening-dialogs)
    - [提交与关闭](#submitting-dialogs)
    - [确认删除](#confirming)
- [注意事项](#caveats)

<a id="introduction"></a>
## 简介

按钮、表格、下拉菜单、对话框这些界面组件，推荐使用 [shadcn-templ](https://github.com/axadrn/shadcn-templ)。它是 [shadcn/ui](https://ui.shadcn.com) 的 templ 移植版，组件的外观由 CSS 变量决定，交互（焦点锁定、键盘导航、弹层定位）来自 Base UI 的移植。

与 shadcn/ui 一样，shadcn-templ 不是依赖库。它的命令行把组件源码复制进你的项目，之后由你自己维护，可以随意修改。框架不依赖它，只是约定了几处衔接：组件脚本作为[静态文件](assets.md)提供，[提示消息](views.md#flash-messages)显示为 toast，对话框由服务端打开和关闭。

本章给博客的编辑后台加上组件：在对话框中编辑文章、确认删除，保存后弹出提示。

<a id="installation"></a>
## 安装

安装命令行，在项目根目录初始化。`--css` 指向[前端资源](assets.md#tailwind)一章中的样式入口：

```bash
go install github.com/axadrn/shadcn-templ/v2/cmd/shadcn-templ@latest
shadcn-templ init --preset nova --css internal/web/app.css
```

`init` 在 `app.css` 中导入主题变量和动画，生成 `components.json`，并把共用的 `utils` 包放在项目根目录。按博客的目录结构，把组件放进 `internal/web/ui`，组件脚本打包到静态文件目录。先移动 `utils`：

```bash
mkdir -p internal/web/ui && mv utils internal/web/ui/utils
```

再修改 `components.json` 中的这两项：

```json
{
  "scripts": {
    "dir": "internal/web/static/js",
    "path": "/static/js"
  },
  "aliases": {
    "components": "example.com/blog/internal/web/ui",
    "utils": "example.com/blog/internal/web/ui/utils"
  }
}
```

然后添加需要的组件，依赖的组件会自动一起添加：

```bash
shadcn-templ add button dialog alert-dialog dropdown-menu table field input toast
```

`add` 把组件写进 `internal/web/ui`，并把组件脚本打包为 `internal/web/static/js/shadcn-templ-<哈希>.js`。打包后的文件名带内容哈希，`web.Static` 会让浏览器永久缓存它，见[缓存](assets.md#caching)。最后运行 `go tool templ generate` 和 `go mod tidy`。

开发时，[dev 命令](commands.md#dev-command)还要监听组件脚本的变化。在 `Tools` 中加上：

```go
devcmd.Tool{Watch: []string{"shadcn-templ", "bundle", "--watch"}, Build: []string{"shadcn-templ", "bundle"}}
```

<a id="loading"></a>
## 在布局中加载

[视图](views.md#layouts)一章的布局，加上组件脚本、应用脚本、对话框的位置和提示：

```templ
package web

import (
	"example.com/blog/internal/web/ui"
	"example.com/blog/internal/web/ui/toast"
	"github.com/ryan053857/mygoravel/web"
)

templ Layout(title string) {
	<!DOCTYPE html>
	<html lang="zh-CN">
		<head>
			<meta charset="utf-8"/>
			<title>{ title }</title>
			<link rel="stylesheet" href={ web.StaticURL(ctx, "css/app.css") }/>
			@web.Scripts()
			@ui.Scripts()
			<script defer src={ web.StaticURL(ctx, "js/app.js") }></script>
		</head>
		<body>
			<div id="flash"></div>
			<main id="main">
				{ children... }
			</main>
			<div id="modal"></div>
			@toast.Toaster()
			for _, m := range web.Flashes(ctx) {
				@toast.Toast(toast.Props{Title: m.Title, Description: m.Description, Type: toast.Type(m.Type)})
			}
		</body>
	</html>
}

templ ErrorFragment(status int, message string) {
	@toast.Toast(toast.Props{Title: message, Type: toast.TypeError})
}
```

- `ui.Scripts()` 加载打包后的组件脚本。它和 `web.Scripts()` 一样支持[内容安全策略](views.md#content-security-policy)的 nonce。
- `#modal` 是放置对话框的位置，见[打开对话框](#opening-dialogs)。
- 整页渲染时，`web.Flashes` 取出的提示交给 `toast.Toast`，页面加载后弹出。
- 局部请求出错时，`ErrorFragment` 渲染的 toast 被放进 `#flash`（`ErrorTarget`），同样弹出。

应用自己的脚本 `internal/web/static/js/app.js` 把服务端的事件交给组件：

```js
// htmx 请求带来的提示（web.Flash）交给 toast。
document.addEventListener("web:flash", (e) => e.detail.messages.forEach((m) => window.templ.toast.add(m)));
// 服务端用 web.Trigger(w, "dialog:close", …) 关闭对话框。
document.addEventListener("dialog:close", (e) => window.templ.dialog.close(e.detail.id));
```

`web.Message` 的 JSON 字段（`type`、`title`、`description`）正好是 `toast.add` 的参数。`window.templ` 是组件脚本提供的全局对象。

<a id="using-components"></a>
## 使用组件

组件是带参数结构体的 templ 组件，子组件组合成完整的结构。`Attributes` 可以传入任意属性，包括 htmx 的属性：

```templ
@button.Button(button.Props{
	Variant:    button.VariantOutline,
	Attributes: templ.Attributes{"hx-get": "/admin/articles/" + item.ID.String() + "/edit", "hx-target": "#modal"},
}) {
	编辑
}
```

每个组件的参数和示例见 [shadcn-templ 文档](https://shadcn-templ.com/docs)。表单字段配合 `web.Form` 显示验证错误。组件的样式按 `data-invalid="true"`、`aria-invalid="true"` 切换，所以属性的值必须是 `"true"`，没有错误时不写这个属性：

```templ
templ textField(f *web.Form[article.Changes], name, label string) {
	@field.Field(field.Props{Attributes: invalid("data-invalid", f.Error(name))}) {
		@field.Label(field.LabelProps{For: name}) {
			{ label }
		}
		@input.Input(input.Props{ID: name, Name: name, Value: f.Value(name), Attributes: invalid("aria-invalid", f.Error(name))})
		if msg := f.Error(name); msg != "" {
			@field.Error() {
				{ msg }
			}
		}
	}
}

func invalid(attr, msg string) templ.Attributes {
	if msg == "" {
		return nil
	}
	return templ.Attributes{attr: "true"}
}
```

<a id="toasts"></a>
## 提示消息

在布局中完成上面的衔接后，`web.Flash` 存入的提示总会以 toast 弹出，不论之后的响应是什么：跳转后的整页、htmx 请求的片段、`204` 还是错误片段。完整规则见[提示消息](views.md#flash-messages)。

<a id="dialogs"></a>
## 对话框

对话框的开关状态在浏览器中，内容和数据在服务端。博客的约定是：由服务端渲染一个打开着的对话框，放进 `#modal`；保存成功后，由服务端通知浏览器关闭它。

<a id="opening-dialogs"></a>
### 打开对话框

列表中的「编辑」按钮用 `hx-get` 请求对话框，放进 `#modal`（见[使用组件](#using-components)）。处理器渲染对话框，表单沿用[表单](forms.md#rendering-forms)一章的 `ArticleForm`：

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

`h.find` 按路径参数查询文章。`DefaultOpen` 让对话框加载后立即打开，焦点进入对话框，按 Esc 或点击遮罩关闭。下次点击「编辑」时，新的对话框替换 `#modal` 的内容，旧的随之销毁。

<a id="submitting-dialogs"></a>
### 提交与关闭

`ArticleForm` 带 `hx-swap="outerMorph"`。验证失败时，处理器返回 `422` 和重新渲染的表单，只替换表单，对话框保持打开，见[验证失败后重新显示](forms.md#redisplaying-forms)。保存成功后，处理器不再跳转，而是：

```go
web.Trigger(w, "dialog:close", map[string]any{"id": "article-dialog"})
web.Trigger(w, "articles:changed", nil)
web.Flash(r.Context(), web.Message{Type: "success", Title: "文章已保存"})
w.WriteHeader(http.StatusNoContent)
return nil
```

- `dialog:close` 由 `app.js` 交给 `templ.dialog.close`，对话框带着关闭动画收起；
- `articles:changed` 让列表区域按当前的筛选和页码刷新：列表区域上写 `hx-trigger="articles:changed from:body"`，`hx-get` 指向自己的地址，见[列表页](views.md#list-pages)；
- 提示随 `204` 响应送达，弹出 toast。

<a id="confirming"></a>
### 确认删除

确认框用 `alert-dialog`，同样加载进 `#modal`。「删除」按钮直接发出 `hx-delete`：

```templ
templ DeleteDialog(item *article.Article) {
	@alertdialog.AlertDialog(alertdialog.Props{ID: "delete-dialog", DefaultOpen: true}) {
		@alertdialog.Content() {
			@alertdialog.Header() {
				@alertdialog.Title() {
					删除「{ item.Title }」？
				}
				@alertdialog.Description() {
					删除后无法恢复。
				}
			}
			@alertdialog.Footer() {
				@alertdialog.Cancel() {
					取消
				}
				@alertdialog.Action(alertdialog.ActionProps{
					Variant:    button.VariantDestructive,
					Attributes: templ.Attributes{"hx-delete": "/admin/articles/" + item.ID.String()},
				}) {
					删除
				}
			}
		}
	}
}
```

删除的处理器与保存相同：触发 `dialog:close`（`id` 为 `delete-dialog`）和 `articles:changed`，存入提示，返回 `204`。文章已经不存在等错误由 `ErrorFragment` 显示为 toast，确认框保持打开。

<a id="caveats"></a>
## 注意事项

- **给对话框固定的 `ID`。** 组件没有 `ID` 时会生成随机值，同一个对话框重新渲染后就对不上了；`dialog:close` 也要靠 `ID` 找到它。
- **对话框不要放在 `<table>` 里。** 对话框渲染的是 `<div>`，浏览器解析 HTML 时会把 `<tbody>` 中的 `<div>` 移到表格外面。表格行里的按钮只负责请求，对话框放进 `#modal`。下拉菜单的内容可以放在单元格中，菜单项可以直接带 `hx-get`。
- **templ 会拦截字符串形式的事件处理器。** `templ.Attributes{"onclick": "…"}` 渲染出来是一段注释，不会执行。在元素上写 `data-*` 属性，在 `app.js` 中用事件委托处理，这样也符合[内容安全策略](views.md#content-security-policy)。
- **对话框关闭后，焦点回到页面开头。** 对话框是 `hx-get` 加载的，没有关联的触发按钮。需要时可以在 `app.js` 中把焦点放回合适的元素。
- **升级组件会覆盖本地修改。** `shadcn-templ add dialog --overwrite` 用上游的版本覆盖组件。升级前先提交，再用 `git diff` 把自己的修改合并回去。
