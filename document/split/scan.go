package split

import (
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// rank 是在某个字节位置之前切开的质量，越高越好。
type rank int8

const (
	rankInvalid  rank = iota - 2 // 位于 UTF-8 序列或 CRLF 中间，永远不在这里切开
	rankJoined                   // 位于字素簇中间，只有够不到簇边界时才切开
	rankNone                     // 只作为最后的手段
	rankEnclosed                 // 代码、表格、HTML 或行内代码中的标点或空格
	rankSpace
	rankClause
	rankSentence
	rankLine
	rankBlock
)

// lineKind 是一行在结构中的角色。
type lineKind uint8

const (
	kindBlank     lineKind = iota
	kindText               // 顶层段落
	kindHeading            // 顶层标题，包括 setext 标题的下划线
	kindContainer          // 列表、引用、分隔线等其他非封闭的结构
	kindEnclosed           // 代码、表格、HTML 块和 front matter，只提供换行边界
)

// line 是输入的一行；[start, end) 不含行终止符。
type line struct {
	start, end int
	kind       lineKind
	block      int // 结构块编号，空行为 -1
}

// headingBlock 是一个顶层标题：ATX 标题的一行，或者 setext 标题的段落连同它的下划线。
type headingBlock struct {
	start, end int
	level      int
	text       string
	attached   bool // 忽略空行时，直接跟在另一个标题之后
}

// tableBlock 是一个顶层 GFM 表格；表头行和分隔行占据 [headerStart, headerEnd)。
type tableBlock struct {
	headerStart, headerEnd int
	dataStart, end         int
	hasHeader              bool
}

// layout 保存切分器需要知道的关于一份输入的全部信息。
type layout struct {
	text       string
	ranks      []rank  // 每个字节位置一项，共 len(text)+1 项
	cp         []int32 // 每个字节位置之前的码点数，CRLF 计一次
	headings   []headingBlock
	tables     []tableBlock
	hardStarts []int // 不依附于上一个标题的标题开始位置，升序
	blocks     []int // 级别为 rankBlock 的位置，升序
	// headingEnds 是紧跟在标题块之后的行首位置，升序；只有标题的正文放不下时才在这里切开。
	headingEnds []int

	contentEnds map[int]int // contentEndBefore 的缓存
}

// markdown 按 CommonMark 和 GFM 表格解析块结构，可以并发使用。它不做行内解析：
// 切分只需要块结构，行内代码由 codeSpans 逐行识别，也就避开了行内解析在病态输入上的超线性开销。
var markdown = parser.NewParser(
	parser.WithBlockParsers(parser.DefaultBlockParsers()...),
	parser.WithParagraphTransformers(append(parser.DefaultParagraphTransformers(),
		util.Prioritized(extension.NewTableParagraphTransformer(), 200))...),
)

// scan 测量文本并识别它的 Markdown 结构。
func scan(text string) *layout {
	if len(text) > math.MaxInt32 {
		panic("split: input longer than math.MaxInt32 bytes")
	}
	l := &layout{text: text, ranks: make([]rank, len(text)+1), cp: make([]int32, len(text)+1)}
	l.measure()
	lines := splitLines(text)
	l.classify(lines)
	l.rankLines(lines)
	for position, value := range l.ranks {
		if value == rankBlock {
			l.blocks = append(l.blocks, position)
		}
	}
	for _, heading := range l.headings {
		if !heading.attached {
			l.hardStarts = append(l.hardStarts, heading.start)
		}
	}
	return l
}

// measure 填写码点索引，并标出会切开一个字符或 CRLF 的位置。
func (l *layout) measure() {
	text := l.text
	for i := 0; i < len(text); {
		count := l.cp[i]
		if text[i] == '\r' && i+1 < len(text) && text[i+1] == '\n' {
			l.cp[i+1], l.ranks[i+1] = count, rankInvalid
			l.cp[i+2] = count + 1
			i += 2
			continue
		}
		_, size := utf8.DecodeRuneInString(text[i:])
		for k := 1; k < size; k++ {
			l.cp[i+k], l.ranks[i+k] = count, rankInvalid
		}
		l.cp[i+size] = count + 1
		i += size
	}
}

// splitLines 在 LF、CRLF 和单独的 CR 处切分文本。以行终止符结尾时，最后一行为空。
func splitLines(text string) []line {
	var lines []line
	start := 0
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '\n':
			lines = append(lines, line{start: start, end: i, block: -1})
			start = i + 1
		case '\r':
			lines = append(lines, line{start: start, end: i, block: -1})
			if i+1 < len(text) && text[i+1] == '\n' {
				i++
			}
			start = i + 1
		}
	}
	return append(lines, line{start: start, end: len(text), block: -1})
}

