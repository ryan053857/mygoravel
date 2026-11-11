package split

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
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
	kindBlank lineKind = iota
	kindText
	kindHeading // ATX 标题
	kindSetext  // setext 下划线
	kindBreak   // 分隔线
	kindFence   // 开始或关闭的代码围栏
	kindCode    // 围栏代码的内容
	kindIndented
	kindTable
	kindListItem
	kindListCont
	kindQuote
	kindHTML
	kindFrontMatter
)

// enclosed 报告这种行是否只提供换行边界。
func (k lineKind) enclosed() bool {
	switch k {
	case kindFence, kindCode, kindIndented, kindTable, kindHTML, kindFrontMatter:
		return true
	}
	return false
}

// line 是输入的一行；[start, end) 不含行终止符。
type line struct {
	start, end int
	kind       lineKind
	block      int // 结构块编号，空行为 -1
}

// headingBlock 是一行 ATX 标题，或者一个 setext 标题段落连同它的下划线。
type headingBlock struct {
	start, end int
	level      int
	text       string
	attached   bool // 忽略空行时，直接跟在另一个标题之后
}

// tableBlock 是一个 GFM 表格；表头行和分隔行占据 [headerStart, headerEnd)。
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

// htmlEnd 是未关闭的 HTML 块的结束条件。
type htmlEnd uint8

const (
	htmlNone htmlEnd = iota
	htmlClosingTag
	htmlComment
	htmlBlankLine
)

var (
	htmlRawTags   = []string{"script", "pre", "style", "textarea"}
	htmlBlockTags = map[string]bool{
		"address": true, "article": true, "aside": true, "base": true, "basefont": true, "blockquote": true,
		"body": true, "caption": true, "center": true, "col": true, "colgroup": true, "dd": true, "details": true,
		"dialog": true, "dir": true, "div": true, "dl": true, "dt": true, "fieldset": true, "figcaption": true,
		"figure": true, "footer": true, "form": true, "frame": true, "frameset": true, "h1": true, "h2": true,
		"h3": true, "h4": true, "h5": true, "h6": true, "head": true, "header": true, "hr": true, "html": true,
		"iframe": true, "legend": true, "li": true, "link": true, "main": true, "menu": true, "menuitem": true,
		"nav": true, "noframes": true, "ol": true, "optgroup": true, "option": true, "p": true, "param": true,
		"search": true, "section": true, "summary": true, "table": true, "tbody": true, "td": true, "tfoot": true,
		"th": true, "thead": true, "title": true, "tr": true, "track": true, "ul": true,
	}
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
			lines = append(lines, line{start: start, end: i})
			start = i + 1
		case '\r':
			lines = append(lines, line{start: start, end: i})
			if i+1 < len(text) && text[i+1] == '\n' {
				i++
			}
			start = i + 1
		}
	}
	return append(lines, line{start: start, end: len(text)})
}

