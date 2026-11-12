// Package outboundtest 提供不连接网络的 http.RoundTripper，用于测试发出 HTTP 请求的代码：
// 按方法和地址登记响应，记录发出的每个请求，没有登记的请求让测试失败。
//
//	fake := outboundtest.New(t)
//	fake.On("POST https://api.stripe.com/v1/refunds").Reply(outboundtest.JSON(200, refund))
//	action := &Refund{client: fake.Client()}
//	// ...
//	sent := fake.Sent("POST https://api.stripe.com/v1/refunds")
//
// 它替换的是传输层，被测代码只要接收 *http.Client（或者 SDK 的 WithHTTPClient 选项）就能使用，
// 不需要把服务地址改成本机测试服务器的地址。
package outboundtest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
)

// Transport 是不连接网络的 http.RoundTripper，可并发使用。
type Transport struct {
	t         testing.TB
	mu        sync.Mutex
	routes    []*Route
	requests  []Request
	unmatched []string
}

var _ http.RoundTripper = (*Transport)(nil)

// New 创建一个没有登记任何响应的 Transport。测试结束时，如果有请求没有匹配到登记的地址，测试失败。
func New(t testing.TB) *Transport {
	f := &Transport{t: t}
	t.Cleanup(func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, u := range f.unmatched {
			t.Errorf("outboundtest: unexpected request %s", u)
		}
	})
	return f
}

// Client 返回使用这个 Transport 的客户端。
func (f *Transport) Client() *http.Client { return &http.Client{Transport: f} }

// On 登记一个地址，用返回的 Route 设置它的响应。pattern 的格式见 Sent。
// 一个请求匹配多个登记时，后登记的优先，测试可以覆盖公共辅助函数登记的响应。
func (f *Transport) On(pattern string) *Route {
	r := &Route{mu: &f.mu, pattern: parse(pattern), responses: []Response{Status(http.StatusOK)}}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes = append(f.routes, r)
	return r
}

// Requests 返回发出的所有请求，包括没有匹配的，按发出的顺序排列。
func (f *Transport) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

// Sent 返回匹配 pattern 的请求，按发出的顺序排列。
//
// pattern 是可选的方法加地址，例如 "POST https://api.stripe.com/v1/refunds"；省略方法时匹配任何方法。
// 地址中的 * 匹配任意字符（包括 /），省略协议（"api.github.com/users/*"）时匹配 http 和 https。
// pattern 不含 ? 时忽略请求的查询参数，含 ? 时与包括查询参数的完整地址比较。
func (f *Transport) Sent(pattern string) []Request {
	p := parse(pattern)
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Request
	for _, r := range f.requests {
		if p.match(r.Method, r.URL) {
			out = append(out, r)
		}
	}
	return out
}

// RoundTrip 记录请求，返回匹配的登记的下一个响应；没有匹配时返回错误，并在测试结束时报告。
func (f *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		var err error
		body, err = io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return nil, err
		}
	}
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	rec := Request{Method: req.Method, URL: cloneURL(req.URL), Header: req.Header.Clone(), Body: body}

	f.mu.Lock()
	f.requests = append(f.requests, rec)
	var route *Route
	for _, r := range slices.Backward(f.routes) {
		if r.pattern.match(req.Method, req.URL) {
			route = r
			break
		}
	}
	if route == nil {
		f.unmatched = append(f.unmatched, req.Method+" "+req.URL.String())
		f.mu.Unlock()
		return nil, fmt.Errorf("outboundtest: no response registered for %s %s", req.Method, req.URL)
	}
	respond := route.next()
	f.mu.Unlock()

	in := req.Clone(req.Context())
	in.Body = io.NopCloser(bytes.NewReader(body))
	resp, err := respond(in)
	if err != nil {
		return nil, err
	}
	// 与真实的传输层一样，请求在收到响应之前取消或超时，返回 ctx 的错误。
	if err := req.Context().Err(); err != nil {
		resp.Body.Close()
		return nil, err
	}
	resp.Request = req
	return resp, nil
}

// Route 是 On 登记的地址。
type Route struct {
	mu        *sync.Mutex // Transport 的锁
	pattern   pattern
	responses []Response
	served    int
}

// Reply 设置依次返回的响应：第 n 个请求得到第 n 个响应，用完之后一直返回最后一个。
// 没有调用 Reply 的登记返回没有响应体的 200。
func (r *Route) Reply(first Response, more ...Response) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.responses = append([]Response{first}, more...)
	r.served = 0
}

// next 返回下一个响应，调用方持有 Transport 的锁。
func (r *Route) next() Response {
	i := min(r.served, len(r.responses)-1)
	r.served++
	return r.responses[i]
}

// Request 是记录下来的请求。
type Request struct {
	Method string
	URL    *url.URL
	Header http.Header
	// Body 是完整的请求体，没有请求体时为 nil。
	Body []byte
}

