// Package wire 是生成的 Go 客户端的运行时：发送请求、刷新凭据、解码错误。
// 生成的方法调用它，应用代码通常只用到 Options、Auth、Error 和 Code。
package wire

import (
	"bytes"
	"context"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Code 是业务错误码，例如 article.slug_taken。生成的错误码常量是 Code，
// 用 errors.Is(err, article.ErrSlugTaken) 判断接口返回的是不是这个错误。
type Code string

// Error 返回错误码本身。
func (c Code) Error() string { return string(c) }

// ErrUpgradeRequired 是 App 版本低于服务端要求的最低版本时的错误码（426）。
const ErrUpgradeRequired Code = "app.upgrade_required"

// Error 是接口返回的非 2xx 响应。
type Error struct {
	// Status 是 HTTP 状态码。
	Status int
	// Code 是业务错误码；不是业务错误时为空。
	Code Code
	// Detail 是可以展示给用户的说明，按 Options.Language 翻译。
	Detail string
	// Fields 是输入格式错误（422）时每个字段的问题。
	Fields []FieldError
	// RequestID 是响应头 X-Request-ID，报告问题时提供给服务端排查。
	RequestID string
}

// FieldError 是一个输入字段的问题。
type FieldError struct {
	Message  string `json:"message"`
	Location string `json:"location"`
	Value    any    `json:"value,omitempty"`
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("api: ")
	b.WriteString(strconv.Itoa(e.Status))
	if e.Code != "" {
		b.WriteString(" ")
		b.WriteString(string(e.Code))
	}
	if e.Detail != "" {
		b.WriteString(": ")
		b.WriteString(e.Detail)
	}
	for _, f := range e.Fields {
		b.WriteString("; ")
		b.WriteString(f.Location)
		b.WriteString(": ")
		b.WriteString(f.Message)
	}
	return b.String()
}

// Is 让 errors.Is(err, code) 按错误码比较。
func (e *Error) Is(target error) bool {
	c, ok := target.(Code)
	return ok && e.Code != "" && c == e.Code
}

// Auth 提供受保护接口的 Bearer token。
type Auth interface {
	// Token 返回当前的 access token。
	Token(ctx context.Context) (string, error)
	// Refresh 换一对新的 token，在受保护的接口返回 401 时调用。并发请求同时 401 时只调用一次；
	// 调用前 token 已经被换掉时不调用，直接用新 token 重试。返回错误表示需要重新登录，
	// 调用方收到原来的 401。
	Refresh(ctx context.Context) error
}

// StaticToken 返回总是使用 token 的 Auth，例如不透明的 API token；它不能刷新。
func StaticToken(token string) Auth { return staticToken(token) }

type staticToken string

func (t staticToken) Token(context.Context) (string, error) { return string(t), nil }
func (staticToken) Refresh(context.Context) error {
	return errors.New("wire: a static token cannot be refreshed")
}

// Options 配置客户端。
type Options struct {
	// BaseURL 是服务端的地址，例如 https://blog.example.com；路由的前缀（例如 /api）由生成的代码加上。
	BaseURL string
	// HTTPClient 发送请求；nil 时使用 http.DefaultClient。需要额外的请求头时在它的 Transport 中添加。
	HTTPClient *http.Client
	// Auth 提供受保护接口的凭据；nil 时不带凭据，受保护的接口返回 401。公开接口从不带凭据。
	Auth Auth
	// AppVersion 是 App 的平台和版本，例如 ios/1.4.2，写入每个请求的 X-App-Version。
	AppVersion string
	// Language 返回用户的语言，例如 zh-CN，写入 Accept-Language，错误的 Detail 按它翻译；可以为 nil。
	Language func() string
}

// Client 发送生成的方法构造的请求。
type Client struct {
	opts Options
	base string

	mu         sync.Mutex
	refreshing map[string]*refresh
}

type refresh struct {
	done chan struct{}
	err  error
}

// New 创建客户端，basePath 是 API 挂载的路径前缀。
func New(opts Options, basePath string) *Client {
	if opts.HTTPClient == nil {
		opts.HTTPClient = http.DefaultClient
	}
	return &Client{
		opts:       opts,
		base:       strings.TrimRight(opts.BaseURL, "/") + strings.TrimRight(basePath, "/"),
		refreshing: map[string]*refresh{},
	}
}

// Request 是一次接口调用。
type Request struct {
	Method string
	// Path 是已经填好路径参数的路径，例如 /articles/0193a5c2-…/publish。
	Path   string
	Query  url.Values
	Header http.Header
	// Body 编码为 JSON 请求体；nil 或空指针表示没有请求体。
	Body any
	// Auth 表示接口需要凭据。
	Auth bool
	// Out 是 JSON 响应体的解码目标；nil 表示忽略响应体。
	Out any
	// Response 在成功时读取状态码和响应头。
	Response func(*http.Response) error
}

// Do 发送请求：受保护的接口带上凭据，401 时刷新凭据并重试一次；非 2xx 响应返回 *Error。
func (c *Client) Do(ctx context.Context, r *Request) error {
	body, err := encodeBody(r.Body)
	if err != nil {
		return err
	}
	auth := r.Auth && c.opts.Auth != nil
	var token string
	if auth {
		if token, err = c.opts.Auth.Token(ctx); err != nil {
			return err
		}
	}
	res, err := c.send(ctx, r, body, token)
	if err != nil {
		return err
	}
	if res.StatusCode == http.StatusUnauthorized && auth {
		fresh, ok := c.renew(ctx, token)
		if ok {
			drain(res)
			if res, err = c.send(ctx, r, body, fresh); err != nil {
				return err
			}
		}
	}
	defer drain(res)
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return decodeError(res)
	}
	if r.Response != nil {
		if err := r.Response(res); err != nil {
			return err
		}
	}
	if r.Out == nil || res.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(res.Body).Decode(r.Out); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("wire: decode %s %s response: %w", r.Method, r.Path, err)
	}
	return nil
}

