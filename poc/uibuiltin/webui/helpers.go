package webui

import "github.com/a-h/templ"

func or(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func first[T any](props []T) T {
	var p T
	if len(props) > 0 {
		p = props[0]
	}
	return p
}

// invalid 在 msg 非空时返回 {attr: "true"}：组件的样式按 data-invalid="true"、aria-invalid="true" 切换。
func invalid(attr, msg string) templ.Attributes {
	if msg == "" {
		return nil
	}
	return templ.Attributes{attr: "true"}
}

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

func hxGet(p PaginationProps, n int) templ.Attributes {
	if !p.HTMX {
		return nil
	}
	return templ.Attributes{"hx-get": p.URL(n)}
}
