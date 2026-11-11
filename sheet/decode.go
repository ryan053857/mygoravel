package sheet

import (
	"context"
	"iter"
	"reflect"
	"slices"
	"strings"
	"sync"
	"unicode"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/form"
)

// Record 是 Decode 解码出的一行。
type Record[T any] struct {
	// Line 是这一行在文件中的行号，与 Excel 中看到的行号一致。
	Line int
	// Data 是解码后的数据。验证失败时只包含能转换的字段。
	Data T
	// Values 是这一行的原始单元格，与 Reader.Header 一一对应，包括没有对应字段的列。
	Values []string
	// Errors 以字段名（json 标签）为键，是验证失败的提示；全部有效时为 nil。
	Errors map[string]string
}

// Valid 报告这一行没有任何字段错误。
func (r *Record[T]) Valid() bool { return len(r.Errors) == 0 }

// SetError 设置字段的错误提示，用于业务规则的错误，例如 slug 已被占用。
func (r *Record[T]) SetError(field, message string) {
	if r.Errors == nil {
		r.Errors = map[string]string{}
	}
	r.Errors[field] = message
}

// Decode 按 T 的字段找到各自的列，返回逐行解码并验证的迭代器。
//
// 字段对应的列由表头决定：表头与字段的 json 标签或 sheet 标签中的任意一个名称相同即可，
// 比较时不区分大小写，忽略空白、连字符和下划线。sheet 标签用逗号分隔多个名称，第一个是 Header 和 Template
// 写出的列名；sheet:"-" 的字段不从表格读取。每个字段取第一个匹配的列，表格中多余的列被忽略。
//
// 必填字段（见 form.Schema 的 Required）在表头中找不到对应的列时返回 ErrMissingColumns。
// 每一行用 form.Decode 验证，切片字段的单元格按逗号（包括中文逗号）拆分为多个值。
// 验证失败的行同样会产出，通过 Record.Valid 判断；只有文件无法读取时迭代器产出错误并停止。
func Decode[T any](ctx context.Context, r *Reader) (iter.Seq2[Record[T], error], error) {
	fs := fieldsOf(reflect.TypeFor[T]())
	columns := make([]int, len(r.header)) // 表头第 i 列对应的字段，-1 表示没有
	found := make([]bool, len(fs))
	for i, h := range r.header {
		columns[i] = -1
		name := normalize(h)
		for j, f := range fs {
			if !found[j] && slices.Contains(f.names, name) {
				columns[i], found[j] = j, true
				break
			}
		}
	}
	var missing []string
	for j, f := range fs {
		if f.required && !found[j] {
			missing = append(missing, f.header)
		}
	}
	if len(missing) > 0 {
		return nil, ErrMissingColumns.New(errorx.Public("columns", strings.Join(missing, "、")))
	}

	return func(yield func(Record[T], error) bool) {
		for row, err := range r.Rows() {
			if err != nil {
				yield(Record[T]{}, err)
				return
			}
			values := make(map[string][]string, len(fs))
			for i, j := range columns {
				if j < 0 {
					continue
				}
				v := ""
				if i < len(row.Values) {
					v = row.Values[i]
				}
				f := fs[j]
				if f.multiple {
					values[f.name] = split(v)
				} else {
					values[f.name] = []string{v}
				}
			}
			data, errs := form.Decode[T](ctx, values)
			if !yield(Record[T]{Line: row.Line, Data: data, Values: row.Values, Errors: errs}, nil) {
				return
			}
		}
	}, nil
}

// Header 返回 T 在表格中的列名：每个字段的 sheet 标签中的第一个名称，没有时用 json 标签。
func Header[T any]() []string {
	fs := fieldsOf(reflect.TypeFor[T]())
	header := make([]string, len(fs))
	for i, f := range fs {
		header[i] = f.header
	}
	return header
}

// Template 写出导入 T 的模板：Header 作为表头，examples 中的每一项作为一行示例。
// 切片字段的值以逗号连接，因此填好的模板可以原样被 Decode 读取。Template 不会关闭 w。
func Template[T any](w *Writer, examples ...T) error {
	fs := fieldsOf(reflect.TypeFor[T]())
	row := make([]any, len(fs))
	for i, f := range fs {
		row[i] = f.header
	}
	if err := w.Write(row...); err != nil {
		return err
	}
	for _, ex := range examples {
		v := reflect.ValueOf(ex)
		for i, f := range fs {
			row[i] = v.FieldByIndex(f.index).Interface()
		}
		if err := w.Write(row...); err != nil {
			return err
		}
	}
	return nil
}

type field struct {
	name     string   // json 标签中的名称，也是 form.Decode 的字段名
	header   string   // 写出模板时的列名
	names    []string // 规范化后能匹配的表头
	index    []int
	required bool
	multiple bool
}

var cache sync.Map // reflect.Type -> []field

func fieldsOf(t reflect.Type) []field {
	if fs, ok := cache.Load(t); ok {
		return fs.([]field)
	}
	s := form.Schema(t)
	var fs []field
	for _, sf := range reflect.VisibleFields(t) {
		if !sf.IsExported() || sf.Anonymous {
			continue
		}
		name, _, _ := strings.Cut(sf.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = sf.Name
		}
		if _, ok := s.Properties[name]; !ok {
			continue
		}
		tag := sf.Tag.Get("sheet")
		if tag == "-" {
			continue
		}
		f := field{
			name:     name,
			header:   name,
			names:    []string{normalize(name)},
			index:    sf.Index,
			required: slices.Contains(s.Required, name),
			multiple: sf.Type.Kind() == reflect.Slice && sf.Type.Elem().Kind() != reflect.Uint8,
		}
		if tag != "" {
			for i, alias := range strings.Split(tag, ",") {
				alias = strings.TrimSpace(alias)
				if i == 0 {
					f.header = alias
				}
				f.names = append(f.names, normalize(alias))
			}
		}
		fs = append(fs, f)
	}
	actual, _ := cache.LoadOrStore(t, fs)
	return actual.([]field)
}

// normalize 转为小写，去掉空白、连字符和下划线，例如 "Published At" 和 "published_at" 相同。
func normalize(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || r == '-' || r == '_' {
			return -1
		}
		return unicode.ToLower(r)
	}, s)
}

// split 按逗号（包括中文逗号）拆分单元格，去掉空白和空项。
func split(v string) []string {
	parts := strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == '，' })
	list := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			list = append(list, p)
		}
	}
	return list
}
