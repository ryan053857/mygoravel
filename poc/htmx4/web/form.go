package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/validation"
)

// FieldErrors 是表单字段到错误提示的映射，键为字段的 json 名。
type FieldErrors map[string]string

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
	s := registry.Schema(t, false, "")
	schemas.Store(t, s)
	return s
}

func resolve(s *huma.Schema) *huma.Schema {
	if s != nil && s.Ref != "" {
		return registry.SchemaFromRef(s.Ref)
	}
	return s
}

// Form 解析请求的表单（urlencoded 或 multipart），按 T 的 Huma 标签验证，返回解码后的 T。
// 字段名取 json 标签。按 schema 类型转换：整数和数字解析失败时保留原字符串，由验证报出类型错误；
// 布尔字段没有提交时为 false（未勾选的复选框）；数字字段提交空字符串视为未提交。
// 验证失败时 FieldErrors 非空，T 中保留所有能转换的值，便于重新渲染表单。
// 请求体无法解析时返回 error。
func Form[T any](r *http.Request) (T, FieldErrors, error) {
	var out T
	if err := r.ParseMultipartForm(32 << 20); err != nil && err != http.ErrNotMultipart {
		return out, nil, err
	}
	s := resolve(schemaOf(reflect.TypeOf(out)))
	values := map[string]any{}
	for name, prop := range s.Properties {
		prop = resolve(prop)
		raw, present := r.PostForm[name]
		if prop.Type == huma.TypeArray {
			items := resolve(prop.Items)
			list := make([]any, 0, len(raw))
			for _, v := range raw {
				list = append(list, convert(items.Type, v))
			}
			values[name] = list
			continue
		}
		if prop.Type == huma.TypeBoolean && !present {
			values[name] = false
			continue
		}
		if !present || ((prop.Type == huma.TypeInteger || prop.Type == huma.TypeNumber) && raw[0] == "") {
			continue
		}
		values[name] = convert(prop.Type, raw[0])
	}

	res := &huma.ValidateResult{}
	huma.Validate(registry, s, huma.NewPathBuffer(nil, 0), huma.ModeWriteToServer, values, res)
	var errs FieldErrors
	for _, e := range res.Errors {
		d := e.(*huma.ErrorDetail)
		field, msg := describe(d)
		if errs == nil {
			errs = FieldErrors{}
		}
		if _, dup := errs[field]; !dup {
			errs[field] = msg
		}
	}
	// 类型错误的字段不参与解码，其余值照常写入 T。
	for field := range errs {
		if _, isString := values[field].(string); isString && resolve(s.Properties[field]).Type != huma.TypeString {
			delete(values, field)
		}
	}
	b, _ := json.Marshal(values)
	if err := json.Unmarshal(b, &out); err != nil {
		return out, errs, err
	}
	return out, errs, nil
}

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

// rule 把 Huma 的英文验证消息反解为规则和参数，再换成中文提示。
type rule struct {
	re  *regexp.Regexp
	msg func(args []string) string
}

func pattern(format string, msg func([]string) string) rule {
	quoted := regexp.QuoteMeta(format)
	for _, verb := range []string{"%v", "%d", "%s"} {
		quoted = strings.ReplaceAll(quoted, regexp.QuoteMeta(verb), "(.+)")
	}
	return rule{regexp.MustCompile("^" + quoted + "$"), msg}
}

var rules = []rule{
	pattern(validation.MsgExpectedRequiredProperty, func([]string) string { return "必填" }),
	pattern(validation.MsgExpectedMinLength, func(a []string) string {
		if a[0] == "1" {
			return "不能为空"
		}
		return fmt.Sprintf("至少 %s 个字符", a[0])
	}),
	pattern(validation.MsgExpectedMaxLength, func(a []string) string { return fmt.Sprintf("最多 %s 个字符", a[0]) }),
	pattern(validation.MsgExpectedMinimumNumber, func(a []string) string { return fmt.Sprintf("不能小于 %s", a[0]) }),
	pattern(validation.MsgExpectedMaximumNumber, func(a []string) string { return fmt.Sprintf("不能大于 %s", a[0]) }),
	pattern(validation.MsgExpectedMinItems, func(a []string) string { return fmt.Sprintf("至少选择 %s 项", a[0]) }),
	pattern(validation.MsgExpectedMaxItems, func(a []string) string { return fmt.Sprintf("最多选择 %s 项", a[0]) }),
	pattern(validation.MsgExpectedInteger, func([]string) string { return "必须是整数" }),
	pattern(validation.MsgExpectedNumber, func([]string) string { return "必须是数字" }),
	pattern(validation.MsgExpectedOneOf, func([]string) string { return "不是可选的值" }),
	pattern(validation.MsgExpectedRFC5322Email, func([]string) string { return "邮箱格式不正确" }),
	pattern(validation.MsgExpectedRFC5322EmailBare, func([]string) string { return "邮箱格式不正确" }),
	// patternDescription 存在时 Huma 使用 MsgExpectedBePattern，参数就是描述文字。
	pattern(validation.MsgExpectedBePattern, func(a []string) string { return "必须是" + a[0] }),
	pattern(validation.MsgExpectedMatchPattern, func([]string) string { return "格式不正确" }),
}

func describe(d *huma.ErrorDetail) (field, msg string) {
	field = d.Location
	for _, r := range rules {
		m := r.re.FindStringSubmatch(d.Message)
		if m == nil {
			continue
		}
		if r.re.String() == rules[0].re.String() {
			// 必填错误的位置是父对象，字段名在消息里。
			field = joinPath(d.Location, m[1])
		}
		return field, r.msg(m[1:])
	}
	return field, d.Message
}

func joinPath(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "." + name
}