// classify 为每一行确定类别和块编号，并记录标题和表格。
func (l *layout) classify(lines []line) {
	var (
		fenceChar      byte
		fenceLen       int
		inFence        bool
		html           htmlEnd
		inList         bool
		tableOpen      bool
		frontMatterEnd = l.frontMatterEnd(lines)
		blockID        = -1
		paraStart      = -1 // 当前段落的第一行
		lastNonBlank   = -1 // 上一个非空行的序号
		prevKind       = kindBlank
	)
	newBlock := func() int {
		blockID++
		return blockID
	}
	for i := range lines {
		ln := &lines[i]
		content := l.text[ln.start:ln.end]
		if i == 0 {
			content = strings.TrimPrefix(content, "\uFEFF")
		}
		indent, rest := indentation(content)
		blank := strings.TrimLeft(content, " \t") == ""
		sameAsPrev := func() int {
			if lastNonBlank < 0 {
				return newBlock()
			}
			return lines[lastNonBlank].block
		}

		switch {
		case i <= frontMatterEnd:
			ln.kind = kindFrontMatter
			if i == 0 {
				ln.block = newBlock()
			} else {
				ln.block = lines[0].block
			}
		case inFence:
			ln.kind, ln.block = kindCode, sameAsPrevLine(lines, i)
			if indent <= 3 {
				if run := leadingRun(rest, fenceChar); run >= fenceLen && isBlank(rest[run:]) {
					ln.kind, inFence = kindFence, false
				}
			}
		case html != htmlNone && (html != htmlBlankLine || !blank):
			ln.kind, ln.block = kindHTML, sameAsPrevLine(lines, i)
			if htmlEnds(html, content) {
				html = htmlNone
			}
		case tableOpen && !blank && hasUnescapedPipe(rest):
			ln.kind, ln.block = kindTable, sameAsPrevLine(lines, i)
			l.tables[len(l.tables)-1].end = ln.end
		default:
			html, tableOpen = htmlNone, false
			ln.kind, ln.block = l.classifyFresh(lines, i, indent, rest, blank, inList, prevKind, paraStart, lastNonBlank, newBlock, sameAsPrev)
			switch ln.kind {
			case kindFence:
				inFence, fenceChar, fenceLen = true, rest[0], leadingRun(rest, rest[0])
			case kindHTML:
				html = htmlStart(rest)
				if htmlEnds(html, rest[1:]) && html != htmlBlankLine {
					html = htmlNone
				}
			case kindTable:
				tableOpen = true
			case kindListItem:
				inList = true
			case kindText:
				if indent == 0 {
					inList = false
				}
			case kindHeading, kindSetext, kindBreak, kindQuote, kindIndented:
				if indent == 0 {
					inList = false
				}
			}
		}

		if ln.kind == kindBlank {
			ln.block = -1
		}
		if ln.kind == kindText {
			if prevKind != kindText {
				paraStart = i
			}
		} else {
			paraStart = -1
		}
		if ln.kind != kindBlank {
			lastNonBlank = i
		}
		prevKind = ln.kind
	}
}

// classifyFresh 归类一个不在围栏、HTML 块、表格或 front matter 延续中的行。
func (l *layout) classifyFresh(lines []line, i, indent int, rest string, blank, inList bool, prevKind lineKind,
	paraStart, lastNonBlank int, newBlock, sameAsPrev func() int) (lineKind, int) {
	ln := &lines[i]
	block := indent <= 3
	switch {
	case blank:
		return kindBlank, -1
	case block && isFenceOpen(rest):
		return kindFence, newBlock()
	case block && htmlStart(rest) != htmlNone:
		return kindHTML, newBlock()
	case block && prevKind == kindText && isSetextUnderline(rest):
		level := 1
		if rest[0] == '-' {
			level = 2
		}
		id := newBlock()
		parts := make([]string, 0, i-paraStart)
		for j := paraStart; j < i; j++ {
			lines[j].kind, lines[j].block = kindHeading, id
			parts = append(parts, strings.TrimSpace(l.text[lines[j].start:lines[j].end]))
		}
		previous := paraStart - 1
		for previous >= 0 && lines[previous].kind == kindBlank {
			previous--
		}
		l.addHeading(lines, paraStart, i, level, strings.Join(parts, " "), previous)
		return kindSetext, id
	case block && isThematicBreak(rest):
		return kindBreak, newBlock()
	case block && isListItem(rest) && (prevKind != kindText || interruptsParagraph(rest)):
		return kindListItem, newBlock()
	case inList && indent >= 1:
		return kindListCont, sameAsPrev()
	case block && atxLevel(rest) > 0:
		level := atxLevel(rest)
		l.addHeading(lines, i, i, level, atxText(rest[level:]), lastNonBlank)
		return kindHeading, newBlock()
	case block && prevKind == kindText && isDelimiterRow(rest):
		header := l.text[lines[i-1].start:lines[i-1].end]
		headerCells := tableCells(header)
		if hasUnescapedPipe(header) && len(headerCells) == len(tableCells(rest)) {
			id := newBlock()
			lines[i-1].kind, lines[i-1].block = kindTable, id
			table := tableBlock{headerStart: lines[i-1].start, headerEnd: ln.end, dataStart: ln.end, end: ln.end}
			if i+1 < len(lines) {
				table.dataStart = lines[i+1].start
			}
			table.hasHeader = slices.ContainsFunc(headerCells, func(cell string) bool { return cell != "" })
			l.tables = append(l.tables, table)
			return kindTable, id
		}
	case block && rest[0] == '>':
		if prevKind == kindQuote {
			return kindQuote, sameAsPrev()
		}
		return kindQuote, newBlock()
	case indent >= 4 && !inList && prevKind != kindText && prevKind != kindQuote && prevKind != kindListCont:
		if lastNonBlank >= 0 && lines[lastNonBlank].kind == kindIndented {
			return kindIndented, sameAsPrev()
		}
		return kindIndented, newBlock()
	}
	if inList && prevKind != kindBlank {
		return kindListCont, sameAsPrev()
	}
	if prevKind == kindText {
		return kindText, sameAsPrev()
	}
	return kindText, newBlock()
}

