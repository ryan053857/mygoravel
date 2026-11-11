package text_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"

	"github.com/ryan053857/mygoravel/document/convert"
	"github.com/ryan053857/mygoravel/document/convert/text"
)

// convertBytes 用 c 转换 data。
func convertBytes(t *testing.T, c convert.Converter, data []byte, charset string) (convert.Document, error) {
	t.Helper()
	return c.Convert(context.Background(), convert.Input{Reader: bytes.NewReader(data), Charset: charset})
}

func TestDecoding(t *testing.T) {
	gb, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte("中文"))
	if err != nil {
		t.Fatal(err)
	}
	utf16le, err := unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewEncoder().Bytes([]byte("中文"))
	if err != nil {
		t.Fatal(err)
	}
	utf16be, err := unicode.UTF16(unicode.BigEndian, unicode.UseBOM).NewEncoder().Bytes([]byte("中文"))
	if err != nil {
		t.Fatal(err)
	}
	plain := text.New(text.Options{})
	withFallback := text.New(text.Options{Fallback: simplifiedchinese.GB18030})
	for name, tc := range map[string]struct {
		c       convert.Converter
		data    []byte
		charset string
		want    string
	}{
		"utf-8":              {plain, []byte("中文"), "", "中文"},
		"utf-8 bom":          {plain, []byte("\xEF\xBB\xBF中文"), "", "中文"},
		"utf-16le bom":       {plain, utf16le, "", "中文"},
		"utf-16be bom":       {plain, utf16be, "", "中文"},
		"declared gbk":       {plain, gb, "GBK", "中文"},
		"declared unknown":   {plain, []byte("中文"), "x-unknown", "中文"},
		"bom beats declared": {plain, []byte("\xEF\xBB\xBF中文"), "gbk", "中文"},
		"fallback":           {withFallback, gb, "", "中文"},
		"fallback unused":    {withFallback, []byte("中文"), "", "中文"},
		"invalid":            {plain, []byte("a\xffb"), "", "a\uFFFDb"},
		"crlf and nul":       {plain, []byte("a\r\nb\rc\x00d"), "", "a\nb\ncd"},
	} {
		doc, err := convertBytes(t, tc.c, tc.data, tc.charset)
		if err != nil || doc.Markdown != tc.want {
			t.Errorf("%s: got %q, %v; want %q", name, doc.Markdown, err, tc.want)
		}
	}
}

func TestPassthroughKeepsMarkdown(t *testing.T) {
	in := "  # Title  \n\n    code\n\ttab\n\n"
	doc, err := convertBytes(t, text.New(text.Options{}), []byte(in), "")
	if err != nil || doc.Markdown != in || doc.Sections != nil {
		t.Errorf("got %q, %v", doc.Markdown, err)
	}
}

func TestJSONFence(t *testing.T) {
	doc, err := convertBytes(t, text.JSON(text.Options{}), []byte("{\"a\": \"```\"}\r\n"), "")
	want := "````json\n{\"a\": \"```\"}\n````"
	if err != nil || doc.Markdown != want {
		t.Errorf("got %q, %v; want %q", doc.Markdown, err, want)
	}
}

func TestEmptyInput(t *testing.T) {
	for _, c := range []convert.Converter{text.New(text.Options{}), text.JSON(text.Options{})} {
		doc, err := convertBytes(t, c, nil, "")
		if err != nil || doc.Markdown != "" {
			t.Errorf("got %q, %v", doc.Markdown, err)
		}
	}
}

func TestLimits(t *testing.T) {
	data := []byte(strings.Repeat("x", 100))
	for name, tc := range map[string]struct {
		limits convert.Limits
		data   []byte
		kind   string
	}{
		"source at limit": {convert.Limits{MaxBytes: 100}, data, ""},
		"source over":     {convert.Limits{MaxBytes: 99}, data, convert.LimitSource},
		"source disabled": {convert.Limits{MaxBytes: -1}, data, ""},
		"output over":     {convert.Limits{MaxOutputBytes: 50}, data, convert.LimitOutput},
		"output at limit": {convert.Limits{MaxOutputBytes: 100}, data, ""},
	} {
		_, err := convertBytes(t, text.New(text.Options{Limits: tc.limits}), tc.data, "")
		var limit *convert.LimitError
		switch {
		case tc.kind == "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case tc.kind != "" && (!errors.As(err, &limit) || limit.Limit != tc.kind || !errors.Is(err, convert.ErrTooLarge)):
			t.Errorf("%s: got %v, want %s limit", name, err, tc.kind)
		}
	}
}

// failingReader 返回一些数据后出错。
type failingReader struct{ sent bool }

func (r *failingReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, "abc"), nil
	}
	return 0, io.ErrUnexpectedEOF
}

func TestReadErrors(t *testing.T) {
	c := text.New(text.Options{})
	_, err := c.Convert(context.Background(), convert.Input{Reader: &failingReader{}, Name: "a.txt"})
	if !errors.Is(err, io.ErrUnexpectedEOF) || !strings.Contains(err.Error(), "a.txt") {
		t.Errorf("reader error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Convert(ctx, convert.Input{Reader: strings.NewReader("x")}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled = %v", err)
	}
	if _, err := c.Convert(context.Background(), convert.Input{}); err == nil {
		t.Error("nil reader accepted")
	}
}
