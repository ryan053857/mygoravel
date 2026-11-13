// Package local 把文件保存在本地目录，并以 http.Handler 公开访问。
package local

import (
	"context"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"

	"github.com/go-tenon/tenon/filesystem"
)

// Disk 是本地磁盘，实现 filesystem.Disk 和 http.Handler。
// 把它挂在 public_url 的路径上即可公开访问：mux.Handle("/storage/", disk)。
type Disk struct {
	root    *os.Root
	baseURL string
	prefix  string
	handler http.Handler
}

var _ filesystem.Disk = (*Disk)(nil)

// Open 按 filesystem.disks.local 配置创建并打开存储目录。
func Open(cfg filesystem.LocalConfig) (*Disk, error) {
	if cfg.IsZero() {
		return nil, errors.New("local: disk is not configured")
	}
	if err := cfg.Validate(); err != nil {
		return nil, errors.Wrap(err, "local")
	}
	u, err := url.Parse(strings.TrimSpace(cfg.PublicURL))
	if err != nil {
		return nil, errors.Wrap(err, "local")
	}
	if err := os.MkdirAll(cfg.Root, 0o750); err != nil {
		return nil, errors.Wrap(err, "local")
	}
	root, err := os.OpenRoot(cfg.Root)
	if err != nil {
		return nil, errors.Wrap(err, "local")
	}
	prefix := strings.TrimSuffix(u.Path, "/")
	d := &Disk{root: root, baseURL: strings.TrimSuffix(u.String(), "/"), prefix: prefix}
	d.handler = http.StripPrefix(prefix, http.FileServerFS(publicFS{root.FS()}))
	return d, nil
}

// Close 释放目录句柄。
func (d *Disk) Close() error { return d.root.Close() }

// URL 返回对象的公开地址。
func (d *Disk) URL(key string) string { return d.baseURL + "/" + escapeKey(key) }

// ServeHTTP 提供文件下载，不列目录，也不暴露写入中的临时文件。
//
// 文件和应用同源，类型由扩展名决定，而扩展名来自上传者提交的文件名。为了不让上传的文件在应用的源下执行脚本，
// 所有响应都带 X-Content-Type-Options: nosniff；HTML、SVG、XML 这类浏览器会执行脚本的类型，
// 还会加上 Content-Security-Policy: sandbox 并作为附件下载。<img> 中引用的 SVG 不受影响。
func (d *Disk) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.handler.ServeHTTP(&safeWriter{ResponseWriter: w}, r)
}

// activeTypes 是浏览器直接打开时可能执行脚本的类型。
var activeTypes = map[string]bool{
	"text/html": true, "application/xhtml+xml": true, "image/svg+xml": true,
	"text/xml": true, "application/xml": true,
}

// safeWriter 在写出响应头之前，按文件服务器确定的 Content-Type 补上安全响应头。
type safeWriter struct {
	http.ResponseWriter
	done bool
}

func (s *safeWriter) secure() {
	if s.done {
		return
	}
	s.done = true
	h := s.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	t, _, _ := strings.Cut(h.Get("Content-Type"), ";")
	if activeTypes[strings.ToLower(strings.TrimSpace(t))] {
		h.Set("Content-Security-Policy", "sandbox")
		h.Set("Content-Disposition", "attachment")
	}
}

func (s *safeWriter) WriteHeader(code int) {
	s.secure()
	s.ResponseWriter.WriteHeader(code)
}

func (s *safeWriter) Write(b []byte) (int, error) {
	s.secure()
	return s.ResponseWriter.Write(b)
}

func (s *safeWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// Put 写入同目录临时文件后原子重命名，失败时不发布部分内容。
func (d *Disk) Put(ctx context.Context, key, _ string, body io.Reader, _ int64) (err error) {
	if err := validKey(key); err != nil {
		return err
	}
	dir := path.Dir(key)
	if err := d.root.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp := path.Join(dir, ".tmp-"+uuid.NewString())
	f, err := d.root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = d.root.Remove(tmp)
		}
	}()
	if _, err = io.Copy(f, ctxReader{ctx, body}); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return d.root.Rename(tmp, key)
}

// Open 打开对象，调用方负责关闭。
func (d *Disk) Open(_ context.Context, key string) (io.ReadCloser, error) {
	if err := validKey(key); err != nil {
		return nil, err
	}
	return d.root.Open(key)
}

// Stat 返回对象大小。
func (d *Disk) Stat(_ context.Context, key string) (filesystem.Object, error) {
	if err := validKey(key); err != nil {
		return filesystem.Object{}, err
	}
	info, err := d.root.Stat(key)
	if err != nil {
		return filesystem.Object{}, err
	}
	if !info.Mode().IsRegular() {
		return filesystem.Object{}, fs.ErrNotExist
	}
	return filesystem.Object{Size: info.Size()}, nil
}

// Delete 删除对象，不存在视为成功。
func (d *Disk) Delete(_ context.Context, key string) error {
	if err := validKey(key); err != nil {
		return err
	}
	if err := d.root.Remove(key); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func validKey(key string) error {
	if !fs.ValidPath(key) || key == "." || strings.ContainsAny(key, "\\\x00") {
		return errors.Wrapf(filesystem.ErrInvalid, "invalid key %q", key)
	}
	return nil
}

func escapeKey(key string) string {
	parts := strings.Split(key, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// publicFS 只公开普通文件：目录和以点开头的文件（写入中的临时文件）都视为不存在。
type publicFS struct{ fs.FS }

func (p publicFS) Open(name string) (fs.File, error) {
	if strings.HasPrefix(path.Base(name), ".") && name != "." {
		return nil, fs.ErrNotExist
	}
	f, err := p.FS.Open(name)
	if err != nil {
		return nil, err
	}
	if info, err := f.Stat(); err != nil || info.IsDir() {
		_ = f.Close()
		return nil, fs.ErrNotExist
	}
	return f, nil
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
