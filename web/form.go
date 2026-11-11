package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/form"
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

// ParseForm 解析请求体中的表单（urlencoded 或 multipart），用 form.Decode 按 T 的 Huma 标签验证并解码。
//
// T 应当是只有一层字段的结构体，字段为字符串、数字、布尔或它们的切片，转换规则见 form.Decode。
// 布尔字段没有提交时为 false（未勾选的复选框）。验证提示按 context 中的语言翻译。
//
// 只有请求体无法解析时返回错误（ErrBadForm）；验证失败通过 Form.Valid 判断。
func ParseForm[T any](r *http.Request) (*Form[T], error) {
	if err := r.ParseMultipartForm(maxMemory); err != nil && err != http.ErrNotMultipart {
		return nil, ErrBadForm.Wrap(err)
	}
	data, errs := form.Decode[T](r.Context(), r.PostForm)
	return &Form[T]{Data: data, values: r.PostForm, errors: errs}, nil
}

// Valid 报告表单没有任何字段错误。
func (f *Form[T]) Valid() bool { return len(f.errors) == 0 }

// Value 返回字段的原始输入；字段有多个值时返回第一个。
func (f *Form[T]) Value(field string) string { return f.values.Get(field) }

// Values 返回字段的全部原始输入，用于多选框和多选列表。
func (f *Form[T]) Values(field string) []string { return f.values[field] }

// Checked 报告布尔字段是否选中。
func (f *Form[T]) Checked(field string) bool {
	switch strings.ToLower(f.values.Get(field)) {
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
