// Package convert 定义文档怎样转换为 Markdown：Document 和 Section 类型、Converter 接口、
// 按格式分派的 Registry、资源上限 Limits，以及转换器报告的错误。
//
// 转换器放在各自的子包中，每个子包只引入自己的依赖：convert/text、convert/html、convert/csv、
// convert/docx、convert/pptx、convert/xlsx 和 convert/pdf；convert/all 注册全部转换器。
//
// 内置转换器遵守同样的规则：
//
//   - 输出是合法的 UTF-8，换行为 LF，没有 BOM 和 NUL 字节；除格式本身的需要外，内容不做裁剪或改写。
//   - Section 按最终的 Markdown 记录，之后不再改写 Markdown。一个 Section 包括为它生成的全部内容，
//     例如生成的标题；内置转换器的 Section 互不重叠，它们之间的文本不属于任何 Section。
//     没有文字的页、幻灯片和工作表保留一个空的 Section，编号保持稳定。
//   - 会形成 Markdown 结构的原文会被转义：行首的 #、>、列表标记、代码围栏和表格中的竖线。
//   - 文档本身的问题用 %w 包装 ErrEncrypted、ErrCorrupt 等错误，并保留底层原因；读取失败、
//     PDFium 启动失败等 I/O 和运行时错误只附加上下文，不归类。取消时 errors.Is(err, context.Canceled)
//     或 context.DeadlineExceeded 成立。失败时 Document 为零值。
//   - 文本类格式（纯文本、Markdown、JSON、CSV、HTML）的空输入得到空的 Document；二进制格式
//     （DOCX、PPTX、XLSX、PDF）的空输入是 ErrCorrupt。没有文字层的 PDF 得到空的 Markdown，
//     以及每页一个空的 Section。
//   - 原件最多读取 MaxBytes+1 字节以发现超限。输出在生成过程中计入上限：每个块、每行表格、每行文字
//     加入时就检查，文档不会先生成远超上限的内容再失败。由第三方库生成输出时，先限制交给它的输入。
//   - Word 和 PowerPoint 的 XML 解析为最多 256 层嵌套的树，更深的是 ErrCorrupt；每个元素在自身字节之外
//     再计 64 字节到 MaxExpandedBytes，解析后的树最多约为上限的两倍。Excel 的 XML 在交给表格库之前
//     做同样的深度检查。
//   - 在读取之间、等待 worker 时、XML 记号和 PDF 字符的批次之间，以及每页、每张幻灯片、每个工作表、
//     每行或每个块之间检查取消。阻塞中的 Read 或单次第三方调用不会被打断。
//   - 内置转换器可以并发使用。自定义转换器自己负责并发安全，Registry 在调用它之前释放锁。
//
// 因此 split 得到的分段字节区间，可以用 Document.SectionsIn 映射回原件的页、幻灯片或工作表。
package convert

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"

	"github.com/go-tenon/tenon/errorx"
)

// Format 是规范化的文件扩展名：小写、不带点、别名已经换成标准名称。用 ParseFormat 或 FormatOf 得到它。
type Format string

// 内置转换器支持的格式。
const (
	Text     Format = "txt"
	Markdown Format = "md"
	JSON     Format = "json"
	HTML     Format = "html"
	CSV      Format = "csv"
	DOCX     Format = "docx"
	PPTX     Format = "pptx"
	XLSX     Format = "xlsx"
	PDF      Format = "pdf"
)

// formatAliases 把扩展名的别名映射到标准格式。"yaml" 没有内置转换器，这个别名供自定义转换器使用。
var formatAliases = map[string]Format{
	"markdown": Markdown,
	"htm":      HTML,
	"yml":      "yaml",
	"text":     Text,
}

// ParseFormat 规范化格式名或扩展名，例如 "PDF"、".pdf" 或 "markdown"。
func ParseFormat(s string) Format {
	s = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "."))
	if alias, ok := formatAliases[s]; ok {
		return alias
	}
	return Format(s)
}

