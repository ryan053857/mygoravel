package pptx_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"testing"

	"github.com/go-tenon/tenon/document/convert"
	"github.com/go-tenon/tenon/document/convert/pptx"
	"github.com/go-tenon/tenon/document/internal/ooxmltest"
)

const ns = `xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:c="http://schemas.openxmlformats.org/drawingml/2006/chart"`

const rels = `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">%s</Relationships>`

// rel 构造一个关系元素。
func rel(id, kind, target string) string {
	return fmt.Sprintf(`<Relationship Id="%s" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/%s" Target="%s"/>`, id, kind, target)
}

// shape 构造一个文本形状；kind 是占位符类型或 ""。
func shape(kind string, paragraphs ...string) string {
	ph := ""
	if kind != "" {
		ph = `<p:ph type="` + kind + `"/>`
	}
	var body strings.Builder
	for _, text := range paragraphs {
		body.WriteString(`<a:p><a:r><a:t>` + text + `</a:t></a:r></a:p>`)
	}
	return `<p:sp><p:nvSpPr><p:nvPr>` + ph + `</p:nvPr></p:nvSpPr><p:txBody>` + body.String() + `</p:txBody></p:sp>`
}

// deck 用幻灯片的元素树构造演示文稿；以 "hidden:" 开头的幻灯片是隐藏的。
func deck(t *testing.T, notes map[int]string, slides ...string) []byte {
	t.Helper()
	return ooxmltest.Build(t, deckFiles(notes, slides...))
}

// deckFiles 返回 deck 要打包的各个部件，供需要先修改它们的测试使用。
func deckFiles(notes map[int]string, slides ...string) map[string]string {
	files := map[string]string{}
	var ids, presRels strings.Builder
	for i, tree := range slides {
		n := i + 1
		show := ""
		if after, ok := strings.CutPrefix(tree, "hidden:"); ok {
			tree, show = after, ` show="0"`
		}
		fmt.Fprintf(&ids, `<p:sldId id="%d" r:id="rId%d"/>`, 255+n, n)
		presRels.WriteString(rel(fmt.Sprint("rId", n), "slide", fmt.Sprintf("slides/slide%d.xml", n)))
		files[fmt.Sprintf("ppt/slides/slide%d.xml", n)] = `<p:sld ` + ns + show + `><p:cSld><p:spTree>` + tree + `</p:spTree></p:cSld></p:sld>`
		slideRels := rel("rIdChart", "chart", "../charts/chart1.xml")
		if note, ok := notes[n]; ok {
			slideRels += rel("rIdNotes", "notesSlide", fmt.Sprintf("../notesSlides/notesSlide%d.xml", n))
			files[fmt.Sprintf("ppt/notesSlides/notesSlide%d.xml", n)] = `<p:notes ` + ns + `><p:cSld><p:spTree>` +
				shape("sldImg") + shape("body", note) + shape("sldNum", "7") + `</p:spTree></p:cSld></p:notes>`
		}
		files[fmt.Sprintf("ppt/slides/_rels/slide%d.xml.rels", n)] = fmt.Sprintf(rels, slideRels)
	}
	files["ppt/presentation.xml"] = `<p:presentation ` + ns + `><p:sldIdLst>` + ids.String() + `</p:sldIdLst></p:presentation>`
	files["ppt/_rels/presentation.xml.rels"] = fmt.Sprintf(rels, presRels.String())
	files["ppt/charts/chart1.xml"] = `<c:chartSpace ` + ns + `><c:chart><c:title><c:tx><c:rich><a:p><a:r><a:t>销量</a:t></a:r></a:p></c:rich></c:tx></c:title><c:plotArea><c:barChart>
<c:ser><c:tx><c:strRef><c:strCache><c:pt idx="0"><c:v>2025</c:v></c:pt></c:strCache></c:strRef></c:tx>
<c:cat><c:strRef><c:strCache><c:pt idx="0"><c:v>一月</c:v></c:pt><c:pt idx="1"><c:v>二月</c:v></c:pt></c:strCache></c:strRef></c:cat>
<c:val><c:numRef><c:numCache><c:pt idx="0"><c:v>10</c:v></c:pt><c:pt idx="1"><c:v>12</c:v></c:pt></c:numCache></c:numRef></c:val></c:ser>
</c:barChart></c:plotArea></c:chart></c:chartSpace>`
	return files
}

