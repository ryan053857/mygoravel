package docs

import (
	"cmp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark/ast"
)

// searchPath 是搜索页相对 Prefix 的地址。
const searchPath = "_search"

// maxResults 是一次搜索最多返回的结果数。
const maxResults = 20

// entry 是搜索索引中的一项：一页开头的正文，或者一个二、三级标题下的正文。
type entry struct {
	page    *page
	heading heading // 页面开头的正文时为零值
	text    string
	// lower 是小写的页面标题、小节标题和正文，用于匹配。
	lowerTitle, lowerHeading, lowerText string
}

// index 按小节收集 doc 中的文字，在 outline 加上锚点链接之前调用。
func index(p *page, doc ast.Node, src []byte) []entry {
	cur := &entry{page: p}
	var out []*entry
	var b strings.Builder
	flush := func() {
		cur.text = strings.Join(strings.Fields(b.String()), " ")
		b.Reset()
		out = append(out, cur)
	}
	for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
		if h, ok := n.(*ast.Heading); ok && (h.Level == 2 || h.Level == 3) {
			if v, ok := h.AttributeString("id"); ok {
				flush()
				id, _ := v.([]byte)
				cur = &entry{page: p, heading: heading{Level: h.Level, ID: string(id), Text: plainText(h, src)}}
				continue
			}
		}
		b.WriteString(blockText(n, src))
		b.WriteByte(' ')
	}
	flush()
	entries := make([]entry, 0, len(out))
	for _, e := range out {
		e.lowerTitle = strings.ToLower(p.title + " " + p.description)
		e.lowerHeading = strings.ToLower(e.heading.Text)
		e.lowerText = strings.ToLower(e.text)
		entries = append(entries, *e)
	}
	return entries
}

// blockText 返回一个块中的文字，包括代码块的内容。
func blockText(n ast.Node, src []byte) string {
	var b strings.Builder
	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch c.(type) {
		case *ast.FencedCodeBlock, *ast.CodeBlock:
			for i := 0; i < c.Lines().Len(); i++ {
				line := c.Lines().At(i)
				b.Write(line.Value(src))
			}
			b.WriteByte(' ')
			return ast.WalkSkipChildren, nil
		case *ast.Paragraph, *ast.Heading, *ast.TextBlock:
			b.WriteString(plainText(c, src))
			b.WriteByte(' ')
			return ast.WalkSkipChildren, nil
		case *ast.Text, *ast.String: // 表格的单元格
			b.WriteString(plainText(c, src))
			b.WriteByte(' ')
		}
		return ast.WalkContinue, nil
	})
	return b.String()
}

// result 是一条搜索结果。
type result struct {
	page    *page
	heading heading
	snippet []fragment
	score   int
	order   int
}

// fragment 是摘要中的一段文字，mark 为 true 时是匹配的词。
type fragment struct {
	text string
	mark bool
}

// search 返回包含 q 中全部词（空白分隔，不区分大小写）的页面和小节：页面标题中匹配的排在最前，
// 其次是小节标题，再次是正文，同分的按侧边栏的顺序。页面标题只为页面开头那一项计分，
// 小节借用页面标题凑齐全部词，但至少要有一个词出现在小节自己的标题或正文中。
func (t *tree) search(q string) []result {
	terms := strings.Fields(strings.ToLower(q))
	if len(terms) == 0 {
		return nil
	}
	var out []result
	for i, e := range t.entries {
		score, top := 0, e.heading.ID == ""
		for _, term := range terms {
			switch {
			case top && strings.Contains(e.lowerTitle, term):
				score += 4
			case strings.Contains(e.lowerHeading, term):
				score += 2
			case strings.Contains(e.lowerText, term):
				score++
			case strings.Contains(e.lowerTitle, term): // 小节借用页面标题，不计分
			default:
				score = -1
			}
			if score < 0 {
				break
			}
		}
		if score > 0 {
			out = append(out, result{page: e.page, heading: e.heading, snippet: snippet(e, terms), score: score, order: i})
		}
	}
	slices.SortStableFunc(out, func(a, b result) int { return cmp.Or(cmp.Compare(b.score, a.score), cmp.Compare(a.order, b.order)) })
	if len(out) > maxResults {
		out = out[:maxResults]
	}
	return out
}

// snippetRunes 是摘要在第一个匹配之前和之后保留的字数。
const snippetRunes = 40

// snippet 返回正文中第一个匹配附近的文字，标出其中的全部匹配。正文中没有匹配时返回开头的文字。
func snippet(e entry, terms []string) []fragment {
	text, lower := e.text, e.lowerText
	if len(text) != len(lower) {
		// 个别字符小写后字节数不同，无法按位置对应，只显示小写的正文。
		text = lower
	}
	start := len(text)
	for _, term := range terms {
		if i := strings.Index(lower, term); i >= 0 && i < start {
			start = i
		}
	}
	if start == len(text) {
		start = 0
	}
	from := start
	for n := 0; from > 0 && n < snippetRunes; n++ {
		_, size := utf8.DecodeLastRuneInString(text[:from])
		from -= size
	}
	to := start
	for n := 0; to < len(text) && n < 2*snippetRunes; n++ {
		_, size := utf8.DecodeRuneInString(text[to:])
		to += size
	}
	var out []fragment
	if from > 0 {
		out = append(out, fragment{text: "…"})
	}
	for pos := from; pos < to; {
		next, length := to, 0
		for _, term := range terms {
			if i := strings.Index(lower[pos:to], term); i >= 0 && pos+i < next {
				next, length = pos+i, len(term)
			}
		}
		if next > pos {
			out = append(out, fragment{text: text[pos:next]})
		}
		if length == 0 {
			break
		}
		end := min(next+length, to)
		out = append(out, fragment{text: text[next:end], mark: true})
		pos = end
	}
	if to < len(text) {
		out = append(out, fragment{text: "…"})
	}
	return out
}
