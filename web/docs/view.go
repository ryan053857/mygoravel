package docs

import "github.com/a-h/templ"

// SidebarID 是文档侧边栏的 ID。
const SidebarID = "web-docs-sidebar"

// PageID 是正文区域的 ID：侧边栏、正文中指向其他文档的链接、上一页和下一页只用 htmx 替换这个区域。
const PageID = "docs-page"

// SearchResultsID 是顶栏搜索框下方的结果弹层的 ID。
const SearchResultsID = "docs-search-results"

// boost 是只替换正文区域的属性：写在侧边栏和正文区域上，其中带 hx-boost 的链接沿用它们。
var boost = templ.Attributes{
	"hx-target:inherited": "#" + PageID,
	"hx-select:inherited": "#" + PageID,
	"hx-swap:inherited":   "outerHTML",
}

// view 是渲染一页需要的数据。
type view struct {
	site *Site
	tree *tree
	page *page
}

func (v view) url(p *page) string { return pageURL(v.site.opts.Prefix, p.path) }

func (v view) home() string { return pageURL(v.site.opts.Prefix, "") }

func (v view) searchURL() string { return pageURL(v.site.opts.Prefix, searchPath) }

// resultURL 是搜索结果的地址：小节的结果带上标题的锚点。
func (v view) resultURL(r result) string {
	if r.heading.ID == "" {
		return v.url(r.page)
	}
	return v.url(r.page) + "#" + r.heading.ID
}

// pageTitle 是 p 的标题：首页没有标题时为文档的名称。
func (v view) pageTitle(p *page) string {
	if p.title == "" {
		return v.site.opts.Title
	}
	return p.title
}

// title 是页面的标题：首页没有标题时为文档的名称。
func (v view) title() string { return v.pageTitle(v.page) }

// documentTitle 是 <title>：页面的标题加上文档的名称。
func (v view) documentTitle() string {
	t := v.title()
	if t == v.site.opts.Title || v.site.opts.Title == "" {
		return t
	}
	return t + " · " + v.site.opts.Title
}

// crumbs 返回当前页面所在的各级目录的首页，不含首页。
func (v view) crumbs() []*page {
	var out []*page
	for s := v.page.parent; s != nil && s.index != v.tree.home; s = s.index.parent {
		out = append([]*page{s.index}, out...)
	}
	return out
}

// listing 报告当前页面是没有正文的目录首页，这时显示目录中的页面列表。
func (v view) listing() []*page {
	if v.page.section == nil || v.page.content {
		return nil
	}
	return v.page.section.children
}

// navGroup 是侧边栏中的一组：顶层的连续页面（section 为 nil），或者一个顶层目录。
type navGroup struct {
	section *page
	items   []*page
}

// groups 按侧边栏的分组返回顶层的页面和目录。
func (v view) groups() []navGroup {
	var gs []navGroup
	for _, c := range v.tree.home.section.children {
		if c.section != nil {
			gs = append(gs, navGroup{section: c, items: c.section.children})
			continue
		}
		if len(gs) == 0 || gs[len(gs)-1].section != nil {
			gs = append(gs, navGroup{})
		}
		gs[len(gs)-1].items = append(gs[len(gs)-1].items, c)
	}
	return gs
}

// active 报告 p 是当前页面。
func (v view) active(p *page) bool { return v.page == p }

// open 报告当前页面在目录 p 中，这时侧边栏中的这一项默认展开。
func (v view) open(p *page) bool { return v.page == p || v.page.parent == p.section }

// navLink 是侧边栏中页面链接的属性：htmx 只替换正文区域；切换后衔接脚本按 data-docs-nav 更新高亮。
func navLink() templ.Attributes { return templ.Attributes{"hx-boost": "true", "data-docs-nav": "true"} }
