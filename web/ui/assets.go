package ui

import (
	"bytes"
	"compress/gzip"
	"embed"
	"io"
	"mime"
	"net/http"
	"path"
	"runtime/debug"
	"strings"
	"time"

	"github.com/ryan053857/mygoravel/httpx"
)

//go:embed assets
var assets embed.FS

// AssetsPath 是 ui 资源的挂载路径：router.Handle(ui.AssetsPath+"*", ui.Assets())。
const AssetsPath = "/_web/ui/"

const shadcnTemplModule = "github.com/axadrn/shadcn-templ/v2"

// Assets 提供预编译的样式表、组件脚本和衔接脚本。文件名带内容哈希，响应永久缓存；
// 文件以 gzip 压缩保存，不接受 gzip 的客户端收到解压后的内容。
//
// 程序中的 shadcn-templ 与资源生成时的版本（ShadcnTemplVersion）不同时 panic：
// 组件的 HTML 与样式、脚本不配套。应用不要在自己的 go.mod 中升级 shadcn-templ，随框架升级。
func Assets() http.Handler {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range info.Deps {
			if dep.Path == shadcnTemplModule && dep.Replace == nil && dep.Version != ShadcnTemplVersion {
				panic("ui: the program uses " + shadcnTemplModule + " " + dep.Version + ", but the assets were generated for " +
					ShadcnTemplVersion + "; use the version required by mygoravel")
			}
		}
	}
	return assetsHandler{}
}

type assetsHandler struct{}

func (assetsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	name, ok := strings.CutPrefix(r.URL.Path, AssetsPath)
	if !ok || strings.Contains(name, "/") {
		http.NotFound(w, r)
		return
	}
	data, err := assets.ReadFile("assets/" + name + ".gz")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", mime.TypeByExtension(path.Ext(name)))
	h.Set("Cache-Control", "public, max-age=31536000, immutable")
	h.Add("Vary", "Accept-Encoding")
	if acceptsGzip(r) {
		h.Set("Content-Encoding", "gzip")
	} else if data, err = gunzip(data); err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}

// DescribeRoute 实现 httpx.RouteDescriber：资源是公开路由。
func (assetsHandler) DescribeRoute() httpx.Route { return httpx.Route{Access: httpx.AccessPublic} }

func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		enc, q, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.EqualFold(strings.TrimSpace(enc), "gzip") && strings.ReplaceAll(strings.TrimSpace(q), " ", "") != "q=0" {
			return true
		}
	}
	return false
}

func gunzip(data []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	return io.ReadAll(zr)
}

// StylesheetURL 返回 style 风格预编译的样式表：全部组件、该风格、默认主题和常用工具类。
// style 为空或不是 web.Styles 之一时为 nova。
func StylesheetURL(style string) string {
	if f, ok := stylesheets[style]; ok {
		return AssetsPath + f
	}
	return AssetsPath + stylesheets["nova"]
}
