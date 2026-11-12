package gse_test

import (
	"slices"
	"testing"

	"github.com/ryan053857/mygoravel/search"
	"github.com/ryan053857/mygoravel/search/gse"
)

func texts(tokens []search.Token) []string {
	out := make([]string, len(tokens))
	for i, t := range tokens {
		out[i] = t.Text
	}
	return out
}

func TestTokenizer(t *testing.T) {
	text := "我们用搜索引擎做全文搜索，Go语言"
	query := texts(gse.Tokenizer.Query(text))
	index := texts(gse.Tokenizer.Index(text))
	t.Logf("query: %q", query)
	t.Logf("index: %q", index)
	for _, want := range []string{"搜索引擎", "全文", "go", "语言"} {
		if !slices.Contains(query, want) {
			t.Errorf("query tokens %q lack %q", query, want)
		}
	}
	for _, want := range []string{"搜索引擎", "搜索", "引擎"} {
		if !slices.Contains(index, want) {
			t.Errorf("index tokens %q lack %q", index, want)
		}
	}
	if slices.Contains(index, "文搜") {
		t.Errorf("index tokens %q contain a bigram across words", index)
	}
	for _, tok := range gse.Tokenizer.Index(text) {
		if got := text[tok.Start:tok.End]; got != tok.Text && tok.Text != "go" {
			t.Errorf("token %q covers %q", tok.Text, got)
		}
	}
}