// FormatOf 按文件名或路径的扩展名返回格式。
func FormatOf(filename string) (Format, bool) {
	base := filename[strings.LastIndexAny(filename, `/\`)+1:]
	dot := strings.LastIndexByte(base, '.')
	if dot <= 0 || dot == len(base)-1 {
		return "", false
	}
	return ParseFormat(base[dot+1:]), true
}

// Input 是一份原件。
type Input struct {
	// Reader 提供文档内容，转换器不会关闭它。
	Reader io.Reader
	// Name 可选，在错误信息中标识这份文档。
	Name string
	// Charset 可选，声明文本编码，例如取自 HTTP 响应头。
	Charset string
	// BaseURL 可选，用于解析 HTML 中的相对链接。
	BaseURL string
}

// Document 是由原件转换得到的 Markdown。
type Document struct {
	// Markdown 是合法的 UTF-8，换行为 LF。
	Markdown string
	// Sections 记录原件的页、幻灯片或工作表在 Markdown 中的位置，按 Start 排序。
	Sections []Section
}

// SectionKind 是 Section 所代表的原件单位。
type SectionKind string

// 内置转换器使用的 Section 类别。
const (
	KindPage  SectionKind = "page"
	KindSlide SectionKind = "slide"
	KindSheet SectionKind = "sheet"
)

// Section 是原件中可以引用的一个单位，以及它在 Document.Markdown 中的字节区间。
type Section struct {
	Kind SectionKind
	// Number 是这个单位在原件中的位置，从 1 开始，空的和跳过的单位也计数。
	Number int
	// Name 是已知的工作表名、幻灯片标题或页码标签。
	Name string
	// Start 和 End 界定这个单位的 Markdown，包括为它生成的标题。
	Start, End int
}

// SectionsIn 返回区间与 [start, end) 相交的 Section，结果是新的切片。
// 空的 Section 和空的区间永远不相交。
func (d Document) SectionsIn(start, end int) []Section {
	var sections []Section
	for _, s := range d.Sections {
		if start < end && s.Start < s.End && s.Start < end && start < s.End {
			sections = append(sections, s)
		}
	}
	return sections
}

// Converter 把一份原件转换为 Markdown。
type Converter interface {
	Convert(ctx context.Context, in Input) (Document, error)
}

// ConverterFunc 把函数适配为 Converter。
type ConverterFunc func(ctx context.Context, in Input) (Document, error)

// Convert 调用 f。
func (f ConverterFunc) Convert(ctx context.Context, in Input) (Document, error) {
	return f(ctx, in)
}

// Registry 把格式映射到转换器。零值可以直接使用，也可以并发使用。格式经过 ParseFormat 规范化，
// 后注册的替换先注册的。Registry 不会关闭它持有的转换器。
type Registry struct {
	mu         sync.RWMutex
	converters map[Format]Converter
}

// Register 把 c 注册为 formats 的转换器。c 为 nil 时 panic。
func (r *Registry) Register(c Converter, formats ...Format) {
	if c == nil {
		panic("convert: Register with a nil Converter")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.converters == nil {
		r.converters = make(map[Format]Converter)
	}
	for _, f := range formats {
		r.converters[ParseFormat(string(f))] = c
	}
}

// Lookup 返回为 f 注册的转换器。
func (r *Registry) Lookup(f Format) (Converter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.converters[ParseFormat(string(f))]
	return c, ok
}

// Formats 返回已注册的格式，按名称排序。
func (r *Registry) Formats() []Format {
	r.mu.RLock()
	defer r.mu.RUnlock()
	formats := make([]Format, 0, len(r.converters))
	for f := range r.converters {
		formats = append(formats, f)
	}
	slices.Sort(formats)
	return formats
}

// Convert 用为 f 注册的转换器转换 in。没有注册时返回包装了 ErrUnsupported 的错误。
func (r *Registry) Convert(ctx context.Context, f Format, in Input) (Document, error) {
	c, ok := r.Lookup(f)
	if !ok {
		return Document{}, fmt.Errorf("%w: %q", ErrUnsupported, ParseFormat(string(f)))
	}
	return c.Convert(ctx, in)
}

// 默认的上限。
const (
	DefaultMaxBytes         = 32 << 20
	DefaultMaxExpandedBytes = 128 << 20
	DefaultMaxOutputBytes   = 64 << 20
)

// Limits 限制一次转换可以使用的资源。每个内置转换器的 Options 都嵌入了 Limits；
// Registry.Convert 自己不施加任何上限。零值字段取默认值，负数表示不限制。
type Limits struct {
	// MaxBytes 限制读取的原件字节数，默认 32 MiB。
	MaxBytes int64
	// MaxExpandedBytes 限制从压缩包格式中解压读取的字节数，默认 128 MiB；Word 和 PowerPoint
	// 解析的每个 XML 元素额外计 64 字节，HTML 展开合并单元格时按复制量的估计值计入。
	MaxExpandedBytes int64
	// MaxOutputBytes 限制生成的 Markdown 字节数，默认 64 MiB。
	MaxOutputBytes int64
}

// Effective 返回填入默认值后的上限，-1 表示不限制。
func (l Limits) Effective() Limits {
	pick := func(value, fallback int64) int64 {
		switch {
		case value == 0:
			return fallback
		case value < 0:
			return -1
		}
		return value
	}
	return Limits{
		MaxBytes:         pick(l.MaxBytes, DefaultMaxBytes),
		MaxExpandedBytes: pick(l.MaxExpandedBytes, DefaultMaxExpandedBytes),
		MaxOutputBytes:   pick(l.MaxOutputBytes, DefaultMaxOutputBytes),
	}
}

// 转换器报告的错误。转换器用 %w 包装它们，并在错误链中保留底层原因。
// 前四个属于 errorx.InvalidArgument：action 直接返回时得到 422 和翻译好的提示，
// 在队列任务中应当用 queue.Permanent 标记，重试也不会成功。
var (
	// ErrUnsupported 表示没有这种格式的转换器。
	ErrUnsupported = errorx.Define(errorx.InvalidArgument, "document.unsupported_format")
	// ErrEncrypted 表示文档加了密码。
	ErrEncrypted = errorx.Define(errorx.InvalidArgument, "document.encrypted")
	// ErrCorrupt 表示文档已损坏或不是声明的格式。
	ErrCorrupt = errorx.Define(errorx.InvalidArgument, "document.corrupt")
	// ErrTooLarge 表示超出了 Limits 中的某个上限，具体是哪一个见 LimitError。
	ErrTooLarge = errorx.Define(errorx.InvalidArgument, "document.too_large")
	// ErrClosed 表示转换器已经关闭。这是程序的错误，不是文档的问题。
	ErrClosed = errors.New("convert: converter closed")
)

// LimitError 使用的上限名称。LimitPages 和 LimitPageChars 是 PDF 转换器的页数和每页字符数上限。
const (
	LimitSource    = "source"
	LimitExpanded  = "expanded"
	LimitOutput    = "output"
	LimitPages     = "pages"
	LimitPageChars = "page_chars"
)

// LimitError 表示转换超出了 Limits 中的某个上限，errors.Is(err, ErrTooLarge) 对它成立。
type LimitError struct {
	// Limit 是上面的上限名称之一。
	Limit string
	// Max 是上限的值：原件和输出是字节数；展开是字节数或 HTML 复制量的估计值；
	// LimitPages 是页数；LimitPageChars 是字符数。
	Max int64
}

// Error 说明超出了哪个上限。
func (e *LimitError) Error() string {
	return fmt.Sprintf("convert: %s limit of %d exceeded", e.Limit, e.Max)
}

// Unwrap 返回 ErrTooLarge，因此 errors.Is(err, ErrTooLarge) 成立，errorx.KindOf 也能得到它的分类。
func (e *LimitError) Unwrap() error { return ErrTooLarge }
