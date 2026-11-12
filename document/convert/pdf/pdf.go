// Package pdf 使用编译为 WebAssembly 的 PDFium 把 PDF 文档转换为 Markdown，不需要 cgo 或系统库。
//
// 每一页成为一个 convert.KindPage 类的 Section。文字行按字号分组：文字最多的字号是正文，
// 更大的短行成为标题，除非设置了 Options.NoHeadings。行距变大或字号变化处分段。
// 阅读顺序沿用 PDFium 的文字顺序，不尝试还原多栏排版和表格。没有文字层的 PDF 得到空的 Markdown，
// 以及每页一个空的 Section。需要密码才能打开的文档返回 convert.ErrEncrypted；
// 只设置了所有者密码的文档照常转换，不检查权限标志。
//
// Options.MaxPages 和 Options.MaxPageChars 限制文档的页数和一页的字符数，读取的行文字计入
// Limits.MaxOutputBytes。PDFium 用一次无法中断的调用解析一页，之后才能统计它的字符。
package pdf

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/klippa-app/go-pdfium"
	pdfiumerrors "github.com/klippa-app/go-pdfium/errors"
	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"

	"github.com/ryan053857/mygoravel/document/convert"
	"github.com/ryan053857/mygoravel/document/internal/mdwrite"
	"github.com/ryan053857/mygoravel/document/internal/source"
)

// 标题识别：标题行的字号至少是正文的 headingScale 倍，并且最多 headingMaxRunes 个字符；
// 超过 headingLevels 种的字号都映射到最低一级。
const (
	headingScale    = 1.15
	headingMaxRunes = 60
	headingLevels   = 3
)

// 默认的页数和每页字符数上限。
const (
	DefaultMaxPages     = 10000
	DefaultMaxPageChars = 1 << 20
)

// Options 配置 PDF 转换器，零值即默认配置。
type Options struct {
	convert.Limits
	// Workers 限制同时进行的转换和 PDFium 实例数，零值表示 2。
	Workers int
	// NoHeadings 把每一行都当作正文。
	NoHeadings bool
	// MaxPages 限制文档的页数。零值表示 DefaultMaxPages，负数表示不限制。
	MaxPages int
	// MaxPageChars 限制 PDFium 报告的一页的字符数，包括它生成的空格和换行。
	// 零值表示 DefaultMaxPageChars，负数表示不限制。
	MaxPageChars int
}

// Converter 转换 PDF 文档。它可以并发使用，用完必须关闭以释放 PDFium 实例。
type Converter struct {
	opts  Options
	slots chan struct{}
	done  chan struct{}

	mu       sync.Mutex
	closed   bool
	released chan struct{} // Close 释放实例后关闭
	closeErr error
	pool     pdfium.Pool
	inFlight sync.WaitGroup
}

// compiled 让进程中的所有转换器共用编译好的 PDFium 模块：编译需要几秒，只在第一个转换器启动时进行一次。
var compiled = wazero.NewCompilationCache()

// runtimeConfig 与 go-pdfium 的默认配置相同（PDFium 用 WebAssembly 的异常处理实现 setjmp/longjmp），只是加上共用的编译缓存。
func runtimeConfig() wazero.RuntimeConfig {
	return wazero.NewRuntimeConfig().
		WithCoreFeatures(api.CoreFeaturesV2 | experimental.CoreFeaturesExceptionHandling).
		WithCompilationCache(compiled)
}

// New 返回 PDF 转换器。PDFium 在第一次使用时启动。
func New(opts Options) *Converter {
	if opts.Workers <= 0 {
		opts.Workers = 2
	}
	return &Converter{opts: opts, slots: make(chan struct{}, opts.Workers), done: make(chan struct{}), released: make(chan struct{})}
}

