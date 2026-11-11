package webui

import (
	"context"
	"html"
	"io"

	"github.com/a-h/templ"

	"github.com/ryan053857/mygoravel/web"
)

// Head 按区域主题（ThemeFrom）输出 <head> 的内容：暗色模式脚本、样式表、主题变量，
// 以及 htmx、Alpine、组件脚本和衔接脚本。样式表取 Theme.Stylesheet，为空时用框架为 Theme.Style 预编译的样式表。
// 与 web.Scripts 一样支持 templ.WithNonce 设置的 CSP nonce。
func Head() templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		t := ThemeFrom(ctx)
		nonce := ""
		if n := templ.GetNonce(ctx); n != "" {
			nonce = ` nonce="` + html.EscapeString(n) + `"`
		}
		stylesheet := StylesheetURL(t.Style)
		if t.Stylesheet != "" {
			stylesheet = web.StaticURL(ctx, t.Stylesheet)
		}
		out := ""
		if s := t.modeScript(); s != "" {
			out += "<script" + nonce + ">" + s + "</script>\n"
		}
		out += `<link rel="stylesheet" href="` + html.EscapeString(stylesheet) + `">` + "\n"
		if css := t.css(); css != "" {
			out += "<style" + nonce + ">" + css + "</style>\n"
		}
		if _, err := io.WriteString(w, out); err != nil {
			return err
		}
		if err := web.Scripts().Render(ctx, w); err != nil {
			return err
		}
		_, err := io.WriteString(w, `<script defer`+nonce+` src="`+scriptURL()+`"></script>`+"\n"+
			`<script defer`+nonce+` src="`+glueURL()+`"></script>`+"\n")
		return err
	})
}
