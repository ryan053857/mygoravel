package docs

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"strings"

	"github.com/a-h/templ"

	"github.com/ryan053857/mygoravel/web"
	"github.com/ryan053857/mygoravel/web/ui"
)

// Options 配置 New。
type Options struct {
	// Content 是文档目录：Markdown 文件和它们引用的图片等文件，通常是 embed.FS 的子目录（fs.Sub）。
	Content fs.FS
	// Prefix 是文档的地址前缀，例如 /help；为空时文档在网站的根路径。
	Prefix string
	// Title 是文档的名称：显示在侧边栏顶部，也是每页 <title> 的后缀。
	Title string
	// Logo 是名称旁边的图标。
	Logo ui.Icon
	// Actions 是顶栏右侧的内容，例如返回控制台的链接。暗色模式开关总是显示在最右侧。
	Actions templ.Component
	// HTML 为 true 时原样输出 Markdown 中的 HTML，默认去掉。只在文档由可信的人编写时打开。
	HTML bool
	// Reload 为 true 时每次请求都重新读取 Content，编辑文档后刷新页面就能看到，用于开发。
	// 默认只在 New 时读取一次。
	Reload bool
}

// Site 是一组文档。
type Site struct {
	opts Options
	// tree 是 New 时加载的文档，Reload 时为 nil。
	tree *tree
}

// New 读取 opts.Content 中的全部文档并渲染：front matter 不合法、目录超过两层、
// 两篇文档的地址相同、相对链接指向不存在的文件时返回错误。
func New(opts Options) (*Site, error) {
	if opts.Content == nil {
		return nil, errors.New("web/docs: Options.Content is required")
	}
	opts.Prefix = strings.TrimSuffix(opts.Prefix, "/")
	if opts.Prefix != "" && !strings.HasPrefix(opts.Prefix, "/") {
		return nil, fmt.Errorf("web/docs: Options.Prefix %q must start with /", opts.Prefix)
	}
	s := &Site{opts: opts}
	t, err := s.load()
	if err != nil {
		return nil, err
	}
	if !opts.Reload {
		s.tree = t
	}
	return s, nil
}

func (s *Site) load() (*tree, error) {
	t, err := load(s.opts.Content, s.opts.Prefix, s.opts.HTML)
	if err != nil {
		return nil, fmt.Errorf("web/docs: %w", err)
	}
	return t, nil
}

// Register 在 rt 上注册文档的路由：Prefix 和 Prefix 之下的 GET 请求。
// 内部文档用 HTTP Basic 认证等中间件保护：pages.Routes(router.With(…))。
//
//	site.Register(pages.Routes(router))
func (s *Site) Register(rt *web.Routes) {
	rt.Get(pageURL(s.opts.Prefix, ""), s.serve)
	rt.Get(s.opts.Prefix+"/*", s.serve)
}

// serve 渲染文档，或者返回文档引用的文件。
func (s *Site) serve(w http.ResponseWriter, r *http.Request) error {
	t := s.tree
	if t == nil {
		var err error
		if t, err = s.load(); err != nil {
			return err
		}
	}
	rel, _ := strings.CutPrefix(r.URL.Path, s.opts.Prefix)
	if rel != "/" && strings.HasSuffix(rel, "/") {
		u := url.URL{Path: strings.TrimSuffix(r.URL.Path, "/"), RawQuery: r.URL.RawQuery}
		http.Redirect(w, r, u.String(), http.StatusMovedPermanently)
		return nil
	}
	rel = strings.Trim(rel, "/")
	if rel == searchPath {
		return s.search(w, r, t)
	}
	if p, ok := t.pages[rel]; ok {
		v := view{site: s, tree: t, page: p}
		return web.Render(w, r, http.StatusOK, layout(v, pageView(v)))
	}
	if etag, ok := t.files[rel]; ok {
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, s.opts.Content, rel)
		return nil
	}
	return web.ErrNotFound.New()
}

// search 渲染搜索结果：顶栏搜索框的 htmx 请求只返回结果列表，否则返回搜索页。
func (s *Site) search(w http.ResponseWriter, r *http.Request, t *tree) error {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	results := t.search(q)
	v := view{site: s, tree: t, page: &page{path: searchPath, title: text(r.Context(), "docs.search")}}
	if web.IsPartial(r) {
		return web.Render(w, r, http.StatusOK, searchResults(v, q, results))
	}
	return web.Render(w, r, http.StatusOK, layout(v, searchView(v, q, results)))
}