func (c *Client) send(ctx context.Context, r *Request, body []byte, token string) (*http.Response, error) {
	target := c.base + r.Path
	if len(r.Query) > 0 {
		target += "?" + r.Query.Encode()
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, target, reader)
	if err != nil {
		return nil, err
	}
	for name, values := range r.Header {
		req.Header[name] = values
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if c.opts.AppVersion != "" {
		req.Header.Set("X-App-Version", c.opts.AppVersion)
	}
	if c.opts.Language != nil {
		if lang := c.opts.Language(); lang != "" {
			req.Header.Set("Accept-Language", lang)
		}
	}
	return c.opts.HTTPClient.Do(req)
}

// renew 在 stale 被拒绝后取得新 token。别的请求已经换过 token 时直接使用；
// 否则同一个 stale 只调用一次 Auth.Refresh。
func (c *Client) renew(ctx context.Context, stale string) (string, bool) {
	c.mu.Lock()
	call, ok := c.refreshing[stale]
	if !ok {
		// 在锁内检查：刷新完成后才从 refreshing 中删除，所以这里看到的要么是正在进行的刷新，要么是新 token。
		if current, err := c.opts.Auth.Token(ctx); err == nil && current != stale && current != "" {
			c.mu.Unlock()
			return current, true
		}
		call = &refresh{done: make(chan struct{})}
		c.refreshing[stale] = call
		c.mu.Unlock()
		// 一个调用方取消不应让一起等待的其他请求失败。
		call.err = c.opts.Auth.Refresh(context.WithoutCancel(ctx))
		c.mu.Lock()
		delete(c.refreshing, stale)
		close(call.done)
	}
	c.mu.Unlock()
	select {
	case <-call.done:
	case <-ctx.Done():
		return "", false
	}
	if call.err != nil {
		return "", false
	}
	fresh, err := c.opts.Auth.Token(ctx)
	return fresh, err == nil && fresh != ""
}

func encodeBody(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	if rv := reflect.ValueOf(v); rv.Kind() == reflect.Pointer && rv.IsNil() {
		return nil, nil
	}
	return json.Marshal(v)
}

func drain(res *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
	_ = res.Body.Close()
}

func decodeError(res *http.Response) error {
	e := &Error{Status: res.StatusCode, RequestID: res.Header.Get("X-Request-ID")}
	var problem struct {
		Detail string       `json:"detail"`
		Code   string       `json:"code"`
		Errors []FieldError `json:"errors"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&problem) == nil {
		e.Code, e.Detail, e.Fields = Code(problem.Code), problem.Detail, problem.Errors
	}
	if e.Detail == "" {
		e.Detail = http.StatusText(res.StatusCode)
	}
	return e
}

// PathValue 把路径参数转换为转义后的路径片段。
func PathValue(v any) string { return url.PathEscape(format(v)) }

// SetQuery 设置查询参数；零值不发送，由服务端使用默认值。切片默认用逗号连接，explode 时每个元素一个参数。
func SetQuery(q url.Values, name string, v any, explode bool) {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() || rv.IsZero() {
		return
	}
	for rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	if rv.Kind() == reflect.Slice && rv.Type().Elem().Kind() != reflect.Uint8 {
		values := make([]string, rv.Len())
		for i := range values {
			values[i] = format(rv.Index(i).Interface())
		}
		if explode {
			q[name] = append(q[name], values...)
		} else {
			q.Set(name, strings.Join(values, ","))
		}
		return
	}
	q.Set(name, format(rv.Interface()))
}

// SetHeader 设置请求头；零值不发送。
func SetHeader(h http.Header, name string, v any) {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() || rv.IsZero() {
		return
	}
	for rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	h.Set(name, format(rv.Interface()))
}

// ParseHeader 把响应头 name 解析到 dst；响应中没有这个头时保持零值。
func ParseHeader(res *http.Response, name string, dst any) error {
	value := res.Header.Get(name)
	if value == "" {
		return nil
	}
	switch d := dst.(type) {
	case *string:
		*d = value
		return nil
	case *time.Time:
		t, err := http.ParseTime(value)
		if err != nil {
			return fmt.Errorf("wire: header %s: %w", name, err)
		}
		*d = t
		return nil
	case encoding.TextUnmarshaler:
		if err := d.UnmarshalText([]byte(value)); err != nil {
			return fmt.Errorf("wire: header %s: %w", name, err)
		}
		return nil
	}
	rv := reflect.ValueOf(dst).Elem()
	var err error
	switch rv.Kind() {
	case reflect.String:
		rv.SetString(value)
	case reflect.Bool:
		var b bool
		b, err = strconv.ParseBool(value)
		rv.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		var n int64
		n, err = strconv.ParseInt(value, 10, 64)
		rv.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		var n uint64
		n, err = strconv.ParseUint(value, 10, 64)
		rv.SetUint(n)
	case reflect.Float32, reflect.Float64:
		var f float64
		f, err = strconv.ParseFloat(value, 64)
		rv.SetFloat(f)
	default:
		err = fmt.Errorf("unsupported type %s", rv.Type())
	}
	if err != nil {
		return fmt.Errorf("wire: header %s: %w", name, err)
	}
	return nil
}

// format 把参数值转换为字符串：time.Time 用 RFC 3339，实现了 encoding.TextMarshaler 的用它，其余用 fmt。
func format(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case time.Time:
		return x.Format(time.RFC3339Nano)
	case encoding.TextMarshaler:
		if b, err := x.MarshalText(); err == nil {
			return string(b)
		}
	case fmt.Stringer:
		return x.String()
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.String:
		return rv.String()
	case reflect.Bool:
		return strconv.FormatBool(rv.Bool())
	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(rv.Float(), 'f', -1, 64)
	}
	return fmt.Sprint(v)
}
