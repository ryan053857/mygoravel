package sheet_test

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/text/encoding/simplifiedchinese"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/i18n"
	"github.com/ryan053857/mygoravel/sheet"
)

type changes struct {
	Title    string   `json:"title" sheet:"标题,文章标题" minLength:"1" maxLength:"20"`
	Slug     string   `json:"slug" pattern:"^[a-z0-9-]+$" patternDescription:"小写字母、数字或连字符"`
	Priority int      `json:"priority,omitempty" sheet:"优先级" maximum:"10"`
	Tags     []string `json:"tags,omitempty" sheet:"标签" maxItems:"3"`
	Note     string   `json:"note,omitempty" sheet:"-"`
}

func zh(t *testing.T) context.Context {
	t.Helper()
	b, err := i18n.New("zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	return i18n.WithLocale(context.Background(), b, "zh-CN")
}

func read(t *testing.T, data []byte, f sheet.Format) *sheet.Reader {
	t.Helper()
	r, err := sheet.NewReader(bytes.NewReader(data), f)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func rows(t *testing.T, r *sheet.Reader) []sheet.Row {
	t.Helper()
	var list []sheet.Row
	for row, err := range r.Rows() {
		if err != nil {
			t.Fatal(err)
		}
		list = append(list, row)
	}
	return list
}

func TestFormatOf(t *testing.T) {
	for name, want := range map[string]sheet.Format{"a.csv": sheet.CSV, "报表.XLSX": sheet.XLSX} {
		if f, err := sheet.FormatOf(name); err != nil || f != want {
			t.Errorf("%s: %q %v", name, f, err)
		}
	}
	if _, err := sheet.FormatOf("a.xls"); !errors.Is(err, sheet.ErrFormat) {
		t.Fatalf("xls: %v", err)
	}
}

func TestReadCSV(t *testing.T) {
	data := "\xef\xbb\xbf\n 标题 ,slug\nHello, hello \n,\n\"多\n行\",b\n"
	r := read(t, []byte(data), sheet.CSV)
	if !reflect.DeepEqual(r.Header(), []string{"标题", "slug"}) {
		t.Fatalf("header: %q", r.Header())
	}
	want := []sheet.Row{{Line: 3, Values: []string{"Hello", "hello"}}, {Line: 5, Values: []string{"多\n行", "b"}}}
	if got := rows(t, r); !reflect.DeepEqual(got, want) {
		t.Fatalf("rows: %+v", got)
	}
}

func TestReadCSVEncodingAndDelimiter(t *testing.T) {
	gbk, err := simplifiedchinese.GB18030.NewEncoder().String("标题;备注\n你好;世界\n")
	if err != nil {
		t.Fatal(err)
	}
	r := read(t, []byte(gbk), sheet.CSV)
	if !reflect.DeepEqual(r.Header(), []string{"标题", "备注"}) {
		t.Fatalf("header: %q", r.Header())
	}
	if got := rows(t, r); !reflect.DeepEqual(got[0].Values, []string{"你好", "世界"}) {
		t.Fatalf("rows: %+v", got)
	}

	r = read(t, []byte("a\tb\n1\t2\n"), sheet.CSV)
	if !reflect.DeepEqual(r.Header(), []string{"a", "b"}) {
		t.Fatalf("tab header: %q", r.Header())
	}
}

func TestReadErrors(t *testing.T) {
	if _, err := sheet.NewReader(strings.NewReader("\n , \n"), sheet.CSV); !errors.Is(err, sheet.ErrNoHeader) {
		t.Fatalf("empty csv: %v", err)
	}
	if _, err := sheet.NewReader(strings.NewReader("not a zip"), sheet.XLSX); !errors.Is(err, sheet.ErrBadFile) {
		t.Fatalf("bad xlsx: %v", err)
	}
	if _, err := sheet.NewReader(strings.NewReader("a"), "xls"); !errors.Is(err, sheet.ErrFormat) {
		t.Fatalf("format: %v", err)
	}
}

func TestWriteCSV(t *testing.T) {
	var buf bytes.Buffer
	w := sheet.NewWriter(&buf, sheet.CSV)
	at := time.Date(2026, 11, 11, 9, 30, 0, 0, time.FixedZone("CST", 8*3600))
	var none *time.Time
	id := uuid.MustParse("0193e7a2-7c1e-7b4a-9d2f-3c5a1e8b6f10")
	must(t, w.Write("文本", "数字", "时间", "空", "列表", "ID"))
	must(t, w.Write("=1+1", -5, at, none, []string{"a", "b"}, id))
	must(t, w.Write("+86 138", "-5", time.Time{}, true, 1.5, &at))
	must(t, w.Close())
	want := "\xef\xbb\xbf文本,数字,时间,空,列表,ID\n" +
		"'=1+1,-5,2026-11-11 09:30:00,,\"a,b\",0193e7a2-7c1e-7b4a-9d2f-3c5a1e8b6f10\n" +
		"'+86 138,-5,,true,1.5,2026-11-11 09:30:00\n"
	if buf.String() != want {
		t.Fatalf("csv:\n%s", buf.String())
	}
}

func TestXLSXRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	w := sheet.NewWriter(&buf, sheet.XLSX)
	at := time.Date(2026, 11, 11, 9, 30, 0, 0, time.FixedZone("CST", 8*3600))
	must(t, w.Write("标题", "数量", "时间", "公式"))
	must(t, w.Write("Hello", 3, at, "=1+1"))
	must(t, w.Write())
	must(t, w.Write("World", 2.5, nil, true))
	must(t, w.Close())
	if err := w.Write("late"); err == nil {
		t.Fatal("write after close")
	}

	r := read(t, buf.Bytes(), sheet.XLSX)
	if !reflect.DeepEqual(r.Header(), []string{"标题", "数量", "时间", "公式"}) {
		t.Fatalf("header: %q", r.Header())
	}
	want := []sheet.Row{
		{Line: 2, Values: []string{"Hello", "3", "2026-11-11 09:30:00", "=1+1"}},
		{Line: 4, Values: []string{"World", "2.5", "", "TRUE"}},
	}
	if got := rows(t, r); !reflect.DeepEqual(got, want) {
		t.Fatalf("rows: %+v", got)
	}
}

func TestDecode(t *testing.T) {
	data := "文章标题,SLUG,优先级,标签,note,其他\n" +
		"Hello,hello,3,\"go，web\",ignored,x\n" +
		",Bad Slug,11,\"a,b,c,d\",,\n"
	r := read(t, []byte(data), sheet.CSV)
	records, err := sheet.Decode[changes](zh(t), r)
	if err != nil {
		t.Fatal(err)
	}
	var got []sheet.Record[changes]
	for rec, err := range records {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, rec)
	}
	if len(got) != 2 {
		t.Fatalf("records: %+v", got)
	}
	ok := got[0]
	if !ok.Valid() || ok.Line != 2 || !reflect.DeepEqual(ok.Data, changes{Title: "Hello", Slug: "hello", Priority: 3, Tags: []string{"go", "web"}}) {
		t.Fatalf("valid record: %+v", ok)
	}
	if !reflect.DeepEqual(ok.Values, []string{"Hello", "hello", "3", "go，web", "ignored", "x"}) {
		t.Fatalf("values: %q", ok.Values)
	}
	bad := got[1]
	want := map[string]string{"title": "不能为空", "slug": "必须是小写字母、数字或连字符", "priority": "不能大于 10", "tags": "最多选择 3 项"}
	if bad.Valid() || bad.Line != 3 || !reflect.DeepEqual(bad.Errors, want) {
		t.Fatalf("invalid record: %+v", bad)
	}

	ok.SetError("slug", "已被占用")
	if ok.Valid() || ok.Errors["slug"] != "已被占用" {
		t.Fatalf("set error: %+v", ok.Errors)
	}
}

