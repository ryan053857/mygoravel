// Package webtest 测试服务端渲染的页面：像浏览器一样向应用的路由发送请求，保存会话 cookie，
// 不走登录表单就能以某个主体登录，并读取 htmx 的响应头（跳转、提示、事件）。
//
//	c := webtest.New(t, router)
//	c.Login(pages, identity.Subject{Type: "user", ID: "alice", Permissions: []string{"articles"}})
//	res := c.Put("/admin/articles/"+id, url.Values{"title": {""}}, webtest.HTMX)
//	if res.Code != http.StatusUnprocessableEntity { … }
package webtest

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/session"
	"github.com/ryan053857/mygoravel/web"
)

// Origin 是请求的地址。使用 https，会话 cookie 带 Secure 时同样能发送。
const Origin = "https://example.com"

// Client 向 h 发送请求，不监听端口。它像浏览器一样保存响应设置的 cookie，之后的请求带上它们，
// 所以登录状态和会话中的提示可以延续到下一个请求。不能并发使用。
type Client struct {
	t   testing.TB
	h   http.Handler
	jar http.CookieJar
}

// New 返回向 h 发送请求的 Client，h 通常是应用注册了页面路由的 chi.Router。
func New(t testing.TB, h http.Handler) *Client {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &Client{t: t, h: h, jar: jar}
}

var origin, _ = url.Parse(Origin)

// Login 把 sub 登录进 pages 的会话，之后的请求以 sub 的身份访问 pages 的路由，与 Pages.Login 的结果相同，
// 但不需要经过应用的登录页。一个应用有多组 Pages 时，每组分别登录。
func (c *Client) Login(pages *web.Pages, sub identity.Subject) {
	c.t.Helper()
	sm := pages.Sessions()
	if sm == nil {
		c.t.Fatal("webtest: Login needs Pages created with Options.Sessions")
	}
	ctx, err := sm.Load(context.Background(), "")
	if err != nil {
		c.t.Fatal(err)
	}
	if err := session.Login(ctx, sm, sub, session.Device{IP: "192.0.2.1", UserAgent: "webtest"}); err != nil {
		c.t.Fatal(err)
	}
	token, expiry, err := sm.Commit(ctx)
	if err != nil {
		c.t.Fatal(err)
	}
	c.jar.SetCookies(origin, []*http.Cookie{{
		Name: sm.Cookie.Name, Value: token, Path: sm.Cookie.Path, Domain: sm.Cookie.Domain,
		Secure: sm.Cookie.Secure, HttpOnly: true, Expires: expiry,
	}})
}

// Option 修改要发送的请求。
type Option func(*http.Request)

// HTMX 让请求像 htmx 替换页面中某个元素时发出的一样（HX-Request: true，HX-Request-Type: partial）：
// 错误作为提示送达，跳转改写为 HX-Redirect，web.IsPartial 为 true。
func HTMX(r *http.Request) {
	r.Header.Set("HX-Request", "true")
	r.Header.Set("HX-Request-Type", "partial")
}

// HTMXPage 让请求像 htmx 替换整个 body 时发出的一样（HX-Request-Type: full），例如后退时恢复页面。
func HTMXPage(r *http.Request) {
	r.Header.Set("HX-Request", "true")
	r.Header.Set("HX-Request-Type", "full")
}

// Header 设置请求头，例如 webtest.Header("Accept-Language", "en")。
func Header(name, value string) Option {
	return func(r *http.Request) { r.Header.Set(name, value) }
}

// Get 发送 GET 请求，path 可以带查询参数。
func (c *Client) Get(path string, opts ...Option) *Response {
	c.t.Helper()
	return c.send(http.MethodGet, path, nil, opts)
}

// Post 以表单（application/x-www-form-urlencoded）提交 form。
func (c *Client) Post(path string, form url.Values, opts ...Option) *Response {
	c.t.Helper()
	return c.send(http.MethodPost, path, form, opts)
}

