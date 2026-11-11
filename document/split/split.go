package split

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Options 配置 Splitter。长度按码点计算，CRLF 和单独的 CR 算作一个 LF，无效 UTF-8 序列的每个字节算一个。
type Options struct {
	// Size 是目标分段长度，必须为正数。
	Size int
	// MaxSize 是分段长度的硬上限。零表示 Size + Size/5，略长于 Size 的小节因此保持完整，
	// 不会留下很短的尾段；非零时不能小于 Size。
	MaxSize int
	// Overlap 是相邻分段的最大重叠，从 0 到 Size-1。
	Overlap int
}

// Splitter 把 Markdown 切分为分段。它不可变，可以并发使用。
type Splitter struct {
	size, maxSize, overlap int
}

// New 检查 opts 并返回 Splitter。
func New(opts Options) (*Splitter, error) {
	switch {
	case opts.Size <= 0:
		return nil, errors.New("split: Size must be positive")
	case opts.MaxSize < 0:
		return nil, errors.New("split: MaxSize must not be negative")
	case opts.Overlap < 0 || opts.Overlap >= opts.Size:
		return nil, fmt.Errorf("split: Overlap must be in [0, %d)", opts.Size)
	}
	maxSize := opts.MaxSize
	if maxSize == 0 {
		maxSize = opts.Size + min(opts.Size/5, math.MaxInt-opts.Size)
	}
	if maxSize < opts.Size {
		return nil, errors.New("split: MaxSize must be at least Size")
	}
	return &Splitter{size: opts.Size, maxSize: maxSize, overlap: opts.Overlap}, nil
}

// Chunk 是输入中连续的一个字节区间。
type Chunk struct {
	// Text 是 input[Start:End]，与输入共用内存。
	Text string
	// Start 和 End 是在输入中的字节偏移。
	Start, End int
	// Length 是按 Options 的标准测量的 Text 长度。
	Length int
	// Headings 是继承的标题路径，最外层在前。不包括从分段第一个非空白字节开始或包含它的标题。
	Headings []Heading
	// TableHeader 在分段的第一个非空白字节位于表格正文中时设置。
	TableHeader *TableHeader
}

// Heading 是分段继承的一个标题。
type Heading struct {
	// Level 是 1 到 6。
	Level int
	// Text 是标题的行内 Markdown，不含块标记。
	Text string
	// Start 和 End 界定标题块在输入中的位置，不含行终止符。
	Start, End int
}

// TableHeader 是分段开始处所在表格的表头行和分隔行。
type TableHeader struct {
	// Text 是这两行在输入中的原文。
	Text string
	// Start 和 End 界定这两行在输入中的位置，不含最后的行终止符。
	Start, End int
}

// Context 渲染标题路径和表头：标题文本用 " > " 连接（不转义），表头原样；两部分之间用一个换行分隔，
// 为空的部分不加分隔符，都为空时返回 ""，结尾不加换行。它的长度不受 Options 限制：
// 很长的标题或很宽的表头会完整地出现在它下面的每一段中，有向量长度预算的调用方需要自己截断，
// 或者用 Headings 和 TableHeader 自己组装。
func (c Chunk) Context() string {
	parts := make([]string, 0, 2)
	if len(c.Headings) > 0 {
		texts := make([]string, len(c.Headings))
		for i, heading := range c.Headings {
			texts[i] = heading.Text
		}
		parts = append(parts, strings.Join(texts, " > "))
	}
	if c.TableHeader != nil {
		parts = append(parts, c.TableHeader.Text)
	}
	return strings.Join(parts, "\n")
}

// TextWithContext 返回 Context、一个空行和 Text；Context 为空时只返回 Text。
func (c Chunk) TextWithContext() string {
	context := c.Context()
	if context == "" {
		return c.Text
	}
	return context + "\n\n" + c.Text
}

// Split 把 text 切分为分段，只有空白的文本得到 nil。text 超过 math.MaxInt32 字节时 panic。
func (s *Splitter) Split(text string) []Chunk {
	if strings.TrimFunc(text, unicode.IsSpace) == "" {
		return nil
	}
	l := scan(text)
	b := builder{layout: l}
	var chunks []Chunk
	prevEnd := 0
	for start := 0; ; {
		// 标题之前的空白并入标题所在的分段。
		hard := l.nextHardStart(start)
		for hard < len(text) && l.contentEndBefore(hard) <= start {
			hard = l.nextHardStart(hard)
		}
		end := hard
		if l.length(start, hard) > s.maxSize {
			end = s.cut(l, start, hard, prevEnd)
		}
		chunks = append(chunks, b.chunk(start, end))
		if end == len(text) {
			return chunks
		}
		next := end
		if !l.isHardStart(end) {
			next = s.overlapStart(l, start, end)
		}
		prevEnd, start = end, next
	}
}

