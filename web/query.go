package web

import (
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/go-tenon/tenon/errorx"
	"github.com/go-tenon/tenon/form"
)

// ErrBadQuery 表示 URL 查询参数不符合规则，例如 ?page=0。公开字段 field、message 是第一个出错的参数及其提示。
var ErrBadQuery = errorx.Define(errorx.InvalidArgument, "web.bad_query")

// ParseQuery 把请求的 URL 查询参数解码为 T，用于列表页的筛选、排序和分页。
//
// 规则与 ParseForm 相同：字段名取 json 标签，按 Huma 标签验证，转换规则见 form.Decode。
// 不同的是，查询参数都是可选的：
//
//   - 字段要带 omitempty，例如 `json:"q,omitempty"`；
//   - 空值等同于没有这个参数，所以 ?status= 与不带 status 相同；
//   - 没有的参数取 default 标签的值。
//
// 可以嵌入 page.OffsetParams 得到 page、per_page 两个参数。验证失败时返回 ErrBadQuery，
// 页面呈现为 422 错误。
func ParseQuery[T any](r *http.Request) (T, error) {
	values := url.Values{}
	for name, vs := range r.URL.Query() {
		if vs = slices.DeleteFunc(vs, func(v string) bool { return v == "" }); len(vs) > 0 {
			values[name] = vs
		}
	}
	for name, prop := range form.Schema(reflect.TypeFor[T]()).Properties {
		if _, ok := values[name]; !ok && prop.Default != nil {
			values.Set(name, fmt.Sprint(prop.Default))
		}
	}
	data, errs := form.Decode[T](r.Context(), values)
	if len(errs) > 0 {
		fields := make([]string, 0, len(errs))
		for f := range errs {
			fields = append(fields, f)
		}
		slices.Sort(fields)
		return data, ErrBadQuery.New(errorx.Public("field", fields[0]), errorx.Public("message", errs[fields[0]]))
	}
	return data, nil
}

// QueryURL 返回 path 加上 q 编码成的查询参数，是 ParseQuery 的逆操作：
// 参数名取 json 标签，按字段顺序排列；零值和等于 default 标签的值省略，因此同一个状态总是得到同一个地址。
// 嵌入的结构体（例如 page.OffsetParams）展开为同一层参数。q 不是结构体时 panic。
//
// 列表页的链接由它生成：
//
//	func (q ArticleQuery) URL() string { return web.QueryURL("/admin/articles", q) }
//	func (q ArticleQuery) SortBy(s string) ArticleQuery { q.Sort, q.Page = s, 1; return q }
func QueryURL(path string, q any) string {
	v := reflect.ValueOf(q)
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		panic(fmt.Sprintf("web: QueryURL needs a struct, got %T", q))
	}
	var b strings.Builder
	encodeQuery(&b, v)
	if b.Len() == 0 {
		return path
	}
	return path + "?" + b.String()
}

func encodeQuery(b *strings.Builder, v reflect.Value) {
	t := v.Type()
	for i := range t.NumField() {
		f, fv := t.Field(i), v.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "-" {
			continue
		}
		if f.Anonymous && name == "" && fv.Kind() == reflect.Struct {
			encodeQuery(b, fv)
			continue
		}
		if name == "" {
			name = f.Name
		}
		var texts []string
		if fv.Kind() == reflect.Slice {
			for j := range fv.Len() {
				texts = append(texts, queryText(fv.Index(j)))
			}
		} else if !fv.IsZero() {
			if s := queryText(fv); s != f.Tag.Get("default") {
				texts = append(texts, s)
			}
		}
		for _, s := range texts {
			if b.Len() > 0 {
				b.WriteByte('&')
			}
			b.WriteString(url.QueryEscape(name) + "=" + url.QueryEscape(s))
		}
	}
}

func queryText(v reflect.Value) string {
	switch v.Kind() {
	case reflect.String:
		return v.String()
	case reflect.Bool:
		return strconv.FormatBool(v.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(v.Uint(), 10)
	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(v.Float(), 'f', -1, 64)
	default:
		return fmt.Sprint(v.Interface())
	}
}

// PushURL 让 htmx 在处理完这个响应后，把浏览器地址栏改为 url 并记入历史，后退时回到之前的地址。
// 列表页的局部响应用它写入规范地址（QueryURL），元素上就不需要 hx-push-url：
// 筛选表单提交时会带上空字段（?q=&status=），由服务端推送的地址不含这些参数。必须在写出响应之前调用。
func PushURL(w http.ResponseWriter, url string) {
	w.Header().Set("HX-Push-Url", url)
}
