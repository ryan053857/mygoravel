package rest

import (
	"reflect"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// bare 是类型名不加包名的模块：框架自身（例如 ErrorModel、page.Offset）和 Huma（例如 ErrorDetail）。
// 它们的类型名由框架保证不冲突。
var bare = []string{
	strings.TrimSuffix(reflect.TypeOf(ErrorModel{}).PkgPath(), "/httpx/rest"),
	"github.com/danielgtaylor/huma/v2",
}

// schemaName 生成 OpenAPI 中的 schema 名，也是客户端生成器生成的类型名。
//
// 名称是包名加类型名，例如 article.Changes 为 ArticleChanges、comment.Changes 为 CommentChanges，
// 不同包的同名类型因此不会冲突；类型名已经以包名开头时不重复，例如 article.ArticleDTO 仍为 ArticleDTO。
// 泛型实参按同样的规则拼接，例如 page.Offset[article.ArticleDTO] 为 OffsetArticleDTO。
// main 包、框架和 Huma 的类型只用类型名；匿名类型使用 Huma 给出的 hint，例如请求体为 PublishArticleRequest。
func schemaName(t reflect.Type, hint string) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	name := t.Name()
	if name == "" {
		return upperFirst(hint)
	}
	if pkg := t.PkgPath(); pkg != "" {
		name = pkg + "." + name
	}
	// 与 Huma 的默认命名一致：[]int 写作 ListInt。
	name = strings.ReplaceAll(name, "[]", "List[")
	var b strings.Builder
	for _, part := range strings.FieldsFunc(name, func(r rune) bool {
		return r == '[' || r == ']' || r == '*' || r == ','
	}) {
		b.WriteString(qualify(part))
	}
	return b.String()
}

// qualify 把 "github.com/x/article.Changes" 这样的完整名称转换为 ArticleChanges。
func qualify(part string) string {
	dot := strings.LastIndex(part, ".")
	base := upperFirst(part[dot+1:])
	if dot < 0 {
		return base // 内置类型，例如 int。
	}
	path := part[:dot]
	if path == "main" || slices.ContainsFunc(bare, func(m string) bool { return path == m || strings.HasPrefix(path, m+"/") }) {
		return base
	}
	pkg := upperFirst(pkgName(path))
	if pkg == "" || strings.HasPrefix(strings.ToLower(base), strings.ToLower(pkg)) {
		return base
	}
	return pkg + base
}

var majorVersion = regexp.MustCompile(`^v[0-9]+$`)

// pkgName 按导入路径推断包名：取最后一段，跳过 v2 这样的主版本后缀和外部测试包的 _test 后缀，去掉非字母数字字符。
func pkgName(path string) string {
	path = strings.TrimSuffix(path, "_test")
	segs := strings.Split(path, "/")
	last := segs[len(segs)-1]
	if majorVersion.MatchString(last) && len(segs) > 1 {
		last = segs[len(segs)-2]
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return -1
	}, last)
}

func upperFirst(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}
