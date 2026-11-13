package i18n_test

import (
	"context"
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/go-tenon/tenon/i18n"
)

func yamlFS(files map[string]string) fstest.MapFS {
	m := fstest.MapFS{}
	for name, data := range files {
		m[name] = &fstest.MapFile{Data: []byte(data)}
	}
	return m
}

func withLocale(t *testing.T, locale, defaultLocale string, sources ...fs.FS) context.Context {
	t.Helper()
	b, err := i18n.New(defaultLocale, sources...)
	if err != nil {
		t.Fatal(err)
	}
	return i18n.WithLocale(context.Background(), b, locale)
}

func TestBuiltinMessages(t *testing.T) {
	for locale, want := range map[string]string{"en": "Authentication required", "zh-CN": "请先认证"} {
		ctx := withLocale(t, locale, i18n.LocaleZhCN)
		if got := i18n.T(ctx, "auth.unauthenticated", nil); got != want {
			t.Fatalf("%s: got %q want %q", locale, got, want)
		}
	}
}

func TestLaterSourcesOverrideEarlier(t *testing.T) {
	app := yamlFS(map[string]string{"zh-CN.yaml": "auth.unauthenticated: 请登录\ngreet: 你好A\n"})
	patch := yamlFS(map[string]string{"zh-CN.yaml": "greet: 你好B\n"})
	ctx := withLocale(t, "zh-CN", "zh-CN", app, patch)
	if got := i18n.T(ctx, "auth.unauthenticated", nil); got != "请登录" {
		t.Fatalf("app should override builtin, got %q", got)
	}
	if got := i18n.T(ctx, "greet", nil); got != "你好B" {
		t.Fatalf("later source should win, got %q", got)
	}
	if got := i18n.T(ctx, "auth.forbidden", nil); got != "没有访问权限" {
		t.Fatalf("untouched builtin message lost, got %q", got)
	}
}

func TestBundlesAreIndependent(t *testing.T) {
	first := withLocale(t, "zh-CN", "zh-CN", yamlFS(map[string]string{"zh-CN.yaml": "only: x\n"}))
	second := withLocale(t, "zh-CN", "zh-CN")
	if got := i18n.T(first, "only", nil); got != "x" {
		t.Fatal(got)
	}
	if got := i18n.T(second, "only", nil); got != "only" {
		t.Fatalf("second bundle should not see first bundle messages, got %q", got)
	}
}

func TestTTemplateAndFallbacks(t *testing.T) {
	app := yamlFS(map[string]string{
		"en.yaml":    "greet: Hello {{.Name}}\nen_only: English only\n",
		"zh-CN.yaml": "greet: 你好 {{.Name}}\n",
	})
	zh := withLocale(t, "zh-CN", "en", app)
	if got := i18n.T(zh, "greet", map[string]any{"Name": "Ada"}); got != "你好 Ada" {
		t.Fatal(got)
	}
	if got := i18n.T(zh, "en_only", nil); got != "English only" {
		t.Fatalf("missing message should fall back to default locale, got %q", got)
	}
	if got := i18n.T(zh, "nowhere", nil); got != "nowhere" {
		t.Fatalf("unknown message should return id, got %q", got)
	}
	if got := i18n.T(context.Background(), "greet", nil); got != "greet" {
		t.Fatalf("context without locale should return id, got %q", got)
	}
}

func TestContextAccessors(t *testing.T) {
	ctx := withLocale(t, "en", "zh-CN")
	if i18n.Locale(ctx) != "en" || i18n.Localizer(ctx) == nil {
		t.Fatalf("locale=%q", i18n.Locale(ctx))
	}
	if i18n.Locale(context.Background()) != "" || i18n.Localizer(context.Background()) != nil {
		t.Fatal("empty context should carry no locale")
	}
}

func TestInvalidInput(t *testing.T) {
	if _, err := i18n.New(""); err == nil {
		t.Fatal("empty default locale should fail")
	}
	if _, err := i18n.New("en", yamlFS(map[string]string{"en.yaml": "a: [\n"})); err == nil {
		t.Fatal("malformed yaml should fail")
	}
}

func TestMatch(t *testing.T) {
	bundle, err := i18n.New(i18n.LocaleZhCN)
	if err != nil {
		t.Fatal(err)
	}
	for header, want := range map[string]string{
		"en-US": "en", "en-US,en;q=0.9": "en", "en-GB": "en", "zh-Hans": "zh-CN",
		"fr": "zh-CN", "": "zh-CN", "???": "zh-CN",
	} {
		if got := i18n.Match(bundle, header); got != want {
			t.Fatalf("%q: got %q want %q", header, got, want)
		}
	}
	en, err := i18n.New(i18n.LocaleEn)
	if err != nil {
		t.Fatal(err)
	}
	if got := i18n.Match(en, "fr"); got != "en" {
		t.Fatal(got)
	}
	if got := i18n.Match(en); got != "en" {
		t.Fatalf("no preferences should use default, got %q", got)
	}
	if got := i18n.Match(bundle, "fr", "en;q=0.5"); got != "en" {
		t.Fatalf("multiple values should form one list, got %q", got)
	}
}
