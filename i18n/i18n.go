package i18n

import (
	"context"
	"embed"
	"io/fs"
	"path"
	"strings"

	"github.com/cockroachdb/errors"
	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"
	"go.yaml.in/yaml/v4"
	"golang.org/x/text/language"
)

// 内置语言标签。
const (
	LocaleZhCN = "zh-CN"
	LocaleEn   = "en"
)

//go:embed locales/*.yaml
var builtin embed.FS

// New 创建以 defaultLocale 为默认语言的消息目录。先加载框架内置的中英文消息，
// 再按顺序加载 sources 中的 YAML 翻译文件（文件名即语言标签，如 en.yaml）；
// 后加载的同名消息覆盖先加载的。返回的 Bundle 应在处理请求前完成全部修改。
func New(defaultLocale string, sources ...fs.FS) (*goi18n.Bundle, error) {
	tag, err := language.Parse(defaultLocale)
	if err != nil {
		return nil, errors.Wrapf(err, "i18n: default locale %q", defaultLocale)
	}
	b := goi18n.NewBundle(tag)
	b.RegisterUnmarshalFunc("yaml", yaml.Unmarshal)
	b.RegisterUnmarshalFunc("yml", yaml.Unmarshal)
	for _, src := range append([]fs.FS{builtin}, sources...) {
		if err := load(b, src); err != nil {
			return nil, err
		}
	}
	return b, nil
}

func load(b *goi18n.Bundle, src fs.FS) error {
	return fs.WalkDir(src, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if ext := strings.ToLower(path.Ext(p)); ext != ".yaml" && ext != ".yml" {
			return nil
		}
		data, err := fs.ReadFile(src, p)
		if err != nil {
			return err
		}
		if _, err := b.ParseMessageFileBytes(data, path.Base(p)); err != nil {
			return errors.Wrapf(err, "i18n: load %s", p)
		}
		return nil
	})
}

// MatchAcceptLanguage 返回 b 中与 Accept-Language 头最匹配的语言，匹配失败时返回默认语言。
func MatchAcceptLanguage(b *goi18n.Bundle, header string) string {
	supported := b.LanguageTags()
	tags, _, err := language.ParseAcceptLanguage(header)
	if err != nil || len(tags) == 0 {
		return supported[0].String()
	}
	_, index, confidence := language.NewMatcher(supported).Match(tags...)
	if confidence == language.No {
		return supported[0].String()
	}
	return supported[index].String()
}

type localeKey struct{}

type localeValue struct {
	locale    string
	localizer *goi18n.Localizer
}

// WithLocale 返回携带 locale 及其 Localizer 的 context。
func WithLocale(ctx context.Context, b *goi18n.Bundle, locale string) context.Context {
	return context.WithValue(ctx, localeKey{}, localeValue{locale, goi18n.NewLocalizer(b, locale)})
}

// Locale 返回 context 中的语言；未设置时返回空字符串。
func Locale(ctx context.Context) string {
	v, _ := ctx.Value(localeKey{}).(localeValue)
	return v.locale
}

// Localizer 返回 context 中的 go-i18n Localizer，用于复数等高级用法；未设置时返回 nil。
func Localizer(ctx context.Context) *goi18n.Localizer {
	v, _ := ctx.Value(localeKey{}).(localeValue)
	return v.localizer
}

// T 按 context 中的语言翻译消息 id，data 为模板数据（可为 nil）。
// 当前语言缺少该消息时使用默认语言的译文；context 未设置语言或默认语言也没有该消息时返回 id。
func T(ctx context.Context, id string, data any) string {
	l := Localizer(ctx)
	if l == nil {
		return id
	}
	if msg, _ := l.Localize(&goi18n.LocalizeConfig{MessageID: id, TemplateData: data}); msg != "" {
		return msg
	}
	return id
}
