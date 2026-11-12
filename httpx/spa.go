package httpx

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
)

// SPAOptions 配置 NewSPA。
type SPAOptions struct {
	// Base 是单页应用挂载的路径，以 / 开头和结尾，与 Vite 的 base 相同；默认 /。
	Base string
	// DevServer 非空时（例如 http://localhost:5173）把全部请求转发给前端的开发服务器，
	// 包括热更新的 WebSocket，不读取 fsys。只在开发时设置。
	DevServer string
}

// SPA 提供打包进二进制的单页应用，例如 Vite 的构建产物 dist。可以并发使用。
//
// 存在的文件原样返回：assets/ 下的文件名带内容哈希，永久缓存；index.html 等其余文件按 ETag 协商。
// 不存在的地址是前端路由，浏览器打开页面（Accept 含 text/html）时返回 index.html，
// 其余请求（脚本、图片、fetch）和 assets/ 下缺失的文件返回 404，不会把页面当作脚本返回。
type SPA struct {
	base  string
	files map[string]spaFile
	proxy *httputil.ReverseProxy
}

type spaFile struct {
	data []byte
	etag string
}

// spaAssets 是 Vite 默认的 assetsDir：其中的文件名带内容哈希。
const spaAssets = "assets/"

// NewSPA 创建 SPA。fsys 是构建产物所在的目录，通常是 embed.FS 的子目录（fs.Sub），
// 必须包含 index.html；创建时读取全部文件。
func NewSPA(fsys fs.FS, opts SPAOptions) (*SPA, error) {
	base := opts.Base
	if base == "" {
		base = "/"
	}
	if !strings.HasPrefix(base, "/") || !strings.HasSuffix(base, "/") {
		return nil, errors.Newf("httpx: SPA base %q must start and end with /", base)
	}
	s := &SPA{base: base}
	if opts.DevServer != "" {
		target, err := url.Parse(opts.DevServer)
		if err != nil || target.Scheme == "" || target.Host == "" {
			return nil, errors.Newf("httpx: invalid SPA dev server %q", opts.DevServer)
		}
		s.proxy = &httputil.ReverseProxy{
			Rewrite: func(r *httputil.ProxyRequest) {
				r.SetURL(target)
				r.SetXForwarded()
			},
			ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
				http.Error(w, fmt.Sprintf("SPA dev server %s: %v", opts.DevServer, err), http.StatusBadGateway)
			},
		}
		return s, nil
	}
	if fsys == nil {
		return nil, errors.New("httpx: nil SPA fs")
	}
	s.files = map[string]spaFile{}
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		s.files[name] = spaFile{data: b, etag: `"` + hex.EncodeToString(sum[:8]) + `"`}
		return nil
	})
	if err != nil {
		return nil, errors.Wrap(err, "httpx: read SPA files")
	}
	if _, ok := s.files["index.html"]; !ok {
		return nil, errors.New("httpx: SPA fs has no index.html; build the frontend first")
	}
	return s, nil
}

// ServeHTTP 提供文件，或为前端路由返回 index.html；开发模式下转发给 DevServer。
func (s *SPA) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.proxy != nil {
		s.proxy.ServeHTTP(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	name, ok := strings.CutPrefix(r.URL.Path, s.base)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if name == "" {
		name = "index.html"
	}
	f, ok := s.files[name]
	if !ok {
		// 前端路由：浏览器打开页面时返回 index.html；其余请求是缺失的资源或写错的接口。
		w.Header().Add("Vary", "Accept")
		if strings.HasPrefix(name, spaAssets) || !strings.Contains(r.Header.Get("Accept"), "text/html") {
			http.NotFound(w, r)
			return
		}
		name, f = "index.html", s.files["index.html"]
	}
	h := w.Header()
	if strings.HasPrefix(name, spaAssets) {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "no-cache")
	}
	h.Set("ETag", f.etag)
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(f.data))
}

// DescribeRoute 实现 RouteDescriber：单页应用的页面和文件是公开路由，登录由前端和 API 处理。
func (s *SPA) DescribeRoute() Route { return Route{Access: AccessPublic} }
