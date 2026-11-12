package search

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Token 是分词结果中的一个词。
type Token struct {
	Text  string // 规范化之后的词，例如转为小写；只有它进入索引
	Start int    // 在原文中的起始字节位置，用于高亮
	End   int    // 在原文中的结束字节位置（不含）
}

// Tokenizer 把文本切分为词。建索引和查询必须使用同一个 Tokenizer，修改分词方式后要重建索引。
type Tokenizer interface {
	// Index 切分要建索引的文本。它可以比 Query 输出更多的词，例如长词中包含的短词，
	// 让查询短词时也能命中长词。
	Index(text string) []Token
	// Query 切分查询文本。查询要求每个词都出现在文档中。
	Query(text string) []Token
}

// Bigram 是默认的分词器，不需要词典：中文、日文和韩文按相邻的两个字切分，例如“全文搜索”切分为
// “全文”“文搜”“搜索”，只有一个字时就是这个字；其他文字按连续的字母和数字切分为词。
// 字母转为小写，全角字母和数字转为半角。
//
// 它不认识词语，查询“文搜”也会命中“全文搜索”，召回多、精度一般。需要按词语切分时使用 search/gse。
var Bigram Tokenizer = bigram{}

type bigram struct{}

func (bigram) Index(text string) []Token { return Split(text, Bigrams) }
func (bigram) Query(text string) []Token { return Split(text, Bigrams) }

// Bigrams 把一段连续的中日韩文字切分为相邻的两个字，只有一个字时返回这个字，供 Split 使用。
func Bigrams(run string, start int) []Token {
	var out []Token
	prev, prevStart := rune(-1), 0
	for i, r := range run {
		if prev >= 0 {
			out = append(out, Token{Text: string([]rune{prev, r}), Start: start + prevStart, End: start + i + utf8.RuneLen(r)})
		}
		prev, prevStart = r, i
	}
	if len(out) == 0 && prev >= 0 {
		out = append(out, Token{Text: run, Start: start, End: start + len(run)})
	}
	return out
}

// Split 是实现 Tokenizer 的基础：它把 text 分为连续的字母数字片段和连续的中日韩文字片段，
// 前者作为一个词（转为小写，全角转为半角），后者交给 cjk 切分，其余字符（空白、标点、符号）是分隔符。
//
// cjk 收到的 run 是 text 中的片段，start 是它在 text 中的字节位置，返回的 Token 的位置要加上它。
func Split(text string, cjk func(run string, start int) []Token) []Token {
	var out []Token
	var b strings.Builder
	runStart, runCJK := -1, false
	flush := func(end int) {
		if runStart < 0 {
			return
		}
		if runCJK {
			out = append(out, cjk(text[runStart:end], runStart)...)
		} else {
			out = append(out, Token{Text: b.String(), Start: runStart, End: end})
		}
		b.Reset()
		runStart = -1
	}
	for i, r := range text {
		r = fold(r)
		var isCJK bool
		switch {
		case isCJKRune(r):
			isCJK = true
		case unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r):
		default:
			flush(i)
			continue
		}
		if runStart >= 0 && runCJK != isCJK {
			flush(i)
		}
		if runStart < 0 {
			runStart, runCJK = i, isCJK
		}
		if !isCJK {
			b.WriteRune(r)
		}
	}
	flush(len(text))
	return out
}

// fold 把全角 ASCII 转为半角，字母转为小写。
func fold(r rune) rune {
	if r >= 0xFF01 && r <= 0xFF5E {
		r -= 0xFEE0
	}
	return unicode.ToLower(r)
}

func isCJKRune(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) || r == 'ー' || r == '々'
}

// maxTerm 是一个词进入索引的最大字节数，更长的词截断到这个长度。
const maxTerm = 32

// maxQueryTerms 是一次查询最多使用的词数，更多的词被忽略。
const maxQueryTerms = 32

// term 返回 Token 进入索引的形式：截断到 maxTerm 字节，不截断半个字符。
func term(s string) string {
	if len(s) <= maxTerm {
		return s
	}
	end := maxTerm
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}

// queryTerms 是查询文本切分后的词。
type queryTerms struct {
	terms  []string // 去重之后的词，每个都必须出现在文档中
	prefix bool     // 最后一个词按前缀匹配：查询文本以它结尾，用户可能还没有输入完
}

func parseQuery(tok Tokenizer, text string) queryTerms {
	var q queryTerms
	seen := map[string]bool{}
	tokens := tok.Query(text)
	for i, t := range tokens {
		s := term(t.Text)
		if s == "" || len(q.terms) == maxQueryTerms {
			continue
		}
		last := i == len(tokens)-1
		if seen[s] && !last {
			continue
		}
		if seen[s] {
			// 最后一个词与前面的词重复：去掉前面的，让它按前缀匹配
			q.terms = removeTerm(q.terms, s)
		}
		seen[s] = true
		q.terms = append(q.terms, s)
		q.prefix = last && t.End == len(text)
	}
	return q
}

func removeTerm(terms []string, s string) []string {
	out := terms[:0]
	for _, t := range terms {
		if t != s {
			out = append(out, t)
		}
	}
	return out
}

// matches 报告索引中的词 s 是否命中查询。
func (q queryTerms) matches(s string) bool {
	for i, t := range q.terms {
		if s == t || (q.prefix && i == len(q.terms)-1 && strings.HasPrefix(s, t)) {
			return true
		}
	}
	return false
}
