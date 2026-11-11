package web

import (
	"context"
	"embed"
	"encoding/json"
	"html"
	"io"
	"net/http"

	"github.com/a-h/templ"

	"github.com/ryan053857/mygoravel/httpx"
)

//go:embed assets
var assets embed.FS

// AssetsPath 是内嵌脚本的挂载路径：router.Handle(web.AssetsPath+"*", web.Assets())。
const AssetsPath = "/_web/"

// 内嵌脚本的版本，体现在文件名中。
const (
	HTMXVersion   = "4.0.1"
	AlpineVersion = "3.17.5"
)

// Assets 提供内嵌的 htmx、hx-sse、hx-alpine-compat 与 Alpine.js（许可见 LICENSES.md）。
// 文件名带版本号，响应可以永久缓存。
func Assets() http.Handler {
	files := http.FileServerFS(assets)
	return assetsHandler{http.StripPrefix(AssetsPath, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		r.URL.Path = "assets/" + r.URL.Path
		files.ServeHTTP(w, r)
	}))}
}

// assetsHandler 在路由表中显示为公开路由。
type assetsHandler struct{ http.Handler }

// DescribeRoute 实现 httpx.RouteDescriber。
func (assetsHandler) DescribeRoute() httpx.Route { return httpx.Route{Access: httpx.AccessPublic} }

// Scripts 输出加载内嵌脚本的 script 标签，放在布局的 head 中：htmx 与扩展先加载，Alpine 最后以 defer 加载。
//
// ctx 中有 templ.WithNonce 设置的 nonce 时，配合内容安全策略（CSP）使用：每个标签都带上 nonce，
// 并把 htmx.config.inlineScriptNonce 设为它。htmx 执行交换进来的 <script> 时（包括后退时恢复的整页）
// 用这个 nonce，否则这些脚本会被 CSP 拦截。
func Scripts() templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		attr := ""
		nonce := templ.GetNonce(ctx)
		if nonce != "" {
			attr = ` nonce="` + html.EscapeString(nonce) + `"`
		}
		for _, s := range []struct{ file, extra string }{
			{"htmx-" + HTMXVersion + ".min.js", ""},
			{"hx-sse-" + HTMXVersion + ".min.js", ""},
			{"hx-alpine-compat-" + HTMXVersion + ".min.js", ""},
			{"alpine-" + AlpineVersion + ".min.js", " defer"},
		} {
			if _, err := io.WriteString(w, "<script"+s.extra+attr+` src="`+AssetsPath+s.file+`"></script>`+"\n"); err != nil {
				return err
			}
		}
		if nonce != "" {
			js, _ := json.Marshal(nonce) // json 转义 <、>、&，可以放进 <script>
			if _, err := io.WriteString(w, "<script"+attr+">htmx.config.inlineScriptNonce = "+string(js)+"</script>\n"); err != nil {
				return err
			}
		}
		return nil
	})
}