// edit 对 files 的每个部件应用 replace。
func edit(files map[string]string, oldnew ...string) map[string]string {
	replacer := strings.NewReplacer(oldnew...)
	for name, content := range files {
		files[name] = replacer.Replace(content)
	}
	return files
}

const chartFrame = `<p:graphicFrame><a:graphic><a:graphicData><c:chart r:id="rIdChart"/></a:graphicData></a:graphic></p:graphicFrame>`

const tableFrame = `<p:graphicFrame><a:graphic><a:graphicData><a:tbl>
<a:tr><a:tc><a:txBody><a:p><a:r><a:t>项目</a:t></a:r></a:p></a:txBody></a:tc><a:tc><a:txBody><a:p><a:r><a:t>状态</a:t></a:r></a:p></a:txBody></a:tc></a:tr>
<a:tr><a:tc><a:txBody><a:p><a:r><a:t>上线</a:t></a:r></a:p></a:txBody></a:tc><a:tc><a:txBody><a:p><a:r><a:t>完成</a:t></a:r></a:p></a:txBody></a:tc></a:tr>
</a:tbl></a:graphicData></a:graphic></p:graphicFrame>`

// run 用 opts 转换 data。
func run(t *testing.T, opts pptx.Options, data []byte) convert.Document {
	t.Helper()
	doc, err := pptx.New(opts).Convert(context.Background(), convert.Input{Reader: bytes.NewReader(data)})
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestSlides(t *testing.T) {
	data := deck(t, map[int]string{1: "讲者备注"},
		shape("title", "季度", "回顾")+shape("", "# 要点一", "要点二"),
		"",
		`<p:grpSp>`+shape("ctrTitle", "数据")+`</p:grpSp>`+chartFrame+tableFrame,
		"hidden:"+shape("title", "隐藏页"),
	)
	doc := run(t, pptx.Options{}, data)
	want := "# 季度 回顾\n\n\\# 要点一\n要点二\n\n讲者备注\n\n# 数据\n\n销量\n\n|  | 2025 |\n| --- | --- |\n| 一月 | 10 |\n| 二月 | 12 |\n\n| 项目 | 状态 |\n| --- | --- |\n| 上线 | 完成 |"
	if doc.Markdown != want {
		t.Fatalf("got:\n%s\nwant:\n%s", doc.Markdown, want)
	}
	if len(doc.Sections) != 4 {
		t.Fatalf("sections = %+v", doc.Sections)
	}
	for i, s := range doc.Sections {
		if s.Kind != convert.KindSlide || s.Number != i+1 {
			t.Errorf("section %d = %+v", i, s)
		}
	}
	first, empty, third, hidden := doc.Sections[0], doc.Sections[1], doc.Sections[2], doc.Sections[3]
	if got := doc.Markdown[first.Start:first.End]; got != "# 季度 回顾\n\n\\# 要点一\n要点二\n\n讲者备注" || first.Name != "季度 回顾" {
		t.Errorf("first slide = %q, name %q", got, first.Name)
	}
	if empty.Start != empty.End || hidden.Start != hidden.End || hidden.End != len(doc.Markdown) {
		t.Errorf("empty or hidden slide not empty: %+v %+v", empty, hidden)
	}
	if !strings.HasPrefix(doc.Markdown[third.Start:third.End], "# 数据") || third.End != len(doc.Markdown) {
		t.Errorf("third slide = %+v", third)
	}
}

func TestOptions(t *testing.T) {
	data := deck(t, map[int]string{1: "备注"}, shape("title", "一")+shape("", "正文"), "hidden:"+shape("", "隐藏"))
	doc := run(t, pptx.Options{SkipNotes: true, IncludeHidden: true}, data)
	if doc.Markdown != "# 一\n\n正文\n\n隐藏" {
		t.Errorf("got %q", doc.Markdown)
	}
	if s := doc.Sections[1]; doc.Markdown[s.Start:s.End] != "隐藏" {
		t.Errorf("hidden section = %+v", s)
	}
}

func TestHiddenFalse(t *testing.T) {
	data := ooxmltest.Build(t, edit(deckFiles(nil, shape("", "shown"), "hidden:"+shape("", "secret")), `show="0"`, `show="false"`))
	if doc := run(t, pptx.Options{}, data); doc.Markdown != "shown" {
		t.Errorf("got %q", doc.Markdown)
	}
}

func TestCharts(t *testing.T) {
	point := func(idx int, v string) string { return fmt.Sprintf(`<c:pt idx="%d"><c:v>%s</c:v></c:pt>`, idx, v) }
	values := func(tag string, vs ...string) string {
		var b strings.Builder
		for i, v := range vs {
			b.WriteString(point(i, v))
		}
		return `<c:` + tag + `><c:numRef><c:numCache>` + b.String() + `</c:numCache></c:numRef></c:` + tag + `>`
	}
	name := func(n string) string {
		return `<c:tx><c:strRef><c:strCache>` + point(0, n) + `</c:strCache></c:strRef></c:tx>`
	}
	chart := `<c:chartSpace ` + ns + `><c:chart><c:title><c:tx><c:strRef><c:strCache>` + point(0, "趋势") + `</c:strCache></c:strRef></c:tx></c:title><c:plotArea>
<c:scatterChart><c:ser>` + name("A") + values("xVal", "1", "2") + values("yVal", "5", "7") + `</c:ser>
<c:ser>` + name("B") + values("xVal", "100", "200") + values("yVal", "6", "8") + `</c:ser></c:scatterChart>
<c:bubbleChart><c:ser>` + values("xVal", "3") + values("yVal", "4") + values("bubbleSize", "9") + `</c:ser></c:bubbleChart>
</c:plotArea></c:chart></c:chartSpace>`
	want := "趋势\n\n|  | x | y | size |\n| --- | --- | --- | --- |\n| A | 1 | 5 |  |\n| A | 2 | 7 |  |\n| B | 100 | 6 |  |\n| B | 200 | 8 |  |\n|  | 3 | 4 | 9 |"
	if doc := run(t, pptx.Options{}, deckWithChart(t, chart)); doc.Markdown != want {
		t.Errorf("got:\n%s\nwant:\n%s", doc.Markdown, want)
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

func TestSparseChartPoints(t *testing.T) {
	// 只在最后一个可能的数据点有值的系列，只为这一个数据点占用内存。
	const series = 1000
	var chart strings.Builder
	chart.WriteString(`<c:chartSpace ` + ns + `><c:chart><c:plotArea><c:barChart>`)
	for range series {
		chart.WriteString(`<c:ser><c:val><c:numCache><c:pt idx="65535"><c:v>1</c:v></c:pt></c:numCache></c:val></c:ser>`)
	}
	chart.WriteString(`</c:barChart><c:scatterChart><c:ser><c:tx><c:v>S</c:v></c:tx>` +
		`<c:xVal><c:numLit><c:pt idx="0"><c:v>1</c:v></c:pt><c:pt idx="5"><c:v>2</c:v></c:pt></c:numLit></c:xVal>` +
		`<c:yVal><c:numLit><c:pt idx="5"><c:v>3</c:v></c:pt></c:numLit></c:yVal></c:ser></c:scatterChart></c:plotArea></c:chart></c:chartSpace>`)
	data := deckWithChart(t, chart.String())
	var doc convert.Document
	if bytes := allocated(func() { doc = run(t, pptx.Options{}, data) }); bytes > 64<<20 {
		t.Errorf("allocated %d MiB", bytes>>20)
	}
	tables := strings.Split(doc.Markdown, "\n\n")
	if len(tables) != 2 {
		t.Fatalf("got %d blocks:\n%s", len(tables), doc.Markdown)
	}
	if rows := strings.Split(tables[0], "\n"); len(rows) != 3 || strings.Count(rows[2], "| 1 ") != series {
		t.Errorf("category table has %d rows", len(rows))
	}
	// 散点图只为有值的数据点序号生成行，不为它们之间的空隙生成。
	if want := "|  | x | y |\n| --- | --- | --- |\n| S | 1 |  |\n| S | 2 | 3 |"; tables[1] != want {
		t.Errorf("point table:\n%s\nwant:\n%s", tables[1], want)
	}
}

func TestOutputLimitIsExact(t *testing.T) {
	point := `<c:pt idx="0"><c:v>1</c:v></c:pt>`
	chart := `<c:chartSpace ` + ns + `><c:chart><c:title><c:tx><c:strRef><c:strCache>` + point + `</c:strCache></c:strRef></c:tx></c:title><c:plotArea>` +
		`<c:barChart><c:ser><c:val><c:numCache>` + point + `</c:numCache></c:val></c:ser></c:barChart>` +
		`<c:scatterChart><c:ser><c:xVal><c:numLit>` + point + `</c:numLit></c:xVal><c:yVal><c:numLit>` + point + `</c:numLit></c:yVal></c:ser></c:scatterChart>` +
		`</c:plotArea></c:chart></c:chartSpace>`
	files := deckFiles(nil, "", shape("title", "一")+chartFrame, shape("", "二"))
	files["ppt/charts/chart1.xml"] = chart
	data := ooxmltest.Build(t, files)
	size := int64(len(run(t, pptx.Options{}, data).Markdown))
	run(t, pptx.Options{Limits: convert.Limits{MaxOutputBytes: size}}, data)
	_, err := pptx.New(pptx.Options{Limits: convert.Limits{MaxOutputBytes: size - 1}}).Convert(context.Background(), convert.Input{Reader: bytes.NewReader(data)})
	var limit *convert.LimitError
	if !errors.As(err, &limit) || limit.Limit != convert.LimitOutput || limit.Max != size-1 {
		t.Errorf("one byte over: %v", err)
	}
}

func TestDeepNesting(t *testing.T) {
	deep := strings.Repeat("<x>", 1<<20) + strings.Repeat("</x>", 1<<20)
	for name, data := range map[string][]byte{
		"slide": deck(t, nil, `<p:sp><p:txBody><a:p>`+deep+`</a:p></p:txBody></p:sp>`),
		"chart": deckWithChart(t, `<c:chartSpace `+ns+`><c:chart><c:plotArea><c:barChart><c:ser><c:val>`+deep+`</c:val></c:ser></c:barChart></c:plotArea></c:chart></c:chartSpace>`),
	} {
		_, err := pptx.New(pptx.Options{}).Convert(context.Background(), convert.Input{Reader: bytes.NewReader(data)})
		if !errors.Is(err, convert.ErrCorrupt) {
			t.Errorf("%s: err = %v, want ErrCorrupt", name, err)
		}
	}
}

func TestSlideOutputBudget(t *testing.T) {
	// 每个表格单独都放得进输出上限；幻灯片在放不下的表格处停止。
	cell := strings.Repeat("x", 100<<10)
	table := `<p:graphicFrame><a:graphic><a:graphicData><a:tbl><a:tr><a:tc><a:txBody><a:p><a:r><a:t>` + cell +
		`</a:t></a:r></a:p></a:txBody></a:tc></a:tr></a:tbl></a:graphicData></a:graphic></p:graphicFrame>`
	data := deck(t, nil, strings.Repeat(table, 200))
	var err error
	opts := pptx.Options{Limits: convert.Limits{MaxOutputBytes: 1 << 20}}
	if bytes := allocated(func() {
		_, err = pptx.New(opts).Convert(context.Background(), convert.Input{Reader: bytes.NewReader(data)})
	}); bytes > 48<<20 {
		t.Errorf("allocated %d MiB", bytes>>20)
	}
	var limit *convert.LimitError
	if !errors.As(err, &limit) || limit.Limit != convert.LimitOutput || limit.Max != 1<<20 {
		t.Fatalf("err = %v, want the output limit", err)
	}
}

func TestSlideListAndNotesOrder(t *testing.T) {
	files := deckFiles(nil, shape("", "one"), shape("", "two"))
	files["ppt/presentation.xml"] = strings.Replace(files["ppt/presentation.xml"], "<p:sldIdLst>", "<p:sldIdLst><p:extLst/>", 1)
	notes := func(text string) string {
		return `<p:notes ` + ns + `><p:cSld><p:spTree>` + shape("body", text) + `</p:spTree></p:cSld></p:notes>`
	}
	files["ppt/notesSlides/a.xml"], files["ppt/notesSlides/b.xml"] = notes("second"), notes("first")
	files["ppt/slides/_rels/slide1.xml.rels"] = fmt.Sprintf(rels, rel("rId10", "notesSlide", "../notesSlides/a.xml")+rel("rId2", "notesSlide", "../notesSlides/b.xml"))
	doc := run(t, pptx.Options{}, ooxmltest.Build(t, files))
	if doc.Markdown != "one\n\nfirst\n\nsecond\n\ntwo" || len(doc.Sections) != 2 || doc.Sections[0].Number != 1 || doc.Sections[1].Number != 2 {
		t.Errorf("got %q, %d sections", doc.Markdown, len(doc.Sections))
	}
}

// deckWithChart 构造只有一张幻灯片、图表部件为 chart 的演示文稿。
func deckWithChart(t *testing.T, chart string) []byte {
	t.Helper()
	files := map[string]string{
		"ppt/presentation.xml":             `<p:presentation ` + ns + `><p:sldIdLst><p:sldId id="256" r:id="rId1"/></p:sldIdLst></p:presentation>`,
		"ppt/_rels/presentation.xml.rels":  fmt.Sprintf(rels, rel("rId1", "slide", "slides/slide1.xml")),
		"ppt/slides/slide1.xml":            `<p:sld ` + ns + `><p:cSld><p:spTree>` + chartFrame + `</p:spTree></p:cSld></p:sld>`,
		"ppt/slides/_rels/slide1.xml.rels": fmt.Sprintf(rels, rel("rIdChart", "chart", "../charts/chart1.xml")),
		"ppt/charts/chart1.xml":            chart,
	}
	return ooxmltest.Build(t, files)
}

func TestStrict(t *testing.T) {
	files := edit(deckFiles(nil, shape("title", "严格")),
		"http://schemas.openxmlformats.org/officeDocument/2006/relationships", "http://purl.oclc.org/ooxml/officeDocument/relationships")
	if !strings.Contains(files["ppt/presentation.xml"], "purl.oclc.org") {
		t.Fatal("namespace not replaced")
	}
	data := ooxmltest.Build(t, files)
	if doc := run(t, pptx.Options{}, data); doc.Markdown != "# 严格" {
		t.Errorf("got %q", doc.Markdown)
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
	_, err := pptx.New(pptx.Options{}).Convert(ctx, convert.Input{Reader: cancelOnEOF{bytes.NewReader(deck(t, nil)), cancel}})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("got %v", err)
	}
}

func TestErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		data []byte
		want error
	}{
		"empty":           {nil, convert.ErrCorrupt},
		"encrypted":       {ooxmltest.Encrypted(), convert.ErrEncrypted},
		"no presentation": {ooxmltest.Build(t, map[string]string{"x.xml": "<x/>"}), convert.ErrCorrupt},
		"missing slide": {ooxmltest.Build(t, map[string]string{
			"ppt/presentation.xml":            `<p:presentation ` + ns + `><p:sldIdLst><p:sldId id="256" r:id="rId1"/></p:sldIdLst></p:presentation>`,
			"ppt/_rels/presentation.xml.rels": fmt.Sprintf(rels, rel("rId1", "slide", "slides/slide1.xml")),
		}), convert.ErrCorrupt},
	} {
		_, err := pptx.New(pptx.Options{}).Convert(context.Background(), convert.Input{Reader: bytes.NewReader(tc.data)})
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", name, err, tc.want)
		}
	}
	_, err := pptx.New(pptx.Options{Limits: convert.Limits{MaxOutputBytes: 10}}).Convert(context.Background(),
		convert.Input{Reader: bytes.NewReader(deck(t, nil, shape("", strings.Repeat("长", 20))))})
	if !errors.Is(err, convert.ErrTooLarge) {
		t.Errorf("output limit: %v", err)
	}
}

func FuzzConvert(f *testing.F) {
	f.Add([]byte("PK"))
	c := pptx.New(pptx.Options{})
	f.Fuzz(func(t *testing.T, data []byte) {
		doc, err := c.Convert(context.Background(), convert.Input{Reader: bytes.NewReader(data)})
		if err != nil {
			return
		}
		for _, s := range doc.Sections {
			if s.Start < 0 || s.End > len(doc.Markdown) || s.Start > s.End {
				t.Fatalf("bad section %+v", s)
			}
		}
	})
}
