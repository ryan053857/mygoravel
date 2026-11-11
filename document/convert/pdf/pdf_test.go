package pdf_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/document/convert"
	"github.com/ryan053857/mygoravel/document/convert/pdf"
	"github.com/ryan053857/mygoravel/document/internal/pdftest"
)

// converter 在整个包的测试中共用，PDFium 只启动一次。
var converter = pdf.New(pdf.Options{})

// run 用共用的转换器转换 data。
func run(t *testing.T, data []byte) (convert.Document, error) {
	t.Helper()
	return converter.Convert(context.Background(), convert.Input{Reader: bytes.NewReader(data)})
}

func TestPages(t *testing.T) {
	data := pdftest.Build(t, [][]pdftest.Text{
		{pdftest.L(24, 700, "Annual Report"), pdftest.L(12, 660, "Revenue grew in every region this year,"), pdftest.L(12, 646, "led by strong demand."), pdftest.L(12, 600, "- not a list")},
		{},
		{pdftest.L(18, 700, "Outlook"), pdftest.L(12, 660, "Next year looks steady.")},
	}, "")
	doc, err := run(t, data)
	if err != nil {
		t.Fatal(err)
	}
	want := "# Annual Report\n\nRevenue grew in every region this year,\nled by strong demand.\n\n\\- not a list\n\n## Outlook\n\nNext year looks steady."
	if doc.Markdown != want {
		t.Fatalf("got:\n%s\nwant:\n%s", doc.Markdown, want)
	}
	if len(doc.Sections) != 3 {
		t.Fatalf("sections = %+v", doc.Sections)
	}
	for i, s := range doc.Sections {
		if s.Kind != convert.KindPage || s.Number != i+1 {
			t.Errorf("section %d = %+v", i, s)
		}
	}
	first, empty, third := doc.Sections[0], doc.Sections[1], doc.Sections[2]
	if !strings.HasPrefix(doc.Markdown[first.Start:first.End], "# Annual") || !strings.HasSuffix(doc.Markdown[first.Start:first.End], "a list") {
		t.Errorf("first page = %q", doc.Markdown[first.Start:first.End])
	}
	if empty.Start != empty.End || empty.Start != third.Start {
		t.Errorf("empty page = %+v, third = %+v", empty, third)
	}
	if doc.Markdown[third.Start:third.End] != "## Outlook\n\nNext year looks steady." {
		t.Errorf("third page = %q", doc.Markdown[third.Start:third.End])
	}
}

func TestNoHeadingsAndNoText(t *testing.T) {
	c := pdf.New(pdf.Options{NoHeadings: true, Workers: 1})
	defer func() { _ = c.Close() }()
	doc, err := c.Convert(context.Background(), convert.Input{Reader: bytes.NewReader(pdftest.Build(t, [][]pdftest.Text{{pdftest.L(24, 700, "Title"), pdftest.L(12, 650, "Body text here.")}}, ""))})
	if err != nil || doc.Markdown != "Title\n\nBody text here." {
		t.Errorf("got %q, %v", doc.Markdown, err)
	}
	doc, err = run(t, pdftest.Build(t, [][]pdftest.Text{{}, {}}, ""))
	if err != nil || doc.Markdown != "" || len(doc.Sections) != 2 || doc.Sections[1].Start != 0 {
		t.Errorf("no text: %+v, %v", doc, err)
	}
}