// classifier 把 goldmark 的语法树映射回行。
type classifier struct {
	*layout
	lines  []line
	base   int    // source 在输入中的偏移
	source []byte // 交给 goldmark 的输入：front matter 和 BOM 之后的部分，单独的 CR 换成 LF
	nextID int
}

// classify 为每一行确定类别和块编号，并记录标题、表格和行内代码。
func (l *layout) classify(lines []line) {
	c := &classifier{layout: l, lines: lines}
	if end := l.frontMatterEnd(lines); end >= 0 {
		c.mark(0, end, kindEnclosed, true)
		if end+1 == len(lines) {
			return
		}
		c.base = lines[end+1].start
	} else if strings.HasPrefix(l.text, "\uFEFF") {
		c.base = len("\uFEFF")
	}
	// 交给 goldmark 的是一份等长的副本，语法树中的偏移因此仍然对应原文。
	c.source = []byte(l.text[c.base:])
	for i, b := range c.source {
		// goldmark 不把单独的 CR 当作换行。
		if b == '\r' && (i+1 == len(c.source) || c.source[i+1] != '\n') {
			c.source[i] = '\n'
		}
	}
	defuse(c.source)
	c.children(markdown.Parse(text.NewReader(c.source)), c.lineAt(0), len(lines)-1, true)
}

// children 依次归类 parent 的子块。子块占据从它开始的那一行到下一个子块之前的行，
// 最后一个子块到 last 为止。top 表示 parent 是文档本身。
func (c *classifier) children(parent ast.Node, first, last int, top bool) {
	for node := parent.FirstChild(); node != nil; node = node.NextSibling() {
		start, ok := c.start(node)
		if !ok {
			continue
		}
		to := last
		for next := node.NextSibling(); next != nil; next = next.NextSibling() {
			if nextStart, ok := c.start(next); ok {
				to = c.lineAt(nextStart) - 1
				break
			}
		}
		c.block(node, max(c.lineAt(start), first), min(to, last), top)
	}
}

// block 归类占据 from 到 to 行的一个块。容器中的段落、标题和分隔线沿用容器的类别；
// 任何深度的代码、表格和 HTML 块都是封闭区域，每个列表项是一个结构块。
func (c *classifier) block(node ast.Node, from, to int, top bool) {
	for to >= from && c.blank(to) {
		to--
	}
	if to < from {
		return
	}
	switch node := node.(type) {
	case *ast.FencedCodeBlock, *ast.CodeBlock, *ast.HTMLBlock:
		c.mark(from, to, kindEnclosed, true)
	case *east.Table:
		c.mark(from, to, kindEnclosed, true)
		if top {
			c.addTable(node, to)
		}
	case *ast.List:
		c.children(node, from, to, false)
	case *ast.ListItem, *ast.Blockquote:
		c.mark(from, to, kindContainer, false)
		c.children(node, from, to, false)
	case *ast.Heading:
		if top {
			c.addHeading(node, from, to)
			c.mark(from, to, kindHeading, false)
		}
	case *ast.Paragraph:
		if top {
			c.mark(from, to, kindText, false)
		}
	default:
		if top {
			c.mark(from, to, kindContainer, false)
		}
	}
}

