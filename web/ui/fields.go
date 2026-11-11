package ui

import (
	"slices"
	"strconv"

	"github.com/a-h/templ"
)

// Form 是表单字段组件读取的表单状态，*web.Form[T] 实现了它：Value、Values、Checked 回填用户的输入，
// Error 是字段的错误提示。
type Form interface {
	Value(field string) string
	Values(field string) []string
	Checked(field string) bool
	Error(field string) string
}

// Option 是选择类字段的一个选项。
type Option struct {
	Value string
	Label string
	// Disabled 让这个选项不能选择。
	Disabled bool
}

// FieldProps 是表单字段的可选参数，各字段只使用与自己相关的部分。
type FieldProps struct {
	// ID 是输入控件的 id，默认等于字段名；同一页面有两个同名字段时设置。
	ID string
	// Type 是 TextField 中 input 的类型，默认 text。password 类型不回填输入。
	Type        string
	Placeholder string
	// Description 是字段下方的说明；字段有错误时显示错误提示。
	Description string
	// Rows 是 TextareaField 的行数。
	Rows     int
	Required bool
	Disabled bool
	// Class 加在字段的外层元素上。
	Class string
	// Attributes 加在输入控件上，例如 autocomplete、hx-preserve；单选、多选组加在外层元素上。
	Attributes templ.Attributes
}

func firstProps(props []FieldProps) FieldProps {
	if len(props) > 0 {
		return props[0]
	}
	return FieldProps{}
}

func fieldID(p FieldProps, name string) string { return orDefault(p.ID, name) }

// invalid 在 msg 非空时返回 {attr: "true"}：组件按 data-invalid="true"、aria-invalid="true" 切换样式，
// 值必须是 "true"，没有错误时不写这个属性。
func invalid(attr, msg string) templ.Attributes {
	if msg == "" {
		return nil
	}
	return templ.Attributes{attr: "true"}
}

// merge 返回 a 加上 b 的属性，b 优先。
func merge(a, b templ.Attributes) templ.Attributes {
	if len(a) == 0 {
		return b
	}
	out := templ.Attributes{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func optionID(id string, i int) string { return id + "-" + strconv.Itoa(i) }

func selected(f Form, name, value string) bool { return slices.Contains(f.Values(name), value) }

// Pager 是分页导航需要的分页信息，page.Offset 实现了它。
type Pager interface {
	Current() int
	LastPage() int
	Links(onEachSide int) []int
}

// PaginationProps 配置 Pagination。
type PaginationProps struct {
	// URL 返回第 n 页的地址，通常由 web.QueryURL 生成。必填。
	URL func(n int) string
	// HTMX 为 true 时链接同时带 hx-get，只替换列表区域：区域上写 hx-target:inherited 和 hx-swap:inherited。
	HTMX bool
	// OnEachSide 是当前页两侧显示的页数，默认 1。
	OnEachSide int
	// Previous、Next 是上一页、下一页的文字，默认「上一页」「下一页」。
	Previous, Next string
}

func pageLink(p PaginationProps, n int) (string, templ.Attributes) {
	url := p.URL(n)
	if !p.HTMX {
		return url, nil
	}
	return url, templ.Attributes{"hx-get": url}
}

// requiredAttr 给没有 Required 参数的控件（textarea）加上 required 属性。
func requiredAttr(required bool) templ.Attributes {
	if !required {
		return nil
	}
	return templ.Attributes{"required": true}
}
