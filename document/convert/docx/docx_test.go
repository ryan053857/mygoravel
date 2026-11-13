package docx_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"runtime"
	"strings"
	"testing"

	"github.com/go-tenon/tenon/document/convert"
	"github.com/go-tenon/tenon/document/convert/docx"
	"github.com/go-tenon/tenon/document/internal/ooxmltest"
)

const ns = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"`

// document 构造正文为 body 的 .docx。
func document(t *testing.T, body string) []byte {
	t.Helper()
	return ooxmltest.Build(t, map[string]string{
		"word/document.xml": `<w:document ` + ns + `><w:body>` + body + `</w:body></w:document>`,
		"word/styles.xml": `<w:styles ` + ns + `>
<w:style w:type="paragraph" w:styleId="Title"><w:name w:val="Title"/></w:style>
<w:style w:type="paragraph" w:styleId="H2"><w:name w:val="heading 2"/></w:style>
<w:style w:type="paragraph" w:styleId="Outline"><w:name w:val="Custom"/><w:pPr><w:outlineLvl w:val="2"/></w:pPr></w:style>
<w:style w:type="paragraph"><w:name w:val="Title"/></w:style>
<w:style w:type="paragraph" w:styleId="ListNumber"><w:name w:val="List Number"/><w:pPr><w:numPr><w:numId w:val="1"/></w:numPr></w:pPr></w:style>
<w:style w:type="paragraph" w:styleId="Derived"><w:name w:val="Derived"/><w:basedOn w:val="ListNumber"/></w:style>
<w:style w:type="paragraph" w:styleId="Linked"><w:name w:val="Linked"/><w:pPr><w:numPr><w:numId w:val="1"/></w:numPr></w:pPr></w:style>
<w:style w:type="paragraph" w:styleId="SubHeading"><w:name w:val="Sub"/><w:basedOn w:val="H2"/></w:style>
<w:style w:type="paragraph" w:styleId="NotHeading"><w:name w:val="Body"/><w:basedOn w:val="H2"/><w:pPr><w:outlineLvl w:val="9"/></w:pPr></w:style>
<w:style w:type="paragraph" w:styleId="LinkedOverride"><w:name w:val="Linked override"/><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr></w:style>
<w:style w:type="paragraph" w:styleId="ChainMiddle"><w:name w:val="Chain middle"/><w:basedOn w:val="ListNumber"/></w:style>
<w:style w:type="paragraph" w:styleId="ChainChild"><w:name w:val="Chain child"/><w:basedOn w:val="ChainMiddle"/></w:style>
<w:style w:type="paragraph" w:styleId="LevelOverLink"><w:name w:val="Level over link"/><w:basedOn w:val="Linked"/><w:pPr><w:numPr><w:ilvl w:val="0"/></w:numPr></w:pPr></w:style>
<w:style w:type="paragraph" w:styleId="LevelOnly"><w:name w:val="Level only"/><w:basedOn w:val="ListNumber"/><w:pPr><w:numPr><w:ilvl w:val="1"/></w:numPr></w:pPr></w:style>
</w:styles>`,
		"word/numbering.xml": `<w:numbering ` + ns + `>
<w:abstractNum w:abstractNumId="0"><w:lvl w:ilvl="0"><w:numFmt w:val="decimal"/></w:lvl><w:lvl w:ilvl="1"><w:numFmt w:val="bullet"/></w:lvl><w:lvl w:ilvl="2"><w:numFmt w:val="decimal"/><w:pStyle w:val="Linked"/></w:lvl><w:lvl w:ilvl="3"><w:numFmt w:val="bullet"/><w:pStyle w:val="LinkedOverride"/></w:lvl><w:lvl w:ilvl="4"><w:numFmt w:val="decimal"/><w:pStyle w:val="ChainMiddle"/></w:lvl></w:abstractNum>
<w:num w:numId="1"><w:abstractNumId w:val="0"/></w:num>
<w:num w:numId="2"><w:abstractNumId w:val="0"/><w:lvlOverride w:ilvl="0"><w:lvl w:ilvl="0"><w:numFmt w:val="bullet"/></w:lvl></w:lvlOverride></w:num>
</w:numbering>`,
		"word/_rels/document.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/hyperlink" Target="https://example.com/a" TargetMode="External"/>
</Relationships>`,
	})
}

// p 构造一个段落，可以带段落属性。
func p(props, text string) string {
	return `<w:p><w:pPr>` + props + `</w:pPr><w:r><w:t xml:space="preserve">` + text + `</w:t></w:r></w:p>`
}

// convertData 用转换器转换 data。
func convertData(t *testing.T, data []byte) (convert.Document, error) {
	t.Helper()
	return docx.New(docx.Options{}).Convert(context.Background(), convert.Input{Reader: bytes.NewReader(data)})
}

func TestStructure(t *testing.T) {
	body := p(`<w:pStyle w:val="Title"/>`, "售后  政策") +
		p(`<w:pStyle w:val="H2"/>`, "退款") +
		p(`<w:pStyle w:val="Outline"/>`, "细则") +
		p(``, "# 不是标题") +
		p(`<w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr>`, "第一步") +
		p(`<w:numPr><w:ilvl w:val="1"/><w:numId w:val="1"/></w:numPr>`, "要点") +
		`<w:p><w:r><w:t>见</w:t></w:r><w:hyperlink r:id="rId1"><w:r><w:t>条款 [1]</w:t></w:r></w:hyperlink><w:r><w:tab/><w:t>完</w:t><w:br/><w:t>- 换行</w:t></w:r></w:p>` +
		`<w:sdt><w:sdtContent>` + p(``, "内容控件") + `</w:sdtContent></w:sdt>` +
		p(``, "   ")
	doc, err := convertData(t, document(t, body))
	if err != nil {
		t.Fatal(err)
	}
	want := "# 售后 政策\n\n## 退款\n\n### 细则\n\n\\# 不是标题\n\n1. 第一步\n\n   - 要点\n\n" +
		"见[条款 \\[1\\]](<https://example.com/a>) 完\n\\- 换行\n\n内容控件"
	if doc.Markdown != want {
		t.Errorf("got:\n%s\nwant:\n%s", doc.Markdown, want)
	}
	if doc.Sections != nil {
		t.Errorf("Sections = %v", doc.Sections)
	}
}

func TestLists(t *testing.T) {
	body := p(`<w:numPr><w:numId w:val="1"/></w:numPr>`, "no level") +
		p(`<w:numPr><w:ilvl w:val="0"/><w:numId w:val="0"/></w:numPr>`, "numbering removed") +
		p(`<w:pStyle w:val="ListNumber"/>`, "from style") +
		p(`<w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr>`, "outer") +
		p(`<w:numPr><w:ilvl w:val="1"/><w:numId w:val="1"/></w:numPr>`, "inner") +
		p(`<w:numPr><w:ilvl w:val="9223372036854775807"/><w:numId w:val="1"/></w:numPr>`, "deep") +
		p(``, "plain")
	doc, err := convertData(t, document(t, body))
	if err != nil {
		t.Fatal(err)
	}
	want := "1. no level\n\nnumbering removed\n\n1. from style\n\n1. outer\n\n   - inner\n\n" + strings.Repeat("   ", 8) + "- deep\n\nplain"
	if doc.Markdown != want {
		t.Errorf("got:\n%s\nwant:\n%s", doc.Markdown, want)
	}
}

func TestNumberingInheritance(t *testing.T) {
	body := p(`<w:pStyle w:val="ListNumber"/><w:numPr><w:ilvl w:val="1"/></w:numPr>`, "level from paragraph") +
		p(`<w:pStyle w:val="Derived"/>`, "based on") +
		p(`<w:pStyle w:val="Linked"/>`, "linked level") +
		p(`<w:numPr><w:ilvl w:val="0"/><w:numId w:val="2"/></w:numPr>`, "override") +
		p(`<w:pStyle w:val="SubHeading"/>`, "inherited heading") +
		p(`<w:pStyle w:val="NotHeading"/>`, "body style") +
		p(`<w:pStyle w:val="H2"/><w:outlineLvl w:val="9"/>`, "body paragraph") +
		p(`<w:pStyle w:val="LinkedOverride"/>`, "linked wins") +
		p(`<w:pStyle w:val="LevelOnly"/>`, "derived level") +
		p(`<w:pStyle w:val="ChainChild"/>`, "middle link") +
		p(`<w:pStyle w:val="LevelOverLink"/>`, "own level wins")
	doc, err := convertData(t, document(t, body))
	if err != nil {
		t.Fatal(err)
	}
	want := "   - level from paragraph\n\n1. based on\n\n      1. linked level\n\n- override\n\n## inherited heading\n\nbody style\n\nbody paragraph\n\n         - linked wins\n\n   - derived level\n\n            1. middle link\n\n1. own level wins"
	if doc.Markdown != want {
		t.Errorf("got:\n%s\nwant:\n%s", doc.Markdown, want)
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

func TestCancelAfterReading(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	_, err := docx.New(docx.Options{}).Convert(ctx, convert.Input{Reader: cancelOnEOF{bytes.NewReader(document(t, "")), cancel}})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("got %v", err)
	}
}

func TestUnlimitedExpansion(t *testing.T) {
	data := document(t, p(``, "text"))
	doc, err := docx.New(docx.Options{Limits: convert.Limits{MaxExpandedBytes: math.MaxInt64}}).Convert(context.Background(), convert.Input{Reader: bytes.NewReader(data)})
	if err != nil || doc.Markdown != "text" {
		t.Errorf("got %q, %v", doc.Markdown, err)
	}
}

func TestStrictAndLineEndings(t *testing.T) {
	strict := `xmlns:w="http://purl.oclc.org/ooxml/wordprocessingml/main" xmlns:r="http://purl.oclc.org/ooxml/officeDocument/relationships"`
	data := ooxmltest.Build(t, map[string]string{
		"word/document.xml": `<w:document ` + strict + `><w:body><w:p><w:hyperlink r:id="rId1"><w:r><w:t>link</w:t></w:r></w:hyperlink></w:p>` +
			`<w:p><w:r><w:t>intro&#13;# injected</w:t></w:r></w:p></w:body></w:document>`,
		"word/_rels/document.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://purl.oclc.org/ooxml/officeDocument/relationships/hyperlink" Target="https://e.com/a&lt;b" TargetMode="External"/>
</Relationships>`,
	})
	doc, err := convertData(t, data)
	if want := "[link](<https://e.com/a%3Cb>)\n\nintro\n\\# injected"; err != nil || doc.Markdown != want {
		t.Errorf("got %q, %v; want %q", doc.Markdown, err, want)
	}
}

func TestTable(t *testing.T) {
	cell := func(props, text string) string {
		return `<w:tc><w:tcPr>` + props + `</w:tcPr>` + p(``, text) + `</w:tc>`
	}
	body := `<w:tbl>
<w:tr>` + cell(``, "地区") + cell(``, "首重") + cell(``, "续重") + `</w:tr>
<w:tr>` + cell(`<w:vMerge w:val="restart"/>`, "华东") + cell(`<w:gridSpan w:val="2"/>`, "8 | 2 元") + `</w:tr>
<w:tr>` + cell(`<w:vMerge/>`, "") + cell(``, "- 9") + cell(``, `C:\dir`) + `</w:tr>
</w:tbl>`
	doc, err := convertData(t, document(t, body))
	if err != nil {
		t.Fatal(err)
	}
	want := "| 地区 | 首重 | 续重 |\n| --- | --- | --- |\n| 华东 | 8 \\| 2 元 | 8 \\| 2 元 |\n| 华东 | - 9 | C:\\\\dir |"
	if doc.Markdown != want {
		t.Errorf("got:\n%s\nwant:\n%s", doc.Markdown, want)
	}
}

func TestErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		data []byte
		want error
	}{
		"empty":     {nil, convert.ErrCorrupt},
		"not zip":   {[]byte("plain text"), convert.ErrCorrupt},
		"encrypted": {ooxmltest.Encrypted(), convert.ErrEncrypted},
		"legacy":    {ooxmltest.Legacy(), convert.ErrUnsupported},
		"no body":   {ooxmltest.Build(t, map[string]string{"word/document.xml": `<w:document ` + ns + `/>`}), convert.ErrCorrupt},
		"no part":   {ooxmltest.Build(t, map[string]string{"word/other.xml": `<x/>`}), convert.ErrCorrupt},
		"bad xml":   {ooxmltest.Build(t, map[string]string{"word/document.xml": `<w:document ` + ns + `><w:body>`}), convert.ErrCorrupt},
	} {
		if _, err := convertData(t, tc.data); !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", name, err, tc.want)
		}
	}
}

func TestLimits(t *testing.T) {
	data := document(t, strings.Repeat(p(``, strings.Repeat("长", 100)), 100))
	for name, tc := range map[string]struct {
		limits convert.Limits
		kind   string
	}{
		"expanded": {convert.Limits{MaxExpandedBytes: 1000}, convert.LimitExpanded},
		"output":   {convert.Limits{MaxOutputBytes: 1000}, convert.LimitOutput},
		"source":   {convert.Limits{MaxBytes: 100}, convert.LimitSource},
	} {
		_, err := docx.New(docx.Options{Limits: tc.limits}).Convert(context.Background(), convert.Input{Reader: bytes.NewReader(data)})
		var limit *convert.LimitError
		if !errors.As(err, &limit) || limit.Limit != tc.kind {
			t.Errorf("%s: got %v", name, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := docx.New(docx.Options{}).Convert(ctx, convert.Input{Reader: bytes.NewReader(data)}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
}

func TestOutputLimitIsExact(t *testing.T) {
	cell := `<w:tc><w:tcPr><w:gridSpan w:val="3"/></w:tcPr>` + p(``, "格") + `</w:tc>`
	data := document(t, p(``, "一")+`<w:tbl><w:tr>`+cell+`</w:tr></w:tbl>`+p(``, "二"))
	doc, err := convertData(t, data)
	if err != nil {
		t.Fatal(err)
	}
	size := int64(len(doc.Markdown))
	if _, err := docx.New(docx.Options{Limits: convert.Limits{MaxOutputBytes: size}}).Convert(context.Background(), convert.Input{Reader: bytes.NewReader(data)}); err != nil {
		t.Errorf("at the limit: %v", err)
	}
	_, err = docx.New(docx.Options{Limits: convert.Limits{MaxOutputBytes: size - 1}}).Convert(context.Background(), convert.Input{Reader: bytes.NewReader(data)})
	var limit *convert.LimitError
	if !errors.As(err, &limit) || limit.Limit != convert.LimitOutput || limit.Max != size-1 {
		t.Errorf("one byte over: %v", err)
	}
}

// allocated 返回 f 分配的字节数。
func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func TestSpannedCellsStopAtOutputLimit(t *testing.T) {
	// 每个表格单独都放得进输出上限；转换在第一个放不进剩余空间的表格处停止，合并单元格的副本共用内存。
	cell := `<w:tc><w:tcPr><w:gridSpan w:val="60"/></w:tcPr>` + p(``, strings.Repeat("x", 16<<10)) + `</w:tc>`
	data := document(t, strings.Repeat(`<w:tbl><w:tr>`+cell+`</w:tr></w:tbl>`, 100))
	var err error
	if bytes := allocated(func() {
		_, err = docx.New(docx.Options{Limits: convert.Limits{MaxOutputBytes: 1500 << 10}}).Convert(context.Background(), convert.Input{Reader: bytes.NewReader(data)})
	}); bytes > 32<<20 {
		t.Errorf("allocated %d MiB", bytes>>20)
	}
	var limit *convert.LimitError
	if !errors.As(err, &limit) || limit.Limit != convert.LimitOutput {
		t.Fatalf("err = %v, want the output limit", err)
	}
}

func TestDeepNesting(t *testing.T) {
	deep := strings.Repeat("<x>", 1<<20) + strings.Repeat("</x>", 1<<20)
	for name, body := range map[string]string{
		"body":      deep,
		"paragraph": `<w:p>` + deep + `</w:p>`,
	} {
		if _, err := convertData(t, document(t, body)); !errors.Is(err, convert.ErrCorrupt) {
			t.Errorf("%s: err = %v, want ErrCorrupt", name, err)
		}
	}
}

func TestNestedHyperlinks(t *testing.T) {
	link := func(inner string) string { return `<w:hyperlink r:id="rId1">` + inner + `</w:hyperlink>` }
	body := `<w:p>` + link(`<w:r><w:t>外</w:t></w:r>`+link(`<w:r><w:t>内</w:t></w:r>`)) + `</w:p>`
	doc, err := convertData(t, document(t, body))
	if want := "[外内](<https://example.com/a>)"; err != nil || doc.Markdown != want {
		t.Errorf("got %q, %v; want %q", doc.Markdown, err, want)
	}
}

func FuzzConvert(f *testing.F) {
	f.Add([]byte("PK"))
	c := docx.New(docx.Options{})
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = c.Convert(context.Background(), convert.Input{Reader: bytes.NewReader(data)})
	})
}