// mark 把 from 到 to 行归入一个新的结构块；blanks 为 false 时空行保持为空行。
func (c *classifier) mark(from, to int, kind lineKind, blanks bool) {
	id := c.nextID
	c.nextID++
	for i := from; i <= to; i++ {
		if blanks || !c.blank(i) {
			c.lines[i].kind, c.lines[i].block = kind, id
		}
	}
}

// addHeading 记录占据 from 到 to 行的顶层标题。
func (c *classifier) addHeading(node *ast.Heading, from, to int) {
	parts := make([]string, 0, node.Lines().Len())
	for i := range node.Lines().Len() {
		segment := node.Lines().At(i)
		parts = append(parts, strings.TrimSpace(string(segment.Value(c.source))))
	}
	previous := from - 1
	for previous >= 0 && c.lines[previous].kind == kindBlank {
		previous--
	}
	c.headings = append(c.headings, headingBlock{
		start:    c.lines[from].start,
		end:      c.lines[to].end,
		level:    node.Level,
		text:     strings.Join(parts, " "),
		attached: previous >= 0 && c.lines[previous].kind == kindHeading,
	})
}

// addTable 记录到第 to 行结束的顶层表格。
func (c *classifier) addTable(node *east.Table, to int) {
	header, ok := node.FirstChild().(*east.TableHeader)
	if !ok {
		return
	}
	h := c.lineAt(header.Pos())
	if h+1 > to {
		return
	}
	table := tableBlock{headerStart: c.lines[h].start, headerEnd: c.lines[h+1].end, dataStart: c.lines[h+1].end, end: c.lines[to].end}
	if h+2 < len(c.lines) {
		table.dataStart = c.lines[h+2].start
	}
	for cell := header.FirstChild(); cell != nil; cell = cell.NextSibling() {
		table.hasHeader = table.hasHeader || cell.HasChildren()
	}
	c.tables = append(c.tables, table)
}

// start 返回块在 source 中的开始位置。表格的位置是它所在段落的位置，所以取表头行。
func (c *classifier) start(node ast.Node) (int, bool) {
	if table, ok := node.(*east.Table); ok && table.FirstChild() != nil {
		node = table.FirstChild()
	}
	return node.Pos(), node.Pos() >= 0
}

// lineAt 返回包含 source 中位置 position 的行。
func (c *classifier) lineAt(position int) int {
	position += c.base
	return sort.Search(len(c.lines), func(i int) bool { return c.lines[i].start > position }) - 1
}

// blank 报告第 i 行是否只含空格和制表符。
func (c *classifier) blank(i int) bool {
	return strings.TrimLeft(c.text[c.lines[i].start:c.lines[i].end], " \t") == ""
}

// frontMatterEnd 返回 front matter 结束行的序号，没有时返回 -1。
// front matter 以第一行的 "---" 开始，到下一个 "---" 或 "..." 行结束；其间每一行都必须像 YAML
// （见 yamlLine），并且至少有一行是键。
func (l *layout) frontMatterEnd(lines []line) int {
	first := strings.TrimPrefix(l.text[lines[0].start:lines[0].end], "\uFEFF")
	if strings.TrimRight(first, " \t") != "---" {
		return -1
	}
	keys := false
	for i := 1; i < len(lines); i++ {
		text := l.text[lines[i].start:lines[i].end]
		if closing := strings.TrimRight(text, " \t"); closing == "---" || closing == "..." {
			if !keys {
				return -1
			}
			return i
		}
		key, ok := yamlLine(text)
		if !ok {
			return -1
		}
		keys = keys || key
	}
	return -1
}

// yamlLine 报告 front matter 分隔符之间的一行是否像 YAML：空行、缩进行、注释、列表项，
// 或者后跟 ":" 和空格或行尾的键。key 报告这一行（缩进与否）是否为键。
func yamlLine(text string) (key, ok bool) {
	trimmed := strings.TrimLeft(text, " \t")
	switch {
	case trimmed == "", trimmed[0] == '#', trimmed == "-", strings.HasPrefix(trimmed, "- "):
		return false, true
	}
	key = mappingKey(trimmed)
	return key, key || trimmed != text
}