// cut 在到 hard 为止的文本太长时，选择从 start 开始的分段在哪里结束。
func (s *Splitter) cut(l *layout, start, hard, prevEnd int) int {
	base := int(l.cp[start])
	span := int(l.cp[hard]) - base
	minimum := max(s.overlap+1, s.size/2)
	slack := s.maxSize - s.size
	contentEnd := max(l.contentEndBefore(hard), start)
	// 切点两侧都必须有内容：在第一个非空白字符之后、最后一个之前。
	// 第一处内容只在 MaxSize 以内寻找，超出之后没有切点能包含它。
	window := l.lastPositionAt(base + s.maxSize)
	contentStart := start
	for contentStart < contentEnd && contentStart <= window {
		r, size := utf8.DecodeRuneInString(l.text[contentStart:])
		if !unicode.IsSpace(r) {
			break
		}
		contentStart += size
	}
	// 短于 MaxSize 的结尾空白可以并入最后的内容，因此切点放在它之前；更长的结尾空白本来就要单独成段。
	tailAlone := l.length(contentEnd, hard) >= s.maxSize
	usable := func(i int) bool { return i > contentStart && (i < contentEnd || tailAlone) }

	// MaxSize 以内有块边界时，把到下一个标题为止的文本均匀分成几段；否则只均分当前的块。
	lo := l.positionAt(base + minimum)
	remaining, upper := int(l.cp[l.nextBlock(lo)])-base, s.size
	if remaining <= s.maxSize {
		remaining, upper = span, s.maxSize
	}
	pieces := ceilDiv(remaining-s.overlap, s.size-s.overlap)
	target := ceilDiv(remaining+(pieces-1)*s.overlap, pieces)

	// pick 在 [from, to] 中返回级别不低于 floor、可用的最高级边界，并且最靠近目标位置。
	// strict 时，超过 Size 的边界必须是块边界，并且不能在下一个标题之前留下余量以内的尾巴。
	pick := func(from, to int, floor rank, strict bool) (int, rank) {
		best, bestRank, bestDistance := -1, floor-1, 0
		for i := from; i <= to; i++ {
			value := l.ranks[i]
			length := int(l.cp[i]) - base
			if value < floor || !usable(i) ||
				strict && (length > s.size && value != rankBlock || int(l.cp[hard])-int(l.cp[i]) <= slack) {
				continue
			}
			distance := abs(length - target)
			if value > bestRank || value == bestRank && distance < bestDistance {
				best, bestRank, bestDistance = i, value, distance
			}
		}
		return best, bestRank
	}
	best, bestRank := pick(lo, l.lastPositionAt(base+upper), rankEnclosed, true)
	if bestRank > rankEnclosed {
		return best
	}
	// 一行或一句仍然放得进 MaxSize 时保持完整。
	if wider, _ := pick(lo, l.lastPositionAt(base+s.maxSize), rankSpace, false); wider >= 0 {
		return wider
	}
	// 标题放得下而正文第一行放不下时，保持标题完整。
	if i := sort.SearchInts(l.headingEnds, window+1) - 1; i >= 0 && l.headingEnds[i] > max(start, prevEnd) && usable(l.headingEnds[i]) {
		return l.headingEnds[i]
	}
	// 否则切在最小长度之前的最后一个行边界。
	for i := lo - 1; i > max(start, prevEnd); i-- {
		if l.ranks[i] >= rankLine && usable(i) {
			return i
		}
	}
	if best >= 0 {
		return best
	}
	// 完全没有边界：切在目标位置，尽量留在内容之内。
	position := l.positionAt(base + target)
	if position >= contentEnd && contentStart < contentEnd && !tailAlone {
		// 把内容的最后一个字符留给下一段，避免它只剩空白。
		_, size := utf8.DecodeLastRuneInString(l.text[:contentEnd])
		position = contentEnd - size
		if position <= max(start, prevEnd, contentStart) {
			position = contentEnd
		}
	}
	if after := int(l.cp[contentStart]) + 1; position <= contentStart && contentStart < contentEnd && after-base <= s.maxSize {
		position = l.positionAt(after)
	}
	// 分段必须越过上一段的结束位置。
	if floor := max(start, prevEnd); position <= floor {
		position = l.positionAt(int(l.cp[floor]) + 1)
	}
	// 一个字素簇的某一端在可及范围内时保持它完整，切点移到较近的一端（距离相同时取后一端）。
	// 后一端可能进入结尾的空白，这些空白无法在 MaxSize 以内并入这一段，于是单独成段。
	if l.ranks[position] == rankJoined {
		before, after := -1, -1
		for i := position - 1; i > max(start, prevEnd, contentStart); i-- {
			if l.ranks[i] >= rankNone {
				before = i
				break
			}
		}
		for i, limit := position+1, l.lastPositionAt(base+s.maxSize); i <= limit; i++ {
			if l.ranks[i] >= rankNone {
				after = i
				break
			}
		}
		switch {
		case after >= 0 && (before < 0 || l.length(position, after) <= l.length(before, position)):
			return after
		case before >= 0:
			return before
		}
	}
	return position
}

