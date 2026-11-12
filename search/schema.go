package search

import (
	"encoding"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
)

// maxValue 是过滤字段的值、租户和 key 的最大字节数，与 MySQL 迁移中 VARCHAR(191) 的列宽一致。
const maxValue = 191

// maxField 是字段名的最大字节数，与迁移中 field 列的宽度一致。
const maxField = 64

var (
	timeType          = reflect.TypeFor[time.Time]()
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
)

// valueKind 是过滤字段的值的类型，决定值如何编码为可以按字节比较大小的字符串。
type valueKind int

const (
	kindString valueKind = iota + 1 // string 和实现 encoding.TextMarshaler 的类型，例如 uuid.UUID
	kindBool
	kindInt
	kindUint
	kindFloat
	kindTime
)

// field 是文档类型中带 search 标签的字段。
type field struct {
	name  string // JSON 名称，查询、高亮和分面都用它
	index []int  // reflect 的字段路径
	boost bool   // 文本字段：匹配时权重更高
	kind  valueKind
	typ   reflect.Type // 过滤字段的值类型：切片的元素类型或指针指向的类型
	multi bool         // 过滤字段：切片，一个文档可以有多个值
	ptr   bool         // 过滤字段：指针，nil 表示没有值
}

// schema 是文档类型的结构，Define 时从 search 标签解析。
type schema struct {
	typ     reflect.Type
	text    []*field
	filters map[string]*field
	tenant  *field
}

// parseSchema 解析 D 中带 search 标签的字段：
//
//	text        参与全文检索的文本，string 或 []string
//	text,boost  同上，匹配时权重更高，例如标题
//	filter      用于过滤、排序和分面的值
//	tenant      文档所在的租户，string
func parseSchema(typ reflect.Type) (*schema, error) {
	if typ.Kind() != reflect.Struct {
		return nil, errors.Newf("search: document type %s is not a struct", typ)
	}
	s := &schema{typ: typ, filters: map[string]*field{}}
	names := map[string]bool{}
	for _, sf := range reflect.VisibleFields(typ) {
		tag, ok := sf.Tag.Lookup("search")
		if !ok || sf.Anonymous {
			continue
		}
		if !sf.IsExported() {
			return nil, errors.Newf("search: field %s is not exported", sf.Name)
		}
		f := &field{name: jsonName(sf), index: sf.Index}
		if f.name == "" {
			return nil, errors.Newf("search: field %s is not encoded to JSON", sf.Name)
		}
		if len(f.name) > maxField {
			return nil, errors.Newf("search: field name %q longer than %d bytes", f.name, maxField)
		}
		if names[f.name] {
			return nil, errors.Newf("search: duplicate field name %q", f.name)
		}
		names[f.name] = true
		var err error
		switch tag {
		case "text", "text,boost":
			f.boost = tag == "text,boost"
			err = checkText(sf.Type)
			s.text = append(s.text, f)
		case "filter":
			err = f.setValueType(sf.Type)
			s.filters[f.name] = f
		case "tenant":
			if sf.Type.Kind() != reflect.String {
				err = errors.New("tenant field must be a string")
			}
			if s.tenant != nil {
				err = errors.New("more than one tenant field")
			}
			s.tenant = f
		default:
			err = errors.Newf("unknown tag %q", tag)
		}
		if err != nil {
			return nil, errors.Wrapf(err, "search: field %s", sf.Name)
		}
	}
	if len(s.text) == 0 {
		return nil, errors.Newf(`search: document type %s has no search:"text" field`, typ)
	}
	return s, nil
}

// jsonName 返回字段编码为 JSON 时的名称，json:"-" 时返回空。
func jsonName(sf reflect.StructField) string {
	tag := sf.Tag.Get("json")
	if tag == "-" {
		return ""
	}
	if name, _, _ := strings.Cut(tag, ","); name != "" {
		return name
	}
	return sf.Name
}

func checkText(t reflect.Type) error {
	if t.Kind() == reflect.String || (t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.String) {
		return nil
	}
	return errors.Newf("text field must be a string or []string, got %s", t)
}

func (f *field) setValueType(t reflect.Type) error {
	switch {
	case t.Kind() == reflect.Pointer:
		f.ptr, t = true, t.Elem()
	case t.Kind() == reflect.Slice && t.Elem().Kind() != reflect.Uint8:
		f.multi, t = true, t.Elem()
	}
	f.typ = t
	switch {
	case t == timeType:
		f.kind = kindTime
	case t.Implements(textMarshalerType):
		f.kind = kindString
	default:
		switch t.Kind() {
		case reflect.String:
			f.kind = kindString
		case reflect.Bool:
			f.kind = kindBool
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			f.kind = kindInt
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			f.kind = kindUint
		case reflect.Float32, reflect.Float64:
			f.kind = kindFloat
		default:
			return errors.Newf("unsupported filter type %s", t)
		}
	}
	return nil
}