// mappingKey 报告 text 是否以 YAML 的键开头：":" 之前的文本，":" 后面是空格、制表符或行尾。
func mappingKey(text string) bool {
	colon := strings.Index(text, ":")
	for colon >= 0 && colon+1 < len(text) && text[colon+1] != ' ' && text[colon+1] != '\t' {
		next := strings.Index(text[colon+1:], ":")
		if next < 0 {
			return false
		}
		colon += 1 + next
	}
	return colon > 0
}

// rankLines 给行首和行内的位置定级。
func (l *layout) rankLines(lines []line) {
	lastNonBlank := -1
	for i := range lines {
		ln := lines[i]
		if i > 0 {
			value := rankLine
			if ln.kind != kindBlank && lastNonBlank >= 0 {
				prev := lines[lastNonBlank]
				switch {
				case ln.block != prev.block:
					value = rankBlock
				case lines[i-1].kind == kindBlank && ln.kind != kindEnclosed:
					value = rankBlock
				case ln.kind == kindText && lines[i-1].kind == kindText && softWrap(l.text[lines[i-1].start:lines[i-1].end], l.text[ln.start:ln.end]):
					value = rankSpace
				}
			}
			if l.ranks[ln.start] != rankInvalid {
				l.ranks[ln.start] = value
			}
		}
		l.rankWithin(ln)
		if ln.kind != kindBlank {
			lastNonBlank = i
		}
	}
	// 标题和它正文的开头放在一起：它们之间、以及标题块内部都不切开。
	for i := range lines {
		if lines[i].kind != kindHeading {
			continue
		}
		if i+1 < len(lines) && lines[i+1].kind != kindHeading {
			l.headingEnds = append(l.headingEnds, lines[i+1].start)
		}
		for j := i + 1; j < len(lines); j++ {
			l.ranks[lines[j].start] = rankNone
			if lines[j].kind != kindBlank {
				break
			}
		}
	}
	// 独立的标题是块边界；一串标题只有太长时才在标题之间切开。
	for _, heading := range l.headings {
		if heading.attached {
			l.ranks[heading.start] = rankLine
		} else {
			l.ranks[heading.start] = rankBlock
		}
	}
}

// rankWithin 给一行中每个字符之后的位置定级。
func (l *layout) rankWithin(ln line) {
	text := l.text
	heading, enclosed := ln.kind == kindHeading, ln.kind == kindEnclosed
	var spans [][2]int
	if !enclosed && !heading {
		spans = codeSpans(text[ln.start:ln.end])
	}
	span, indicators := 0, 0 // indicators 统计 position 之前紧挨着的区域指示符个数
	for position := ln.start + 1; position <= ln.end; position++ {
		if l.ranks[position] == rankInvalid {
			continue
		}
		// 只有挨着多字节字符的位置才可能在字素簇内部。
		if text[position-1] < utf8.RuneSelf && (position == ln.end || text[position] < utf8.RuneSelf) {
			indicators = 0
		} else {
			previous, _ := utf8.DecodeLastRuneInString(text[ln.start:position])
			if isRegionalIndicator(previous) {
				indicators++
			} else {
				indicators = 0
			}
			if position < ln.end && joined(previous, text[position:], indicators) {
				l.ranks[position] = rankJoined
				continue
			}
		}
		value := rankNone
		if !heading {
			value = punctuationRank(text[ln.start:position])
		}
		// 代码段已经排序，位置递增，所以当前代码段只会向后移动。
		offset := position - ln.start
		for span < len(spans) && offset >= spans[span][1] {
			span++
		}
		inside := span < len(spans) && offset > spans[span][0]
		if value > rankNone && (enclosed || inside) {
			value = rankEnclosed
		}
		l.ranks[position] = value
	}
}

// zeroWidthJoiner 把表情符号连接成一个字素簇。
const zeroWidthJoiner = '‍'