func TestLayout(t *testing.T) {
	body := pdftest.L(12, 600, "This body paragraph is long enough to outweigh every heading on the page.")
	for name, tc := range map[string]struct {
		lines []pdftest.Text
		want  string
	}{
		"same line":          {[]pdftest.Text{pdftest.L(12, 700, "Hello"), pdftest.LX(12, 110, 700, "world")}, "Hello world"},
		"overlapping":        {[]pdftest.Text{pdftest.L(12, 700, "above"), pdftest.L(12, 697, "below")}, "above below"},
		"underscore":         {[]pdftest.Text{pdftest.L(12, 700, "foo_bar baz")}, "foo_bar baz"},
		"decimal":            {[]pdftest.Text{pdftest.L(12, 700, "Value 1.23 in file.txt")}, "Value 1.23 in file.txt"},
		"heading underscore": {[]pdftest.Text{pdftest.L(24, 700, "snake_case names"), body}, "# snake_case names\n\n" + body.S},
		"continuation":       {[]pdftest.Text{pdftest.L(24, 700, "Line one"), pdftest.L(24, 676, "Line two"), body}, "# Line one Line two\n\n" + body.S},
		"levels": {[]pdftest.Text{pdftest.L(30, 740, "Alpha"), pdftest.L(24, 700, "Beta"), pdftest.L(18, 660, "Gamma"), pdftest.L(14, 630, "Delta"), body},
			"# Alpha\n\n## Beta\n\n### Gamma\n\n### Delta\n\n" + body.S},
		"embedded newline":    {[]pdftest.Text{pdftest.L(24, 700, `Title\n# hidden`), body}, "# Title # hidden\n\n" + body.S},
		"carriage return":     {[]pdftest.Text{pdftest.L(12, 700, `first\rsecond`)}, "first second"},
		"punctuation":         {[]pdftest.Text{pdftest.L(12, 700, "Hello, world!"), pdftest.L(12, 686, ".NET Framework, \"quoted\"")}, "Hello, world!\n.NET Framework, \"quoted\""},
		"heading punctuation": {[]pdftest.Text{pdftest.L(24, 700, "Hello, World!"), body}, "# Hello, World!\n\n" + body.S},
	} {
		doc, err := run(t, pdftest.Build(t, [][]pdftest.Text{tc.lines}, ""))
		if err != nil || doc.Markdown != tc.want {
			t.Errorf("%s: got %q, %v\nwant %q", name, doc.Markdown, err, tc.want)
		}
	}
}

// cancelOnEOF 在 reader 读完时取消 context。
type cancelOnEOF struct {
	r      io.Reader
	cancel func()
}

func (c cancelOnEOF) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if errors.Is(err, io.EOF) {
		c.cancel()
	}
	return n, err
}

func TestSourceLimitAndCancel(t *testing.T) {
	data := pdftest.Build(t, [][]pdftest.Text{{pdftest.L(12, 700, "x")}, {pdftest.L(12, 700, "y")}}, "")
	c := pdf.New(pdf.Options{Limits: convert.Limits{MaxBytes: int64(len(data)) - 1}})
	defer func() { _ = c.Close() }()
	var limit *convert.LimitError
	if _, err := c.Convert(context.Background(), convert.Input{Reader: bytes.NewReader(data)}); !errors.As(err, &limit) || limit.Limit != convert.LimitSource {
		t.Errorf("source limit: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := converter.Convert(ctx, convert.Input{Reader: cancelOnEOF{bytes.NewReader(data), cancel}}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancel after reading: %v", err)
	}
}

func TestConcurrentClose(t *testing.T) {
	data := pdftest.Build(t, [][]pdftest.Text{{pdftest.L(12, 700, "hello")}}, "")
	c := pdf.New(pdf.Options{Workers: 1})
	if _, err := c.Convert(context.Background(), convert.Input{Reader: bytes.NewReader(data)}); err != nil {
		t.Fatal(err)
	}
	blocker := newSlowReader(data)
	go func() { _, _ = c.Convert(context.Background(), convert.Input{Reader: blocker}) }()
	<-blocker.reading
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- c.Close() }()
	}
	select {
	case <-results:
		t.Fatal("a Close returned before the conversion in flight finished")
	case <-time.After(100 * time.Millisecond):
	}
	close(blocker.release)
	for range 2 {
		if err := <-results; err != nil {
			t.Errorf("Close: %v", err)
		}
	}
}

func TestOwnerPasswordOnly(t *testing.T) {
	doc, err := run(t, pdftest.BuildOwnerOnly([][]pdftest.Text{{pdftest.L(12, 700, "readable without a password")}}))
	if err != nil || doc.Markdown != "readable without a password" {
		t.Errorf("got %q, %v", doc.Markdown, err)
	}
}

func TestErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		data []byte
		want error
	}{
		"empty":     {nil, convert.ErrCorrupt},
		"garbage":   {[]byte("%PDF-1.4 not really"), convert.ErrCorrupt},
		"encrypted": {pdftest.Build(t, [][]pdftest.Text{{pdftest.L(12, 700, "secret")}}, "user"), convert.ErrEncrypted},
	} {
		if _, err := run(t, tc.data); !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", name, err, tc.want)
		}
	}
	small := pdf.New(pdf.Options{Limits: convert.Limits{MaxOutputBytes: 5}})
	defer func() { _ = small.Close() }()
	_, err := small.Convert(context.Background(),
		convert.Input{Reader: bytes.NewReader(pdftest.Build(t, [][]pdftest.Text{{pdftest.L(12, 700, "more than five")}}, ""))})
	if !errors.Is(err, convert.ErrTooLarge) {
		t.Errorf("output limit: %v", err)
	}
}

func TestPageLimits(t *testing.T) {
	c := pdf.New(pdf.Options{MaxPages: 2, MaxPageChars: 50, Limits: convert.Limits{MaxOutputBytes: 80}})
	defer func() { _ = c.Close() }()
	page := func(chars int) []pdftest.Text { return []pdftest.Text{pdftest.L(12, 700, strings.Repeat("x", chars))} }
	for name, tc := range map[string]struct {
		pages [][]pdftest.Text
		limit string
		max   int64
	}{
		"within":          {[][]pdftest.Text{page(30), page(30)}, "", 0},
		"pages":           {[][]pdftest.Text{page(1), page(1), page(1)}, convert.LimitPages, 2},
		"page chars":      {[][]pdftest.Text{page(10), page(60)}, convert.LimitPageChars, 50},
		"text over limit": {[][]pdftest.Text{page(45), page(45)}, convert.LimitOutput, 80},
	} {
		_, err := c.Convert(context.Background(), convert.Input{Reader: bytes.NewReader(pdftest.Build(t, tc.pages, ""))})
		var limit *convert.LimitError
		switch {
		case tc.limit == "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case tc.limit != "" && (!errors.As(err, &limit) || limit.Limit != tc.limit || limit.Max != tc.max):
			t.Errorf("%s: err = %v, want the %s limit of %d", name, err, tc.limit, tc.max)
		}
	}
}

func TestLifecycle(t *testing.T) {
	data := pdftest.Build(t, [][]pdftest.Text{{pdftest.L(12, 700, "hello")}}, "")
	c := pdf.New(pdf.Options{Workers: 1})
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if doc, err := c.Convert(context.Background(), convert.Input{Reader: bytes.NewReader(data)}); err != nil || doc.Markdown != "hello" {
				t.Errorf("concurrent convert: %q, %v", doc.Markdown, err)
			}
		})
	}
	wg.Wait()

	// 等待唯一 worker 的调用遵守它的 context。
	blocker := newSlowReader(data)
	go func() { _, _ = c.Convert(context.Background(), convert.Input{Reader: blocker}) }()
	<-blocker.reading
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.Convert(ctx, convert.Input{Reader: bytes.NewReader(data)}); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("waiting call: %v", err)
	}

	// Close 等待进行中的调用，拒绝正在等待的和之后的调用。
	waiting := make(chan error, 1)
	go func() {
		_, err := c.Convert(context.Background(), convert.Input{Reader: bytes.NewReader(data)})
		waiting <- err
	}()
	closed := make(chan error, 1)
	go func() { closed <- c.Close() }()
	if err := <-waiting; !errors.Is(err, convert.ErrClosed) {
		t.Errorf("waiting call after Close: %v", err)
	}
	select {
	case <-closed:
		t.Fatal("Close returned before the call in flight finished")
	case <-time.After(50 * time.Millisecond):
	}
	close(blocker.release)
	if err := <-closed; err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	if _, err := c.Convert(context.Background(), convert.Input{Reader: bytes.NewReader(data)}); !errors.Is(err, convert.ErrClosed) {
		t.Errorf("after Close: %v", err)
	}
}

// slowReader 第一次读取时发出信号，之后阻塞到 release 关闭。
type slowReader struct {
	release chan struct{}
	reading chan struct{}
	data    []byte
	read    bool
}

// newSlowReader 返回提供 data 的 slowReader。
func newSlowReader(data []byte) *slowReader {
	return &slowReader{release: make(chan struct{}), reading: make(chan struct{}), data: data}
}

func (r *slowReader) Read(p []byte) (int, error) {
	if !r.read {
		close(r.reading)
		<-r.release
		r.read = true
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	if n == 0 {
		return 0, io.EOF
	}
	return n, nil
}
