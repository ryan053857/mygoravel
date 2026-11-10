package web

import (
	"bytes"
	"context"
	"net/http"

	"github.com/a-h/templ"

	"github.com/ryan053857/mygoravel/httpx"
)

// SSE 以 text/event-stream 响应请求，并在 fn 返回前保持连接；连接管理见 httpx.SSE。
//
// 它既可以回应 hx-sse:connect 的长连接，也可以回应普通的 hx-post 等请求，逐步推送进度。
func SSE(w http.ResponseWriter, r *http.Request, fn func(s *Stream) error) error {
	return httpx.SSE(w, r, func(s *httpx.Stream) error { return fn(&Stream{s}) })
}

// Stream 是一个发送 templ 组件的 SSE 响应。Send 可以并发调用。
type Stream struct{ s *httpx.Stream }

// Event 是一条消息的可选字段。Name 为空时 htmx 直接替换内容；有名字时 htmx 派发同名 DOM 事件，不替换。
type Event = httpx.Event

// Context 返回在客户端断开或服务器开始排空时取消的 context。
func (s *Stream) Context() context.Context { return s.s.Context() }

// Done 在客户端断开或服务器开始排空时关闭。
func (s *Stream) Done() <-chan struct{} { return s.s.Done() }

// LastEventID 返回重连时浏览器发回的最后一个事件 ID，首次连接时为空。
func (s *Stream) LastEventID() string { return s.s.LastEventID() }

// Send 渲染组件并作为一条普通消息发出。组件可以包含多个 <hx-partial hx-target="...">，一次更新多处。
func (s *Stream) Send(c templ.Component) error { return s.SendEvent(Event{}, c) }

// SendEvent 渲染组件并带上 ev 中的字段发出。
func (s *Stream) SendEvent(ev Event, c templ.Component) error {
	var buf bytes.Buffer
	if err := c.Render(s.Context(), &buf); err != nil {
		return err
	}
	return s.s.Send(ev, buf.String())
}