// joined 报告在 previous 与以 rest 开头的字符之间切开是否会切开一个字素簇：位于组合字符、连接符、
// 变体选择符、表情修饰符或标签字符之前，位于连接符之后，或者在国旗的两个区域指示符之间。
// indicators 是以 previous 结尾的区域指示符个数。它不借助 Unicode 属性表来近似扩展字素簇的边界。
func joined(previous rune, rest string, indicators int) bool {
	// ASCII 和 U+4000 到 U+9FFF 的三字节字符（大部分中日韩文字都在其中）永远不与前一个字符相连。
	if lead := rest[0]; previous != zeroWidthJoiner && (lead < utf8.RuneSelf || lead >= 0xE4 && lead <= 0xE9) {
		return false
	}
	next, _ := utf8.DecodeRuneInString(rest)
	switch {
	case previous == zeroWidthJoiner, next == zeroWidthJoiner:
		return true
	case unicode.In(next, unicode.Mn, unicode.Me, unicode.Mc, unicode.Variation_Selector):
		return true
	case next >= 0x1F3FB && next <= 0x1F3FF, next >= 0xE0020 && next <= 0xE007F:
		return true
	}
	return isRegionalIndicator(next) && indicators%2 == 1
}

// isRegionalIndicator 报告 r 是否是组成国旗的字母之一。
func isRegionalIndicator(r rune) bool {
	return r >= 0x1F1E6 && r <= 0x1F1FF
}

// punctuationRank 给 prefix 最后一个字符之后的切点定级。
func punctuationRank(prefix string) rank {
	last, size := utf8.DecodeLastRuneInString(prefix)
	switch last {
	case '。', '！', '？':
		return rankSentence
	case '，', '；':
		return rankClause
	case ' ', '\t':
		before, _ := utf8.DecodeLastRuneInString(prefix[:len(prefix)-size])
		switch before {
		case '.', '!', '?':
			return rankSentence
		case ',', ';':
			return rankClause
		}
		return rankSpace
	}
	return rankNone
}

// softWrap 报告两行相邻文本之间的换行是否只是句子的折行。
func softWrap(previous, current string) bool {
	if strings.HasSuffix(previous, "  ") || strings.HasSuffix(previous, "\\") {
		return false
	}
	last, _ := utf8.DecodeLastRuneInString(strings.TrimRight(previous, " \t"))
	first, _ := utf8.DecodeRuneInString(strings.TrimLeft(current, " \t"))
	if !unicode.IsLetter(last) && !unicode.IsNumber(last) {
		return false
	}
	return unicode.IsLetter(first) || unicode.IsNumber(first) || strings.ContainsRune("，。、；：！？）」』》,.;:!?)", first)
}

// codeSpans 返回一行中配对的反引号代码段的字节区间，升序。
// 一串反引号开始一个代码段，由下一串同样长度的反引号关闭；代码段内部的反引号是内容。
func codeSpans(content string) [][2]int {
	type run struct{ start, length int }
	var runs []run
	byLength := map[int][]int{} // 按长度分组的反引号串序号，升序
	for i := 0; i < len(content); {
		if content[i] != '`' {
			i++
			continue
		}
		length := leadingRun(content[i:], '`')
		byLength[length] = append(byLength[length], len(runs))
		runs = append(runs, run{i, length})
		i += length
	}
	var spans [][2]int
	next := map[int]int{} // 每种长度在 byLength 中第一个尚未越过的候选
	for i := 0; i < len(runs); i++ {
		candidates := byLength[runs[i].length]
		k := next[runs[i].length]
		for k < len(candidates) && candidates[k] <= i {
			k++
		}
		next[runs[i].length] = k
		if k == len(candidates) {
			continue
		}
		closing := runs[candidates[k]]
		spans = append(spans, [2]int{runs[i].start, closing.start + closing.length})
		i = candidates[k]
	}
	return spans
}

// leadingRun 统计开头等于 c 的字节数。
func leadingRun(s string, c byte) int {
	n := 0
	for n < len(s) && s[n] == c {
		n++
	}
	return n
}
