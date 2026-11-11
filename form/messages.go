package form

import (
	"context"
	"regexp"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/validation"

	"github.com/ryan053857/mygoravel/i18n"
)

// rule 把一条 Huma 验证消息反解为 i18n 消息 ID 和模板数据。
// Huma 只提供格式化好的英文消息，规则表由 validation.Msg* 生成，Huma 修改格式时测试会失败。
type rule struct {
	re   *regexp.Regexp
	id   string
	data func(args []string) map[string]any
}

func on(format, id string, data func([]string) map[string]any) rule {
	quoted := regexp.QuoteMeta(format)
	for _, verb := range []string{"%v", "%d", "%s"} {
		quoted = strings.ReplaceAll(quoted, regexp.QuoteMeta(verb), "(.+)")
	}
	return rule{regexp.MustCompile("^" + quoted + "$"), id, data}
}

func minArg(a []string) map[string]any   { return map[string]any{"Min": a[0]} }
func maxArg(a []string) map[string]any   { return map[string]any{"Max": a[0]} }
func valueArg(a []string) map[string]any { return map[string]any{"Value": a[0]} }

// 顺序有意义：具体的格式规则必须排在 MsgExpectedBePattern（"expected string to be %s"）之前。
var rules = []rule{
	on(validation.MsgExpectedRequiredProperty, "validation.required", nil),
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
	on(validation.MsgExpectedOneOf, "validation.one_of", nil),
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
	on(validation.MsgExpectedMatchPattern, "validation.pattern", nil),
	// 设置了 patternDescription 时，Huma 用描述代替正则表达式。
	on(validation.MsgExpectedBePattern, "validation.pattern_description", func(a []string) map[string]any {
		return map[string]any{"Description": a[0]}
	}),
}

// describe 返回错误所在的字段和翻译后的提示。没有对应规则或译文时返回 Huma 的原始消息。
func describe(ctx context.Context, d *huma.ErrorDetail) (field, message string) {
	field = d.Location
	for _, r := range rules {
		m := r.re.FindStringSubmatch(d.Message)
		if m == nil {
			continue
		}
		var data map[string]any
		if r.data != nil {
			data = r.data(m[1:])
		}
		if r.id == "validation.required" {
			// 必填错误的位置是父对象，字段名在消息里。
			field = join(d.Location, m[1])
		}
		if r.id == "validation.min_length" && m[1] == "1" {
			r.id = "validation.filled"
		}
		if msg := i18n.T(ctx, r.id, data); msg != r.id {
			return field, msg
		}
		return field, d.Message
	}
	return field, d.Message
}

func join(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "." + name
}