// addHeading 记录占据 first 到 last 行的标题；previous 是它之前的最后一个非空行。
func (l *layout) addHeading(lines []line, first, last, level int, text string, previous int) {
	attached := previous >= 0 && (lines[previous].kind == kindHeading || lines[previous].kind == kindSetext)
	l.headings = append(l.headings, headingBlock{
		start: lines[first].start, end: lines[last].end, level: level, text: text, attached: attached,
	})
}

// sameAsPrevLine 返回第 i 行之前那一行的块编号。
func sameAsPrevLine(lines []line, i int) int {
	return lines[i-1].block
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
	headingLine := make([]bool, len(lines))
	for i := range lines {
		headingLine[i] = lines[i].kind == kindHeading || lines[i].kind == kindSetext
	}
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
				case lines[i-1].kind == kindBlank && !ln.kind.enclosed():
					value = rankBlock
				case ln.kind == kindText && lines[i-1].kind == kindText && softWrap(l.text[lines[i-1].start:lines[i-1].end], l.text[ln.start:ln.end]):
					value = rankSpace
				}
			}
			if l.ranks[ln.start] != rankInvalid {
				l.ranks[ln.start] = value
			}
		}
		l.rankWithin(ln, headingLine[i])
		if ln.kind != kindBlank {
			lastNonBlank = i
		}
	}
	// 标题和它正文的开头放在一起：它们之间、以及标题块内部都不切开。
	for i := range lines {
		if !headingLine[i] {
			continue
		}
		if i+1 < len(lines) && !headingLine[i+1] {
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
func (l *layout) rankWithin(ln line, heading bool) {
	text := l.text
	enclosed := ln.kind.enclosed()
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
const zeroWidthJoiner = '\u200D'

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

// indentation 返回缩进宽度（制表符补齐到 4 的倍数）和这一行的其余部分。
func indentation(content string) (int, string) {
	width := 0
	for i := 0; i < len(content); i++ {
		switch content[i] {
		case ' ':
			width++
		case '\t':
			width += 4 - width%4
		default:
			return width, content[i:]
		}
	}
	return width, ""
}

// isBlank 报告 s 是否只含空格和制表符。
func isBlank(s string) bool {
	return strings.TrimLeft(s, " \t") == ""
}

// leadingRun 统计开头等于 c 的字节数。
func leadingRun(s string, c byte) int {
	n := 0
	for n < len(s) && s[n] == c {
		n++
	}
	return n
}

// isFenceOpen 报告 rest 是否开始一个围栏代码块。
func isFenceOpen(rest string) bool {
	if rest == "" || rest[0] != '`' && rest[0] != '~' {
		return false
	}
	run := leadingRun(rest, rest[0])
	return run >= 3 && (rest[0] == '~' || !strings.Contains(rest[run:], "`"))
}

// atxLevel 返回 ATX 标题的级别，不是标题时返回 0。
func atxLevel(rest string) int {
	level := leadingRun(rest, '#')
	if level < 1 || level > 6 {
		return 0
	}
	if level < len(rest) && rest[level] != ' ' && rest[level] != '\t' {
		return 0
	}
	return level
}

// atxText 返回去掉可选的结尾 # 串之后的标题内容。
func atxText(after string) string {
	text := strings.TrimSpace(after)
	trimmed := strings.TrimRight(text, "#")
	switch {
	case trimmed == "":
		return ""
	case len(trimmed) < len(text) && (strings.HasSuffix(trimmed, " ") || strings.HasSuffix(trimmed, "\t")):
		return strings.TrimSpace(trimmed)
	}
	return text
}

// isSetextUnderline 报告 rest 是否是一串 '=' 或 '-'，后面可以有空格。
func isSetextUnderline(rest string) bool {
	trimmed := strings.TrimRight(rest, " \t")
	if trimmed == "" || trimmed[0] != '=' && trimmed[0] != '-' {
		return false
	}
	return leadingRun(trimmed, trimmed[0]) == len(trimmed)
}

// isThematicBreak 报告 rest 是否是三个或更多 '*'、'-' 或 '_'，可以夹空格。
func isThematicBreak(rest string) bool {
	if rest == "" || !strings.ContainsRune("*-_", rune(rest[0])) {
		return false
	}
	count := 0
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case rest[0]:
			count++
		case ' ', '\t':
		default:
			return false
		}
	}
	return count >= 3
}

// isListItem 报告 rest 是否以无序或有序列表标记开头。
func isListItem(rest string) bool {
	marker := 0
	switch {
	case rest == "":
		return false
	case strings.ContainsRune("-*+", rune(rest[0])):
		marker = 1
	default:
		digits := 0
		for digits < len(rest) && digits < 10 && rest[digits] >= '0' && rest[digits] <= '9' {
			digits++
		}
		if digits == 0 || digits > 9 || digits == len(rest) || rest[digits] != '.' && rest[digits] != ')' {
			return false
		}
		marker = digits + 1
	}
	return marker == len(rest) || rest[marker] == ' ' || rest[marker] == '\t'
}

// interruptsParagraph 报告一个列表项能否打断段落：它有内容，有序列表时还要从 1 开始。
func interruptsParagraph(rest string) bool {
	marker := strings.IndexAny(rest, " \t")
	if marker < 0 || isBlank(rest[marker:]) {
		return false
	}
	if strings.ContainsRune("-*+", rune(rest[0])) {
		return true
	}
	number, err := strconv.Atoi(rest[:marker-1])
	return err == nil && number == 1
}

// htmlStart 返回从 rest 开始的 HTML 块的结束条件。
func htmlStart(rest string) htmlEnd {
	if !strings.HasPrefix(rest, "<") {
		return htmlNone
	}
	if strings.HasPrefix(rest, "<!--") {
		return htmlComment
	}
	lower := strings.ToLower(rest)
	for _, tag := range htmlRawTags {
		if after, ok := strings.CutPrefix(lower, "<"+tag); ok && (after == "" || strings.ContainsRune(" \t>", rune(after[0]))) {
			return htmlClosingTag
		}
	}
	name := strings.TrimPrefix(lower[1:], "/")
	end := 0
	for end < len(name) && (name[end] >= 'a' && name[end] <= 'z' || name[end] >= '0' && name[end] <= '9') {
		end++
	}
	if !htmlBlockTags[name[:end]] {
		return htmlNone
	}
	after := name[end:]
	if after == "" || strings.ContainsRune(" \t>", rune(after[0])) || strings.HasPrefix(after, "/>") {
		return htmlBlankLine
	}
	return htmlNone
}

// htmlEnds 报告 content 是否满足未关闭的 HTML 块的结束条件。
func htmlEnds(end htmlEnd, content string) bool {
	switch end {
	case htmlComment:
		return strings.Contains(content, "-->")
	case htmlClosingTag:
		lower := strings.ToLower(content)
		for _, tag := range htmlRawTags {
			if strings.Contains(lower, "</"+tag+">") {
				return true
			}
		}
	}
	return false
}

// hasUnescapedPipe 报告 s 中是否有前面不紧挨反斜杠的 '|'；与 GFM 一样，无论反斜杠之前是什么，
// 它都转义后面的竖线。
func hasUnescapedPipe(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '|' && (i == 0 || s[i-1] != '\\') {
			return true
		}
	}
	return false
}

// tableCells 在前面不紧挨反斜杠的竖线处把表格行切成单元格并去掉两侧空白，忽略可选的两侧竖线。
func tableCells(row string) []string {
	row = strings.TrimSpace(row)
	row = strings.TrimPrefix(row, "|")
	if strings.HasSuffix(row, "|") && !strings.HasSuffix(row, "\\|") {
		row = row[:len(row)-1]
	}
	var cells []string
	start := 0
	for i := 0; i < len(row); i++ {
		if row[i] == '|' && (i == 0 || row[i-1] != '\\') {
			cells = append(cells, strings.TrimSpace(row[start:i]))
			start = i + 1
		}
	}
	return append(cells, strings.TrimSpace(row[start:]))
}

// isDelimiterRow 报告 rest 是否是 GFM 表格的分隔行。
func isDelimiterRow(rest string) bool {
	if !strings.Contains(rest, "|") {
		return false
	}
	for _, cell := range tableCells(rest) {
		cell = strings.TrimSuffix(strings.TrimPrefix(cell, ":"), ":")
		if cell == "" || leadingRun(cell, '-') != len(cell) {
			return false
		}
	}
	return true
}
