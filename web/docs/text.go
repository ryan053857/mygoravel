package docs

import (
	"context"
	"html"
	"strings"

	"github.com/ryan053857/mygoravel/i18n"
)

// fallbacks 是本包的文案 id 和英文的 fallback：context 中没有语言（没有经过 httpx.Locale）时使用。
var fallbacks = map[string]string{
	"docs.toc":             "On this page",
	"ui.sidebar.toggle":    "Toggle sidebar",
	"docs.previous":        "Previous",
	"docs.next":            "Next",
	"docs.overview":        "Overview",
	"docs.copy":            "Copy",
	"docs.alert.note":      "Note",
	"docs.alert.tip":       "Tip",
	"docs.alert.important": "Important",
	"docs.alert.warning":   "Warning",
	"docs.alert.caution":   "Caution",
}

// text 按请求的语言翻译文案 id。
func text(ctx context.Context, id string) string {
	if s := i18n.T(ctx, id, nil); s != id {
		return s
	}
	return fallbacks[id]
}

// label 是正文中按请求的语言替换的文案（复制按钮、提示块的标题）。正文在 New 时渲染一次，
// 渲染时还不知道语言，所以先写入 NUL 包围的 id，每次请求再替换（localize）。
// goldmark 把 Markdown 中的 NUL 换成 U+FFFD，正文的其他部分不会出现这样的标记。
func label(id string) string { return "\x00" + id + "\x00" }

// localize 把 body 中的 label 替换为请求的语言的文字。
func localize(ctx context.Context, body string) string {
	if !strings.Contains(body, "\x00") {
		return body
	}
	pairs := make([]string, 0, 2*len(fallbacks))
	for id := range fallbacks {
		pairs = append(pairs, label(id), html.EscapeString(text(ctx, id)))
	}
	return strings.NewReplacer(pairs...).Replace(body)
}
