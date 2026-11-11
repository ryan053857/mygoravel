// Package static 是「应用静态资源」的原型：托管应用自己的 CSS、JS、字体和图片。
//
// 生产模式按内容哈希给文件加指纹：URL("css/app.css") 返回 /assets/css/app.3f2a9c1d.css，
// 带指纹的地址永久缓存；文件名本身已经带内容哈希的（例如 shadcn-templ 打包出的
// shadcn-templ-<16 位十六进制>.js）也按永久缓存处理。其余地址用 ETag 协商缓存。
//
// 开发模式直接读磁盘，URL 不加指纹，响应 no-store，修改后刷新即可看到。
package static

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
)

// Assets 托管 fsys 中的文件，挂载在 Prefix 下。
type Assets struct {
	prefix string
	fsys   fs.FS
	dev    bool
	// 逻辑名 → 带指纹的名字；带指纹的名字 → 逻辑名；逻辑名 → ETag。
	hashed  map[string]string
	logical map[string]string
	etags   map[string]string
}

// New 创建 Assets。dev 为 true 时不计算指纹、不缓存。
func New(prefix string, fsys fs.FS, dev bool) (*Assets, error) {
	a := &Assets{prefix: strings.TrimSuffix(prefix, "/") + "/", fsys: fsys, dev: dev,
		hashed: map[string]string{}, logical: map[string]string{}, etags: map[string]string{}}
	if dev {
		return a, nil
	}
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		h := hex.EncodeToString(sum[:])
		a.etags[name] = `"` + h[:16] + `"`
		if alreadyHashed(name) {
			return nil
		}
		ext := path.Ext(name)
		fp := strings.TrimSuffix(name, ext) + "." + h[:8] + ext
		a.hashed[name], a.logical[fp] = fp, name
		return nil
	})
	return a, err
}

var hashedName = regexp.MustCompile(`[-.][0-9a-f]{16,}\.[a-z0-9]+$`)

func alreadyHashed(name string) bool { return hashedName.MatchString(name) }

// URL 返回 name（相对 fsys 的路径）的地址；生产模式带指纹。不存在的文件原样返回，不报错。
func (a *Assets) URL(name string) string {
	if fp, ok := a.hashed[name]; ok {
		return a.prefix + fp
	}
	return a.prefix + name
}

// ServeHTTP 提供文件。挂载：router.Handle(prefix+"*", assets)。
func (a *Assets) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, a.prefix)
	h := w.Header()
	switch {
	case a.dev:
		h.Set("Cache-Control", "no-store")
	case a.logical[name] != "":
		name = a.logical[name]
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	case alreadyHashed(name):
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	default:
		h.Set("Cache-Control", "no-cache")
	}
	if etag := a.etags[name]; etag != "" {
		h.Set("ETag", etag)
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/" + name
	http.FileServerFS(a.fsys).ServeHTTP(w, r2)
}

type ctxKey struct{}

// Middleware 把 Assets 放进请求的 context，templ 组件用 static.URL(ctx, name) 取地址。
func (a *Assets) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, a)))
	})
}

// URL 在 templ 组件中取静态资源地址。
func URL(ctx context.Context, name string) string {
	if a, ok := ctx.Value(ctxKey{}).(*Assets); ok {
		return a.URL(name)
	}
	return "/assets/" + name
}
