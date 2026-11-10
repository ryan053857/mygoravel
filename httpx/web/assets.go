package web

import (
	"context"
	"embed"
	"io"
	"net/http"
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
	return http.StripPrefix(AssetsPath, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		r.URL.Path = "assets/" + r.URL.Path
		files.ServeHTTP(w, r)
	}))
}

var scripts = `<script src="` + AssetsPath + `htmx-` + HTMXVersion + `.min.js"></script>
<script src="` + AssetsPath + `hx-sse-` + HTMXVersion + `.min.js"></script>
<script src="` + AssetsPath + `hx-alpine-compat-` + HTMXVersion + `.min.js"></script>
<script defer src="` + AssetsPath + `alpine-` + AlpineVersion + `.min.js"></script>
`

// Scripts 输出加载内嵌脚本的 script 标签，放在布局的 head 中：htmx 与扩展先加载，Alpine 最后以 defer 加载。
func Scripts() Component {
	return ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, scripts)
		return err
	})
}
