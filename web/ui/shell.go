package ui

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/a-h/templ"
	"github.com/axadrn/shadcn-templ/v2/components/dropdownmenu"
	"github.com/axadrn/shadcn-templ/v2/components/icon"

	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/web"
)

// Icon 是 shadcn-templ 的图标，例如 icon.FileText。
type Icon = func(...icon.Props) templ.Component

// NavItem 是 AppShell 侧边栏的一项导航。
type NavItem struct {
	Label string
	// Href 是链接地址。当前页面（web.CurrentPath）等于它或在它之下（Href + "/…"）时高亮，Exact 时只比较相等。
	Href  string
	Exact bool
	Icon  Icon
	// Permission 非空时，只有当前主体能执行这个权限（identity.Subject.Can）才显示。
	Permission string
	// Group 是分组标题：连续的、Group 相同的项显示在同一组下。
	Group string
	// Badge 是右侧的小标记，例如未读数量。
	Badge string
	// Items 是二级导航，点击展开；当前页面在其中时默认展开。
	Items []NavItem
}

// ShellUser 是侧边栏底部的当前用户。
type ShellUser struct {
	Name  string
	Email string
	// Avatar 是头像地址，为空时显示 Name 的第一个字。
	Avatar string
	// Menu 是点击用户后弹出的菜单，例如个人设置、退出登录。
	Menu []MenuItem
}

// MenuItem 是一个菜单项。
type MenuItem struct {
	Label string
	Href  string
	Icon  Icon
	// Post 为 true 时，点击发出 POST 请求（hx-post），用于退出登录等改变状态的操作。
	Post bool
}

// AppShellProps 配置 AppShell。
type AppShellProps struct {
	// Brand 是侧边栏顶部的名称，Logo 是它旁边的图标，Home 是点击它去的地址（默认 /）。
	Brand string
	Logo  Icon
	Home  string
	Nav   []NavItem
	// User 是侧边栏底部的当前用户，nil 时不显示。
	User *ShellUser
	// Title 是顶栏中的页面标题。
	Title string
	// Actions 是顶栏右侧的内容，例如 ThemeToggle、通知按钮。
	Actions templ.Component
	// Collapsed 让侧边栏默认收起成图标。用户点击收起、展开后，选择记在 cookie 中，之后的页面按用户的选择渲染。
	Collapsed bool
}

// navGroup 是连续的、Group 相同的导航项。
type navGroup struct {
	label string
	items []NavItem
}

// navGroups 按当前主体的权限过滤 items，再按 Group 分组。
func navGroups(ctx context.Context, items []NavItem) []navGroup {
	var groups []navGroup
	for _, it := range visibleNav(ctx, items) {
		if len(groups) == 0 || groups[len(groups)-1].label != it.Group {
			groups = append(groups, navGroup{label: it.Group})
		}
		g := &groups[len(groups)-1]
		g.items = append(g.items, it)
	}
	return groups
}

// visibleNav 去掉当前主体没有权限的项；二级导航全部被去掉、自己又没有链接的项也去掉。
func visibleNav(ctx context.Context, items []NavItem) []NavItem {
	sub, _ := identity.FromContext(ctx)
	var out []NavItem
	for _, it := range items {
		if it.Permission != "" && !sub.Can(it.Permission) {
			continue
		}
		if len(it.Items) > 0 {
			it.Items = visibleNav(ctx, it.Items)
			if len(it.Items) == 0 && it.Href == "" {
				continue
			}
		}
		out = append(out, it)
	}
	return out
}

// navActive 报告 it 或它的二级导航是否是当前页面。
func navActive(ctx context.Context, it NavItem) bool {
	path := web.CurrentPath(ctx)
	if it.Href != "" && (path == it.Href || !it.Exact && strings.HasPrefix(path, strings.TrimSuffix(it.Href, "/")+"/")) {
		return true
	}
	for _, sub := range it.Items {
		if navActive(ctx, sub) {
			return true
		}
	}
	return false
}

// initial 返回 name 的第一个字，用于没有头像时。
func initial(name string) string {
	r, _ := utf8.DecodeRuneInString(strings.TrimSpace(name))
	if r == utf8.RuneError {
		return "?"
	}
	return strings.ToUpper(string(r))
}

// AuthCardProps 配置 AuthCard。
type AuthCardProps struct {
	Title       string
	Description string
	// Brand 是卡片上方的名称，Logo 是它旁边的图标，Home 是点击它去的地址（默认 /）。为空时不显示。
	Brand string
	Logo  Icon
	Home  string
	// Footer 是卡片下方的内容，例如「还没有账号？注册」。
	Footer templ.Component
}

// menuItemProps 把 MenuItem 转换为下拉菜单项：Post 时发出 hx-post，否则是链接。
func menuItemProps(m MenuItem) dropdownmenu.ItemProps {
	if m.Post {
		return dropdownmenu.ItemProps{Attributes: templ.Attributes{"hx-post": m.Href}}
	}
	return dropdownmenu.ItemProps{Href: m.Href}
}

// sidebarCookie 是侧边栏脚本记录收起状态的 cookie：true 为展开，false 为收起。
const sidebarCookie = "sidebar_state"

// collapsed 返回侧边栏是否收起：用户选择过时按 cookie，否则按 def。
func collapsed(ctx context.Context, def bool) bool {
	if r := web.RequestFrom(ctx); r != nil {
		if c, err := r.Cookie(sidebarCookie); err == nil {
			return c.Value == "false"
		}
	}
	return def
}
