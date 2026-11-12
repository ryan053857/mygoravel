package docs

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/url"
	"path"
	"slices"
	"strings"

	"github.com/yuin/goldmark/ast"
	"go.yaml.in/yaml/v4"
)

// maxDepth 是目录嵌套的最大层数：第一层是侧边栏的分组，第二层是分组中可以展开的一项。
const maxDepth = 2

// page 是一篇文档，或者一个目录的首页。
type page struct {
	// file 是相对 Content 的文件名；没有 _index.md 的目录为空。
	file string
	// path 是相对 Prefix 的地址，不含首尾的 /，首页为 ""。
	path        string
	title       string
	description string
	order       int
	// body 是渲染好的正文，其中的 label 在每次请求时替换（localize）。
	body string
	toc  []heading
	// entries 是这一页的搜索索引，加载完成后按顺序合并到 tree.entries。
	entries []entry
	// content 报告页面有正文。目录的首页没有正文时显示目录中的页面列表，也不出现在侧边栏中。
	content bool
	// section 非 nil 时，这一页是这个目录的首页。
	section *section
	// parent 是所在的目录，首页为 nil。
	parent     *section
	prev, next *page
}

// section 是 Content 中的一个目录。
type section struct {
	dir   string
	index *page
	// children 是目录中的页面，子目录以它的首页出现；按 order、名称排序。
	children []*page
}

// name 是排序时比较的名称：文件名或目录名。
func (p *page) name() string {
	if p.section != nil {
		return path.Base(p.section.dir)
	}
	return strings.TrimSuffix(path.Base(p.file), ".md")
}

// tree 是加载好的全部文档。
type tree struct {
	home  *page
	pages map[string]*page  // 按 path
	files map[string]string // Markdown 之外的文件（例如图片）和它们的 ETag
	// entries 是搜索索引，按侧边栏的顺序。
	entries []entry
}

// meta 是文档开头的 front matter。
type meta struct {
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
	// Order 决定页面在目录中的位置：写了的按从小到大排在前面，没写的排在后面，再按名称排序。
	Order int `yaml:"order"`
}

// parsedPage 是解析后、渲染前的页面。
type parsedPage struct {
	page *page
	src  []byte
	doc  ast.Node
}

// load 读取 fsys 中的全部文档，检查链接并渲染正文。
func load(fsys fs.FS, prefix string, rawHTML bool) (*tree, error) {
	md := newMarkdown(rawHTML)
	t := &tree{pages: map[string]*page{}, files: map[string]string{}}
	sections := map[string]*section{".": {dir: "."}}
	byFile := map[string]*page{}
	var parsed []parsedPage

	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name != "." && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		if path.Ext(name) != ".md" {
			sum := sha256.Sum256(data)
			t.files[name] = `"` + hex.EncodeToString(sum[:8]) + `"`
			return nil
		}
		m, src, err := frontMatter(data)
		if err != nil {
			return fmt.Errorf("%s: front matter: %w", name, err)
		}
		doc := parse(md, src)
		p := &page{file: name, title: m.Title, description: m.Description, order: m.Order}
		if h1 := takeTitle(doc, src); p.title == "" {
			p.title = h1
		}
		p.content = doc.HasChildren()
		dir := path.Dir(name)
		if depth := strings.Count(name, "/"); depth > maxDepth {
			return fmt.Errorf("%s: directories nest at most %d levels", name, maxDepth)
		}
		if path.Base(name) == "_index.md" {
			p.section = sectionOf(sections, dir)
			p.section.index = p
			p.path = dirPath(dir)
		} else {
			p.path = strings.TrimSuffix(name, ".md")
			p.parent = sectionOf(sections, dir)
			p.parent.children = append(p.parent.children, p)
		}
		if p.title == "" {
			p.title = p.name()
		}
		if other, ok := t.pages[p.path]; ok {
			return fmt.Errorf("%s and %s have the same address /%s", other.file, name, p.path)
		}
		if p.path == searchPath {
			return fmt.Errorf("%s: the address /%s is reserved for search", name, searchPath)
		}
		t.pages[p.path] = p
		byFile[name] = p
		parsed = append(parsed, parsedPage{page: p, src: src, doc: doc})
		return nil
	})
	if err != nil {
		return nil, err
	}

	// 没有 _index.md 的目录以目录名为标题；子目录的首页放进上级目录。只有图片等文件的目录不是文档的目录。
	for _, dir := range slices.Sorted(maps.Keys(sections)) {
		s := sections[dir]
		if s.index == nil {
			s.index = &page{path: dirPath(dir), title: path.Base(dir), section: s}
			if dir == "." {
				s.index.title = ""
			}
			if other, ok := t.pages[s.index.path]; ok {
				return nil, fmt.Errorf("%s and directory %s have the same address /%s", other.file, dir, s.index.path)
			}
			t.pages[s.index.path] = s.index
		}
		if dir != "." {
			s.index.parent = sections[path.Dir(dir)]
			s.index.parent.children = append(s.index.parent.children, s.index)
		}
	}
	for _, s := range sections {
		slices.SortFunc(s.children, func(a, b *page) int {
			if (a.order == 0) != (b.order == 0) {
				if a.order == 0 {
					return 1
				}
				return -1
			}
			return cmp.Or(cmp.Compare(a.order, b.order), strings.Compare(a.name(), b.name()))
		})
	}
	t.home = sections["."].index

	var errs []error
	for _, pp := range parsed {
		if err := links(pp, prefix, t, byFile, sections); err != nil {
			errs = append(errs, err)
			continue
		}
		alerts(pp.doc, pp.src)
		pp.page.entries = index(pp.page, pp.doc, pp.src)
		pp.page.toc = outline(pp.doc, pp.src)
		var b bytes.Buffer
		if err := md.Renderer().Render(&b, pp.src, pp.doc); err != nil {
			return nil, fmt.Errorf("%s: %w", pp.page.file, err)
		}
		pp.page.body = b.String()
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}

	// 上一页、下一页按侧边栏的顺序：首页、有正文的目录首页和其他页面。
	var seq []*page
	var visit func(s *section)
	visit = func(s *section) {
		if s.index.content {
			seq = append(seq, s.index)
		}
		for _, c := range s.children {
			if c.section != nil {
				visit(c.section)
			} else {
				seq = append(seq, c)
			}
		}
	}
	visit(sections["."])
	for i, p := range seq {
		if i > 0 {
			p.prev = seq[i-1]
		}
		if i < len(seq)-1 {
			p.next = seq[i+1]
		}
	}

	// 搜索索引按侧边栏的顺序，不在侧边栏中的页面（没有正文的目录首页）排在后面。
	inSeq := map[*page]bool{}
	for _, p := range seq {
		inSeq[p] = true
		t.entries = append(t.entries, p.entries...)
	}
	for _, pp := range parsed {
		if !inSeq[pp.page] {
			t.entries = append(t.entries, pp.page.entries...)
		}
	}
	return t, nil
}