// Close 等进行中的转换结束后释放 PDFium 实例。正在等待 worker 的调用和之后的调用返回 convert.ErrClosed。
// 每次调用 Close 都在实例释放之后返回。
func (c *Converter) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		<-c.released
		return c.closeErr
	}
	c.closed = true
	close(c.done)
	c.mu.Unlock()
	c.inFlight.Wait()
	c.mu.Lock()
	if c.pool != nil {
		c.closeErr = c.pool.Close()
		c.pool = nil
	}
	c.mu.Unlock()
	close(c.released)
	return c.closeErr
}

// acquire 占用一个 worker 位置并返回 PDFium 池，需要时启动它。
func (c *Converter) acquire(ctx context.Context) (pdfium.Pool, error) {
	select {
	case c.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		return nil, convert.ErrClosed
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		<-c.slots
		return nil, convert.ErrClosed
	}
	if c.pool == nil {
		pool, err := webassembly.Init(webassembly.Config{MaxTotal: c.opts.Workers, RuntimeConfig: runtimeConfig()})
		if err != nil {
			<-c.slots
			return nil, fmt.Errorf("pdf: start PDFium: %w", err)
		}
		c.pool = pool
	}
	c.inFlight.Add(1)
	return c.pool, nil
}

// release 归还一个 worker 位置。
func (c *Converter) release() {
	c.inFlight.Done()
	<-c.slots
}

// Convert 把 in 转换为 Markdown，每页一个 Section。
func (c *Converter) Convert(ctx context.Context, in convert.Input) (convert.Document, error) {
	pool, err := c.acquire(ctx)
	if err != nil {
		return convert.Document{}, err
	}
	defer c.release()
	limits := c.opts.Effective()
	data, err := source.Read(ctx, in, limits.MaxBytes)
	if err != nil {
		return convert.Document{}, err
	}
	if len(data) == 0 {
		return convert.Document{}, fmt.Errorf("%w: empty file", convert.ErrCorrupt)
	}
	maxPages, maxPageChars := c.opts.MaxPages, c.opts.MaxPageChars
	if maxPages == 0 {
		maxPages = DefaultMaxPages
	}
	if maxPageChars == 0 {
		maxPageChars = DefaultMaxPageChars
	}
	lines, pages, err := extract(ctx, pool, data, extractLimits{
		pages: maxPages, pageChars: maxPageChars, output: limits.MaxOutputBytes,
	})
	if err != nil {
		return convert.Document{}, err
	}
	doc, err := render(lines, pages, !c.opts.NoHeadings, limits.MaxOutputBytes)
	if err != nil {
		return convert.Document{}, err
	}
	if err := ctx.Err(); err != nil {
		return convert.Document{}, err
	}
	return doc, nil
}

// line 是页面上的一行文字，坐标单位是 PDF 点，y 轴向上。
type line struct {
	page        int
	text        string
	size        float64
	top, bottom float64
}

// extractLimits 限制文字提取，负数字段表示不限制。
type extractLimits struct {
	pages, pageChars int
	// output 限制行文字的字节数，Markdown 总是比它更长。
	output int64
}

// extract 读取每一页的文字行和页数。
func extract(ctx context.Context, pool pdfium.Pool, data []byte, limits extractLimits) ([]line, int, error) {
	instance, err := pool.GetInstanceWithContext(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = instance.Close() }()
	document, err := instance.OpenDocument(&requests.OpenDocument{File: &data})
	switch {
	case errors.Is(err, pdfiumerrors.ErrPassword):
		return nil, 0, fmt.Errorf("%w: %w", convert.ErrEncrypted, err)
	case err != nil:
		return nil, 0, fmt.Errorf("%w: %w", convert.ErrCorrupt, err)
	}
	defer func() {
		_, _ = instance.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: document.Document})
	}()
	count, err := instance.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: document.Document})
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %w", convert.ErrCorrupt, err)
	}
	if limits.pages >= 0 && count.PageCount > limits.pages {
		return nil, 0, &convert.LimitError{Limit: convert.LimitPages, Max: int64(limits.pages)}
	}
	b := &lineBuilder{output: limits.output, max: limits.output}
	for index := range count.PageCount {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		if err := readPage(ctx, instance, document.Document, index, limits.pageChars, b); err != nil {
			return nil, 0, err
		}
	}
	return b.lines, count.PageCount, nil
}

