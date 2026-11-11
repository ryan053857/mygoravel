// Package html 把 HTML 转换为带 GFM 表格的 CommonMark。
//
// 编码依次取自 BOM、Input.Charset（必须是已知编码）、<body> 之前第一个 <meta> 中声明的字符集
// （与浏览器一样，UTF-16 按 UTF-8 读取），最后是 UTF-8 或 Options.Fallback。
// 转换之前，表格从最内层开始展开为没有合并单元格的矩形：合并的单元格（按 HTML 标准的上限截断，
// 并且不超出所在的行组）重复内容，较短的行补齐，复制量的估计值必须在 Limits.MaxExpandedBytes 之内；
// 包含另一个表格的表格交给转换库处理，它把这类表格渲染为内层表格前后的文字。
// 嵌套超过 8 层的引用块和列表按第 8 层渲染。没有表头单元格的表格把第一行提升为表头，
// 相对链接按 Input.BaseURL 解析。
package html

import (
	"bytes"
	"context"
	"fmt"
	"mime"
	"strings"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/table"
	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/htmlindex"

	"github.com/ryan053857/mygoravel/document/convert"
	"github.com/ryan053857/mygoravel/document/internal/mdwrite"
	"github.com/ryan053857/mygoravel/document/internal/source"
	"github.com/ryan053857/mygoravel/document/internal/textdecode"
)

// Options 配置 HTML 转换器，零值即默认配置。
type Options struct {
	convert.Limits
	// Fallback 解码既没有声明编码、又不是合法 UTF-8 的输入，例如 simplifiedchinese.GB18030。
	// nil 时把无效字节替换为 U+FFFD。
	Fallback encoding.Encoding
}

// Converter 转换 HTML，可以并发使用。
type Converter struct {
	opts      Options
	converter *converter.Converter
}

// New 返回 HTML 转换器。
func New(opts Options) *Converter {
	return &Converter{opts: opts, converter: converter.NewConverter(converter.WithPlugins(
		base.NewBasePlugin(),
		commonmark.NewCommonmarkPlugin(),
		table.NewTablePlugin(
			table.WithSpanCellBehavior(table.SpanBehaviorMirror),
			table.WithNewlineBehavior(table.NewlineBehaviorPreserve),
			table.WithCellPaddingBehavior(table.CellPaddingBehaviorMinimal),
			table.WithHeaderPromotion(true),
		),
	))}
}

// Convert 把 in 转换为 Markdown。
func (c *Converter) Convert(ctx context.Context, in convert.Input) (convert.Document, error) {
	limits := c.opts.Effective()
	data, err := source.Read(ctx, in, limits.MaxBytes)
	if err != nil {
		return convert.Document{}, err
	}
	declared := in.Charset
	if textdecode.Lookup(declared) == nil {
		declared = metaCharset(data)
		// 能按 ASCII 读出的 <meta> 不可能是 UTF-16；与 HTML 标准的预扫描一样，这种声明按 UTF-8 处理。
		if e := textdecode.Lookup(declared); e != nil {
			if name, _ := htmlindex.Name(e); name == "utf-16le" || name == "utf-16be" {
				declared = "utf-8"
			}
		}
	}
	text := textdecode.Decode(data, declared, c.opts.Fallback)
	if strings.TrimSpace(text) == "" {
		return convert.Document{}, nil
	}
	doc, err := xhtml.Parse(strings.NewReader(text))
	if err != nil {
		return convert.Document{}, fmt.Errorf("%w: %w", convert.ErrCorrupt, err)
	}
	limitNesting(doc)
	if err := prepareTables(doc, limits.MaxExpandedBytes, in.BaseURL); err != nil {
		return convert.Document{}, err
	}
	options := []converter.ConvertOptionFunc{converter.WithContext(ctx)}
	if in.BaseURL != "" {
		options = append(options, converter.WithDomain(in.BaseURL))
	}
	output, err := c.converter.ConvertNode(doc, options...)
	// 转换库不检查 ctx，所以在它返回之后检查一次取消。
	if ctxErr := ctx.Err(); ctxErr != nil {
		return convert.Document{}, ctxErr
	}
	if err != nil {
		return convert.Document{}, fmt.Errorf("%w: %w", convert.ErrCorrupt, err)
	}
	markdown := string(output)
	w := mdwrite.New(limits.MaxOutputBytes)
	w.WriteString(textdecode.Normalize(markdown))
	if err := w.Err(); err != nil {
		return convert.Document{}, err
	}
	return convert.Document{Markdown: w.String()}, nil
}

// maxNesting 是保留的引用块和列表的最深嵌套。每一层都会给其中的每一行加前缀或缩进，
// 转换库还会为每一层把内层的 Markdown 重写一遍。
const maxNesting = 8

// nesting 是受 maxNesting 限制嵌套深度的元素。
var nesting = map[atom.Atom]bool{atom.Blockquote: true, atom.Ul: true, atom.Ol: true, atom.Menu: true}

// limitNesting 把嵌套超过 maxNesting 的引用块和列表，以及这类列表的列表项，改为 div 元素，
// 使它们的内容按保留的最深一层渲染。
func limitNesting(doc *xhtml.Node) {
	type frame struct {
		node  *xhtml.Node
		depth int
	}
	stack := []frame{{doc, 0}}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for child := current.node.LastChild; child != nil; child = child.PrevSibling {
			depth := current.depth
			if child.Type == xhtml.ElementNode && nesting[child.DataAtom] {
				depth++
				if depth > maxNesting {
					flattened := child.DataAtom != atom.Blockquote
					child.DataAtom, child.Data = atom.Div, "div"
					for item := child.FirstChild; item != nil && flattened; item = item.NextSibling {
						if item.Type == xhtml.ElementNode && item.DataAtom == atom.Li {
							item.DataAtom, item.Data = atom.Div, "div"
						}
					}
				}
			}
			stack = append(stack, frame{child, depth})
		}
	}
}

// metaCharset 返回 <body> 之前第一个 <meta> 声明的字符集，没有时返回 ""。
func metaCharset(data []byte) string {
	tokenizer := xhtml.NewTokenizer(bytes.NewReader(data))
	for {
		switch tokenizer.Next() {
		case xhtml.ErrorToken:
			return ""
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken:
			token := tokenizer.Token()
			if token.Data == "body" {
				return ""
			}
			if token.Data != "meta" {
				continue
			}
			name, content, contentType := "", "", false
			for _, attr := range token.Attr {
				switch attr.Key {
				case "charset":
					name = attr.Val
				case "content":
					content = attr.Val
				case "http-equiv":
					contentType = strings.EqualFold(attr.Val, "content-type")
				}
			}
			// http-equiv 形式把字符集声明为媒体类型的参数。
			if _, params, err := mime.ParseMediaType(content); name == "" && contentType && err == nil {
				name = params["charset"]
			}
			if name != "" {
				return name
			}
		}
	}
}
