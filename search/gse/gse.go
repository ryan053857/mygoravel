// Package gse 提供按词语切分中文的 search.Tokenizer，基于 github.com/go-ego/gse 的简体和繁体中文词典。
//
// 与默认的 search.Bigram 相比，它按词语建索引，查询“文搜”不会再命中“全文搜索”，结果更准确；
// 代价是程序大约增大 25 MB（嵌入的词典），第一次分词时加载词典需要约 0.5 秒，常驻约 300 MB 内存。
//
//	var Articles = search.Define[ArticleDoc]("articles", search.WithTokenizer(gse.Tokenizer))
//
// 建索引时除了整个词，还输出长词中包含的两字词和三字词（“搜索引擎”还输出“搜索”“引擎”），
// 查询短词也能命中长词。词典中没有的词按单字切分，查询时同样按单字切分，仍然可以命中。
package gse

import (
	"sync"
	"unicode/utf8"

	"github.com/go-ego/gse"

	"github.com/go-tenon/tenon/search"
)

// Tokenizer 按词语切分中文，其他文字的切分与 search.Bigram 相同。词典在第一次分词时加载，所有索引共用。
var Tokenizer search.Tokenizer = &tokenizer{}

type tokenizer struct {
	once sync.Once
	seg  gse.Segmenter
}

func (t *tokenizer) segmenter() *gse.Segmenter {
	t.once.Do(func() {
		t.seg.SkipLog = true
		if err := t.seg.LoadDictEmbed("zh"); err != nil {
			panic("search/gse: load embedded dictionary: " + err.Error())
		}
	})
	return &t.seg
}

func (t *tokenizer) Index(text string) []search.Token {
	return search.Split(text, func(run string, start int) []search.Token { return t.words(run, start, true) })
}

func (t *tokenizer) Query(text string) []search.Token {
	return search.Split(text, func(run string, start int) []search.Token { return t.words(run, start, false) })
}

// words 切分一段连续的中日韩文字。sub 为 true 时还输出长词中包含的、词典中有的两字词和三字词。
func (t *tokenizer) words(run string, start int, sub bool) []search.Token {
	seg := t.segmenter()
	var out []search.Token
	for _, s := range seg.Segment([]byte(run)) {
		word := run[s.Start():s.End()]
		out = append(out, search.Token{Text: word, Start: start + s.Start(), End: start + s.End()})
		if !sub {
			continue
		}
		n := utf8.RuneCountInString(word)
		if n < 3 {
			continue
		}
		offsets := make([]int, 0, n+1) // 每个字在 word 中的字节位置，最后是 len(word)
		for i := range word {
			offsets = append(offsets, i)
		}
		offsets = append(offsets, len(word))
		for size := 2; size <= 3 && size < n; size++ {
			for i := 0; i+size <= n; i++ {
				part := word[offsets[i]:offsets[i+size]]
				if freq, _, ok := seg.Find(part); ok && freq > 0 {
					from := start + s.Start() + offsets[i]
					out = append(out, search.Token{Text: part, Start: from, End: from + len(part)})
				}
			}
		}
	}
	return out
}