// readPage 按 PDFium 的文字顺序把第 index 页的行加入 b：字符流中的 CRLF 结束一行，
// 它生成或读到的空格分隔单词，其他控制字符按空格计。每个字符只读一次，每读一批字符检查一次取消。
func readPage(ctx context.Context, instance pdfium.Pdfium, document references.FPDF_DOCUMENT, index, maxChars int, b *lineBuilder) error {
	corrupt := func(err error) error {
		return fmt.Errorf("%w: page %d: %w", convert.ErrCorrupt, index+1, err)
	}
	text, err := instance.FPDFText_LoadPage(&requests.FPDFText_LoadPage{
		Page: requests.Page{ByIndex: &requests.PageByIndex{Document: document, Index: index}},
	})
	if err != nil {
		return corrupt(err)
	}
	defer func() { _, _ = instance.FPDFText_ClosePage(&requests.FPDFText_ClosePage{TextPage: text.TextPage}) }()
	count, err := instance.FPDFText_CountChars(&requests.FPDFText_CountChars{TextPage: text.TextPage})
	if err != nil {
		return corrupt(err)
	}
	if maxChars >= 0 && count.Count > maxChars {
		return &convert.LimitError{Limit: convert.LimitPageChars, Max: int64(maxChars)}
	}
	b.page, b.open, b.space = index, false, false
	carriageReturn := false
	for i := range count.Count {
		if i%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		char, err := instance.FPDFText_GetUnicode(&requests.FPDFText_GetUnicode{TextPage: text.TextPage, Index: i})
		if err != nil {
			return corrupt(err)
		}
		r, s := utf8.RuneError, ""
		if char.Unicode != 0 {
			s = string(rune(char.Unicode))
			r, _ = utf8.DecodeRuneInString(s)
		}
		// CR 后面跟着 LF 时是行尾，否则是控制字符。
		if carriageReturn {
			carriageReturn = false
			if r == '\n' {
				b.finish()
				continue
			}
			b.space = b.space || b.open
		}
		if s == "\r" {
			carriageReturn = true
			continue
		}
		if s == "" || unicode.IsSpace(r) || unicode.IsControl(r) {
			b.space = b.space || b.open
			continue
		}
		size, err := instance.FPDFText_GetFontSize(&requests.FPDFText_GetFontSize{TextPage: text.TextPage, Index: i})
		if err != nil {
			return corrupt(err)
		}
		rendered := size.FontSize
		if matrix, err := instance.FPDFText_GetMatrix(&requests.FPDFText_GetMatrix{TextPage: text.TextPage, Index: i}); err == nil {
			rendered *= math.Sqrt(float64(matrix.Matrix.C)*float64(matrix.Matrix.C) + float64(matrix.Matrix.D)*float64(matrix.Matrix.D))
		}
		box, err := instance.FPDFText_GetCharBox(&requests.FPDFText_GetCharBox{TextPage: text.TextPage, Index: i})
		if err != nil {
			return corrupt(err)
		}
		if err := b.add(s, rendered, box.Top, box.Bottom); err != nil {
			return err
		}
	}
	b.finish()
	return nil
}

// lineBuilder 把字符组合成行。一行的字号是其中多数字符的字号，它的外框包围所有字符。
type lineBuilder struct {
	lines       []line
	page        int
	text        []byte
	weights     map[float64]int
	open, space bool
	// output 是行文字还能使用的字节数，负数表示不限制；max 是错误中报告的上限。
	output, max int64
}