// sectionOf 返回 dir 的 section，需要时创建它和它的上级目录。
func sectionOf(sections map[string]*section, dir string) *section {
	if s, ok := sections[dir]; ok {
		return s
	}
	sectionOf(sections, path.Dir(dir))
	s := &section{dir: dir}
	sections[dir] = s
	return s
}

// frontMatter 分开开头 --- 之间的 YAML 和正文。没有 front matter 时 m 为零值，src 是 data。
func frontMatter(data []byte) (m meta, src []byte, err error) {
	first, rest, ok := cutLine(data)
	if !ok || first != "---" {
		return m, data, nil
	}
	var head []byte
	for body := rest; ; {
		line, next, more := cutLine(body)
		if line == "---" {
			head = rest[:len(rest)-len(body)]
			src = next
			break
		}
		if !more {
			return m, nil, errors.New("missing closing ---")
		}
		body = next
	}
	if len(bytes.TrimSpace(head)) > 0 {
		if err := yaml.Load(head, &m, yaml.WithKnownFields()); err != nil {
			return m, nil, err
		}
	}
	return m, src, nil
}

// cutLine 返回 data 的第一行（不含行尾的 \r\n 或 \n）和其余部分；没有换行时 more 为 false。
func cutLine(data []byte) (line string, rest []byte, more bool) {
	l, rest, more := bytes.Cut(data, []byte("\n"))
	return strings.TrimSuffix(string(l), "\r"), rest, more
}

// links 改写 pp 中指向其他文档和文件的相对链接，指向的文件不存在时返回错误：
// 文档（.md）和目录改写为页面的地址，并让 htmx 只替换正文区域；其他文件改写为 Prefix 下的地址。
// 带协议、以 / 开头和只有 #片段的链接保持不变。
func links(pp parsedPage, prefix string, t *tree, byFile map[string]*page, sections map[string]*section) error {
	var errs []error
	_ = ast.Walk(pp.doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		var dest *[]byte
		switch l := n.(type) {
		case *ast.Link:
			dest = &l.Destination
		case *ast.Image:
			dest = &l.Destination
		default:
			return ast.WalkContinue, nil
		}
		u, err := url.Parse(string(*dest))
		if err != nil || u.Scheme != "" || u.Host != "" || u.Path == "" || strings.HasPrefix(u.Path, "/") {
			return ast.WalkContinue, nil
		}
		target := path.Join(path.Dir(pp.page.file), u.Path)
		var to string
		page := false
		switch p, s := byFile[target], sections[target]; {
		case target == ".." || strings.HasPrefix(target, "../"):
			errs = append(errs, fmt.Errorf("%s: link %s points outside the content", pp.page.file, *dest))
			return ast.WalkContinue, nil
		case p != nil:
			to, page = pageURL(prefix, p.path), true
		case s != nil:
			to, page = pageURL(prefix, s.index.path), true
		case t.files[target] != "":
			to = pageURL(prefix, target)
		default:
			errs = append(errs, fmt.Errorf("%s: link %s points to a missing file", pp.page.file, *dest))
			return ast.WalkContinue, nil
		}
		u.Path, u.RawPath = to, ""
		*dest = []byte(u.String())
		if l, ok := n.(*ast.Link); ok && page {
			l.SetAttributeString("data-hx-boost", []byte("true"))
		}
		return ast.WalkContinue, nil
	})
	return errors.Join(errs...)
}

// dirPath 返回目录首页的 path：根目录为 ""。
func dirPath(dir string) string {
	if dir == "." {
		return ""
	}
	return dir
}

// pageURL 返回 prefix 下 p 的地址。
func pageURL(prefix, p string) string {
	switch {
	case p == "" && prefix == "":
		return "/"
	case p == "":
		return prefix
	}
	return prefix + "/" + p
}
