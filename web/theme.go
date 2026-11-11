package web

import (
	"context"
	"regexp"
	"slices"

	"github.com/cockroachdb/errors"
)

// Styles 是 web/ui 内置的组件风格，即 shadcn 的 8 套风格。
var Styles = []string{"vega", "nova", "lyra", "maia", "mira", "luma", "sera", "rhea"}

// Theme 是一组页面（一个 Pages）的外观，由 web/ui 渲染。字段都可以写在 YAML 配置中：
//
//	theme:
//	  style: maia
//	  radius: 1rem
//	  vars:
//	    primary: "oklch(0.55 0.22 275)"
//
// 同一个应用的运营后台、租户端等区域各用一个 Pages，各有自己的 Theme。
type Theme struct {
	// Style 是组件风格，见 Styles；为空时为 nova。
	Style string `yaml:"style"`
	// Mode 是颜色模式：light、dark，或 system（默认）：跟随系统，用户切换后记在浏览器中。
	Mode string `yaml:"mode"`
	// Radius 是圆角的基准，例如 0.5rem。
	Radius string `yaml:"radius"`
	// Font 是正文字体，CSS font-family 的值。字体文件由应用作为静态文件提供。
	Font string `yaml:"font"`
	// Vars 覆盖亮色主题的 CSS 变量，键不带 --，例如 primary、primary-foreground、ring。
	Vars map[string]string `yaml:"vars"`
	// DarkVars 覆盖暗色主题的 CSS 变量。
	DarkVars map[string]string `yaml:"dark_vars"`
	// Stylesheet 是用 Tailwind 生成的样式表，Options.Static 中的文件名，例如 css/app.css；
	// 为空时使用 web/ui 为 Style 预编译的样式表。
	Stylesheet string `yaml:"stylesheet"`
}

var (
	themeVarName  = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	themeBadValue = regexp.MustCompile(`[<>{};]`)
)

// Validate 检查主题，在配置校验中调用。值不能包含 <、>、{、}、;，它们会被写进页面的 <style>。
func (t Theme) Validate() error {
	var errs []error
	if t.Style != "" && !slices.Contains(Styles, t.Style) {
		errs = append(errs, errors.Newf("style %q is not one of %v", t.Style, Styles))
	}
	if t.Mode != "" && t.Mode != "light" && t.Mode != "dark" && t.Mode != "system" {
		errs = append(errs, errors.Newf("mode %q is not light, dark or system", t.Mode))
	}
	if themeBadValue.MatchString(t.Radius) {
		errs = append(errs, errors.Newf("radius %q contains <>{};", t.Radius))
	}
	if themeBadValue.MatchString(t.Font) {
		errs = append(errs, errors.Newf("font %q contains <>{};", t.Font))
	}
	for _, vars := range []map[string]string{t.Vars, t.DarkVars} {
		for name, value := range vars {
			if !ThemeVarValid(name, value) {
				errs = append(errs, errors.Newf("var %s: %q is not a valid CSS variable", name, value))
			}
		}
	}
	return errors.Join(errs...)
}

// ThemeVarValid 报告 name、value 能否作为 CSS 变量写进 <style>：
// name 是小写字母、数字和连字符，value 不含 <、>、{、}、;。
func ThemeVarValid(name, value string) bool {
	return themeVarName.MatchString(name) && !themeBadValue.MatchString(value)
}

type themeKey struct{}

// ThemeFrom 返回当前页面路由的 Options.Theme；不在页面路由中时为零值。
func ThemeFrom(ctx context.Context) Theme {
	t, _ := ctx.Value(themeKey{}).(Theme)
	return t
}
