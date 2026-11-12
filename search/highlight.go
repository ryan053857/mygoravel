package search

import (
	"slices"
	"strings"
	"unicode/utf8"
)

// SnippetLength 是摘要的最大字符数。文本字段不超过它时摘要是全文，否则从第一个命中的词前面不远处开始截取。
const SnippetLength = 160

// snippetLead 是截取摘要时保留在第一个命中的词前面的字符数。
const snippetLead = 30

// Part 是摘要中的一段文字。
type Part struct {
	Text string `json:"text"`
	Mark bool   `json:"mark,omitempty"` // 命中查询的词
}

// Snippet 是一个文本字段的摘要，按顺序分为命中和未命中查询的部分。截取时首尾加上省略号。
// 页面按 Part 渲染，命中的部分放在 <mark> 中，不需要拼接 HTML。
type Snippet []Part

// String 返回摘要的文字。
func (s Snippet) String() string {
	var b strings.Builder
	for _, p := range s {
		b.WriteString(p.Text)
	}
	return b.String()
}

// highlight 在 text 中标出命中 q 的词，没有命中时 ok 为 false。
func highlight(tok Tokenizer, q queryTerms, text string) (s Snippet, ok bool) {
	type span struct{ start, end int }
	var spans []span
	for _, t := range tok.Index(text) {
		if t.Start < t.End && t.End <= len(text) && q.matches(term(t.Text)) {
			spans = append(spans, span{t.Start, t.End})
		}
	}
	if len(spans) == 0 {
		return nil, false
	}
	slices.SortFunc(spans, func(a, b span) int { return a.start - b.start })
	merged := spans[:1]
	for _, sp := range spans[1:] {
		last := &merged[len(merged)-1]
		if sp.start <= last.end {
			last.end = max(last.end, sp.end)
		} else {
			merged = append(merged, sp)
		}
	}

	from, to := window(text, merged[0].start)
	if from > 0 {
		s = append(s, Part{Text: "…"})
	}
	pos := from
	for _, sp := range merged {
		start, end := max(sp.start, from), min(sp.end, to)
		if start >= end {
			continue
		}
		if start > pos {
			s = append(s, Part{Text: text[pos:start]})
		}
		s = append(s, Part{Text: text[start:end], Mark: true})
		pos = end
	}
	if pos < to {
		s = append(s, Part{Text: text[pos:to]})
	}
	if to < len(text) {
		s = append(s, Part{Text: "…"})
	}
	return mergeText(s), true
}

// window 返回摘要在 text 中的字节范围：不超过 SnippetLength 个字符，first 前面保留 snippetLead 个字符。
func window(text string, first int) (from, to int) {
	if utf8.RuneCountInString(text) <= SnippetLength {
		return 0, len(text)
	}
	from = first
	for n := 0; n < snippetLead && from > 0; n++ {
		_, size := utf8.DecodeLastRuneInString(text[:from])
		from -= size
	}
	to = from
	for n := 0; n < SnippetLength && to < len(text); n++ {
		_, size := utf8.DecodeRuneInString(text[to:])
		to += size
	}
	// 结尾不够长时向前补足，让摘要尽量有 SnippetLength 个字符
	for n := utf8.RuneCountInString(text[from:to]); n < SnippetLength && from > 0; n++ {
		_, size := utf8.DecodeLastRuneInString(text[:from])
		from -= size
	}
	return from, to
}

// mergeText 合并相邻的未命中部分，例如开头的省略号和后面的文字。
func mergeText(s Snippet) Snippet {
	out := s[:0]
	for _, p := range s {
		if n := len(out); n > 0 && !p.Mark && !out[n-1].Mark {
			out[n-1].Text += p.Text
			continue
		}
		out = append(out, p)
	}
	return out
}