// JSON 把请求体解析到 v 中。
func (r Request) JSON(v any) error { return json.Unmarshal(r.Body, v) }

// Form 解析 application/x-www-form-urlencoded 格式的请求体。
func (r Request) Form() (url.Values, error) { return url.ParseQuery(string(r.Body)) }

// Response 根据请求生成响应。用 JSON、Text、Status、Fail、Handler 创建，也可以直接写一个函数。
// 传入的请求的 Body 可以读取。
type Response func(*http.Request) (*http.Response, error)

// Handler 用 h 处理请求，返回它写入的响应。h 可以检查请求、等待 r.Context() 结束来模拟慢响应。
func Handler(h http.Handler) Response {
	return func(req *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Result(), nil
	}
}

// HandlerFunc 与 Handler 相同，接收函数。
func HandlerFunc(h func(http.ResponseWriter, *http.Request)) Response {
	return Handler(http.HandlerFunc(h))
}

// JSON 返回状态码为 status、响应体为 v 的 JSON 编码的响应。
func JSON(status int, v any) Response {
	body, err := json.Marshal(v)
	if err != nil {
		return Fail(fmt.Errorf("outboundtest: encode JSON response: %w", err))
	}
	return reply(status, "application/json", body)
}

// Text 返回状态码为 status、响应体为 body 的纯文本响应。
func Text(status int, body string) Response {
	return reply(status, "text/plain; charset=utf-8", []byte(body))
}

// Status 返回状态码为 status、没有响应体的响应。
func Status(status int) Response { return reply(status, "", nil) }

// Fail 让请求返回 err 而不是响应，用于模拟连接失败、超时（context.DeadlineExceeded）
// 或者 outbound.ErrForbidden。客户端返回的错误用 errors.Is 能识别 err。
func Fail(err error) Response {
	return func(*http.Request) (*http.Response, error) { return nil, err }
}

// Header 返回设置了响应头 key 的 Response，覆盖原有的值，例如 Status(429).Header("Retry-After", "1")、
// Text(200, page).Header("Content-Type", "text/html")。
func (r Response) Header(key, value string) Response {
	return func(req *http.Request) (*http.Response, error) {
		resp, err := r(req)
		if err != nil {
			return nil, err
		}
		resp.Header.Set(key, value)
		return resp, nil
	}
}

func reply(status int, contentType string, body []byte) Response {
	return func(*http.Request) (*http.Response, error) {
		header := http.Header{}
		if contentType != "" {
			header.Set("Content-Type", contentType)
		}
		return &http.Response{
			Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
			StatusCode:    status,
			Proto:         "HTTP/1.1",
			ProtoMajor:    1,
			ProtoMinor:    1,
			Header:        header,
			Body:          io.NopCloser(bytes.NewReader(body)),
			ContentLength: int64(len(body)),
		}, nil
	}
}

// pattern 是解析后的 On 和 Sent 的参数。
type pattern struct {
	method string // 空表示任何方法
	url    string // 去掉了协议（scheme 为假时）
	scheme bool   // url 包含协议
	query  bool   // url 包含查询参数
}

func parse(s string) pattern {
	s = strings.TrimSpace(s)
	var p pattern
	if method, rest, ok := strings.Cut(s, " "); ok && isMethod(method) {
		p.method, s = method, strings.TrimSpace(rest)
	}
	p.scheme = strings.Contains(s, "://")
	p.query = strings.Contains(s, "?")
	host := s
	if p.scheme {
		_, host, _ = strings.Cut(s, "://")
	}
	if !strings.Contains(host, "/") && !strings.HasSuffix(host, "*") {
		s += "/" // "https://example.com" 与 "https://example.com/" 相同
	}
	p.url = s
	return p
}

func isMethod(s string) bool {
	return s != "" && strings.ToUpper(s) == s && !strings.ContainsAny(s, "/:*.")
}

func (p pattern) match(method string, u *url.URL) bool {
	if p.method != "" && p.method != method {
		return false
	}
	path := u.Path
	if path == "" {
		path = "/"
	}
	target := u.Host + path
	if p.scheme {
		target = u.Scheme + "://" + target
	}
	if p.query && u.RawQuery != "" {
		target += "?" + u.RawQuery
	}
	return glob(p.url, target)
}

// glob 判断 s 是否匹配 pattern，pattern 中的 * 匹配任意字符。
func glob(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	last := parts[len(parts)-1]
	for _, part := range parts[1 : len(parts)-1] {
		i := strings.Index(s, part)
		if i < 0 {
			return false
		}
		s = s[i+len(part):]
	}
	return len(s) >= len(last) && strings.HasSuffix(s, last)
}

func cloneURL(u *url.URL) *url.URL {
	c := *u
	if u.User != nil {
		user := *u.User
		c.User = &user
	}
	return &c
}
