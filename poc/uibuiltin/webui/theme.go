package webui

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/cockroachdb/errors"
)

// Styles 是 shadcn 的 8 套组件风格。
var Styles = []string{"vega", "nova", "lyra", "maia", "mira", "luma", "sera", "rhea"}

// Theme 是一个区域的外观，字段都可以写在 YAML 配置中：
//
//	theme:
//	  style: maia
//	  radius: 1rem
//	  vars: { primary: "oklch(0.55 0.22 275)" }
//
// 不同区域（运营后台、租户端）各用一个 Theme。
type Theme struct {
	// Style 是组件风格，见 Styles；默认 nova。
	Style string `yaml:"style"`
	// Mode 是颜色模式：light、dark 或 system（默认，跟随系统，用户切换后记在 localStorage）。
	Mode string `yaml:"mode"`
	// Radius 是圆角基准，例如 0.5rem。
	Radius string `yaml:"radius"`
	// Font 是正文字体，CSS font-family 的值。
	Font string `yaml:"font"`
	// Vars 覆盖亮色主题的变量，键不带 --，例如 primary、ring、sidebar。
	Vars map[string]string `yaml:"vars"`
	// DarkVars 覆盖暗色主题的变量。
	DarkVars map[string]string `yaml:"dark_vars"`
	// Stylesheet 是区域自己用 Tailwind 生成的样式表，静态文件名（web.StaticURL），例如 css/app.css；
	// 为空时使用框架为 Style 预编译的样式表。
	Stylesheet string `yaml:"stylesheet"`
}

type themeKey struct{}

// Use 返回把 t 设为区域主题的中间件，安装在区域的路由组上。Page、Head、ErrorPage 从 context 读取主题，
// 区域内的页面、错误页和 404 都使用它。框架化后为 web.Options.Theme。
func Use(t Theme) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), themeKey{}, t)))
		})
	}
}

// ThemeFrom 返回 Use 设置的主题，没有时为零值（nova、跟随系统）。
func ThemeFrom(ctx context.Context) Theme {
	t, _ := ctx.Value(themeKey{}).(Theme)
	return t
}

var (
	varName  = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	badValue = regexp.MustCompile(`[<>{};]`)
)

// Validate 检查配置，供 config 校验时调用。
func (t Theme) Validate() error {
	var errs []error
	if t.Style != "" && !slices.Contains(Styles, t.Style) {
		errs = append(errs, errors.Newf("style %q is not one of %v", t.Style, Styles))
	}
	if t.Mode != "" && t.Mode != "light" && t.Mode != "dark" && t.Mode != "system" {
		errs = append(errs, errors.Newf("mode %q is not light, dark or system", t.Mode))
	}
	for _, v := range []string{t.Radius, t.Font} {
		if badValue.MatchString(v) {
			errs = append(errs, errors.Newf("value %q contains <>{};", v))
		}
	}
	for _, m := range []map[string]string{t.Vars, t.DarkVars} {
		for k, v := range m {
			if !varName.MatchString(k) || badValue.MatchString(v) {
				errs = append(errs, errors.Newf("var %s: %q", k, v))
			}
		}
	}
	return errors.Join(errs...)
}

// Class 是 <html> 上的 class：选择组件风格，固定为暗色时加上 dark。
func (t Theme) Class() string {
	style := t.Style
	if style == "" {
		style = "nova"
	}
	if t.Mode == "dark" {
		return "style-" + style + " dark"
	}
	return "style-" + style
}

// css 返回覆盖默认主题的 CSS，没有覆盖时为空。值在 Validate 中检查过，这里再过滤一次，
// 保证不能闭合 <style>。
func (t Theme) css() string {
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
	block := func(sel string, vars map[string]string) {
		keys := slices.Sorted(maps.Keys(vars))
		keys = slices.DeleteFunc(keys, func(k string) bool { return !varName.MatchString(k) || badValue.MatchString(vars[k]) })
		if len(keys) == 0 {
			return
		}
		b.WriteString(sel + "{")
		for _, k := range keys {
			fmt.Fprintf(&b, "--%s:%s;", k, vars[k])
		}
		b.WriteString("}")
	}
	block(":root", light)
	block(".dark", t.DarkVars)
	return b.String()
}

// modeScript 在首次绘制前设置暗色模式，避免闪烁。
func (t Theme) modeScript() string {
	switch t.Mode {
	case "light", "dark":
		return ""
	}
	return `(function(){var t=null;try{t=localStorage.getItem("theme")}catch(e){}if(t==="dark"||(!t&&matchMedia("(prefers-color-scheme: dark)").matches))document.documentElement.classList.add("dark")})()`
}