// Put 以表单提交 form。
func (c *Client) Put(path string, form url.Values, opts ...Option) *Response {
	c.t.Helper()
	return c.send(http.MethodPut, path, form, opts)
}

// Patch 以表单提交 form。
func (c *Client) Patch(path string, form url.Values, opts ...Option) *Response {
	c.t.Helper()
	return c.send(http.MethodPatch, path, form, opts)
}

// Delete 发送 DELETE 请求。
func (c *Client) Delete(path string, opts ...Option) *Response {
	c.t.Helper()
	return c.send(http.MethodDelete, path, nil, opts)
}

func (c *Client) send(method, path string, form url.Values, opts []Option) *Response {
	c.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	r := httptest.NewRequest(method, Origin+path, body)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, o := range opts {
		o(r)
	}
	return c.Do(r)
}

// Do 发送 r：带上保存的 cookie，保存响应设置的 cookie。r 的地址应当以 Origin 开头，例如由 httptest.NewRequest 创建，
// 用于 multipart 表单等 Get、Post 不能表达的请求。
func (c *Client) Do(r *http.Request) *Response {
	c.t.Helper()
	for _, ck := range c.jar.Cookies(r.URL) {
		r.AddCookie(ck)
	}
	rr := httptest.NewRecorder()
	c.h.ServeHTTP(rr, r)
	c.jar.SetCookies(r.URL, rr.Result().Cookies())
	return &Response{ResponseRecorder: rr, t: c.t}
}

// Response 是一个响应。Code、Body、Header 来自 httptest.ResponseRecorder。
type Response struct {
	*httptest.ResponseRecorder
	t testing.TB
}

// Text 返回响应体。
func (r *Response) Text() string { return r.Body.String() }

// Redirect 返回跳转的目标：3xx 响应的 Location，或 htmx 请求的 HX-Redirect；不是跳转时为空。
func (r *Response) Redirect() string {
	if r.Code >= 300 && r.Code < 400 {
		return r.Header().Get("Location")
	}
	return r.Header().Get("HX-Redirect")
}

// PushURL 返回 web.PushURL 设置的地址（HX-Push-Url）。
func (r *Response) PushURL() string { return r.Header().Get("HX-Push-Url") }

// Event 返回 HX-Trigger 中事件 name 的 detail（web.Trigger 的第三个参数），以及这个事件是否存在。
// 事件的值不是对象时，detail 的 value 是这个值。
// 例如 web.CloseDialog 触发 web.DialogCloseEvent，detail 中的 id 是对话框的 ID。
func (r *Response) Event(name string) (map[string]any, bool) {
	r.t.Helper()
	raw := r.Header().Get("HX-Trigger")
	if raw == "" {
		return nil, false
	}
	if !strings.HasPrefix(strings.TrimSpace(raw), "{") {
		for _, n := range strings.Split(raw, ",") {
			if strings.TrimSpace(n) == name {
				return map[string]any{}, true
			}
		}
		return nil, false
	}
	var events map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &events); err != nil {
		r.t.Fatalf("webtest: HX-Trigger %q: %v", raw, err)
	}
	v, ok := events[name]
	if !ok {
		return nil, false
	}
	var detail map[string]any
	if json.Unmarshal(v, &detail) != nil || detail == nil {
		var value any
		_ = json.Unmarshal(v, &value)
		return map[string]any{"value": value}, true // 其他写法（例如 {"saved": "文章"}）的值
	}
	delete(detail, "target") // web.Trigger 加上的派发位置
	return detail, true
}

// Flashes 返回随这个 htmx 响应送达的提示：web.Flash 存入的，以及局部请求的错误（Type 为 error）。
// 整页响应的提示由页面渲染，跳转响应的提示留到下一个请求。
func (r *Response) Flashes() []web.Message {
	r.t.Helper()
	detail, ok := r.Event(web.FlashEvent)
	if !ok {
		return nil
	}
	b, _ := json.Marshal(detail["messages"])
	var list []web.Message
	if err := json.Unmarshal(b, &list); err != nil {
		r.t.Fatalf("webtest: flash messages: %v", err)
	}
	return list
}
