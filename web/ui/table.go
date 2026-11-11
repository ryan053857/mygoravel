package ui

import "github.com/a-h/templ"

// SortHeadProps 是 SortHead 的可选参数。
type SortHeadProps struct {
	// HTMX 为 true 时链接同时带 hx-get，只替换列表区域（区域上写 hx-target:inherited 和 hx-swap:inherited）。
	HTMX bool
	// DescFirst 让第一次点击按降序排列，适合时间、数量这类通常从大到小看的列。
	DescFirst bool
	Class     string
}

// sortDirection 返回 key 在 current 中的排序方向：ascending、descending，没有按它排序时为空。
// current 是排序参数，key 表示升序，-key 表示降序。
func sortDirection(current, key string) string {
	switch current {
	case key:
		return "ascending"
	case "-" + key:
		return "descending"
	}
	return ""
}

// nextSort 返回点击 key 的表头后的排序参数：已经按它升序时改为降序，降序时改为升序，
// 没有按它排序时按 descFirst 选择。
func nextSort(current, key string, descFirst bool) string {
	switch sortDirection(current, key) {
	case "ascending":
		return "-" + key
	case "descending":
		return key
	}
	if descFirst {
		return "-" + key
	}
	return key
}

func sortHeadAttrs(current, key string) templ.Attributes {
	if d := sortDirection(current, key); d != "" {
		return templ.Attributes{"aria-sort": d}
	}
	return nil
}

func sortLinkAttrs(url string, htmx bool) templ.Attributes {
	if !htmx {
		return nil
	}
	return templ.Attributes{"hx-get": url}
}

// EmptyProps 配置 Empty。
type EmptyProps struct {
	// Icon 是标题上方的图标，例如 icon.FileText。
	Icon        Icon
	Title       string
	Description string
	Class       string
}

func firstSortProps(props []SortHeadProps) SortHeadProps {
	if len(props) > 0 {
		return props[0]
	}
	return SortHeadProps{}
}

func sortedAttr(dir string) templ.Attributes {
	if dir == "" {
		return nil
	}
	return templ.Attributes{"data-sorted": dir}
}
