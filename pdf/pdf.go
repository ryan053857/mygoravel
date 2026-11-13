// Package pdf 把 HTML 页面生成为 PDF。
//
// Document 是一个 templ 组件和它引用的文件，纸张、方向、边距和页码都用 CSS @page 设置。
// Renderer 由驱动实现：pdf/gotenberg 调用 Gotenberg 服务中的 Chromium，pdf/pdftest 用于测试。
//
//	data, err := renderer.Render(ctx, pdf.Document{HTML: views.Invoice(inv), Assets: assets})
package pdf

import (
	"context"
	"io/fs"

	"github.com/a-h/templ"

	"github.com/go-tenon/tenon/errorx"
)

// Document 是一份 PDF：一个 HTML 页面和它引用的文件。
type Document struct {
	// HTML 渲染出整个页面。纸张、边距和页码用 CSS @page 设置，没有 @page size 时为 A4；
	// PDF 的标题取自 <title>。
	HTML templ.Component
	// Assets 是页面引用的样式、图片和字体，可以为 nil。只能包含顶层文件，不能有子目录，
	// 也不能有 index.html；HTML 中按文件名引用，例如 href="invoice.css"。
	Assets fs.FS
}

// Renderer 把 Document 生成为 PDF，实现可以并发使用。
type Renderer interface {
	Render(ctx context.Context, doc Document) ([]byte, error)
}

// ErrUnavailable 表示生成服务暂时不可用：连不上、排队已满或超时。
var ErrUnavailable = errorx.Define(errorx.Unavailable, "pdf.unavailable")
