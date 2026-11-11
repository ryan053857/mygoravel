package ui_test

import (
	"context"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/alexedwards/scs/v2/memstore"
	"github.com/axadrn/shadcn-templ/v2/components/icon"
	"github.com/go-chi/chi/v5"

	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/session"
	"github.com/ryan053857/mygoravel/web"
	"github.com/ryan053857/mygoravel/web/ui"
	"github.com/ryan053857/mygoravel/web/webtest"
)

// withChildren 以 children 渲染 parent，相当于 templ 中的 @parent { children }。
func withChildren(parent, children templ.Component) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		return parent.Render(templ.WithChildren(ctx, children), w)
	})
}

var adminNav = []ui.NavItem{
	{Label: "概览", Href: "/admin", Exact: true, Icon: icon.House},
	{Label: "文章", Href: "/admin/articles", Icon: icon.FileText, Group: "内容", Permission: "articles", Badge: "3"},
	{Label: "评论", Href: "/admin/comments", Group: "内容", Permission: "comments"},
	{Label: "设置", Icon: icon.Settings, Group: "系统", Items: []ui.NavItem{
		{Label: "用户", Href: "/admin/users", Permission: "users"},
		{Label: "角色", Href: "/admin/roles", Permission: "users"},
	}},
}

// shell 注册 /admin 下渲染 AppShell 的页面，返回以 permissions 登录的客户端。
func shell(t *testing.T, permissions ...string) *webtest.Client {
	t.Helper()
	var cfg session.Config
	cfg.SetDefaults()
	pages := web.New(web.Options{Sessions: session.New(cfg, memstore.New()), Login: "/login", ErrorPage: ui.ErrorPage})
	r := chi.NewRouter()
	user := &ui.ShellUser{Name: "alice", Email: "alice@example.com", Menu: []ui.MenuItem{
		{Label: "个人设置", Href: "/admin/profile", Icon: icon.User},
		{Label: "退出登录", Href: "/logout", Post: true},
	}}
	page := func(w http.ResponseWriter, r *http.Request) error {
		app := ui.AppShell(ui.AppShellProps{Brand: "Blog 后台", Logo: icon.GalleryVerticalEnd, Nav: adminNav, User: user, Title: "文章", Actions: ui.ThemeToggle()})
		return web.Render(w, r, http.StatusOK, withChildren(ui.Page(ui.PageProps{Title: "后台"}), withChildren(app, templ.Raw("<p id=content>正文</p>"))))
	}
	routes := pages.Routes(r, web.Require())
	for _, p := range []string{"/admin", "/admin/articles", "/admin/articles/{id}", "/admin/users"} {
		routes.Get(p, page)
	}
	c := webtest.New(t, r)
	c.Login(pages, identity.Subject{ID: "alice", Type: identity.SubjectUser, Permissions: permissions})
	return c
}

// active 返回高亮的导航项的链接。
func active(html string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`<a [^>]*>`).FindAllString(html, -1) {
		if strings.Contains(m, "data-active") {
			out = append(out, regexp.MustCompile(`href="([^"]*)"`).FindStringSubmatch(m)[1])
		}
	}
	return out
}

func TestAppShellNav(t *testing.T) {
	c := shell(t, "articles")
	html := c.Get("/admin/articles/42").Text()

	// 有权限的项显示，没有权限的隐藏；二级导航全部没有权限时整组隐藏。
	contains(t, html, `href="/admin/articles"`, ">文章<", ">内容<", ">3<", "Blog 后台", "<p id=content>正文</p>", ">文章</h1>", "data-web-ui-theme-toggle")
	lacks(t, html, "/admin/comments", ">设置<", ">系统<", "/admin/users")
	// 当前页面在 /admin/articles 之下：文章高亮，Exact 的概览不高亮。
	if got := active(html); len(got) != 1 || got[0] != "/admin/articles" {
		t.Errorf("active = %v", got)
	}
	// 用户菜单：链接和 POST 项。
	contains(t, html, ">alice<", "alice@example.com", `href="/admin/profile"`, `hx-post="/logout"`, `id="`+ui.SidebarID+`"`)

	if got := active(c.Get("/admin").Text()); len(got) != 1 || got[0] != "/admin" {
		t.Errorf("active on /admin = %v", got)
	}
}

func TestAppShellSubNav(t *testing.T) {
	html := shell(t, "users").Get("/admin/users").Text()
	contains(t, html, ">设置<", ">系统<", `href="/admin/users"`, `href="/admin/roles"`)
	lacks(t, html, "/admin/articles")
	if got := active(html); len(got) != 1 || got[0] != "/admin/users" {
		t.Errorf("active = %v", got)
	}
	// 当前页面在二级导航中：这一组默认展开。
	if !regexp.MustCompile(`data-slot="collapsible"[^>]*data-open`).MatchString(html) {
		t.Errorf("sub nav not open:\n%s", html)
	}
}

func TestAuthCard(t *testing.T) {
	footer := templ.Raw(`还没有账号？<a href="/register">注册</a>`)
	html := renderIn(t, "zh-CN", withChildren(ui.AuthCard(ui.AuthCardProps{Title: "登录", Description: "使用邮箱登录", Brand: "Blog", Home: "/home", Footer: footer}), templ.Raw("<form id=login></form>")))
	contains(t, html, ">登录<", "使用邮箱登录", `href="/home"`, ">Blog", `<form id=login>`, `href="/register"`)
	html = render(t, withChildren(ui.AuthCard(ui.AuthCardProps{}), templ.Raw("<form></form>")))
	lacks(t, html, `<a href`, "card-header")
}

// 用户收起侧边栏后（脚本写入 sidebar_state=false），之后的页面按收起渲染。
func TestAppShellRemembersCollapsed(t *testing.T) {
	c := shell(t, "articles")
	if html := c.Get("/admin").Text(); !strings.Contains(html, `data-state="expanded"`) {
		t.Fatal("not expanded by default")
	}
	html := c.Get("/admin", webtest.Header("Cookie", "sidebar_state=false")).Text()
	if !strings.Contains(html, `data-state="collapsed"`) {
		t.Fatalf("cookie ignored")
	}
}
