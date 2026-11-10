package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ryan053857/mygoravel/errorx"
)

// ErrBadForm 表示请求体不是可以解析的表单。
var ErrBadForm = errorx.Define(errorx.InvalidArgument, "web.bad_form")

// maxMemory 是 multipart 表单在内存中保存的上限，超出部分写入临时文件。
const maxMemory = 32 << 20

// Form 是一次表单提交：解码后的数据、原始输入和每个字段的错误。
//
// 字段名取 T 的 json 标签。重新渲染表单时用 Value 回填原始输入（类似 Laravel 的 old()），
// 这样类型错误的输入（例如在数字字段里填了 abc）也能原样保留。
type Form[T any] struct {
	// Data 是解码后的数据。验证失败时只包含能转换的字段。
	Data   T
	values url.Values
	errors map[string]string
}

// NewForm 用已有数据创建表单，用于首次渲染编辑页：Value 返回 data 中对应字段的文本。
func NewForm[T any](data T) *Form[T] {
	f := &Form[T]{Data: data, values: url.Values{}}
	b, _ := json.Marshal(data)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for name, v := range m {
		if list, ok := v.([]any); ok {
			for _, item := range list {
				f.values.Add(name, text(item))
			}
			continue
		}
		if v != nil && v != false {
			f.values.Set(name, text(v))
		}
	}
	return f
}

func text(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

// ParseForm 解析请求体中的表单（urlencoded 或 multipart），按 T 的 Huma 标签验证并解码。
//
// T 应当是只有一层字段的结构体，字段为字符串、数字、布尔或它们的切片。按 schema 的类型转换：
// 数字字段的空字符串视为未填写；布尔字段没有提交时为 false（未勾选的复选框），on、true、1 为 true。
// 请求中多余的字段被忽略。验证提示按 context 中的语言翻译，见 i18n 的 validation.* 消息。
//
// 只有请求体无法解析时返回错误（ErrBadForm）；验证失败通过 Form.Valid 判断。
func ParseForm[T any](r *http.Request) (*Form[T], error) {
	if err := r.ParseMultipartForm(maxMemory); err != nil && err != http.ErrNotMultipart {
		return nil, ErrBadForm.Wrap(err)
	}
	var data T
	s := schemaOf(reflect.TypeOf(data))
	f := &Form[T]{values: r.PostForm}
	input := map[string]any{}
	for name, prop := range s.Properties {
		prop = resolve(prop)
		raw, present := r.PostForm[name]
		switch {
		case prop.Type == huma.TypeArray:
			items := resolve(prop.Items)
			list := make([]any, 0, len(raw))
			for _, v := range raw {
				list = append(list, convert(items.Type, v))
			}
			input[name] = list
		case prop.Type == huma.TypeBoolean && !present:
			input[name] = false
		case !present || isNumber(prop.Type) && strings.TrimSpace(raw[0]) == "":
		default:
			input[name] = convert(prop.Type, raw[0])
		}
	}

	res := &huma.ValidateResult{}
	huma.Validate(registry, s, huma.NewPathBuffer(nil, 0), huma.ModeWriteToServer, input, res)
	for _, e := range res.Errors {
		d, ok := e.(*huma.ErrorDetail)
		if !ok {
			continue
		}
		field, msg := describe(r.Context(), d)
		if f.Error(field) == "" {
			f.SetError(field, msg)
		}
	}
	// 没能转换成目标类型的值不参与解码，其余字段照常写入 Data。
	for field := range f.errors {
		if _, isText := input[field].(string); isText && resolve(s.Properties[field]).Type != huma.TypeString {
			delete(input, field)
		}
	}
	b, err := json.Marshal(input)
	if err == nil {
		err = json.Unmarshal(b, &data)
	}
	if err != nil {
		return nil, ErrBadForm.Wrap(err)
	}
	f.Data = data
	return f, nil
}

// Valid 报告表单没有任何字段错误。
func (f *Form[T]) Valid() bool { return len(f.errors) == 0 }

// Value 返回字段的原始输入；字段有多个值时返回第一个。
func (f *Form[T]) Value(field string) string { return f.values.Get(field) }

// Values 返回字段的全部原始输入，用于多选框和多选列表。
func (f *Form[T]) Values(field string) []string { return f.values[field] }

// Checked 报告布尔字段是否选中。
func (f *Form[T]) Checked(field string) bool {
	switch f.values.Get(field) {
	case "on", "true", "1":
		return true
	}
	return false
}

// Error 返回字段的错误提示，没有错误时为空字符串。
func (f *Form[T]) Error(field string) string { return f.errors[field] }

// Errors 返回全部字段错误。
func (f *Form[T]) Errors() map[string]string { return f.errors }

// SetError 设置字段的错误提示，用于业务规则验证，例如把 slug 重复的业务错误放到 slug 字段旁。
func (f *Form[T]) SetError(field, message string) {
	if f.errors == nil {
		f.errors = map[string]string{}
	}
	f.errors[field] = message
}

var (
	registry = huma.NewMapRegistry("#/components/schemas/", huma.DefaultSchemaNamer)
	regMu    sync.Mutex
	schemas  sync.Map // reflect.Type -> *huma.Schema
)

func schemaOf(t reflect.Type) *huma.Schema {
	if s, ok := schemas.Load(t); ok {
		return s.(*huma.Schema)
	}
	regMu.Lock()
	defer regMu.Unlock()
	s := resolve(registry.Schema(t, false, ""))
	schemas.Store(t, s)
	return s
}

func resolve(s *huma.Schema) *huma.Schema {
	if s != nil && s.Ref != "" {
		return registry.SchemaFromRef(s.Ref)
	}
	return s
}

func isNumber(typ string) bool { return typ == huma.TypeInteger || typ == huma.TypeNumber }

// convert 把表单文本转换为 schema 类型；无法转换时保留原文，由验证报出类型错误。
func convert(typ, v string) any {
	switch typ {
	case huma.TypeInteger:
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			return n
		}
	case huma.TypeNumber:
		if n, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return n
		}
	case huma.TypeBoolean:
		switch v {
		case "on", "true", "1":
			return true
		case "", "off", "false", "0":
			return false
		}
	}
	return v
}
