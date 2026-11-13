package ui_test

import (
	"context"
	"io"
	"testing"

	"github.com/a-h/templ"
	"github.com/axadrn/shadcn-templ/v2/components/icon"

	"github.com/go-tenon/tenon/web/ui"
)

func sortURL(sort string) string { return "/admin/articles?sort=" + sort }

func TestSortHead(t *testing.T) {
	for _, c := range []struct {
		current, next, aria, icon string
		props                     ui.SortHeadProps
	}{
		{"", "title", "", "lucide-chevrons-up-down", ui.SortHeadProps{}},
		{"-created", "title", "", "lucide-chevrons-up-down", ui.SortHeadProps{}},
		{"title", "-title", `aria-sort="ascending"`, "lucide-arrow-up", ui.SortHeadProps{}},
		{"-title", "title", `aria-sort="descending"`, "lucide-arrow-down", ui.SortHeadProps{}},
		{"", "-title", "", "lucide-chevrons-up-down", ui.SortHeadProps{DescFirst: true}},
		{"-title", "title", `aria-sort="descending"`, "lucide-arrow-down", ui.SortHeadProps{DescFirst: true}},
	} {
		html := render(t, ui.SortHead(c.current, "title", "标题", sortURL, c.props))
		contains(t, html, `href="/admin/articles?sort=`+c.next+`"`, ">标题", c.icon, "<th")
		lacks(t, html, "hx-get")
		if c.aria != "" {
			contains(t, html, c.aria, "data-sorted")
		} else {
			lacks(t, html, "aria-sort", "data-sorted")
		}
	}
	html := render(t, ui.SortHead("title", "title", "标题", sortURL, ui.SortHeadProps{HTMX: true}))
	contains(t, html, `hx-get="/admin/articles?sort=-title"`)
}

func TestEmpty(t *testing.T) {
	html := render(t, withChildren(ui.Empty(ui.EmptyProps{Icon: icon.FileText, Title: "还没有文章", Description: "写下第一篇文章吧。"}), templ.Raw(`<a id="new" href="/admin/articles/new">新建文章</a>`)))
	contains(t, html, `data-slot="empty"`, "lucide-file-text", ">还没有文章<", "写下第一篇文章吧。", `<a id="new"`, "border-dashed")
	html = render(t, withChildren(ui.Empty(ui.EmptyProps{Title: "没有结果"}), templ.NopComponent))
	lacks(t, html, "lucide-", "empty-description")
}

func withChildren(parent, children templ.Component) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		return parent.Render(templ.WithChildren(ctx, children), w)
	})
}
