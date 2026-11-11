package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/a-h/templ"

	"github.com/ryan053857/mygoravel/web"
)

// Class 返回 <html> 的 class：选择当前主题（web.ThemeFrom）的风格，Mode 为 dark 时加上 dark。
// 自己写 <html> 时使用：<html class={ ui.Class(ctx) }>。
func Class(ctx context.Context) string {
	t := web.ThemeFrom(ctx)
	style := t.Style
	if _, ok := stylesheets[style]; !ok {
		style = "nova"
	}
	if t.Mode == "dark" {
		return "style-" + style + " dark"
	}
	return "style-" + style
}

// Head 输出当前主题（web.ThemeFrom）的 <head> 内容：
//
//   - Mode 为 system 时，在首次绘制前按用户的选择或系统设置加上 dark，避免闪烁；
//   - 样式表：Theme.Stylesheet（web.StaticURL），为空时为 Theme.Style 预编译的样式表；
//   - 主题变量：Radius、Font、Vars、DarkVars；
//   - web.Scripts 的 htmx、Alpine，组件脚本和衔接脚本。
//
// 与 web.Scripts 一样支持 templ.WithNonce 设置的 CSP nonce。
func Head() templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		t := web.ThemeFrom(ctx)
		nonce := ""
		if n := templ.GetNonce(ctx); n != "" {
			nonce = ` nonce="` + html.EscapeString(n) + `"`
		}
		var b strings.Builder
		if t.Mode != "light" && t.Mode != "dark" {
			b.WriteString("<script" + nonce + ">" + modeScript + "</script>\n")
		}
		stylesheet := StylesheetURL(t.Style)
		if t.Stylesheet != "" {
			stylesheet = web.StaticURL(ctx, t.Stylesheet)
		}
		b.WriteString(`<link rel="stylesheet" href="` + html.EscapeString(stylesheet) + `">` + "\n")
		if css := themeCSS(t); css != "" {
			b.WriteString("<style" + nonce + ">" + css + "</style>\n")
		}
		if _, err := io.WriteString(w, b.String()); err != nil {
			return err
		}
		if err := web.Scripts().Render(ctx, w); err != nil {
			return err
		}
		extras := map[string]string{}
		for selector, file := range extraScripts {
			extras[selector] = AssetsPath + file
		}
		data, _ := json.Marshal(extras)
		_, err := fmt.Fprintf(w, "<script defer%s src=\"%s\"></script>\n<script defer%s src=\"%s\" data-extras=\"%s\"></script>\n",
			nonce, AssetsPath+scriptFile, nonce, AssetsPath+glueFile, html.EscapeString(string(data)))
		return err
	})
}

// modeScript 在用户选择了暗色，或没有选择而系统是暗色时，给 <html> 加上 dark。
const modeScript = `(function(){var t=null;try{t=localStorage.getItem("theme")}catch(e){}if(t==="dark"||(!t&&matchMedia("(prefers-color-scheme: dark)").matches))document.documentElement.classList.add("dark")})()`

// themeCSS 返回覆盖默认主题的 CSS，没有覆盖时为空。不合法的变量（见 web.ThemeVarValid）被忽略，
// 所以值不能闭合 <style>。
func themeCSS(t web.Theme) string {
	light := maps.Clone(t.Vars)
	if light == nil {
		light = map[string]string{}
	}
	if t.Radius != "" {
		light["radius"] = t.Radius
	}
	if t.Font != "" {
		light["font-sans"] = t.Font
	}
	var b strings.Builder
	block := func(selector string, vars map[string]string) {
		names := slices.DeleteFunc(slices.Sorted(maps.Keys(vars)), func(n string) bool { return !web.ThemeVarValid(n, vars[n]) })
		if len(names) == 0 {
			return
		}
		b.WriteString(selector + "{")
		for _, n := range names {
			b.WriteString("--" + n + ":" + vars[n] + ";")
		}
		b.WriteString("}")
	}
	block(":root", light)
	block(".dark", t.DarkVars)
	return b.String()
}
