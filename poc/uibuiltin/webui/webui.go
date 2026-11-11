// Package webui 模拟将来框架中的 web/ui：内置 shadcn-templ（go.mod 中固定版本）的样式和脚本，
// 提供主题、提示、对话框的衔接，以及绑定 web.Form、page.Offset 的组件。
//
// 组件本身直接 import shadcn-templ，例如 github.com/axadrn/shadcn-templ/v2/components/button；
// webui 只补上框架和组件之间的衔接，不包装组件。每个区域（运营后台、租户端……）用自己的 Theme，
// 自己决定是否使用 Toaster、ErrorPage 等内置部件。
package webui

import (
	"embed"
	"net/http"
	"strings"

	"github.com/ryan053857/mygoravel/web"
)

//go:embed assets
var assets embed.FS

// AssetsPath 是 webui 资源的挂载路径：router.Handle(webui.AssetsPath+"*", webui.Assets())。
// 框架化后并入 web.AssetsPath。
const AssetsPath = "/_web/ui/"

// Assets 提供预编译的样式表、组件脚本和衔接脚本。文件名带内容哈希的永久缓存。
func Assets() http.Handler {
	files := http.FileServerFS(assets)
	return http.StripPrefix(AssetsPath, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "-") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		r.URL.Path = "assets/" + r.URL.Path
		files.ServeHTTP(w, r)
	}))
}

// StylesheetURL 是 style 风格预编译的样式表：全部组件、该风格和默认主题。style 为空时为 nova。
// 使用 Tailwind 的区域改用自己生成的样式表。
func StylesheetURL(style string) string { return AssetsPath + stylesheets[or(style, "nova")] }

func scriptURL() string { return AssetsPath + scriptFile }

func glueURL() string { return AssetsPath + "glue.js" }

// ErrorTarget 是 Toaster 中放置错误片段的位置，交给 web.Options.ErrorTarget；
// ErrorFragment 把错误渲染为 toast。框架化后，局部请求的错误直接走 web:flash，不再需要它。
const ErrorTarget = "#webui-errors"

// DialogCloseEvent 是关闭对话框的事件，衔接脚本把它交给 templ.dialog.close。
const DialogCloseEvent = "web:dialog-close"

// CloseDialog 让 htmx 处理完这个响应后关闭 id 对应的对话框。框架化后为 web.CloseDialog。
func CloseDialog(w http.ResponseWriter, id string) {
	web.Trigger(w, DialogCloseEvent, map[string]any{"id": id})
}

// FormState 是 *web.Form[T] 的只读视图，表单字段组件用它回填输入、显示错误。
type FormState interface {
	Value(field string) string
	Error(field string) string
}

// Pager 是 page.Offset[T] 的分页信息。
type Pager interface {
	Links(onEachSide int) []int
	LastPage() int
}