// get 返回 doc 中这个字段的值；经过的嵌入指针为 nil 时 ok 为 false。
func (f *field) get(doc reflect.Value) (v reflect.Value, ok bool) {
	v, err := doc.FieldByIndexErr(f.index)
	return v, err == nil
}

// texts 返回文本字段的内容。
func (f *field) texts(doc reflect.Value) []string {
	v, ok := f.get(doc)
	if !ok {
		return nil
	}
	if v.Kind() == reflect.String {
		return []string{v.String()}
	}
	out := make([]string, v.Len())
	for i := range out {
		out[i] = v.Index(i).String()
	}
	return out
}

// values 返回过滤字段在 doc 中的值，编码为可以按字节比较大小的字符串，去掉重复的值。
func (f *field) values(doc reflect.Value) ([]string, error) {
	v, ok := f.get(doc)
	if !ok {
		return nil, nil
	}
	var items []reflect.Value
	switch {
	case f.ptr:
		if v.IsNil() {
			return nil, nil
		}
		items = []reflect.Value{v.Elem()}
	case f.multi:
		for i := range v.Len() {
			items = append(items, v.Index(i))
		}
	default:
		items = []reflect.Value{v}
	}
	out := make([]string, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		s, err := f.encode(item)
		if err != nil {
			return nil, err
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out, nil
}

// encodeAny 把查询条件中的值转换为字段的类型后编码。
func (f *field) encodeAny(value any) (string, error) {
	v := reflect.ValueOf(value)
	if !v.IsValid() {
		return "", errors.Newf("search: nil value for field %q", f.name)
	}
	if v.Type() != f.typ {
		if !v.Type().ConvertibleTo(f.typ) || (f.kind == kindString && v.Kind() != reflect.String) {
			return "", errors.Newf("search: field %q takes %s, got %T", f.name, f.typ, value)
		}
		v = v.Convert(f.typ)
	}
	return f.encode(v)
}

// encode 把值编码为字符串，字符串的字节顺序与值的大小顺序一致，使范围条件和排序可以直接比较字符串。
func (f *field) encode(v reflect.Value) (string, error) {
	var s string
	switch f.kind {
	case kindTime:
		s = v.Interface().(time.Time).UTC().Format(timeLayout)
	case kindString:
		if m, ok := v.Interface().(encoding.TextMarshaler); ok {
			b, err := m.MarshalText()
			if err != nil {
				return "", errors.Wrapf(err, "search: field %q", f.name)
			}
			s = string(b)
		} else {
			s = v.String()
		}
	case kindBool:
		s = strconv.FormatBool(v.Bool())
	case kindInt:
		s = fmt.Sprintf("%020d", uint64(v.Int())^(1<<63))
	case kindUint:
		s = fmt.Sprintf("%020d", v.Uint())
	case kindFloat:
		x := v.Float()
		if math.IsNaN(x) {
			return "", errors.Newf("search: field %q is NaN", f.name)
		}
		bits := math.Float64bits(x)
		if bits>>63 == 0 {
			bits ^= 1 << 63
		} else {
			bits = ^bits
		}
		s = fmt.Sprintf("%016x", bits)
	}
	if len(s) > maxValue {
		return "", errors.Newf("search: field %q value longer than %d bytes", f.name, maxValue)
	}
	return s, nil
}

// decode 把编码后的值还原为便于展示的字符串，用于分面。
func (f *field) decode(s string) string {
	switch f.kind {
	case kindInt:
		if u, err := strconv.ParseUint(s, 10, 64); err == nil {
			return strconv.FormatInt(int64(u^(1<<63)), 10)
		}
	case kindUint:
		if u, err := strconv.ParseUint(s, 10, 64); err == nil {
			return strconv.FormatUint(u, 10)
		}
	case kindFloat:
		if bits, err := strconv.ParseUint(s, 16, 64); err == nil {
			if bits>>63 == 1 {
				bits ^= 1 << 63
			} else {
				bits = ^bits
			}
			return strconv.FormatFloat(math.Float64frombits(bits), 'g', -1, 64)
		}
	case kindTime:
		if t, err := time.Parse(timeLayout, s); err == nil {
			return t.Format(time.RFC3339Nano)
		}
	}
	return s
}

// timeLayout 是时间的编码格式：固定宽度的 UTC 时间，按字节比较即按时间先后比较。
const timeLayout = "2006-01-02T15:04:05.000000000Z"
