// Package form 把文本形式的字段值按结构体的 Huma 标签转换、验证并解码。
//
// 网页表单和表格的一行都是“字段名 → 文本”：web.ParseForm 和 sheet.Decode 都用它，
// 因此 JSON 接口、页面表单和表格导入共用同一个结构体上的验证规则。
//
//	data, errs := form.Decode[article.Changes](ctx, url.Values{"title": {"Hello"}})
package form

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"sync"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ryan053857/mygoravel/i18n"
)

// Decode 按 T 的 Huma 标签转换、验证 values 并解码为 T。
//
// 字段名取 T 的 json 标签。T 应当是只有一层字段的结构体，字段为字符串、数字、布尔或它们的切片。
// 按 schema 的类型转换：数字字段的空字符串视为未填写；布尔字段没有提交时为 false，
// on、true、1 为 true，off、false、0 和空字符串为 false（不区分大小写）。多余的字段被忽略。
//
// 返回的错误以字段名为键，提示按 ctx 中的语言翻译，见 i18n 的 validation.* 消息；全部有效时为 nil。
// 验证失败时 T 只包含能转换的字段。
func Decode[T any](ctx context.Context, values map[string][]string) (T, map[string]string) {
	var data T
	s := Schema(reflect.TypeOf(data))
	input := map[string]any{}
	for name, prop := range s.Properties {
		prop = resolve(prop)
		raw, present := values[name]
		switch {
		case prop.Type == huma.TypeArray:
			items := resolve(prop.Items)
			list := make([]any, 0, len(raw))
			for _, v := range raw {
				list = append(list, convert(items.Type, v))
			}
			input[name] = list
		case prop.Type == huma.TypeBoolean && (!present || len(raw) == 0):
			input[name] = false
		case !present || len(raw) == 0 || isNumber(prop.Type) && strings.TrimSpace(raw[0]) == "":
		default:
			input[name] = convert(prop.Type, raw[0])
		}
	}

	var errs map[string]string
	set := func(field, msg string) {
		if errs == nil {
			errs = map[string]string{}
		}
		if _, ok := errs[field]; !ok {
			errs[field] = msg
		}
	}
	res := &huma.ValidateResult{}
	huma.Validate(registry, s, huma.NewPathBuffer(nil, 0), huma.ModeWriteToServer, input, res)
	for _, e := range res.Errors {
		if d, ok := e.(*huma.ErrorDetail); ok {
			set(describe(ctx, d))
		}
	}
	// 没能转换成目标类型的值不参与解码，其余字段照常写入 data。
	for field := range errs {
		if _, isText := input[field].(string); isText && resolve(s.Properties[field]).Type != huma.TypeString {
			delete(input, field)
		}
	}
	b, _ := json.Marshal(input)
	if err := json.Unmarshal(b, &data); err != nil {
		// 通过了验证却放不进 Go 类型，例如 300 超出 int8 的范围。
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) {
			msg := i18n.T(ctx, "validation.format", nil)
			if msg == "validation.format" {
				msg = "expected " + te.Type.String()
			}
			set(te.Field, msg)
		}
	}
	return data, errs
}

var (
	registry = huma.NewMapRegistry("#/components/schemas/", huma.DefaultSchemaNamer)
	regMu    sync.Mutex
	schemas  sync.Map // reflect.Type -> *huma.Schema
)

// Schema 返回 Decode 验证 t 时使用的 schema，例如用于查看哪些字段是必填的（Required）。
// 返回值被缓存并共享，不要修改它。
func Schema(t reflect.Type) *huma.Schema {
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

// convert 把文本转换为 schema 类型；无法转换时保留原文，由验证报出类型错误。
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
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "on", "true", "1":
			return true
		case "", "off", "false", "0":
			return false
		}
	}
	return v
}