// overlapStart 返回 [start, end) 之后的分段从哪里开始：重叠窗口内级别最高、最靠前的边界；
// 窗口内没有边界时返回 end。
func (s *Splitter) overlapStart(l *layout, start, end int) int {
	lo := l.positionAt(int(l.cp[end]) - s.overlap)
	if lo <= start {
		return end
	}
	tail := end
	for tail > lo {
		r, size := utf8.DecodeLastRuneInString(l.text[:tail])
		if !unicode.IsSpace(r) {
			break
		}
		tail -= size
	}
	best, bestRank := end, rankEnclosed
	for i := lo; i < tail; i++ {
		if l.ranks[i] > bestRank {
			best, bestRank = i, l.ranks[i]
		}
	}
	return best
}

// builder 把字节区间转换为分段，并随着分段前进跟踪标题路径。
type builder struct {
	*layout
	stack       []headingBlock
	nextHeading int
	table       int
}

// chunk 为 [start, end) 生成分段。
func (b *builder) chunk(start, end int) Chunk {
	anchor := start
	for anchor < end {
		r, size := utf8.DecodeRuneInString(b.text[anchor:])
		if !unicode.IsSpace(r) {
			break
		}
		anchor += size
	}
	for b.nextHeading < len(b.headings) && b.headings[b.nextHeading].start < anchor {
		item := b.headings[b.nextHeading]
		for len(b.stack) > 0 && b.stack[len(b.stack)-1].level >= item.level {
			b.stack = b.stack[:len(b.stack)-1]
		}
		b.stack = append(b.stack, item)
		b.nextHeading++
	}
	// 去掉从锚点开始或包含锚点的标题，以及它的同级和下级标题。
	path := b.stack
	if n := len(path); n > 0 && path[n-1].end > anchor {
		path = path[:n-1]
	}
	if b.nextHeading < len(b.headings) && b.headings[b.nextHeading].start == anchor {
		for len(path) > 0 && path[len(path)-1].level >= b.headings[b.nextHeading].level {
			path = path[:len(path)-1]
		}
	}
	c := Chunk{Text: b.text[start:end], Start: start, End: end, Length: b.length(start, end)}
	if len(path) > 0 {
		c.Headings = make([]Heading, len(path))
		for i, item := range path {
			c.Headings[i] = Heading{Level: item.level, Text: item.text, Start: item.start, End: item.end}
		}
	}
	for b.table < len(b.tables) && b.tables[b.table].end <= anchor {
		b.table++
	}
	if b.table < len(b.tables) {
		if table := b.tables[b.table]; table.hasHeader && table.dataStart <= anchor && anchor < table.end {
			c.TableHeader = &TableHeader{
				Text: b.text[table.headerStart:table.headerEnd], Start: table.headerStart, End: table.headerEnd,
			}
		}
	}
	return c
}

// length 返回 text[start:end] 的测量长度。
func (l *layout) length(start, end int) int {
	return int(l.cp[end] - l.cp[start])
}

// positionAt 返回码点索引不小于 c 的第一个有效位置。
func (l *layout) positionAt(c int) int {
	i := sort.Search(len(l.cp), func(i int) bool { return int(l.cp[i]) >= c })
	for i < len(l.text) && l.ranks[i] == rankInvalid {
		i++
	}
	return i
}

// lastPositionAt 返回码点索引不大于 c 的最后一个有效位置。
func (l *layout) lastPositionAt(c int) int {
	i := sort.Search(len(l.cp), func(i int) bool { return int(l.cp[i]) > c }) - 1
	for i > 0 && l.ranks[i] == rankInvalid {
		i--
	}
	return i
}

// nextHardStart 返回 position 之后第一个独立标题的开始位置，没有时返回输入长度。
func (l *layout) nextHardStart(position int) int {
	i := sort.SearchInts(l.hardStarts, position+1)
	if i == len(l.hardStarts) {
		return len(l.text)
	}
	return l.hardStarts[i]
}

// isHardStart 报告 position 是否是一个独立标题的开始。
func (l *layout) isHardStart(position int) bool {
	i := sort.SearchInts(l.hardStarts, position)
	return i < len(l.hardStarts) && l.hardStarts[i] == position
}

// nextBlock 返回不早于 position 的第一个块边界，没有时返回输入长度。
func (l *layout) nextBlock(position int) int {
	i := sort.SearchInts(l.blocks, position)
	if i == len(l.blocks) {
		return len(l.text)
	}
	return l.blocks[i]
}

// contentEndBefore 返回 hard 之前最后一个非空白字符之后的位置，没有时返回 0。
// 结果按 hard 缓存，很长的结尾空白只扫描一次。
func (l *layout) contentEndBefore(hard int) int {
	if end, ok := l.contentEnds[hard]; ok {
		return end
	}
	end := lastContentEnd(l.text, 0, hard)
	if l.contentEnds == nil {
		l.contentEnds = make(map[int]int)
	}
	l.contentEnds[hard] = end
	return end
}

// lastContentEnd 返回 text[start:end] 中最后一个非空白字符之后的位置。
func lastContentEnd(text string, start, end int) int {
	for end > start {
		r, size := utf8.DecodeLastRuneInString(text[start:end])
		if !unicode.IsSpace(r) {
			break
		}
		end -= size
	}
	return end
}

// ceilDiv 向上取整的除法，两个操作数都是正数。
func ceilDiv(a, b int) int {
	return (a + b - 1) / b
}

// abs 返回 n 的绝对值。
func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
