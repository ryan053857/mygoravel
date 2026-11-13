package sheet

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/go-tenon/tenon/errorx"
)

// ErrUnknownColumn 表示 Columns.Select 收到了不存在的列，公开参数 column 是这个列的 Key。
var ErrUnknownColumn = errorx.Define(errorx.InvalidArgument, "sheet.unknown_column")

// timeLayout 是时间在 CSV 中的格式，XLSX 中的时间单元格使用相同的显示格式。
const timeLayout = "2006-01-02 15:04:05"

// Writer 逐行写出表格，第一行是表头。写完必须调用 Close：CSV 在 Close 时写出缓冲区，XLSX 在 Close 时才写出整个文件。
//
// 单元格的值：
//
//	nil、nil 指针、零值 time.Time   空
//	string                          文本
//	整数、浮点数、bool               XLSX 中保持数值、布尔类型
//	time.Time                       按它自己的时区显示为 "2006-01-02 15:04:05"，XLSX 中是日期时间单元格
//	fmt.Stringer                    String() 的结果，例如 uuid.UUID
//	切片                            各项以逗号连接
//	指针                            指向的值
//
// CSV 以 UTF-8 BOM 开头，Excel 打开不会乱码。以 = + - @ 开头又不是数字的文本前面加一个 '，
// 防止 Excel 把它当作公式执行；XLSX 中的文本不会被当作公式，原样写出。
// XLSX 的表头加粗并冻结在第一行。
type Writer struct {
	err    error
	csv    *csv.Writer
	file   *excelize.File
	stream *excelize.StreamWriter
	out    io.Writer
	row    int
	bold   int
	time   int
}

// NewWriter 返回把 f 格式的表格写入 w 的 Writer。创建时的错误由第一次 Write 或 Close 返回。
func NewWriter(w io.Writer, f Format) *Writer {
	switch f {
	case CSV:
		_, err := io.WriteString(w, "\xef\xbb\xbf")
		return &Writer{err: err, csv: csv.NewWriter(w)}
	case XLSX:
		return xlsxWriter(w)
	}
	return &Writer{err: ErrFormat.New()}
}

func xlsxWriter(w io.Writer) *Writer {
	f := excelize.NewFile()
	sw := &Writer{file: f, out: w}
	sw.stream, sw.err = f.NewStreamWriter("Sheet1")
	if sw.err == nil {
		sw.bold, sw.err = f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	}
	if sw.err == nil {
		layout := "yyyy-mm-dd hh:mm:ss"
		sw.time, sw.err = f.NewStyle(&excelize.Style{CustomNumFmt: &layout})
	}
	if sw.err == nil {
		sw.err = sw.stream.SetPanes(&excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"})
	}
	return sw
}

// Write 写出一行。
func (w *Writer) Write(cells ...any) error {
	if w.err != nil {
		return w.err
	}
	w.row++
	if w.csv != nil {
		record := make([]string, len(cells))
		for i, c := range cells {
			record[i] = csvText(value(c))
		}
		w.err = w.csv.Write(record)
		return w.err
	}
	row := make([]any, len(cells))
	for i, c := range cells {
		v := value(c)
		switch {
		case w.row == 1:
			row[i] = excelize.Cell{StyleID: w.bold, Value: v}
		case isTime(v):
			row[i] = excelize.Cell{StyleID: w.time, Value: v}
		default:
			row[i] = v
		}
	}
	cell, _ := excelize.CoordinatesToCellName(1, w.row)
	w.err = w.stream.SetRow(cell, row)
	return w.err
}

// Close 写出剩余的内容，不会关闭传给 NewWriter 的 io.Writer。
func (w *Writer) Close() error {
	if w.csv != nil {
		if w.err == nil {
			w.csv.Flush()
			w.err = w.csv.Error()
		}
		return w.err
	}
	if w.file == nil {
		return w.err
	}
	if w.err == nil {
		w.err = w.stream.Flush()
	}
	if w.err == nil {
		_, w.err = w.file.WriteTo(w.out)
	}
	err := errors.Join(w.err, w.file.Close())
	w.file, w.err = nil, errors.New("sheet: write after close")
	return err
}

// RowWriter 逐行写出表格。*Writer 实现了它，exports.Writer 在写出的同时记录导出进度；
// 导出的业务代码接收 RowWriter，同一份代码可以写入 HTTP 响应，也可以在后台导出。
type RowWriter interface {
	Write(cells ...any) error
}

// Column 是导出的一列。
type Column[T any] struct {
	// Key 是这一列的标识，Select 按它选择列，例如前端让用户勾选要导出的列。
	Key    string
	Header string
	Value  func(item T) any
}

// Columns 是导出的全部列。
type Columns[T any] []Column[T]

// Header 返回表头一行。
func (c Columns[T]) Header() []any {
	row := make([]any, len(c))
	for i, col := range c {
		row[i] = col.Header
	}
	return row
}

// Row 返回 item 的一行。
func (c Columns[T]) Row(item T) []any {
	row := make([]any, len(c))
	for i, col := range c {
		row[i] = col.Value(item)
	}
	return row
}

// Select 按 keys 的顺序返回选中的列；keys 为空时返回全部列。有不存在的 Key 时返回 ErrUnknownColumn。
func (c Columns[T]) Select(keys []string) (Columns[T], error) {
	if len(keys) == 0 {
		return c, nil
	}
	selected := make(Columns[T], 0, len(keys))
next:
	for _, key := range keys {
		for _, col := range c {
			if col.Key == key {
				selected = append(selected, col)
				continue next
			}
		}
		return nil, ErrUnknownColumn.New(errorx.Public("column", key))
	}
	return selected, nil
}

// value 把单元格的值转换为 nil、string、bool、int64、uint64、float64 或 time.Time。
func value(v any) any {
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil
		}
		return value(rv.Elem().Interface())
	}
	switch x := v.(type) {
	case nil:
		return nil
	case time.Time:
		if x.IsZero() {
			return nil
		}
		return x
	case fmt.Stringer:
		return x.String()
	}
	switch rv.Kind() {
	case reflect.String:
		return rv.String()
	case reflect.Bool:
		return rv.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return rv.Uint()
	case reflect.Float32, reflect.Float64:
		return rv.Float()
	case reflect.Slice, reflect.Array:
		items := make([]string, rv.Len())
		for i := range items {
			items[i] = text(value(rv.Index(i).Interface()))
		}
		return strings.Join(items, ",")
	}
	return fmt.Sprint(v)
}

func isTime(v any) bool {
	_, ok := v.(time.Time)
	return ok
}

// text 把 value 的结果转换为文本。
func text(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case time.Time:
		return x.Format(timeLayout)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

// csvText 转换为 CSV 中的文本，以公式字符开头又不是数字的文本前加 '。
func csvText(v any) string {
	s := text(v)
	if _, isText := v.(string); !isText || s == "" || !strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return s
	}
	if _, err := strconv.ParseFloat(s, 64); err == nil {
		return s
	}
	return "'" + s
}
