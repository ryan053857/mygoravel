// Package all 把全部内置转换器注册到一个 Registry 中。
//
// 引入它会编译所有转换器的依赖，包括 PDFium 的 WebAssembly 和 excelize。
// 想让程序更小，就自己创建 convert.Registry，只注册需要的转换器。
package all

import (
	"cmp"

	"golang.org/x/text/encoding"

	"github.com/ryan053857/mygoravel/document/convert"
	"github.com/ryan053857/mygoravel/document/convert/csv"
	"github.com/ryan053857/mygoravel/document/convert/docx"
	"github.com/ryan053857/mygoravel/document/convert/html"
	"github.com/ryan053857/mygoravel/document/convert/pdf"
	"github.com/ryan053857/mygoravel/document/convert/pptx"
	"github.com/ryan053857/mygoravel/document/convert/text"
	"github.com/ryan053857/mygoravel/document/convert/xlsx"
)

// Options 配置全部内置转换器。某个转换器自己的 Limits 中为零的字段取 Limits 中的对应值；
// 自己的 Fallback 为 nil 时取 Fallback。
type Options struct {
	Limits   convert.Limits
	Fallback encoding.Encoding

	Text text.Options
	HTML html.Options
	CSV  csv.Options
	DOCX docx.Options
	PPTX pptx.Options
	XLSX xlsx.Options
	PDF  pdf.Options
}

// Registry 是注册了全部内置转换器的 convert.Registry。Close 释放 New 创建的转换器；
// 之后再注册的转换器由调用方负责。
type Registry struct {
	*convert.Registry
	pdf *pdf.Converter
}

// New 返回注册了 txt、md、json、html、csv、docx、pptx、xlsx 和 pdf 的 Registry。
func New(opts Options) *Registry {
	limits := func(own convert.Limits) convert.Limits {
		return convert.Limits{
			MaxBytes:         cmp.Or(own.MaxBytes, opts.Limits.MaxBytes),
			MaxExpandedBytes: cmp.Or(own.MaxExpandedBytes, opts.Limits.MaxExpandedBytes),
			MaxOutputBytes:   cmp.Or(own.MaxOutputBytes, opts.Limits.MaxOutputBytes),
		}
	}
	fallback := func(own encoding.Encoding) encoding.Encoding {
		if own == nil {
			return opts.Fallback
		}
		return own
	}
	opts.Text.Limits, opts.Text.Fallback = limits(opts.Text.Limits), fallback(opts.Text.Fallback)
	opts.HTML.Limits, opts.HTML.Fallback = limits(opts.HTML.Limits), fallback(opts.HTML.Fallback)
	opts.CSV.Limits, opts.CSV.Fallback = limits(opts.CSV.Limits), fallback(opts.CSV.Fallback)
	opts.DOCX.Limits = limits(opts.DOCX.Limits)
	opts.PPTX.Limits = limits(opts.PPTX.Limits)
	opts.XLSX.Limits = limits(opts.XLSX.Limits)
	opts.PDF.Limits = limits(opts.PDF.Limits)

	r := &Registry{Registry: &convert.Registry{}, pdf: pdf.New(opts.PDF)}
	r.Register(text.New(opts.Text), convert.Text, convert.Markdown)
	r.Register(text.JSON(opts.Text), convert.JSON)
	r.Register(html.New(opts.HTML), convert.HTML)
	r.Register(csv.New(opts.CSV), convert.CSV)
	r.Register(docx.New(opts.DOCX), convert.DOCX)
	r.Register(pptx.New(opts.PPTX), convert.PPTX)
	r.Register(xlsx.New(opts.XLSX), convert.XLSX)
	r.Register(r.pdf, convert.PDF)
	return r
}

// Close 释放 PDF 转换器的 PDFium 实例，可以重复调用。
func (r *Registry) Close() error {
	return r.pdf.Close()
}
