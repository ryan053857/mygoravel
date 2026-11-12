package search

import (
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func texts(tokens []Token) []string {
	out := make([]string, len(tokens))
	for i, t := range tokens {
		out[i] = t.Text
	}
	return out
}

func TestBigram(t *testing.T) {
	for text, want := range map[string][]string{
		"全文搜索":           {"全文", "文搜", "搜索"},
		"搜":              {"搜"},
		"Go语言, iPhone15": {"go", "语言", "iphone15"},
		"ＡＢＣ１２３":         {"abc123"},
		"東京タワーへ行く":       {"東京", "京タ", "タワ", "ワー", "ーへ", "へ行", "行く"},
		"한국어 검색":         {"한국", "국어", "검색"},
		"café naïve":     {"café", "naïve"},
		"  !!  ":         nil,
	} {
		if got := texts(Bigram.Index(text)); !slices.Equal(got, want) {
			t.Errorf("%q: %q, want %q", text, got, want)
		}
	}
	// 位置是原文中的字节位置
	text := "Ａ全文"
	for _, tok := range Bigram.Index(text) {
		if tok.Text == "a" && text[tok.Start:tok.End] != "Ａ" || tok.Text == "全文" && text[tok.Start:tok.End] != "全文" {
			t.Errorf("token %+v covers %q", tok, text[tok.Start:tok.End])
		}
	}
}

func TestParseQuery(t *testing.T) {
	for text, want := range map[string]queryTerms{
		"datab":                 {terms: []string{"datab"}, prefix: true},
		"datab ":                {terms: []string{"datab"}},
		"go go":                 {terms: []string{"go"}, prefix: true},
		"a b a":                 {terms: []string{"b", "a"}, prefix: true},
		"全文搜索":                  {terms: []string{"全文", "文搜", "搜索"}, prefix: true},
		strings.Repeat("x", 40): {terms: []string{strings.Repeat("x", maxTerm)}, prefix: true},
	} {
		if got := parseQuery(Bigram, text); !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %+v, want %+v", text, got, want)
		}
	}
	many := strings.Repeat("词 ", 40)
	if got := parseQuery(Bigram, strings.Repeat("a", 1)+" "+many); len(got.terms) > maxQueryTerms {
		t.Errorf("%d terms", len(got.terms))
	}
}

func TestTermTruncatesOnRuneBoundary(t *testing.T) {
	s := term(strings.Repeat("中", 20)) // 60 字节
	if len(s) != 30 || s != strings.Repeat("中", 10) {
		t.Fatalf("term = %q (%d bytes)", s, len(s))
	}
}

// TestEncodingOrder 确保编码后的值按字节比较的顺序与值的大小顺序一致。
func TestEncodingOrder(t *testing.T) {
	type doc struct {
		I int64     `search:"filter"`
		U uint      `search:"filter"`
		F float64   `search:"filter"`
		T time.Time `search:"filter"`
		B bool      `search:"filter"`
		S string    `search:"text"`
	}
	s, err := parseSchema(reflect.TypeFor[doc]())
	if err != nil {
		t.Fatal(err)
	}
	for name, values := range map[string][]any{
		"I": {int64(math.MinInt64), int64(-100), int64(-1), int64(0), int64(1), int64(99), int64(math.MaxInt64)},
		"U": {uint(0), uint(9), uint(10), uint(math.MaxUint)},
		"F": {math.Inf(-1), -1e10, -1.5, -0.0001, 0.0, 0.0001, 2.5, 1e10, math.Inf(1)},
		"T": {time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("x", 8*3600)), time.Date(2026, 1, 1, 0, 0, 0, 1, time.UTC)},
		"B": {false, true},
	} {
		f := s.filters[name]
		var prev string
		for i, v := range values {
			enc, err := f.encodeAny(v)
			if err != nil {
				t.Fatal(err)
			}
			if i > 0 && enc <= prev {
				t.Errorf("%s: %v encodes to %q, not after %q", name, v, enc, prev)
			}
			prev = enc
		}
	}
	if got := s.filters["I"].decode(mustEncode(t, s.filters["I"], int64(-42))); got != "-42" {
		t.Errorf("decode int = %q", got)
	}
	if got := s.filters["F"].decode(mustEncode(t, s.filters["F"], -1.5)); got != "-1.5" {
		t.Errorf("decode float = %q", got)
	}
	if _, err := s.filters["F"].encodeAny(math.NaN()); err == nil {
		t.Error("NaN encoded")
	}
	if _, err := s.filters["I"].encodeAny("1"); err == nil {
		t.Error("string encoded as int")
	}
}

func mustEncode(t *testing.T, f *field, v any) string {
	t.Helper()
	s, err := f.encodeAny(v)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestHighlight(t *testing.T) {
	q := parseQuery(Bigram, "搜索引擎")
	s, ok := highlight(Bigram, q, "用搜索引擎和引擎盖")
	want := Snippet{{Text: "用"}, {Text: "搜索引擎", Mark: true}, {Text: "和"}, {Text: "引擎", Mark: true}, {Text: "盖"}}
	if !ok || !slices.Equal(s, want) {
		t.Fatalf("snippet = %#v", s)
	}
	if _, ok := highlight(Bigram, q, "没有命中"); ok {
		t.Fatal("highlighted text without a match")
	}
	// 前缀匹配的最后一个词标出整个词
	s, _ = highlight(Bigram, parseQuery(Bigram, "go datab"), "Go and Databases")
	if want := (Snippet{{Text: "Go", Mark: true}, {Text: " and "}, {Text: "Databases", Mark: true}}); !slices.Equal(s, want) {
		t.Fatalf("prefix snippet = %#v", s)
	}
	// 命中在结尾时向前补足长度
	text := strings.Repeat("字", 300) + "搜索"
	s, _ = highlight(Bigram, q, text)
	if n := len([]rune(s.String())); n != SnippetLength+1 || !strings.HasPrefix(s.String(), "…") {
		t.Fatalf("snippet %q has %d runes", s.String(), n)
	}
}

func TestMaxTerms(t *testing.T) {
	type doc struct {
		Title string `search:"text,boost"`
		Body  string `search:"text"`
	}
	ix := Define[doc]("long")
	entries, err := ix.entries([]doc{{Title: "标题", Body: strings.Repeat("长文", MaxTerms)}})
	if err != nil {
		t.Fatal(err)
	}
	e := entries[0]
	if n := len(e.boost) + len(e.body); n != MaxTerms || len(e.boost) != 1 {
		t.Fatalf("indexed %d terms (%d boost)", n, len(e.boost))
	}
	// 正文只有“长文”“文长”两个词，每个词最多记录 maxPositions 个位置
	v := tsvector(e.boost, e.body)
	if n := strings.Count(v, "B"); n != 2*maxPositions {
		t.Fatalf("tsvector has %d body positions: %.200s", n, v)
	}
}
