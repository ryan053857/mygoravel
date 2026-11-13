package form

import (
	"context"
	"regexp"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/validation"

	"github.com/go-tenon/tenon/i18n"
)

// FieldError 是翻译后的一条验证错误。
type FieldError struct {
	// Location 是出错的位置，例如 body.title。Huma 把必填错误报告在缺少字段的父对象上，
	// 这里补上了字段名。
	Location string
	// Code 是验证规则，也是翻译用的消息 ID，例如 validation.min_length；无法识别的消息为空。
	Code string
	// Message 是按 ctx 的语言翻译的提示；无法识别或没有译文时是 Huma 的英文原文。
	Message string
}

// rule 把一条 Huma 验证消息反解为消息 ID 和模板数据。
// Huma 只提供格式化好的英文消息，规则表由 validation.Msg* 生成，Huma 修改格式时测试会失败。
type rule struct {
	re   *regexp.Regexp
	id   string
	data func(args []string) map[string]any
	// named 表示第一个参数是缺少的字段名，要拼到位置上。
	named bool
}

func on(format, id string, data func([]string) map[string]any) rule {
	quoted := regexp.QuoteMeta(format)
	for _, verb := range []string{"%v", "%d", "%s"} {
		quoted = strings.ReplaceAll(quoted, regexp.QuoteMeta(verb), "(.+)")
	}
	return rule{re: regexp.MustCompile("^" + quoted + "$"), id: id, data: data}
}

func named(r rule) rule { r.named = true; return r }

func minArg(a []string) map[string]any   { return map[string]any{"Min": a[0]} }
func maxArg(a []string) map[string]any   { return map[string]any{"Max": a[0]} }
func valueArg(a []string) map[string]any { return map[string]any{"Value": a[0]} }

// 顺序有意义：具体的格式规则必须排在 MsgExpectedBePattern（"expected string to be %s"）之前，
// MsgExpectedOneOf 必须排在 MsgExpectedConst（"expected value to be %v"）之前。
var rules = []rule{
	named(on(validation.MsgExpectedRequiredProperty, "validation.required", nil)),
	on(validation.MsgExpectedMinLength, "validation.min_length", minArg),
	on(validation.MsgExpectedMaxLength, "validation.max_length", maxArg),
	on(validation.MsgExpectedMinimumNumber, "validation.minimum", minArg),
	on(validation.MsgExpectedExclusiveMinimumNumber, "validation.exclusive_minimum", minArg),
	on(validation.MsgExpectedMaximumNumber, "validation.maximum", maxArg),
	on(validation.MsgExpectedExclusiveMaximumNumber, "validation.exclusive_maximum", maxArg),
	on(validation.MsgExpectedNumberBeMultipleOf, "validation.multiple_of", valueArg),
	on(validation.MsgExpectedMinItems, "validation.min_items", minArg),
	on(validation.MsgExpectedMaxItems, "validation.max_items", maxArg),
	on(validation.MsgExpectedArrayItemsUnique, "validation.unique", nil),
	on(validation.MsgExpectedInteger, "validation.integer", nil),
	on(validation.MsgExpectedNumber, "validation.number", nil),
	on(validation.MsgExpectedBoolean, "validation.boolean", nil),
	on(validation.MsgExpectedString, "validation.type", nil),
	on(validation.MsgExpectedArray, "validation.type", nil),
	on(validation.MsgExpectedObject, "validation.type", nil),
	on(validation.MsgExpectedNull, "validation.type", nil),
	on(validation.MsgExpectedOneOf, "validation.one_of", nil),
	on(validation.MsgExpectedConst, "validation.one_of", nil),
	on(validation.MsgUnexpectedProperty, "validation.unexpected", nil),
	on(validation.MsgExpectedRFC5322Email, "validation.email", nil),
	on(validation.MsgExpectedRFC5322EmailBare, "validation.email", nil),
	on(validation.MsgExpectedRFC3986URI, "validation.url", nil),
	on(validation.MsgExpectedRFC3986AbsoluteURI, "validation.url", nil),
	on(validation.MsgExpectedRFC4122UUID, "validation.format", nil),
	on(validation.MsgExpectedRFC3339DateTime, "validation.date_time", nil),
	on(validation.MsgExpectedRFC1123DateTime, "validation.date_time", nil),
	on(validation.MsgExpectedRFC3339Date, "validation.date", nil),
	on(validation.MsgExpectedRFC3339Time, "validation.time", nil),
	on(validation.MsgExpectedRFC5890Hostname, "validation.format", nil),
	on(validation.MsgExpectedRFC2673IPv4, "validation.format", nil),
	on(validation.MsgExpectedRFC2373IPv6, "validation.format", nil),
	on(validation.MsgExpectedRFCIPAddr, "validation.format", nil),
	on(validation.MsgExpectedRFC6570URITemplate, "validation.format", nil),
	on(validation.MsgExpectedRFC6901JSONPointer, "validation.format", nil),
	on(validation.MsgExpectedRFC6901RelativeJSONPointer, "validation.format", nil),
	on(validation.MsgExpectedRegexp, "validation.format", nil),
	on(validation.MsgExpectedBase64String, "validation.format", nil),
	on(validation.MsgExpectedDuration, "validation.format", nil),
	on(validation.MsgExpectedMatchPattern, "validation.pattern", nil),
	// 设置了 patternDescription 时，Huma 用描述代替正则表达式。
	on(validation.MsgExpectedBePattern, "validation.pattern_description", func(a []string) map[string]any {
		return map[string]any{"Description": a[0]}
	}),

	// Huma 解析路径、查询参数和请求头时的消息，不在 validation 包中。位置已经是参数本身。
	on("required %s parameter is missing", "validation.required", nil),
	on("unknown query parameter", "validation.unexpected", nil),
	on("invalid integer", "validation.integer", nil),
	on("invalid float", "validation.number", nil),
	on("invalid floating value", "validation.number", nil),
	on("invalid boolean", "validation.boolean", nil),
	on("invalid date/time for format %s", "validation.date_time", nil),
}

// Describe 识别 Huma 的一条验证错误，返回它的规则和按 ctx 的语言翻译的提示。
// 表单（Decode）和 JSON API（rest）的验证错误都由它翻译。消息 ID 以 validation. 开头，
// 应用的翻译文件中写同名消息即可覆盖。
func Describe(ctx context.Context, d *huma.ErrorDetail) FieldError {
	e := FieldError{Location: d.Location, Message: d.Message}
	for _, r := range rules {
		m := r.re.FindStringSubmatch(d.Message)
		if m == nil {
			continue
		}
		var data map[string]any
		if r.data != nil {
			data = r.data(m[1:])
		}
		if r.named {
			e.Location = join(d.Location, m[1])
		}
		e.Code = r.id
		if r.id == "validation.min_length" && m[1] == "1" {
			e.Code = "validation.filled"
		}
		if msg := i18n.T(ctx, e.Code, data); msg != e.Code {
			e.Message = msg
		}
		return e
	}
	return e
}

func join(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "." + name
}
