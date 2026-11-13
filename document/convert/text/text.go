// Package text 转换纯文本、Markdown 和 JSON。
//
// 纯文本和 Markdown 只做解码和规范化，内容原样输出，因此切分时把纯文本当作 Markdown 读取，
// 大多数纯文本本来也是这样写的。JSON 放进代码围栏，只会在行之间切分。
package text

import (
	"context"

	"golang.org/x/text/encoding"

	"github.com/go-tenon/tenon/document/convert"
	"github.com/go-tenon/tenon/document/internal/mdwrite"
	"github.com/go-tenon/tenon/document/internal/source"
	"github.com/go-tenon/tenon/document/internal/textdecode"
)

// Options 配置文本转换器，零值即默认配置。
type Options struct {
	convert.Limits
	// Fallback 解码既没有声明编码、又不是合法 UTF-8 的输入，例如 simplifiedchinese.GB18030。
	// nil 时把无效字节替换为 U+FFFD。
	Fallback encoding.Encoding
}

// New 返回纯文本和 Markdown 的转换器。
func New(opts Options) convert.Converter {
	return converter{opts: opts}
}

// JSON 返回把 JSON 放进 json 代码围栏的转换器。
func JSON(opts Options) convert.Converter {
	return converter{opts: opts, fence: "json"}
}

// converter 解码文本，需要时放进代码围栏。
type converter struct {
	opts  Options
	fence string
}

// Convert 解码 in 并作为 Markdown 输出。
func (c converter) Convert(ctx context.Context, in convert.Input) (convert.Document, error) {
	limits := c.opts.Effective()
	data, err := source.Read(ctx, in, limits.MaxBytes)
	if err != nil {
		return convert.Document{}, err
	}
	text := textdecode.Normalize(textdecode.Decode(data, in.Charset, c.opts.Fallback))
	if text == "" {
		return convert.Document{}, nil
	}
	w := mdwrite.New(limits.MaxOutputBytes)
	if c.fence != "" {
		w.Fence(c.fence, text)
	} else {
		w.WriteString(text)
	}
	if err := w.Err(); err != nil {
		return convert.Document{}, err
	}
	return convert.Document{Markdown: w.String()}, nil
}
