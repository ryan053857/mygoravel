package web

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ryan053857/mygoravel/httpx"
)

// heartbeatInterval 是 SSE 心跳（一行注释）的间隔，用于保持代理连接、尽早发现断开的客户端。
var heartbeatInterval = 15 * time.Second

// SSE 以 text/event-stream 响应请求，并在 fn 返回前保持连接。
//
// 它取消本请求的写超时（http.Server.WriteTimeout 会切断长连接），设置 X-Accel-Buffering: no
// 让 nginx 不缓冲，并在 fn 运行期间每 15 秒发送一次心跳。fn 应当在 Stream.Done 关闭时返回：
// 客户端断开、请求 context 结束，或服务器开始排空（见 httpx.DrainStarted）。
//
// 流式路由不能安装 httpx.Timeout。SSE 既可以回应 hx-sse:connect 的长连接，
// 也可以回应普通的 hx-post 等请求，逐步推送进度。
func SSE(w http.ResponseWriter, r *http.Request, fn func(s *Stream) error) error {
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		return err
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if err := rc.Flush(); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	if drain := httpx.DrainStarted(r.Context()); drain != nil {
		go func() {
			select {
			case <-drain:
				cancel()
			case <-ctx.Done():
			}
		}()
	}

	s := &Stream{w: w, rc: rc, ctx: ctx, lastEventID: r.Header.Get("Last-Event-ID")}
	heartbeat := make(chan struct{})
	go func() {
		defer close(heartbeat)
		t := time.NewTicker(heartbeatInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if s.write(": ping\n\n") != nil {
					cancel()
					return
				}
			}
		}
	}()
	err := fn(s)
	cancel()
	<-heartbeat
	// 处理器返回后不能再写响应：等进行中的写入结束，之后的写入一律失败。
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return err
}

// Stream 是一个 SSE 响应。Send 可以并发调用。
type Stream struct {
	mu          sync.Mutex
	closed      bool
	w           io.Writer
	rc          *http.ResponseController
	ctx         context.Context
	lastEventID string
}

// Event 是一条消息的可选字段。
type Event struct {
	// Name 是事件名。为空时 htmx 直接替换内容；有名字时 htmx 派发同名 DOM 事件，不替换。
	Name string
	// ID 在重连时由浏览器通过 Last-Event-ID 请求头发回，用于补发断线期间的消息。
	ID string
	// Retry 覆盖客户端的重连延迟。
	Retry time.Duration
}

// Context 返回在客户端断开或服务器开始排空时取消的 context。
func (s *Stream) Context() context.Context { return s.ctx }

// Done 在客户端断开或服务器开始排空时关闭。
func (s *Stream) Done() <-chan struct{} { return s.ctx.Done() }

// LastEventID 返回重连时浏览器发回的最后一个事件 ID，首次连接时为空。
func (s *Stream) LastEventID() string { return s.lastEventID }

// Send 渲染组件并作为一条普通消息发出。组件可以包含多个 <hx-partial hx-target="...">，一次更新多处。
func (s *Stream) Send(c Component) error { return s.SendEvent(Event{}, c) }

// SendEvent 渲染组件并带上 ev 中的字段发出。
func (s *Stream) SendEvent(ev Event, c Component) error {
	var buf bytes.Buffer
	if err := c.Render(s.ctx, &buf); err != nil {
		return err
	}
	var b strings.Builder
	if ev.Name != "" {
		b.WriteString("event: " + oneLine(ev.Name) + "\n")
	}
	if ev.ID != "" {
		b.WriteString("id: " + oneLine(ev.ID) + "\n")
	}
	if ev.Retry > 0 {
		b.WriteString("retry: " + strconv.FormatInt(ev.Retry.Milliseconds(), 10) + "\n")
	}
	for line := range strings.SplitSeq(buf.String(), "\n") {
		b.WriteString("data: " + strings.TrimSuffix(line, "\r") + "\n")
	}
	b.WriteString("\n")
	return s.write(b.String())
}

func (s *Stream) write(text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errStreamClosed
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if _, err := io.WriteString(s.w, text); err != nil {
		return err
	}
	return s.rc.Flush()
}

var errStreamClosed = errors.New("web: stream closed")

func oneLine(s string) string { return strings.NewReplacer("\n", " ", "\r", " ").Replace(s) }
