package all_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
	"golang.org/x/text/encoding/simplifiedchinese"

	"github.com/ryan053857/mygoravel/document/convert"
	"github.com/ryan053857/mygoravel/document/convert/all"
	"github.com/ryan053857/mygoravel/document/convert/docx"
	"github.com/ryan053857/mygoravel/document/convert/pptx"
	"github.com/ryan053857/mygoravel/document/internal/pdftest"
	"github.com/ryan053857/mygoravel/document/split"
)

func TestFormats(t *testing.T) {
	t.Parallel()
	r := all.New(all.Options{})
	defer func() { _ = r.Close() }()
	want := []convert.Format{"csv", "docx", "html", "json", "md", "pdf", "pptx", "txt", "xlsx"}
	if got := r.Formats(); !slices.Equal(got, want) {
		t.Errorf("Formats() = %v", got)
	}
	if _, ok := r.Lookup("htm"); !ok {
		t.Error("htm alias missing")
	}
}

func TestDefaults(t *testing.T) {
	t.Parallel()
	gbk, err := simplifiedchinese.GB18030.NewEncoder().String("名称,数量\n苹果,3\n")
	if err != nil {
		t.Fatal(err)
	}
	r := all.New(all.Options{
		Fallback: simplifiedchinese.GB18030,
		Limits:   convert.Limits{MaxBytes: 2 << 10, MaxOutputBytes: 1 << 10},
		DOCX:     docx.Options{Limits: convert.Limits{MaxBytes: 4}},
		PPTX:     pptx.Options{Limits: convert.Limits{MaxExpandedBytes: 1 << 20}},
	})
	defer func() { _ = r.Close() }()
	doc, err := r.Convert(context.Background(), convert.CSV, convert.Input{Reader: strings.NewReader(gbk)})
	if err != nil || !strings.Contains(doc.Markdown, "苹果") {
		t.Errorf("fallback not applied: %q, %v", doc.Markdown, err)
	}
	if _, err := r.Convert(context.Background(), convert.Text, convert.Input{Reader: strings.NewReader(strings.Repeat("x", 2<<10))}); !errors.Is(err, convert.ErrTooLarge) {
		t.Errorf("shared limit not applied: %v", err)
	}
	var limit *convert.LimitError
	if _, err := r.Convert(context.Background(), convert.PPTX, convert.Input{Reader: strings.NewReader(strings.Repeat("x", 3<<10))}); !errors.As(err, &limit) || limit.Limit != convert.LimitSource {
		t.Errorf("shared MaxBytes with an own expansion limit: %v", err)
	}
	if _, err := r.Convert(context.Background(), convert.DOCX, convert.Input{Reader: strings.NewReader("PK\x03\x04x")}); !errors.As(err, &limit) || limit.Limit != convert.LimitSource {
		t.Errorf("own limit not kept: %v", err)
	}
}

func TestClose(t *testing.T) {
	t.Parallel()
	r := all.New(all.Options{})
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	data := pdftest.Build(t, [][]pdftest.Text{{pdftest.L(12, 700, "x")}}, "")
	if _, err := r.Convert(context.Background(), convert.PDF, convert.Input{Reader: bytes.NewReader(data)}); !errors.Is(err, convert.ErrClosed) {
		t.Errorf("PDF after Close: %v", err)
	}
	if _, err := r.Convert(context.Background(), convert.Text, convert.Input{Reader: strings.NewReader("x")}); err != nil {
		t.Errorf("text after Close: %v", err)
	}
}

// pages 返回分段区间涉及的 Section 编号。
func pages(doc convert.Document, c split.Chunk) []int {
	var numbers []int
	for _, s := range doc.SectionsIn(c.Start, c.End) {
		numbers = append(numbers, s.Number)
	}
	return numbers
}

func TestPDFChunksMapToPages(t *testing.T) {
	t.Parallel()
	sentence := "Every region reported steady growth this quarter."
	var pagesIn [][]pdftest.Text
	for range 3 {
		var lines []pdftest.Text
		for i := range 6 {
			lines = append(lines, pdftest.L(12, float64(700-14*i), sentence))
		}
		pagesIn = append(pagesIn, lines)
	}
	r := all.New(all.Options{})
	defer func() { _ = r.Close() }()
	format, _ := convert.FormatOf("report.PDF")
	doc, err := r.Convert(context.Background(), format, convert.Input{Reader: bytes.NewReader(pdftest.Build(t, pagesIn, ""))})
	if err != nil {
		t.Fatal(err)
	}
	splitter, err := split.New(split.Options{Size: 200, Overlap: 40})
	if err != nil {
		t.Fatal(err)
	}
	chunks := splitter.Split(doc.Markdown)
	if len(chunks) == 0 {
		t.Fatal("no chunks")
	}
	spanning := false
	for _, c := range chunks {
		numbers := pages(doc, c)
		if len(numbers) == 0 {
			t.Errorf("chunk %q maps to no page", c.Text)
		}
		spanning = spanning || len(numbers) > 1
	}
	if !spanning {
		t.Error("no chunk spans two pages")
	}
	if first := pages(doc, chunks[0]); !slices.Equal(first, []int{1}) {
		t.Errorf("first chunk pages = %v", first)
	}
	if last := pages(doc, chunks[len(chunks)-1]); !slices.Equal(last, []int{3}) {
		t.Errorf("last chunk pages = %v", last)
	}
}

func TestXLSXChunksMapToSheets(t *testing.T) {
	t.Parallel()
	f := excelize.NewFile()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(f.SetSheetName("Sheet1", "华东"))
	_, err := f.NewSheet("华南")
	must(err)
	for _, sheet := range []string{"华东", "华南"} {
		must(f.SetSheetRow(sheet, "A1", &[]any{"城市", "销量"}))
		for row := 2; row <= 30; row++ {
			cell, _ := excelize.CoordinatesToCellName(1, row)
			must(f.SetSheetRow(sheet, cell, &[]any{sheet + "城市", row * 10}))
		}
	}
	var buf bytes.Buffer
	must(f.Write(&buf))
	r := all.New(all.Options{})
	defer func() { _ = r.Close() }()
	doc, err := r.Convert(context.Background(), convert.XLSX, convert.Input{Reader: &buf})
	must(err)
	splitter, err := split.New(split.Options{Size: 120})
	must(err)
	chunks := splitter.Split(doc.Markdown)
	if len(chunks) == 0 {
		t.Fatal("no chunks")
	}
	for _, c := range chunks {
		sections := doc.SectionsIn(c.Start, c.End)
		if len(sections) != 1 {
			t.Fatalf("chunk %q maps to %d sheets", c.Text, len(sections))
		}
		if !strings.Contains(c.TextWithContext(), sections[0].Name) {
			t.Errorf("chunk on sheet %s lacks the sheet name in context: %q", sections[0].Name, c.TextWithContext())
		}
		if c.TableHeader != nil && c.TableHeader.Text != "| 城市 | 销量 |\n| --- | --- |" {
			t.Errorf("table header = %q", c.TableHeader.Text)
		}
	}
}
