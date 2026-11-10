package web

import (
	"context"
	"embed"
	"io"
	"net/http"

	"github.com/a-h/templ"
)

//go:embed assets
var assets embed.FS

// AssetsPrefix 是内嵌脚本的挂载路径。
const AssetsPrefix = "/_web/"

// Assets 提供内嵌的 htmx、hx-sse、hx-alpine-compat 与 Alpine.js。文件名带版本号，可以永久缓存。
func Assets() http.Handler {
	files := http.FileServerFS(assets)
	return http.StripPrefix(AssetsPrefix, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		r.URL.Path = "assets/" + r.URL.Path
		files.ServeHTTP(w, r)
	}))
}

// Scripts 输出加载顺序正确的 script 标签：htmx 与扩展先加载，Alpine 最后 defer 加载。
func Scripts() templ.Component {
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, `<script src="/_web/htmx-4.0.1.min.js"></script>
<script src="/_web/hx-sse-4.0.1.min.js"></script>
<script src="/_web/hx-alpine-compat-4.0.1.min.js"></script>
<script defer src="/_web/alpine-3.17.5.min.js"></script>
`)
		return err
	})
}
