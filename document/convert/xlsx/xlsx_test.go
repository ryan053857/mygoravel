package xlsx_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"github.com/ryan053857/mygoravel/document/convert"
	"github.com/ryan053857/mygoravel/document/convert/xlsx"
	"github.com/ryan053857/mygoravel/document/internal/ooxmltest"
)

// workbook 构造一个工作簿：一个数据表、一个空表和一个隐藏表。
func workbook(t *testing.T) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(f.SetSheetName("Sheet1", "销售"))
	must(f.SetSheetRow("销售", "A1", &[]any{"地区", "占比", "备注"}))
	must(f.SetSheetRow("销售", "A2", &[]any{"华东", 0.25, "a|b"}))
	must(f.SetSheetRow("销售", "A4", &[]any{"华南", 0.5}))
	style, err := f.NewStyle(&excelize.Style{NumFmt: 9})
	must(err)
	must(f.SetCellStyle("销售", "B2", "B4", style))
	_, err = f.NewSheet("空表")
	must(err)
	_, err = f.NewSheet("隐藏")
	must(err)
	must(f.SetCellValue("隐藏", "A1", "secret"))
	must(f.SetSheetVisible("隐藏", false))
	var buf bytes.Buffer
	must(f.Write(&buf))
	return buf.Bytes()
}

