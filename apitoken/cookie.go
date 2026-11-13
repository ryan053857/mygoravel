package apitoken

import (
	"net/http"
	"time"
)

// RefreshCookie 描述浏览器中保存 JWT 模式 refresh token 的 cookie。单页应用把 access token 只放在内存中，
// refresh token 放在这个 cookie 中：
//
//   - HttpOnly：页面上的脚本读不到它，XSS 拿不走长期有效的凭据；
//   - SameSite=Strict：其他站点发起的请求不会带上它；
//   - Path 只包含刷新和登出接口：其他请求也不会带上它，它不是 API 的凭据，API 仍然只认 Authorization: Bearer。
//
// 只用于和 API 属于同一站点的浏览器单页应用（同源，或 app.example.com 与 api.example.com）。
// App、桌面端、小程序和脚本仍在请求体中传递 refresh token。
//
// 处理器的输入用 cookie 标签读取它，输出用 Set-Cookie 头写入：
//
//	type RefreshInput struct {
//	    Cookie string `cookie:"editor_refresh"`
//	}
//	type RefreshOutput struct {
//	    SetCookie *http.Cookie `header:"Set-Cookie"`
//	    Body      struct{ AccessToken string `json:"access_token"` }
//	}
type RefreshCookie struct {
	// Name 是 cookie 的名称，与输入结构体 cookie 标签中的名称相同，例如 editor_refresh。
	// 一个站点有多套账号体系时各用一个名称。
	Name string
	// Path 是刷新和登出接口的公共前缀，例如 /admin/api/auth；cookie 只发给这个路径下的请求。
	Path string
	// Domain 为空时 cookie 只发给当前主机；单页应用与 API 在同一站点的不同子域名时，设为它们共同的上级域名。
	Domain string
}

// Set 返回保存 p.Refresh 的 cookie，它在这次登录的截止时间（p.RefreshExpiresAt）过期，每次刷新都随之延长。
// p.Refresh 为空时（宽限期内的重复刷新、Confirm）返回 nil，不改动浏览器中的 cookie。Name 或 Path 为空时 panic。
func (c RefreshCookie) Set(p Pair) *http.Cookie {
	if p.Refresh == "" {
		return nil
	}
	return c.cookie(p.Refresh, p.RefreshExpiresAt)
}

// Clear 返回让浏览器删除这个 cookie 的 Set-Cookie，用于登出。Name 或 Path 为空时 panic。
func (c RefreshCookie) Clear() *http.Cookie {
	cookie := c.cookie("", time.Unix(0, 0))
	cookie.MaxAge = -1
	return cookie
}

func (c RefreshCookie) cookie(value string, expires time.Time) *http.Cookie {
	if c.Name == "" || c.Path == "" {
		panic("apitoken: RefreshCookie needs a Name and a Path")
	}
	return &http.Cookie{
		Name:     c.Name,
		Value:    value,
		Path:     c.Path,
		Domain:   c.Domain,
		Expires:  expires.UTC(),
		Secure:   true, // 浏览器把 http://localhost 当作安全上下文，本地开发同样可用
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	}
}
