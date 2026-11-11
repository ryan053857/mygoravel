package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/cockroachdb/errors"

	"github.com/ryan053857/mygoravel/httpx"
)

// StaticPath 是应用静态资源的挂载路径：router.Handle(web.StaticPath+"*", static)。
const StaticPath = "/static/"

// StaticOptions 配置 NewStatic。
type StaticOptions struct {
	// DevDir 非空时改为直接读取这个目录（开发模式）：地址不带指纹，响应不缓存，
	// 修改文件后刷新页面即可看到，不需要重新编译。生产环境留空，使用 NewStatic 的 fsys。
	DevDir string
}

// Static 托管应用自己的静态资源：样式表、脚本、字体、图片。可以并发使用。
//
// 生产模式下，创建时读取全部文件并按内容计算指纹，StaticURL 返回带指纹的地址
// （css/app.css → /static/css/app.3f2a9c1d.css），这种地址的响应可以永久缓存；
// 内容变化后指纹随之变化，浏览器自然取到新文件。文件名本身已带内容哈希的
// （打包工具生成的，例如 shadcn-templ-86c5f2802d0bc25d.js）同样永久缓存。
// 不带指纹的原始地址仍然可以访问，响应带 ETag，浏览器每次协商。
type Static struct {
	fsys fs.FS
	dev  bool
	// files 按文件名记录指纹与 ETag；prints 由带指纹的文件名找回文件名。
	files  map[string]staticFile
	prints map[string]string
}

type staticFile struct {
	print string // 带指纹的文件名；文件名本身已带哈希时为空
	etag  string
}

// NewStatic 创建 Static，fsys 通常是 embed.FS 的子目录（fs.Sub）。
// 生产模式下读取 fsys 中的全部文件计算指纹，读取失败时返回错误。
func NewStatic(fsys fs.FS, opts StaticOptions) (*Static, error) {
	if opts.DevDir != "" {
		return &Static{fsys: os.DirFS(opts.DevDir), dev: true}, nil
	}
	if fsys == nil {
		return nil, errors.New("web: nil static fs")
	}
	s := &Static{fsys: fsys, files: map[string]staticFile{}, prints: map[string]string{}}
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		hash := hex.EncodeToString(sum[:])
		f := staticFile{etag: `"` + hash[:16] + `"`}
		if !contentHashed(name) {
			ext := path.Ext(name)
			f.print = strings.TrimSuffix(name, ext) + "." + hash[:8] + ext
			s.prints[f.print] = name
		}
		s.files[name] = f
		return nil
	})
	if err != nil {
		return nil, errors.Wrap(err, "web: read static files")
	}
	return s, nil
}

// hashedName 匹配打包工具生成的、已带内容哈希的文件名：-或. 后跟至少 16 位十六进制，再接扩展名。
var hashedName = regexp.MustCompile(`[-.][0-9a-f]{16,}\.[0-9A-Za-z]+$`)

func contentHashed(name string) bool { return hashedName.MatchString(name) }

// URL 返回文件 name（相对 fsys 的路径，例如 css/app.css）的地址。生产模式下带指纹；
// 开发模式、文件不存在或文件名已带哈希时，返回 StaticPath 加 name。
func (s *Static) URL(name string) string {
	if f, ok := s.files[name]; ok && f.print != "" {
		return StaticPath + f.print
	}
	return StaticPath + name
}

// ServeHTTP 提供文件。带指纹或文件名已带哈希的地址永久缓存，其余地址按 ETag 协商；
// 开发模式不缓存。目录和不存在的文件返回 404。
func (s *Static) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	name, ok := strings.CutPrefix(r.URL.Path, StaticPath)
	if !ok || !fs.ValidPath(name) || name == "." {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	if s.dev {
		h.Set("Cache-Control", "no-store")
	} else {
		cache := "no-cache"
		if file, ok := s.prints[name]; ok {
			name, cache = file, "public, max-age=31536000, immutable"
		}
		f, ok := s.files[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if contentHashed(name) {
			cache = "public, max-age=31536000, immutable"
		}
		h.Set("Cache-Control", cache)
		h.Set("ETag", f.etag)
	}
	content, err := open(s.fsys, name)
	if err != nil {
		h.Del("Cache-Control")
		h.Del("ETag")
		http.NotFound(w, r)
		return
	}
	defer content.Close()
	http.ServeContent(w, r, name, time.Time{}, content)
}

// open 打开普通文件并返回可以 Seek 的内容；目录视为不存在。
func open(fsys fs.FS, name string) (io.ReadSeekCloser, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	if st, err := f.Stat(); err != nil || st.IsDir() {
		f.Close()
		return nil, fs.ErrNotExist
	}
	if rs, ok := f.(io.ReadSeekCloser); ok {
		return rs, nil
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	return nopCloser{bytes.NewReader(b)}, nil
}

type nopCloser struct{ *bytes.Reader }

func (nopCloser) Close() error { return nil }

// DescribeRoute 实现 httpx.RouteDescriber：静态资源是公开路由。
func (s *Static) DescribeRoute() httpx.Route { return httpx.Route{Access: httpx.AccessPublic} }

type staticKey struct{}

// withStatic 把 s 放进 ctx，供 StaticURL 使用。
func withStatic(ctx context.Context, s *Static) context.Context {
	if s == nil {
		return ctx
	}
	return context.WithValue(ctx, staticKey{}, s)
}

// StaticURL 在 templ 组件中取静态资源的地址，见 Static.URL：
//
//	<link rel="stylesheet" href={ web.StaticURL(ctx, "css/app.css") }/>
//
// 页面路由与错误页的 ctx 中带有 Options.Static；没有设置时返回 StaticPath 加 name。
func StaticURL(ctx context.Context, name string) string {
	if s, ok := ctx.Value(staticKey{}).(*Static); ok {
		return s.URL(name)
	}
	return StaticPath + name
}