// run 用 opts 转换 data。
func run(t *testing.T, opts xlsx.Options, data []byte) convert.Document {
	t.Helper()
	doc, err := xlsx.New(opts).Convert(context.Background(), convert.Input{Reader: bytes.NewReader(data)})
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestSheets(t *testing.T) {
	doc := run(t, xlsx.Options{}, workbook(t))
	want := "## 销售\n\n| 地区 | 占比 | 备注 |\n| --- | --- | --- |\n| 华东 | 25% | a\\|b |\n| 华南 | 50% |  |"
	if doc.Markdown != want {
		t.Fatalf("got:\n%s\nwant:\n%s", doc.Markdown, want)
	}
	if len(doc.Sections) != 3 {
		t.Fatalf("sections = %+v", doc.Sections)
	}
	for i, name := range []string{"销售", "空表", "隐藏"} {
		if s := doc.Sections[i]; s.Kind != convert.KindSheet || s.Number != i+1 || s.Name != name {
			t.Errorf("section %d = %+v", i, s)
		}
	}
	if s := doc.Sections[0]; s.Start != 0 || s.End != len(doc.Markdown) {
		t.Errorf("first section = %+v", s)
	}
	if s := doc.Sections[2]; s.Start != s.End {
		t.Errorf("hidden section not empty: %+v", s)
	}
}

func TestOutputLimitAcrossSheets(t *testing.T) {
	data := workbook(t)
	size := int64(len(run(t, xlsx.Options{IncludeHidden: true}, data).Markdown))
	run(t, xlsx.Options{IncludeHidden: true, Limits: convert.Limits{MaxOutputBytes: size}}, data)
	_, err := xlsx.New(xlsx.Options{IncludeHidden: true, Limits: convert.Limits{MaxOutputBytes: size - 1}}).Convert(context.Background(), convert.Input{Reader: bytes.NewReader(data)})
	var limit *convert.LimitError
	if !errors.As(err, &limit) || limit.Limit != convert.LimitOutput || limit.Max != size-1 {
		t.Errorf("one byte over: %v", err)
	}
}

func TestDeepNesting(t *testing.T) {
	data := rezip(t, workbook(t), "xl/workbook.xml", func(content string) string {
		return strings.Replace(content, "<sheets>", "<sheets>"+strings.Repeat("<x>", 1<<20)+strings.Repeat("</x>", 1<<20), 1)
	})
	if _, err := xlsx.New(xlsx.Options{}).Convert(context.Background(), convert.Input{Reader: bytes.NewReader(data)}); !errors.Is(err, convert.ErrCorrupt) {
		t.Errorf("err = %v, want ErrCorrupt", err)
	}
}

func TestOptions(t *testing.T) {
	doc := run(t, xlsx.Options{RawValues: true, IncludeHidden: true}, workbook(t))
	want := "## 销售\n\n| 地区 | 占比 | 备注 |\n| --- | --- | --- |\n| 华东 | 0.25 | a\\|b |\n| 华南 | 0.5 |  |\n\n## 隐藏\n\n| secret |\n| --- |"
	if doc.Markdown != want {
		t.Fatalf("got:\n%s\nwant:\n%s", doc.Markdown, want)
	}
	if s := doc.Sections[2]; doc.Markdown[s.Start:s.End] != "## 隐藏\n\n| secret |\n| --- |" {
		t.Errorf("hidden section = %q", doc.Markdown[s.Start:s.End])
	}
}

// rezip 重写 zip 压缩包的条目，对名为 name 的条目应用 edit。
func rezip(t *testing.T, data []byte, name string, edit func(string) string) []byte {
	t.Helper()
	return rezipAll(t, data, func(entry, content string) (string, string) {
		if entry == name {
			return entry, edit(content)
		}
		return entry, content
	})
}

// rezipAll 让每个条目都经过 edit 重写，edit 可以改名。
func rezipAll(t *testing.T, data []byte, edit func(name, content string) (string, string)) []byte {
	t.Helper()
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, entry := range archive.File {
		r, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		name, edited := edit(entry.Name, string(content))
		files[name] = edited
	}
	return ooxmltest.Build(t, files)
}

func TestCorruptSheetAndNUL(t *testing.T) {
	data := workbook(t)
	for name, edit := range map[string]func(string) string{
		"truncated in a tag":     func(s string) string { return s[:strings.Index(s, "<row r=\"2\"")+20] },
		"truncated before a row": func(s string) string { before, _, _ := strings.Cut(s, "<row r=\"2\""); return before },
		"mismatched end tag":     func(s string) string { return strings.Replace(s, "</row>", "</broken>", 1) },
		"two roots":              func(s string) string { return s + "<extra/>" },
	} {
		corrupt := rezip(t, data, "xl/worksheets/sheet1.xml", edit)
		if _, err := xlsx.New(xlsx.Options{}).Convert(context.Background(), convert.Input{Reader: bytes.NewReader(corrupt)}); !errors.Is(err, convert.ErrCorrupt) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// 没有 .xml 扩展名的工作表部件仍然会按它的内容类型检查。
	renamed := rezipAll(t, data, func(name, content string) (string, string) {
		if name == "xl/worksheets/sheet1.xml" {
			return "xl/worksheets/sheet1", strings.Replace(content, "</row>", "</broken>", 1)
		}
		content = strings.ReplaceAll(content, "worksheets/sheet1.xml", "worksheets/sheet1")
		return name, content
	})
	if _, err := xlsx.New(xlsx.Options{}).Convert(context.Background(), convert.Input{Reader: bytes.NewReader(renamed)}); !errors.Is(err, convert.ErrCorrupt) {
		t.Errorf("renamed corrupt sheet: %v", err)
	}
	// 只在关系中改名时，内容类型中写的还是旧的部件名。
	undeclared := rezipAll(t, data, func(name, content string) (string, string) {
		switch name {
		case "xl/worksheets/sheet1.xml":
			return "xl/worksheets/data.bin", strings.Replace(content, "</row>", "</broken>", 1)
		case "xl/_rels/workbook.xml.rels":
			return name, strings.ReplaceAll(content, "worksheets/sheet1.xml", "worksheets/data.bin")
		}
		return name, content
	})
	if _, err := xlsx.New(xlsx.Options{}).Convert(context.Background(), convert.Input{Reader: bytes.NewReader(undeclared)}); !errors.Is(err, convert.ErrCorrupt) {
		t.Errorf("undeclared corrupt sheet: %v", err)
	}
	padded := rezipAll(t, data, func(name, content string) (string, string) {
		switch name {
		case "xl/worksheets/sheet1.xml":
			return "xl/worksheets/data.bin", strings.Repeat(" ", 4096) + strings.Replace(content, "</row>", "</broken>", 1)
		case "xl/_rels/workbook.xml.rels":
			return name, strings.ReplaceAll(content, "worksheets/sheet1.xml", "worksheets/data.bin")
		}
		return name, content
	})
	if _, err := xlsx.New(xlsx.Options{}).Convert(context.Background(), convert.Input{Reader: bytes.NewReader(padded)}); !errors.Is(err, convert.ErrCorrupt) {
		t.Errorf("padded undeclared corrupt sheet: %v", err)
	}
	for name, tc := range map[string]struct{ target, content string }{
		"dot segments": {"/xl/worksheets/../worksheets/data.bin", ""},
		"backslash":    {`worksheets\data.bin`, ""},
		"not like XML": {"worksheets/data.bin", "garbage"},
		"NUL prefix":   {"worksheets/data.bin", "\x00"},
		"whitespace":   {"worksheets/data.bin", "   "},
	} {
		renamed := rezipAll(t, data, func(name, content string) (string, string) {
			switch name {
			case "xl/worksheets/sheet1.xml":
				if tc.content != "" {
					return "xl/worksheets/data.bin", tc.content
				}
				before, _, _ := strings.Cut(content, "<row r=\"2\"")
				return "xl/worksheets/data.bin", before
			case "xl/_rels/workbook.xml.rels":
				return name, strings.ReplaceAll(content, "worksheets/sheet1.xml", tc.target)
			}
			return name, content
		})
		if _, err := xlsx.New(xlsx.Options{}).Convert(context.Background(), convert.Input{Reader: bytes.NewReader(renamed)}); !errors.Is(err, convert.ErrCorrupt) {
			t.Errorf("renamed sheet with %s: %v", name, err)
		}
	}
	// 与表格库一样，在清理路径之后解析反斜杠和点号路径段。
	mixed := rezipAll(t, data, func(name, content string) (string, string) {
		switch name {
		case "xl/worksheets/sheet1.xml":
			return "xl/worksheets/../worksheets/data.bin", "garbage"
		case "xl/_rels/workbook.xml.rels":
			return name, strings.ReplaceAll(content, "worksheets/sheet1.xml", `worksheets\..\worksheets\data.bin`)
		}
		return name, content
	})
	if _, err := xlsx.New(xlsx.Options{}).Convert(context.Background(), convert.Input{Reader: bytes.NewReader(mixed)}); !errors.Is(err, convert.ErrCorrupt) {
		t.Errorf("mixed separators and dot segments: %v", err)
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var twice bytes.Buffer
	zw := zip.NewWriter(&twice)
	for _, entry := range archive.File {
		if err := zw.Copy(entry); err != nil {
			t.Fatal(err)
		}
	}
	extra, err := zw.Create("xl/worksheets/sheet1.xml")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = extra.Write([]byte("<worksheet>"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := xlsx.New(xlsx.Options{}).Convert(context.Background(), convert.Input{Reader: bytes.NewReader(twice.Bytes())}); !errors.Is(err, convert.ErrCorrupt) {
		t.Errorf("duplicate sheet: %v", err)
	}
	// 关系部件本身的名字带反斜杠时，它的引用仍然有效。
	for _, part := range []string{"xl/sharedStrings.xml", "xl/styles.xml", "xl/theme/theme1.xml"} {
		missing := rezipAll(t, data, func(entry, content string) (string, string) {
			switch entry {
			case "xl/_rels/workbook.xml.rels":
				return `xl\_rels\workbook.xml.rels`, content
			case part:
				return "unused/" + entry, content
			}
			return entry, content
		})
		if _, err := xlsx.New(xlsx.Options{}).Convert(context.Background(), convert.Input{Reader: bytes.NewReader(missing)}); !errors.Is(err, convert.ErrCorrupt) {
			t.Errorf("backslash relationships, missing %s: %v", part, err)
		}
	}
	for name, rename := range map[string]func(string) string{
		"directory entry": func(n string) string { return n + "/" },
		"moved elsewhere": func(n string) string { return "unused/" + n },
	} {
		for _, part := range []string{"xl/sharedStrings.xml", "xl/styles.xml", "xl/theme/theme1.xml"} {
			missing := rezipAll(t, data, func(entry, content string) (string, string) {
				if entry == part {
					if renamed := rename(entry); strings.HasSuffix(renamed, "/") {
						return renamed, ""
					}
					return rename(entry), content
				}
				return entry, content
			})
			if _, err := xlsx.New(xlsx.Options{}).Convert(context.Background(), convert.Input{Reader: bytes.NewReader(missing)}); !errors.Is(err, convert.ErrCorrupt) {
				t.Errorf("%s %s: %v", name, part, err)
			}
		}
	}
	for name, rename := range map[string]func(string) string{
		"upper-case shared strings": func(n string) string {
			if n == "xl/sharedStrings.xml" {
				return "xl/SHAREDSTRINGS.XML"
			}
			return n
		},
		"backslash styles": func(n string) string {
			if n == "xl/styles.xml" {
				return `xl\styles.xml`
			}
			return n
		},
	} {
		variant := rezipAll(t, data, func(entry, content string) (string, string) { return rename(entry), content })
		if doc := run(t, xlsx.Options{}, variant); !strings.Contains(doc.Markdown, "华东") || !strings.Contains(doc.Markdown, "25%") {
			t.Errorf("%s: %q", name, doc.Markdown)
		}
	}
	for name, second := range map[string]string{"same name": "xl/worksheets/sheet1.xml", "other case": "XL/worksheets/sheet1.xml", "backslash": `xl\worksheets\sheet1.xml`} {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for _, entry := range archive.File {
			if err := zw.Copy(entry); err != nil {
				t.Fatal(err)
			}
		}
		w, err := zw.Create(second)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData/></worksheet>`))
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := xlsx.New(xlsx.Options{}).Convert(context.Background(), convert.Input{Reader: bytes.NewReader(buf.Bytes())}); !errors.Is(err, convert.ErrCorrupt) {
			t.Errorf("duplicate %s: %v", name, err)
		}
	}
	// 既不存在也没有被引用的固定部件是可选的。
	optional := rezipAll(t, data, func(entry, content string) (string, string) {
		switch entry {
		case "xl/theme/theme1.xml":
			return "unused/theme.xml", content
		case "xl/_rels/workbook.xml.rels":
			return entry, regexp.MustCompile(`<Relationship [^>]*theme[^>]*?(/>|></Relationship>)`).ReplaceAllString(content, "")
		}
		return entry, content
	})
	if doc := run(t, xlsx.Options{}, optional); !strings.Contains(doc.Markdown, "华东") {
		t.Errorf("without theme: %q", doc.Markdown)
	}
	for _, part := range []string{"xl/sharedStrings.xml", "xl/styles.xml"} {
		missing := rezipAll(t, data, func(name, content string) (string, string) {
			if name == part {
				return "unused/" + name, content
			}
			return name, content
		})
		if _, err := xlsx.New(xlsx.Options{}).Convert(context.Background(), convert.Input{Reader: bytes.NewReader(missing)}); !errors.Is(err, convert.ErrCorrupt) {
			t.Errorf("missing %s: %v", part, err)
		}
	}
	// 共享字符串从固定路径读取，不需要关系或 XML 内容类型。
	unlisted := rezipAll(t, data, func(name, content string) (string, string) {
		switch name {
		case "xl/sharedStrings.xml":
			return name, "garbage"
		case "xl/_rels/workbook.xml.rels":
			before, _, _ := strings.Cut(content, "<Relationship Id=\"rId4\"")
			return name, before + "</Relationships>"
		case "[Content_Types].xml":
			return name, strings.ReplaceAll(content, "sharedStrings+xml", "octet-stream")
		}
		return name, content
	})
	if _, err := xlsx.New(xlsx.Options{}).Convert(context.Background(), convert.Input{Reader: bytes.NewReader(unlisted)}); !errors.Is(err, convert.ErrCorrupt) {
		t.Errorf("unlisted shared strings: %v", err)
	}
	for name, edit := range map[string]func(string) string{
		"extra root": func(s string) string { return s + "<extra/>" },
		"text":       func(s string) string { return s + "garbage" },
	} {
		corrupt := rezip(t, data, "[Content_Types].xml", edit)
		if _, err := xlsx.New(xlsx.Options{}).Convert(context.Background(), convert.Input{Reader: bytes.NewReader(corrupt)}); !errors.Is(err, convert.ErrCorrupt) {
			t.Errorf("content types with %s: %v", name, err)
		}
	}
}

func TestBOMAndUnreferencedParts(t *testing.T) {
	data := workbook(t)
	bom := rezipAll(t, data, func(name, content string) (string, string) {
		if name == "xl/worksheets/sheet1.xml" || name == "xl/workbook.xml" {
			return name, "\xEF\xBB\xBF" + content
		}
		return name, content
	})
	if doc := run(t, xlsx.Options{}, bom); !strings.Contains(doc.Markdown, "华东") {
		t.Errorf("BOM workbook: %q", doc.Markdown)
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"custom/notes.txt": "<3 not markup"}
	for _, entry := range archive.File {
		r, _ := entry.Open()
		content, _ := io.ReadAll(r)
		files[entry.Name] = string(content)
	}
	if doc := run(t, xlsx.Options{}, ooxmltest.Build(t, files)); !strings.Contains(doc.Markdown, "华东") {
		t.Errorf("unreferenced text part: %q", doc.Markdown)
	}
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()
	if err := f.SetCellValue("Sheet1", "A1", "a_x0000_b"); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatal(err)
	}
	doc := run(t, xlsx.Options{}, buf.Bytes())
	if strings.ContainsRune(doc.Markdown, 0) {
		t.Errorf("NUL in output: %q", doc.Markdown)
	}
}

func TestSourceLimitAndHiddenFirst(t *testing.T) {
	data := workbook(t)
	if _, err := xlsx.New(xlsx.Options{Limits: convert.Limits{MaxBytes: int64(len(data))}}).Convert(context.Background(), convert.Input{Reader: bytes.NewReader(data)}); err != nil {
		t.Errorf("at limit: %v", err)
	}
	var limit *convert.LimitError
	if _, err := xlsx.New(xlsx.Options{Limits: convert.Limits{MaxBytes: int64(len(data)) - 1}}).Convert(context.Background(), convert.Input{Reader: bytes.NewReader(data)}); !errors.As(err, &limit) || limit.Limit != convert.LimitSource {
		t.Errorf("one byte over: %v", err)
	}
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(f.SetCellValue("Sheet1", "A1", "hidden"))
	_, err := f.NewSheet("visible")
	must(err)
	must(f.SetCellValue("visible", "A1", "shown"))
	f.SetActiveSheet(1)
	must(f.SetSheetVisible("Sheet1", false))
	var buf bytes.Buffer
	must(f.Write(&buf))
	doc := run(t, xlsx.Options{}, buf.Bytes())
	if len(doc.Sections) != 2 || doc.Sections[0].Start != doc.Sections[0].End || doc.Sections[1].Number != 2 || doc.Sections[1].Name != "visible" {
		t.Errorf("sections = %+v", doc.Sections)
	}
}

func TestNoTemporaryFilesLeft(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	// 超过表格库 16 MiB 阈值的工作表会解压到临时文件；条目使用不支持的压缩方法时，打开随即失败。
	f := excelize.NewFile()
	stream, err := f.NewStreamWriter("Sheet1")
	if err != nil {
		t.Fatal(err)
	}
	cell := strings.Repeat("x", 1000)
	for row := 1; row <= 20000; row++ {
		name, _ := excelize.CoordinatesToCellName(1, row)
		if err := stream.SetRow(name, []any{cell}); err != nil {
			t.Fatal(err)
		}
	}
	if err := stream.Flush(); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	archive, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	w := zip.NewWriter(&out)
	for _, entry := range archive.File {
		if err := w.Copy(entry); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := w.CreateRaw(&zip.FileHeader{Name: "xl/media/broken.bin", Method: 99, CompressedSize64: 4, UncompressedSize64: 4})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = raw.Write([]byte("data"))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	for _, entry := range archive.File {
		if entry.Name == "xl/worksheets/sheet1.xml" && entry.UncompressedSize64 <= excelize.StreamChunkSize {
			t.Fatalf("worksheet of %d bytes stays in memory", entry.UncompressedSize64)
		}
	}
	if _, err := xlsx.New(xlsx.Options{}).Convert(context.Background(), convert.Input{Reader: bytes.NewReader(out.Bytes())}); !errors.Is(err, convert.ErrCorrupt) {
		t.Errorf("unsupported entry: %v", err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Errorf("left in the temporary directory: %v, %v", entries, err)
	}
}

func TestExpandedLimitAtTotal(t *testing.T) {
	data := workbook(t)
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, entry := range archive.File {
		total += int64(entry.UncompressedSize64)
	}
	if _, err := xlsx.New(xlsx.Options{Limits: convert.Limits{MaxExpandedBytes: total}}).Convert(context.Background(), convert.Input{Reader: bytes.NewReader(data)}); err != nil {
		t.Errorf("at the limit: %v", err)
	}
	if _, err := xlsx.New(xlsx.Options{Limits: convert.Limits{MaxExpandedBytes: total - 1}}).Convert(context.Background(), convert.Input{Reader: bytes.NewReader(data)}); !errors.Is(err, convert.ErrTooLarge) {
		t.Errorf("one byte over: %v", err)
	}
}

func TestErrors(t *testing.T) {
	data := workbook(t)
	for name, tc := range map[string]struct {
		data   []byte
		limits convert.Limits
		want   error
	}{
		"empty":     {nil, convert.Limits{}, convert.ErrCorrupt},
		"encrypted": {ooxmltest.Encrypted(), convert.Limits{}, convert.ErrEncrypted},
		"not xlsx":  {ooxmltest.Build(t, map[string]string{"a.txt": "x"}), convert.Limits{}, convert.ErrCorrupt},
		"expanded":  {data, convert.Limits{MaxExpandedBytes: 100}, convert.ErrTooLarge},
		"output":    {data, convert.Limits{MaxOutputBytes: 20}, convert.ErrTooLarge},
	} {
		_, err := xlsx.New(xlsx.Options{Limits: tc.limits}).Convert(context.Background(), convert.Input{Reader: bytes.NewReader(tc.data)})
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", name, err, tc.want)
		}
	}
}

func FuzzConvert(f *testing.F) {
	f.Add([]byte("PK"))
	f.Fuzz(func(t *testing.T, data []byte) {
		doc, err := xlsx.New(xlsx.Options{Limits: convert.Limits{MaxOutputBytes: 1 << 20}}).Convert(context.Background(), convert.Input{Reader: bytes.NewReader(data)})
		if err != nil && doc.Markdown != "" {
			t.Fatalf("partial document on error")
		}
	})
}