// add 把一个字符连同字号和纵向范围加入当前行，需要时开始新的一行。
func (b *lineBuilder) add(text string, size, top, bottom float64) error {
	if !b.open {
		b.lines = append(b.lines, line{page: b.page, top: top, bottom: bottom})
		b.text, b.open, b.space = b.text[:0], true, false
		if b.weights == nil {
			b.weights = map[float64]int{}
		}
	}
	grow := int64(len(text))
	if b.space {
		grow++
	}
	if b.output >= 0 {
		if grow > b.output {
			return &convert.LimitError{Limit: convert.LimitOutput, Max: b.max}
		}
		b.output -= grow
	}
	l := &b.lines[len(b.lines)-1]
	if b.space {
		b.text = append(b.text, ' ')
		b.space = false
	}
	b.text = append(b.text, text...)
	b.weights[size]++
	l.top, l.bottom = max(l.top, top), min(l.bottom, bottom)
	return nil
}

// finish 结束当前行（如果有）。
func (b *lineBuilder) finish() {
	if !b.open {
		return
	}
	l := &b.lines[len(b.lines)-1]
	l.text = string(b.text)
	best := -1
	for size, count := range b.weights {
		if count > best || count == best && size < l.size {
			l.size, best = size, count
		}
	}
	clear(b.weights)
	b.open = false
}

// render 把各行写成 Markdown，每页一个 Section。
func render(lines []line, pages int, headings bool, maxOutput int64) (convert.Document, error) {
	weights := map[float64]int{}
	for i := range lines {
		lines[i].text = strings.TrimSpace(lines[i].text)
		lines[i].size = math.Round(lines[i].size*2) / 2
		weights[lines[i].size] += utf8.RuneCountInString(lines[i].text)
	}
	body, weight := 0.0, -1
	for size, count := range weights {
		if count > weight || count == weight && size < body {
			body, weight = size, count
		}
	}
	// 短行中更大的字号从大到小映射为各级标题。
	var sizes []float64
	heading := make([]bool, len(lines))
	for i, l := range lines {
		heading[i] = headings && body > 0 && l.size >= body*headingScale &&
			utf8.RuneCountInString(l.text) <= headingMaxRunes && strings.IndexFunc(l.text, unicode.IsLetter) >= 0
		if heading[i] && !slices.Contains(sizes, l.size) {
			sizes = append(sizes, l.size)
		}
	}
	slices.SortFunc(sizes, func(a, b float64) int { return cmp.Compare(b, a) })

	w := mdwrite.New(maxOutput)
	sections := make([]convert.Section, pages)
	for page := range pages {
		sections[page] = convert.Section{Kind: convert.KindPage, Number: page + 1, Start: -1}
	}
	for i, l := range lines {
		continued := false
		if i > 0 {
			previous := lines[i-1]
			separator := "\n\n"
			// 同一页上字号和角色相同、行距正常的相邻行组成一个段落；同一级的连续标题行组成一个标题。
			if previous.page == l.page && previous.size == l.size && heading[i-1] == heading[i] &&
				previous.bottom-l.top <= (previous.top-previous.bottom)*0.8 {
				separator = "\n"
				if heading[i] {
					separator, continued = " ", true
				}
			}
			w.WriteString(separator)
		}
		if sections[l.page].Start < 0 {
			sections[l.page].Start = w.Len()
		}
		switch {
		case heading[i] && !continued:
			w.WriteString(strings.Repeat("#", min(slices.Index(sizes, l.size)+1, headingLevels)) + " " + l.text)
		case heading[i]:
			w.WriteString(l.text)
		default:
			w.WriteString(mdwrite.Paragraph(l.text))
		}
		sections[l.page].End = w.Len()
	}
	if err := w.Err(); err != nil {
		return convert.Document{}, err
	}
	// 没有文字的页在下一页文字开始处得到一个空的 Section。
	position := w.Len()
	for page := pages - 1; page >= 0; page-- {
		if sections[page].Start < 0 {
			sections[page].Start, sections[page].End = position, position
		}
		position = sections[page].Start
	}
	return convert.Document{Markdown: w.String(), Sections: sections}, nil
}
