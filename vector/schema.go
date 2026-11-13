package vector

import (
	"encoding"
	"math"
	"reflect"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/qdrant/go-client/qdrant"

	"github.com/go-tenon/tenon/search"
)

var (
	timeType          = reflect.TypeFor[time.Time]()
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
)

// valueKind 是过滤字段的值的类型，决定它在 payload 中的类型和 payload 索引的类型。
type valueKind int

const (
	kindKeyword valueKind = iota + 1 // string 和实现 encoding.TextMarshaler 的类型，例如 uuid.UUID
	kindBool
	kindInt
	kindFloat
	kindTime
)

// field 是文档类型中带 vector 标签的字段。
type field struct {
	name  string // JSON 名称，也是 payload 中的字段名
	index []int  // reflect 的字段路径
	kind  valueKind
	typ   reflect.Type // 过滤字段的值类型：切片的元素类型或指针指向的类型
	multi bool         // 过滤字段：切片，一个文档可以有多个值
	ptr   bool         // 过滤字段：指针，nil 表示没有值
}

// schema 是文档类型的结构，Define 时从 vector 标签解析。
type schema struct {
	typ     reflect.Type
	embed   *field
	filters map[string]*field
	tenant  *field
	// tokenizer 是混合索引切分文本的分词器，用于计算稀疏向量；nil 时只有模型计算的稠密向量。
	tokenizer search.Tokenizer
}

// parseSchema 解析 D 中带 vector 标签的字段：
//
//	embed   计算向量的文本，string，有且只有一个
//	filter  过滤用的值
//	tenant  文档所在的租户，string
func parseSchema(typ reflect.Type) (*schema, error) {
	if typ.Kind() != reflect.Struct {
		return nil, errors.Newf("vector: document type %s is not a struct", typ)
	}
	s := &schema{typ: typ, filters: map[string]*field{}}
	for _, sf := range reflect.VisibleFields(typ) {
		tag, ok := sf.Tag.Lookup("vector")
		if !ok || sf.Anonymous {
			continue
		}
		if !sf.IsExported() {
			return nil, errors.Newf("vector: field %s is not exported", sf.Name)
		}
		f := &field{name: jsonName(sf), index: sf.Index}
		var err error
		switch tag {
		case "embed":
			// 只用来计算向量的文本不必保存，可以是 json:"-"
			if sf.Type.Kind() != reflect.String {
				err = errors.New("embed field must be a string")
			}
			if s.embed != nil {
				err = errors.New("more than one embed field")
			}
			s.embed = f
		case "filter":
			if err = checkName(f.name); err == nil {
				if s.filters[f.name] != nil {
					err = errors.Newf("duplicate field name %q", f.name)
				}
				err = errors.CombineErrors(err, f.setValueType(sf.Type))
			}
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
			return nil, errors.Wrapf(err, "vector: field %s", sf.Name)
		}
	}
	if s.embed == nil {
		return nil, errors.Newf(`vector: document type %s has no vector:"embed" field`, typ)
	}
	return s, nil
}

// checkName 检查过滤字段的 JSON 名称：_ 开头的 payload 字段由这个包使用，. 和 [ 在 Qdrant 的字段路径中有特殊含义。
func checkName(name string) error {
	switch {
	case name == "":
		return errors.New("filter field is not encoded to JSON")
	case strings.HasPrefix(name, "_"):
		return errors.Newf("filter field name %q starts with _", name)
	case strings.ContainsAny(name, ".[]"):
		return errors.Newf("filter field name %q contains . [ or ]", name)
	}
	return nil
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
		f.kind = kindKeyword
	default:
		switch t.Kind() {
		case reflect.String:
			f.kind = kindKeyword
		case reflect.Bool:
			f.kind = kindBool
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			f.kind = kindInt
		case reflect.Float32, reflect.Float64:
			f.kind = kindFloat
		default:
			return errors.Newf("unsupported filter type %s", t)
		}
	}
	return nil
}

// fieldType 返回字段的 payload 索引的类型。
func (f *field) fieldType() qdrant.FieldType {
	switch f.kind {
	case kindBool:
		return qdrant.FieldType_FieldTypeBool
	case kindInt:
		return qdrant.FieldType_FieldTypeInteger
	case kindFloat:
		return qdrant.FieldType_FieldTypeFloat
	case kindTime:
		return qdrant.FieldType_FieldTypeDatetime
	default:
		return qdrant.FieldType_FieldTypeKeyword
	}
}

// get 返回 doc 中这个字段的值；经过的嵌入指针为 nil 时 ok 为 false。
func (f *field) get(doc reflect.Value) (v reflect.Value, ok bool) {
	v, err := doc.FieldByIndexErr(f.index)
	return v, err == nil
}

// payload 返回过滤字段在 doc 中的值，没有值时返回 nil。多值字段返回列表。
func (f *field) payload(doc reflect.Value) (*qdrant.Value, error) {
	v, ok := f.get(doc)
	if !ok {
		return nil, nil
	}
	switch {
	case f.ptr:
		if v.IsNil() {
			return nil, nil
		}
		return f.value(v.Elem())
	case f.multi:
		items := make([]*qdrant.Value, v.Len())
		for i := range items {
			item, err := f.value(v.Index(i))
			if err != nil {
				return nil, err
			}
			items[i] = item
		}
		return qdrant.NewValueFromList(items...), nil
	default:
		return f.value(v)
	}
}

// valueOf 把查询条件中的值转换为字段的类型。
func (f *field) valueOf(value any) (reflect.Value, error) {
	v := reflect.ValueOf(value)
	if !v.IsValid() {
		return v, errors.Newf("vector: nil value for field %q", f.name)
	}
	if v.Type() != f.typ {
		if !v.Type().ConvertibleTo(f.typ) || (f.kind == kindKeyword && v.Kind() != reflect.String) {
			return v, errors.Newf("vector: field %q takes %s, got %T", f.name, f.typ, value)
		}
		v = v.Convert(f.typ)
	}
	return v, nil
}

// value 把一个值转换为 payload 中的值：时间为 RFC 3339 格式的 UTC 时间。
func (f *field) value(v reflect.Value) (*qdrant.Value, error) {
	switch f.kind {
	case kindTime:
		return qdrant.NewValueString(v.Interface().(time.Time).UTC().Format(time.RFC3339Nano)), nil
	case kindKeyword:
		s, err := f.keyword(v)
		return qdrant.NewValueString(s), err
	case kindBool:
		return qdrant.NewValueBool(v.Bool()), nil
	case kindInt:
		n, err := f.integer(v)
		return qdrant.NewValueInt(n), err
	default:
		x := v.Float()
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, errors.Newf("vector: field %q is %v", f.name, x)
		}
		return qdrant.NewValueDouble(x), nil
	}
}

func (f *field) keyword(v reflect.Value) (string, error) {
	if m, ok := v.Interface().(encoding.TextMarshaler); ok {
		b, err := m.MarshalText()
		return string(b), errors.Wrapf(err, "vector: field %q", f.name)
	}
	return v.String(), nil
}

// integer 返回整数字段的值。Qdrant 的整数是 int64，更大的无符号整数无法保存。
func (f *field) integer(v reflect.Value) (int64, error) {
	if v.CanUint() {
		if v.Uint() > math.MaxInt64 {
			return 0, errors.Newf("vector: field %q value %d overflows int64", f.name, v.Uint())
		}
		return int64(v.Uint()), nil
	}
	return v.Int(), nil
}