func TestDecodeMissingColumns(t *testing.T) {
	r := read(t, []byte("优先级,备注\n1,x\n"), sheet.CSV)
	_, err := sheet.Decode[changes](zh(t), r)
	if !errors.Is(err, sheet.ErrMissingColumns) || errorx.PublicOf(err)["columns"] != "标题、slug" {
		t.Fatalf("err: %v %v", err, errorx.PublicOf(err))
	}
}

func TestTemplate(t *testing.T) {
	if got := sheet.Header[changes](); !reflect.DeepEqual(got, []string{"标题", "slug", "优先级", "标签"}) {
		t.Fatalf("header: %q", got)
	}
	var buf bytes.Buffer
	w := sheet.NewWriter(&buf, sheet.XLSX)
	example := changes{Title: "示例", Slug: "example", Priority: 1, Tags: []string{"go", "web"}, Note: "x"}
	must(t, sheet.Template(w, example))
	must(t, w.Close())

	records, err := sheet.Decode[changes](zh(t), read(t, buf.Bytes(), sheet.XLSX))
	if err != nil {
		t.Fatal(err)
	}
	for rec, err := range records {
		example.Note = ""
		if err != nil || !rec.Valid() || !reflect.DeepEqual(rec.Data, example) {
			t.Fatalf("record: %+v %v", rec, err)
		}
	}
}

func TestColumns(t *testing.T) {
	type item struct{ Title string }
	cols := sheet.Columns[item]{
		{Key: "title", Header: "标题", Value: func(i item) any { return i.Title }},
		{Key: "length", Header: "长度", Value: func(i item) any { return len(i.Title) }},
	}
	selected, err := cols.Select([]string{"length", "title"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(selected.Header(), []any{"长度", "标题"}) || !reflect.DeepEqual(selected.Row(item{"Go"}), []any{2, "Go"}) {
		t.Fatalf("selected: %v %v", selected.Header(), selected.Row(item{"Go"}))
	}
	if all, _ := cols.Select(nil); len(all) != 2 {
		t.Fatalf("all: %v", all)
	}
	if _, err := cols.Select([]string{"nope"}); !errors.Is(err, sheet.ErrUnknownColumn) || errorx.PublicOf(err)["column"] != "nope" {
		t.Fatalf("unknown: %v", err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
