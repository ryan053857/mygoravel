package csv_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"

	"github.com/go-tenon/tenon/document/convert"
	"github.com/go-tenon/tenon/document/convert/csv"
)

// run 用 opts 转换 src。
func run(opts csv.Options, src string) (convert.Document, error) {
	return csv.New(opts).Convert(context.Background(), convert.Input{Reader: strings.NewReader(src)})
}

func TestTables(t *testing.T) {
	gbk, err := simplifiedchinese.GB18030.NewEncoder().String("名称,数量\n苹果,3\n")
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		opts csv.Options
		src  string
		want string
	}{
		"header":    {csv.Options{}, "name,qty\r\napple,3\r\n\r\npear | x,\"multi\nline\"\n", "| name | qty |\n| --- | --- |\n| apple | 3 |\n| pear \\| x | multi line |"},
		"ragged":    {csv.Options{}, "a\nb,c,d\n", "| a |  |  |\n| --- | --- | --- |\n| b | c | d |"},
		"no header": {csv.Options{NoHeader: true}, "1,2\n3,4\n", "|  |  |\n| --- | --- |\n| 1 | 2 |\n| 3 | 4 |"},
		"semicolon": {csv.Options{Comma: ';'}, "a;b\n1;2\n", "| a | b |\n| --- | --- |\n| 1 | 2 |"},
		"lazy":      {csv.Options{LazyQuotes: true}, "a,b\nx\"y,2\n", "| a | b |\n| --- | --- |\n| x\"y | 2 |"},
		"fallback":  {csv.Options{Fallback: simplifiedchinese.GB18030}, gbk, "| 名称 | 数量 |\n| --- | --- |\n| 苹果 | 3 |"},
		"empty":     {csv.Options{}, "", ""},
		"blank":     {csv.Options{}, ",,\n,\n", ""},
	} {
		doc, err := run(tc.opts, tc.src)
		if err != nil || doc.Markdown != tc.want {
			t.Errorf("%s: got %q, %v\nwant %q", name, doc.Markdown, err, tc.want)
		}
	}
}

func TestErrors(t *testing.T) {
	for _, comma := range []rune{'"', '\n', '\r', utf8.RuneError, -1} {
		if _, err := run(csv.Options{Comma: comma}, "a,b\n"); err == nil || errors.Is(err, convert.ErrCorrupt) {
			t.Errorf("Comma %q: %v", comma, err)
		}
	}
	if _, err := run(csv.Options{}, "a,b\nx\"y,2\n"); !errors.Is(err, convert.ErrCorrupt) {
		t.Errorf("strict quotes: %v", err)
	}
	if _, err := run(csv.Options{Limits: convert.Limits{MaxOutputBytes: 20}}, strings.Repeat("abc,def\n", 10)); !errors.Is(err, convert.ErrTooLarge) {
		t.Errorf("output limit: %v", err)
	}
}

func FuzzConvert(f *testing.F) {
	for _, seed := range []string{"a,b\n1,2\n", "\"x\"\"y\",z\n", "a|b,\x00\n"} {
		f.Add(seed)
	}
	c := csv.New(csv.Options{LazyQuotes: true, Limits: convert.Limits{MaxOutputBytes: 1 << 20}})
	f.Fuzz(func(t *testing.T, src string) {
		doc, err := c.Convert(context.Background(), convert.Input{Reader: strings.NewReader(src)})
		if err != nil && (doc.Markdown != "" || !errors.Is(err, convert.ErrCorrupt) && !errors.Is(err, convert.ErrTooLarge)) {
			t.Fatalf("%q, %v", doc.Markdown, err)
		}
		if strings.ContainsAny(doc.Markdown, "\r\x00") {
			t.Fatalf("output not normalised: %q", doc.Markdown)
		}
	})
}
