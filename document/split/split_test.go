package split

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

var update = flag.Bool("update", false, "rewrite golden files")

// mustNew 返回 Splitter，出错时让测试失败。
func mustNew(t testing.TB, opts Options) *Splitter {
	t.Helper()
	s, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// measure 不依赖实现，按 CRLF 和单独的 CR 各算一个的方式统计码点。
func measure(s string) int {
	n := 0
	for i := 0; i < len(s); {
		if s[i] == '\r' && i+1 < len(s) && s[i+1] == '\n' {
			i += 2
		} else {
			_, size := utf8.DecodeRuneInString(s[i:])
			i += size
		}
		n++
	}
	return n
}

// boundaries 返回 s 的每个字节位置是否可以合法切开。
func boundaries(s string) []bool {
	legal := make([]bool, len(s)+1)
	for i := 0; i < len(s); {
		legal[i] = true
		if s[i] == '\r' && i+1 < len(s) && s[i+1] == '\n' {
			i += 2
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	legal[len(s)] = true
	return legal
}

// checkContract 检查一次切分是否满足文档中的保证。
func checkContract(t testing.TB, s *Splitter, text string, chunks []Chunk) {
	t.Helper()
	if strings.TrimFunc(text, unicode.IsSpace) == "" {
		if chunks != nil {
			t.Fatalf("whitespace-only input gave %d chunks", len(chunks))
		}
		return
	}
	if len(chunks) == 0 {
		t.Fatal("no chunks")
	}
	legal := boundaries(text)
	l := scan(text)
	var rebuilt strings.Builder
	for i, c := range chunks {
		if c.Start < 0 || c.End > len(text) || c.Start >= c.End {
			t.Fatalf("chunk %d: bad range [%d, %d)", i, c.Start, c.End)
		}
		if c.Text != text[c.Start:c.End] {
			t.Fatalf("chunk %d: Text differs from input range", i)
		}
		if !legal[c.Start] || !legal[c.End] {
			t.Fatalf("chunk %d: range [%d, %d) splits a rune or CRLF", i, c.Start, c.End)
		}
		if c.Length != measure(c.Text) {
			t.Fatalf("chunk %d: Length %d, measured %d", i, c.Length, measure(c.Text))
		}
		if c.Length > s.maxSize {
			t.Fatalf("chunk %d: Length %d exceeds MaxSize %d", i, c.Length, s.maxSize)
		}
		if i == 0 {
			if c.Start != 0 {
				t.Fatalf("first chunk starts at %d", c.Start)
			}
			rebuilt.WriteString(c.Text)
		} else {
			prev := chunks[i-1]
			if c.Start <= prev.Start || c.End <= prev.End || c.Start > prev.End {
				t.Fatalf("chunk %d [%d, %d) does not follow [%d, %d)", i, c.Start, c.End, prev.Start, prev.End)
			}
			if overlap := measure(text[c.Start:prev.End]); overlap > s.overlap {
				t.Fatalf("chunk %d: overlap %d exceeds %d", i, overlap, s.overlap)
			}
			rebuilt.WriteString(text[prev.End:c.End])
		}
		// 只有空白的分段只允许出现在至少 Size 长的空白中，或者它无法在 MaxSize 以内并入相邻的分段。
		if strings.TrimFunc(c.Text, unicode.IsSpace) == "" {
			before := strings.TrimRightFunc(text[:c.Start], unicode.IsSpace)
			after := strings.TrimLeftFunc(text[c.Start:], unicode.IsSpace)
			longRun := measure(text[len(before):len(text)-len(after)]) >= s.size
			joinsPrev := i > 0 && measure(text[chunks[i-1].Start:c.End]) <= s.maxSize
			joinsNext := i+1 < len(chunks) && measure(text[c.Start:chunks[i+1].End]) <= s.maxSize
			if !longRun && (joinsPrev || joinsNext) {
				t.Fatalf("chunk %d [%d, %d) is whitespace only", i, c.Start, c.End)
			}
		}
		anchor := c.Start + len(c.Text) - len(strings.TrimLeftFunc(c.Text, unicode.IsSpace))
		for _, hard := range l.hardStarts {
			if hard > anchor && hard < c.End {
				t.Fatalf("chunk %d [%d, %d) spans the heading at %d", i, c.Start, c.End, hard)
			}
		}
		for _, heading := range c.Headings {
			if heading.Start < 0 || heading.End > len(text) || heading.Start >= heading.End || heading.Start >= anchor {
				t.Fatalf("chunk %d: heading %q has bad range [%d, %d)", i, heading.Text, heading.Start, heading.End)
			}
		}
	}
	if chunks[len(chunks)-1].End != len(text) {
		t.Fatalf("last chunk ends at %d of %d", chunks[len(chunks)-1].End, len(text))
	}
	if rebuilt.String() != text {
		t.Fatal("chunks without overlap do not rebuild the input")
	}
}

func TestNewValidates(t *testing.T) {
	for _, opts := range []Options{
		{Size: 0},
		{Size: 10, Overlap: 10},
		{Size: 10, Overlap: -1},
		{Size: 10, MaxSize: 9},
		{Size: 10, MaxSize: -1},
	} {
		if _, err := New(opts); err == nil {
			t.Errorf("New(%+v) succeeded", opts)
		}
	}
	s := mustNew(t, Options{Size: 100})
	if s.maxSize != 120 {
		t.Errorf("default MaxSize = %d, want 120", s.maxSize)
	}
}

func TestWhitespaceOnly(t *testing.T) {
	s := mustNew(t, Options{Size: 10})
	for _, text := range []string{"", " ", "\n\r\n\t", "　"} {
		if chunks := s.Split(text); chunks != nil {
			t.Errorf("Split(%q) = %d chunks", text, len(chunks))
		}
	}
}

// headingTexts 返回分段标题路径中的文本。
func headingTexts(c Chunk) []string {
	var texts []string
	for _, heading := range c.Headings {
		texts = append(texts, heading.Text)
	}
	return texts
}

func TestHeadingPaths(t *testing.T) {
	text := "# A\n\nintro\n\n## B\n\nbody b\n\n### C\n\nbody c\n\n## D\n\nbody d\n"
	s := mustNew(t, Options{Size: 1000})
	chunks := s.Split(text)
	checkContract(t, s, text, chunks)
	want := []struct {
		prefix   string
		headings []string
	}{
		{"# A", nil},
		{"## B", []string{"A"}},
		{"### C", []string{"A", "B"}},
		{"## D", []string{"A"}},
	}
	if len(chunks) != len(want) {
		t.Fatalf("got %d chunks, want %d", len(chunks), len(want))
	}
	for i, w := range want {
		if !strings.HasPrefix(chunks[i].Text, w.prefix) {
			t.Errorf("chunk %d = %q, want prefix %q", i, chunks[i].Text, w.prefix)
		}
		if got := headingTexts(chunks[i]); strings.Join(got, "|") != strings.Join(w.headings, "|") {
			t.Errorf("chunk %d headings = %q, want %q", i, got, w.headings)
		}
	}
	if h := chunks[2].Headings[1]; text[h.Start:h.End] != "## B" || h.Level != 2 {
		t.Errorf("heading B = %+v", h)
	}
}

func TestConsecutiveHeadingsStayTogether(t *testing.T) {
	text := "# A\n\n## B\nbody\n"
	s := mustNew(t, Options{Size: 100})
	chunks := s.Split(text)
	checkContract(t, s, text, chunks)
	if len(chunks) != 1 || chunks[0].Headings != nil {
		t.Fatalf("chunks = %+v", chunks)
	}
}

func TestLongHeadingIsCutInside(t *testing.T) {
	long := strings.Repeat("标题", 80)
	text := "# Root\n\n## " + long + "\n\n" + strings.Repeat("正文内容。", 30) + "\n"
	s := mustNew(t, Options{Size: 60, Overlap: 5})
	chunks := s.Split(text)
	checkContract(t, s, text, chunks)
	headingEnd := strings.Index(text, long) + len(long)
	sawContinuation, sawBody := false, false
	for _, c := range chunks[1:] {
		got := strings.Join(headingTexts(c), "|")
		if c.Start < headingEnd {
			sawContinuation = true
			if got != "Root" {
				t.Errorf("heading chunk at %d inherits %q, want Root", c.Start, got)
			}
			continue
		}
		sawBody = true
		if got != "Root|"+long {
			t.Errorf("body chunk at %d inherits %q", c.Start, got)
		}
	}
	if !sawContinuation || !sawBody {
		t.Fatalf("continuation %v, body %v in %d chunks", sawContinuation, sawBody, len(chunks))
	}
}

func TestCJKSplitsAtSentences(t *testing.T) {
	text := strings.Repeat("退款申请需在签收后七天内提交，客服会在两个工作日内审核。", 10)
	s := mustNew(t, Options{Size: 60, Overlap: 16})
	chunks := s.Split(text)
	checkContract(t, s, text, chunks)
	if len(chunks) < 4 {
		t.Fatalf("got %d chunks", len(chunks))
	}
	for i, c := range chunks[:len(chunks)-1] {
		if !strings.HasSuffix(c.Text, "。") && !strings.HasSuffix(c.Text, "，") {
			t.Errorf("chunk %d ends mid-sentence: %q", i, c.Text)
		}
	}
	if chunks[1].Start >= chunks[0].End {
		t.Error("no overlap between the first chunks")
	}
}

func TestTableHeaderContext(t *testing.T) {
	header := "| 地区 | 首重 |\n| --- | --- |"
	text := "## 运费\n\n" + header + "\n" + strings.Repeat("| 华东 | 8 元 |\n", 40)
	s := mustNew(t, Options{Size: 80})
	chunks := s.Split(text)
	checkContract(t, s, text, chunks)
	if len(chunks) < 3 {
		t.Fatalf("got %d chunks", len(chunks))
	}
	if chunks[0].TableHeader != nil {
		t.Error("first chunk starts before the table body")
	}
	for _, c := range chunks[1:] {
		if c.TableHeader == nil || c.TableHeader.Text != header || text[c.TableHeader.Start:c.TableHeader.End] != header {
			t.Fatalf("chunk %q TableHeader = %+v", c.Text, c.TableHeader)
		}
		if !strings.HasPrefix(c.Text, "| 华东") {
			t.Errorf("chunk does not start at a row: %q", c.Text)
		}
		if want := "运费\n" + header; c.Context() != want {
			t.Errorf("Context() = %q, want %q", c.Context(), want)
		}
	}
}

func TestTableVariants(t *testing.T) {
	s := mustNew(t, Options{Size: 40})
	rows := strings.Repeat("a | b\n", 20)
	for name, tc := range map[string]struct {
		text   string
		header bool
	}{
		"no edge pipes":      {"x | y\n--|--\n" + rows, true},
		"empty header":       {"| | |\n|---|---|\n" + rows, false},
		"cell count":         {"x | y | z\n--|--\n" + rows, false},
		"fewer header cells": {"x \\| y\n--|--\n" + rows, true}, // goldmark 用空单元格补齐表头
		"escaped backslash":  {"x | y\\\\|\n--|--\n" + rows, true},
		"backslash pipe":     {"`\\\\|` | z\n--|--\n" + rows, true},
		"after empty item":   {"Intro\n*\nx | y\n--|--\n" + rows, true},
	} {
		chunks := s.Split(tc.text)
		checkContract(t, s, tc.text, chunks)
		if got := chunks[len(chunks)-1].TableHeader != nil; got != tc.header {
			t.Errorf("%s: TableHeader present = %v, want %v", name, got, tc.header)
		}
	}
}

func TestCRLF(t *testing.T) {
	text := "# 标题\r\n\r\n" + strings.Repeat("第一句话。第二句话，", 20) + "\r\n"
	s := mustNew(t, Options{Size: 30, Overlap: 5})
	chunks := s.Split(text)
	checkContract(t, s, text, chunks)
	if chunks[0].Length >= len([]rune(chunks[0].Text)) && strings.Contains(chunks[0].Text, "\r\n") {
		t.Errorf("CRLF counted twice: Length %d", chunks[0].Length)
	}
	for _, c := range chunks[1:] {
		if got := headingTexts(c); len(got) != 1 || got[0] != "标题" {
			t.Errorf("headings = %q", got)
		}
	}
}

func TestStructuresAreNotHeadings(t *testing.T) {
	s := mustNew(t, Options{Size: 1000})
	for name, text := range map[string]string{
		"fence":          "intro\n\n```sh\n# comment\n```\n\nafter\n",
		"tilde fence":    "intro\n\n~~~~\n# comment\n~~~\n# still code\n~~~~\n",
		"html comment":   "intro\n\n<!--\n# hidden\n-->\n\nafter\n",
		"html block":     "<div>\n# inside\n</div>\n",
		"script":         "<script>\n\n# js\n</script>\n",
		"list item":      "- item\n  # nested\n- next\n",
		"blockquote":     "> # quoted\n> text\n",
		"indented code":  "intro\n\n    # code\n",
		"front matter":   "---\ntitle: x\n# y\n---\nbody\n",
		"code span":      "see `# x` here\n",
		"unclosed fence": "intro\n\n```\n# code\n",
		"html tag line":  "<custom-tag>\n# x\n",
		"php":            "<?php\n# x\n?>\n",
		"link reference": "[ref]: /url\n===\n",
	} {
		chunks := s.Split(text)
		checkContract(t, s, text, chunks)
		if len(scan(text).headings) != 0 {
			t.Errorf("%s: found headings %+v", name, scan(text).headings)
		}
	}
}

func TestFrontMatterNeedsYAML(t *testing.T) {
	for name, tc := range map[string]struct {
		text        string
		frontMatter bool
	}{
		"keys":              {"---\ntitle: x\ntags:\n  - a\n- b\n# comment\n\nurl: http://e.com\n---\n# H\n", true},
		"dots":              {"---\ntitle: x\n...\n# H\n", true},
		"thematic breaks":   {"---\n\n# 第一章\n\n内容一。\n\n---\n\n# 第二章\n", false},
		"comments only":     {"---\n# H\n---\n", false},
		"prose with colon":  {"---\nNote: see below.\nMore prose here\n---\n# H\n", false},
		"colon inside word": {"---\nhttp://e.com\n---\n# H\n", false},
		"indented keys":     {"---\n  title: x\n# Metadata comment\n---\n\nbody\n", true},
		"sequence only":     {"---\n- title: x\n---\n# H\n", false},
		"indented sequence": {"---\n\n# 第一章\n\n  - 作者: 张三\n\n---\n", false},
		"indented comment":  {"---\n\n# 第一章\n\n  # 作者: 张三\n\n---\n", false},
		"nested key":        {"---\nauthor:\n    name: x\n---\n", true},
		// "#" 开头的行被读作 YAML 注释，所以标题后面跟着像键的行时是 front matter。
		"heading and key": {"---\n# 第一章\n作者: 张三\n---\n# 第二章\n", true},
	} {
		end := scan(tc.text).frontMatterEnd(splitLines(tc.text))
		if (end >= 0) != tc.frontMatter {
			t.Errorf("%s: front matter end %d, want front matter %v", name, end, tc.frontMatter)
		}
	}
	// 分隔线之间的各章保持各自的标题路径。
	s := mustNew(t, Options{Size: 4})
	text := "---\n\n# 第一章\n\n内容一。\n\n---\n\n# 第二章\n\n内容二。\n"
	chunks := s.Split(text)
	checkContract(t, s, text, chunks)
	var paths []string
	for _, c := range chunks {
		if strings.HasPrefix(strings.TrimSpace(c.Text), "内容") {
			paths = append(paths, strings.Join(headingTexts(c), " > "))
		}
	}
	if want := []string{"第一章", "第二章"}; !slices.Equal(paths, want) {
		t.Errorf("heading paths = %q, want %q", paths, want)
	}
	// 没有 front matter 时，紧跟在段落下的 "---" 把它变成 setext 标题。
	text = "---\n\n# 第一章\n\n内容一。\n---\n\n后续正文\n"
	chunks = s.Split(text)
	checkContract(t, s, text, chunks)
	last := chunks[len(chunks)-1]
	if got, want := strings.Join(headingTexts(last), " > "), "第一章 > 内容一。"; got != want {
		t.Errorf("setext: last chunk %q under %q, want %q", last.Text, got, want)
	}
}

func TestGraphemeClustersStayWhole(t *testing.T) {
	for name, text := range map[string]string{
		"family emoji": strings.Repeat("👨‍👩‍👧‍👦", 100),
		"flags":        strings.Repeat("🇨🇳", 300),
		"skin tones":   strings.Repeat("👍🏽", 300),
		"combining":    strings.Repeat("e\u0301", 300),
	} {
		s := mustNew(t, Options{Size: 101})
		chunks := s.Split(text)
		checkContract(t, s, text, chunks)
		for _, c := range chunks[:len(chunks)-1] {
			previous, _ := utf8.DecodeLastRuneInString(text[:c.End])
			next, _ := utf8.DecodeRuneInString(text[c.End:])
			flags := strings.Count(text[:c.End], "🇨")
			if previous == '\u200D' || next == '\u200D' || unicode.Is(unicode.Mn, next) || next == 0x1F3FD ||
				name == "flags" && flags != strings.Count(text[:c.End], "🇳") {
				t.Errorf("%s: chunk ends inside a cluster at %d", name, c.End)
			}
		}
	}
	// 长于 MaxSize 的字素簇仍然会在内部切开。
	s := mustNew(t, Options{Size: 50})
	text := "a" + strings.Repeat("\u0301", 500)
	checkContract(t, s, text, s.Split(text))

	// 放得下的字素簇可以在较短的结尾空白之前结束一段，切点移到较近的簇边界。
	for name, tc := range map[string]struct {
		opts    Options
		text    string
		lengths []int
	}{
		"emoji before spaces":     {Options{Size: 8, MaxSize: 8}, "👨‍👩‍👧‍👦   ", []int{7, 3}},
		"combining before spaces": {Options{Size: 8, MaxSize: 10}, "e" + strings.Repeat("\u0301", 9) + "     ", []int{10, 5}},
		"joiner before spaces":    {Options{Size: 8, MaxSize: 14}, strings.Repeat("a\u200D", 6) + "   ", []int{13, 2}},
		"nearer end after":        {Options{Size: 7, MaxSize: 8}, "x👨‍👩‍👧‍👦zz", []int{8, 2}},
		"nearer end":              {Options{Size: 16, MaxSize: 30}, "xxxx" + strings.Repeat("\u0301", 20) + strings.Repeat("y", 30), []int{24, 30}},
		"nearer start":            {Options{Size: 6, MaxSize: 30}, "xxxx" + strings.Repeat("\u0301", 20) + strings.Repeat("y", 30), []int{3, 21, 30}},
	} {
		s := mustNew(t, tc.opts)
		chunks := s.Split(tc.text)
		checkContract(t, s, tc.text, chunks)
		var lengths []int
		for _, c := range chunks {
			lengths = append(lengths, utf8.RuneCountInString(c.Text))
		}
		if !slices.Equal(lengths, tc.lengths) {
			t.Errorf("%s: chunk lengths %v, want %v", name, lengths, tc.lengths)
		}
	}
}

func TestHeadingsAreRecognised(t *testing.T) {
	for name, tc := range map[string]struct {
		text  string
		texts []string
	}{
		"atx":            {"# A #\n\n  ## B\n\n####### no\n#no\n", []string{"A", "B"}},
		"setext":         {"Title\n=====\n\nbody\n\nTwo\nlines\n---\n", []string{"Title", "Two lines"}},
		"after list":     {"- item\n\n# H\n", []string{"H"}},
		"thematic break": {"---\n\n# H\n\n***\n", []string{"H"}},
		"unclosed front": {"---\n# H\n", []string{"H"}},
		"setext dash":    {"Title\n-\n\nbody\n", []string{"Title"}},
		"break not list": {"* * *\n\n  # Real\n", []string{"Real"}},
		"empty item":     {"Foo\n*\n\n# H\n", []string{"H"}},
		"leading zeros":  {"Foo\n01. item\n  # nested\n", []string{"nested"}},
		"lone CR":        {"intro\r\r# H\r", []string{"H"}},
		"bom":            {"\uFEFF# H\n", []string{"H"}},
	} {
		var got []string
		for _, heading := range scan(tc.text).headings {
			got = append(got, heading.text)
		}
		if strings.Join(got, "|") != strings.Join(tc.texts, "|") {
			t.Errorf("%s: headings %q, want %q", name, got, tc.texts)
		}
	}
}

func TestSetextHeadingRange(t *testing.T) {
	text := "Two\nlines\n===\n\n" + strings.Repeat("word ", 20)
	s := mustNew(t, Options{Size: 30})
	chunks := s.Split(text)
	checkContract(t, s, text, chunks)
	last := chunks[len(chunks)-1]
	if len(last.Headings) != 1 {
		t.Fatalf("last chunk %q headings = %+v", last.Text, last.Headings)
	}
	if h := last.Headings[0]; h.Level != 1 || h.Text != "Two lines" || text[h.Start:h.End] != "Two\nlines\n===" {
		t.Errorf("heading = %+v", h)
	}
	if last.Context() != "Two lines" {
		t.Errorf("Context() = %q", last.Context())
	}
}

func TestStructuresFittingMaxSizeStayWhole(t *testing.T) {
	s := mustNew(t, Options{Size: 20, MaxSize: 24})
	heading := "# abcdefghijklmnopqrs\nx\n" + strings.Repeat("y", 60)
	chunks := s.Split(heading)
	checkContract(t, s, heading, chunks)
	if chunks[0].Text != "# abcdefghijklmnopqrs\nx\n" {
		t.Errorf("heading chunk = %q", chunks[0].Text)
	}
	long := "# " + strings.Repeat("h", 19) + "\n" + strings.Repeat("y", 60)
	chunks = s.Split(long)
	checkContract(t, s, long, chunks)
	if chunks[0].Text != "# "+strings.Repeat("h", 19)+"\n" {
		t.Errorf("heading with a long body = %q", chunks[0].Text)
	}
	code := "```\n" + strings.Repeat("c", 21) + "\n" + strings.Repeat("d", 21) + "\n```\n"
	chunks = s.Split(code)
	checkContract(t, s, code, chunks)
	for _, c := range chunks {
		for _, line := range strings.Split(strings.TrimSuffix(c.Text, "\n"), "\n") {
			if strings.Trim(line, "cd") == "" && line != "" && len(line) != 21 {
				t.Errorf("code line cut: %q in %q", line, c.Text)
			}
		}
	}
}

func TestNoWhitespaceChunks(t *testing.T) {
	for _, tc := range []struct {
		text string
		opts Options
	}{
		{"\n" + strings.Repeat("a", 700), Options{Size: 500}},
		{strings.Repeat("word \n", 40), Options{Size: 4}},
		{strings.Repeat("ab\n\n\n\n", 30), Options{Size: 3, MaxSize: 3}},
	} {
		s := mustNew(t, tc.opts)
		checkContract(t, s, tc.text, s.Split(tc.text))
	}
}

func TestMaxSizeEqualsSize(t *testing.T) {
	text := strings.Repeat("一二三四五。六七八九十，", 20)
	s := mustNew(t, Options{Size: 30, MaxSize: 30, Overlap: 5})
	chunks := s.Split(text)
	checkContract(t, s, text, chunks)
	for _, c := range chunks {
		if c.Length > 30 {
			t.Errorf("chunk length %d", c.Length)
		}
	}
}

func TestCodeSpans(t *testing.T) {
	for _, tc := range []struct {
		line  string
		spans [][2]int
	}{
		{"no code", nil},
		{"`a` b", [][2]int{{0, 3}}},
		{"``a`b`` c", [][2]int{{0, 7}}},
		{"`` unclosed `x`", [][2]int{{12, 15}}},
		{"`a ``b`` c` d", [][2]int{{0, 11}}},
		{"``` `a` ``` ``b``", [][2]int{{0, 11}, {12, 17}}},
		{"` `` `` `", [][2]int{{0, 9}}},
		{"```", nil},
	} {
		if got := codeSpans(tc.line); fmt.Sprint(got) != fmt.Sprint(tc.spans) {
			t.Errorf("codeSpans(%q) = %v, want %v", tc.line, got, tc.spans)
		}
	}
}

func TestNestedCodeIsEnclosed(t *testing.T) {
	text := "- item\n\n  ```\n  a. b, c\n  ```\n> ```\n> d. e\n> ```\n"
	l := scan(text)
	for _, after := range []string{"a. ", "b, ", "d. "} {
		position := strings.Index(text, after) + len(after)
		if l.ranks[position] != rankEnclosed {
			t.Errorf("rank after %q = %d, want rankEnclosed", after, l.ranks[position])
		}
	}
}

func TestContextFormat(t *testing.T) {
	headings := []Heading{{Level: 1, Text: "A"}, {Level: 2, Text: "B > C"}}
	table := &TableHeader{Text: "| x |\n| - |"}
	for _, tc := range []struct {
		chunk   Chunk
		context string
	}{
		{Chunk{Text: "t"}, ""},
		{Chunk{Text: "t", Headings: headings}, "A > B > C"},
		{Chunk{Text: "t", TableHeader: table}, "| x |\n| - |"},
		{Chunk{Text: "t", Headings: headings, TableHeader: table}, "A > B > C\n| x |\n| - |"},
	} {
		if got := tc.chunk.Context(); got != tc.context {
			t.Errorf("Context() = %q, want %q", got, tc.context)
		}
		want := "t"
		if tc.context != "" {
			want = tc.context + "\n\nt"
		}
		if got := tc.chunk.TextWithContext(); got != want {
			t.Errorf("TextWithContext() = %q, want %q", got, want)
		}
	}
}

func TestLatinOverlap(t *testing.T) {
	text := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 30)
	s := mustNew(t, Options{Size: 120, Overlap: 50})
	chunks := s.Split(text)
	checkContract(t, s, text, chunks)
	overlapped := 0
	for i, c := range chunks[1:] {
		if c.Start < chunks[i].End {
			overlapped++
			if !strings.HasPrefix(c.Text, "The ") {
				t.Errorf("chunk %d starts mid-sentence: %q", i+1, c.Text[:20])
			}
		}
	}
	if overlapped == 0 {
		t.Error("no chunk overlaps")
	}
}

func TestNoOverlapAcrossHeadings(t *testing.T) {
	text := "# A\n\n" + strings.Repeat("word ", 40) + "\n\n# B\n\nend\n"
	s := mustNew(t, Options{Size: 50, Overlap: 20})
	chunks := s.Split(text)
	checkContract(t, s, text, chunks)
	last := chunks[len(chunks)-1]
	if !strings.HasPrefix(last.Text, "# B") || last.Start != chunks[len(chunks)-2].End {
		t.Errorf("last chunk = %q", last.Text)
	}
}

func TestInvalidUTF8(t *testing.T) {
	text := "abc\xff\xfe def。\xe4\xb8 tail" + strings.Repeat("x", 50)
	s := mustNew(t, Options{Size: 10, Overlap: 2})
	checkContract(t, s, text, s.Split(text))
}

// goldenChunk 是分段在 golden 文件中的序列化形式。
type goldenChunk struct {
	Start, End, Length int
	Context            string `json:",omitempty"`
	Text               string
}

func TestGolden(t *testing.T) {
	s := mustNew(t, Options{Size: 200, Overlap: 30})
	files, err := filepath.Glob("testdata/*.md")
	if err != nil || len(files) == 0 {
		t.Fatalf("no golden inputs: %v", err)
	}
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			input, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			chunks := s.Split(string(input))
			checkContract(t, s, string(input), chunks)
			out := make([]goldenChunk, len(chunks))
			for i, c := range chunks {
				out[i] = goldenChunk{Start: c.Start, End: c.End, Length: c.Length, Context: c.Context(), Text: c.Text}
			}
			got, err := json.MarshalIndent(out, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			golden := strings.TrimSuffix(file, ".md") + ".golden.json"
			if *update {
				if err := os.WriteFile(golden, append(got, '\n'), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if string(want) != string(got)+"\n" {
				t.Errorf("output differs from %s; run go test ./split -update and review the diff", golden)
			}
		})
	}
}

func FuzzSplit(f *testing.F) {
	for _, seed := range []string{
		"# A\n\nbody\n",
		"a\r\nb\rc\n",
		"\uFEFF---\nx: 1\n---\n# H\n",
		"| a | b |\n| - | - |\n| 1 | 2 |\n",
		"```\n# x\n",
		"Title\n===\n\ntext",
		"\xff\xfe\xfd",
		"😀😀😀 emoji 😀",
		strings.Repeat("长句子没有标点", 50),
		"# A\n## B\n### C\n#### D\n",
		"<!--\n# c\n-->\n<div>\n</div>\n",
	} {
		f.Add(seed, uint16(20), uint16(5), uint16(0))
	}
	f.Fuzz(func(t *testing.T, text string, size, overlap, extra uint16) {
		opts := Options{Size: int(size%300) + 1}
		opts.Overlap = int(overlap) % opts.Size
		if extra%3 != 0 {
			opts.MaxSize = opts.Size + int(extra%100)
		}
		s := mustNew(t, opts)
		// 内存分配与输入成线性：正常的文档每字节分配几十到几百字节，病态输入曾经超过十万。
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		chunks := s.Split(text)
		runtime.ReadMemStats(&after)
		if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 1024*uint64(len(text))+1<<20 {
			t.Errorf("allocated %d bytes for %d bytes of input", allocated, len(text))
		}
		checkContract(t, s, text, chunks)
	})
}

func BenchmarkSplitCodeSpans(b *testing.B) {
	text := strings.Repeat("`x` ", 40000)
	s := mustNew(b, Options{Size: 500})
	b.SetBytes(int64(len(text)))
	for b.Loop() {
		s.Split(text)
	}
}

func BenchmarkSplitLongWhitespace(b *testing.B) {
	text := strings.Repeat(" ", 512<<10) + "x"
	s := mustNew(b, Options{Size: 500})
	b.SetBytes(int64(len(text)))
	for b.Loop() {
		s.Split(text)
	}
}

func BenchmarkSplitWhitespaceBeforeHeading(b *testing.B) {
	text := strings.Repeat(" ", 512<<10) + "\n# H\nx"
	s := mustNew(b, Options{Size: 500})
	b.SetBytes(int64(len(text)))
	for b.Loop() {
		s.Split(text)
	}
}

func BenchmarkSplitUnmatchedBackticks(b *testing.B) {
	var text strings.Builder
	text.WriteString("start ")
	for k := 1; k <= 2048; k++ {
		text.WriteString(strings.Repeat("`", k) + "a ")
	}
	s := mustNew(b, Options{Size: 500})
	b.SetBytes(int64(text.Len()))
	for b.Loop() {
		s.Split(text.String())
	}
}

func BenchmarkSplit(b *testing.B) {
	input, err := os.ReadFile("testdata/zh-policy.md")
	if err != nil {
		b.Fatal(err)
	}
	text := strings.Repeat(string(input)+"\n\n", 50)
	s := mustNew(b, Options{Size: 500, Overlap: 50})
	b.SetBytes(int64(len(text)))
	b.ReportAllocs()
	for b.Loop() {
		s.Split(text)
	}
}
