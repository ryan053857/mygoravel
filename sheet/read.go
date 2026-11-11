package sheet

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"errors"
	"io"
	"iter"
	"strings"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// Reader 逐行读取表格。第一个不是空行的行是表头，其余行由 Rows 返回。
//
// 单元格的值去掉首尾空白。XLSX 读取第一个工作表，单元格的值是 Excel 中显示的文本，
// 例如日期按单元格的格式显示；整个文件会被读入内存（工作表很大时写入临时文件），用完要 Close。
//
// CSV 去掉开头的 UTF-8 BOM；内容不是 UTF-8 时按 GB18030 解码，例如 Excel 中文版另存的 CSV；
// 分隔符按表头一行在逗号、分号和制表符中选出现最多的一个。
type Reader struct {
	next   func() ([]string, int, error) // 返回一行和它的行号，读完时返回 io.EOF
	close  func() error
	header []string
	line   int
}

// Row 是表格中的一行。
type Row struct {
	// Line 是这一行在文件中的行号，从 1 开始，与 Excel 中看到的行号一致。
	Line   int
	Values []string
}

// NewReader 读取 r 中 f 格式的表格并读出表头。文件无法读取时返回 ErrBadFile，没有表头时返回 ErrNoHeader。
func NewReader(r io.Reader, f Format) (*Reader, error) {
	var rd *Reader
	var err error
	switch f {
	case CSV:
		rd = csvReader(r)
	case XLSX:
		rd, err = xlsxReader(r)
	default:
		return nil, ErrFormat.New()
	}
	if err != nil {
		return nil, err
	}
	for {
		cells, err := rd.read()
		if err == io.EOF {
			err = ErrNoHeader.New()
		}
		if err != nil {
			_ = rd.Close()
			return nil, err
		}
		if cells != nil {
			rd.header = cells
			return rd, nil
		}
	}
}

// Header 返回表头的各列名称。
func (r *Reader) Header() []string { return r.header }

// Rows 依次返回表头之后的行，跳过空行。文件中途无法读取时产出 ErrBadFile 并停止。只能遍历一次。
func (r *Reader) Rows() iter.Seq2[Row, error] {
	return func(yield func(Row, error) bool) {
		for {
			cells, err := r.read()
			if err == io.EOF {
				return
			}
			if err != nil {
				yield(Row{}, err)
				return
			}
			if cells != nil && !yield(Row{Line: r.line, Values: cells}, nil) {
				return
			}
		}
	}
}

// Close 释放读取 XLSX 时占用的资源，不会关闭传给 NewReader 的 io.Reader。
func (r *Reader) Close() error {
	if r.close == nil {
		return nil
	}
	return r.close()
}

// read 读取下一行并去掉单元格首尾空白；空行返回 nil。
func (r *Reader) read() ([]string, error) {
	cells, line, err := r.next()
	if err != nil {
		return nil, err
	}
	r.line = line
	blank := true
	for i, c := range cells {
		cells[i] = strings.TrimSpace(c)
		blank = blank && cells[i] == ""
	}
	if blank {
		return nil, nil
	}
	return cells, nil
}

// sniffSize 是判断 CSV 编码和分隔符时查看的字节数。
const sniffSize = 64 << 10

func csvReader(r io.Reader) *Reader {
	br := bufio.NewReaderSize(r, sniffSize)
	head, _ := br.Peek(sniffSize)
	if bytes.HasPrefix(head, []byte("\xef\xbb\xbf")) {
		_, _ = br.Discard(3)
		head = head[3:]
	}
	var src io.Reader = br
	if !validUTF8(head) {
		src = transform.NewReader(br, simplifiedchinese.GB18030.NewDecoder())
	}
	cr := csv.NewReader(src)
	cr.Comma = delimiter(head)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	// encoding/csv 跳过空行，用记录开始的物理行号减去之前单元格内的换行数，得到 Excel 中的行号。
	extra := 0
	return &Reader{next: func() ([]string, int, error) {
		cells, err := cr.Read()
		if err == io.EOF {
			return nil, 0, err
		}
		if err != nil {
			return nil, 0, ErrBadFile.Wrap(err)
		}
		start, _ := cr.FieldPos(0)
		line := start - extra
		for _, c := range cells {
			extra += strings.Count(c, "\n")
		}
		return cells, line, nil
	}}
}

// validUTF8 报告 b 是否是 UTF-8，末尾被截断的不完整字符不算错误。
func validUTF8(b []byte) bool {
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		if r == utf8.RuneError && size == 1 {
			return !utf8.FullRune(b)
		}
		b = b[size:]
	}
	return true
}

// delimiter 在第一行中选出现最多的分隔符，相同时优先逗号。
func delimiter(head []byte) rune {
	line, _, _ := bytes.Cut(head, []byte("\n"))
	best, most := ',', bytes.Count(line, []byte(","))
	for _, d := range []rune{';', '\t'} {
		if n := bytes.Count(line, []byte(string(d))); n > most {
			best, most = d, n
		}
	}
	return best
}

func xlsxReader(r io.Reader) (*Reader, error) {
	f, err := excelize.OpenReader(r)
	if err != nil {
		return nil, ErrBadFile.Wrap(err)
	}
	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		_ = f.Close()
		return nil, ErrNoHeader.New()
	}
	rows, err := f.Rows(sheets[0])
	if err != nil {
		_ = f.Close()
		return nil, ErrBadFile.Wrap(err)
	}
	line := 0
	return &Reader{
		next: func() ([]string, int, error) {
			if !rows.Next() {
				if err := rows.Error(); err != nil {
					return nil, 0, ErrBadFile.Wrap(err)
				}
				return nil, 0, io.EOF
			}
			line++
			cells, err := rows.Columns()
			if err != nil {
				return nil, 0, ErrBadFile.Wrap(err)
			}
			return cells, line, nil
		},
		close: func() error {
			return errors.Join(rows.Close(), f.Close())
		},
	}, nil
}
