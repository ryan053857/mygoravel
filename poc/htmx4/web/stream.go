package web

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/a-h/templ"
)

// Stream 是一个 text/event-stream 响应。方法可以并发调用。
type Stream struct {
	mu  sync.Mutex
	w   io.Writer
	rc  *http.ResponseController
	ctx context.Context
}

// NewStream 写出 SSE 响应头并取消本请求的写超时（http.Server.WriteTimeout 会切断长连接）。
// ctx 结束（客户端断开或 drain 开始）后，Wait 返回。
func NewStream(w http.ResponseWriter, r *http.Request) (*Stream, error) {
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		return nil, err
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no") // nginx 不缓冲
	w.WriteHeader(http.StatusOK)
	if err := rc.Flush(); err != nil {
		return nil, err
	}
	return &Stream{w: w, rc: rc, ctx: r.Context()}, nil
}

// Event 是一条 SSE 消息的可选字段。
type Event struct {
	Name  string        // event:，空表示普通消息（htmx 直接替换）
	ID    string        // id:，重连时浏览器以 Last-Event-ID 发回
	Retry time.Duration // retry:，覆盖客户端的重连延迟
}

// Send 渲染组件并作为一条消息发出。组件可以包含多个 <hx-partial>。
func (s *Stream) Send(c templ.Component, ev Event) error {
	var buf bytes.Buffer
	if err := c.Render(s.ctx, &buf); err != nil {
		return err
	}
	return s.write(ev, buf.String())
}

func (s *Stream) write(ev Event, data string) error {
	var b strings.Builder
	if ev.Name != "" {
		b.WriteString("event: " + ev.Name + "\n")
	}
	if ev.ID != "" {
		b.WriteString("id: " + ev.ID + "\n")
	}
	if ev.Retry > 0 {
		b.WriteString("retry: " + strconv.FormatInt(ev.Retry.Milliseconds(), 10) + "\n")
	}
	for line := range strings.SplitSeq(data, "\n") {
		b.WriteString("data: " + strings.TrimSuffix(line, "\r") + "\n")
	}
	b.WriteString("\n")
	return s.raw(b.String())
}

func (s *Stream) raw(text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := io.WriteString(s.w, text); err != nil {
		return err
	}
	return s.rc.Flush()
}

// Heartbeat 每隔 every 发送一行注释，直到 ctx 结束，用于保持代理连接、尽早发现断开的客户端。
func (s *Stream) Heartbeat(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if s.raw(": ping\n\n") != nil {
				return
			}
		}
	}
}

// Drain 在服务器开始排空时通知所有流结束，避免 Shutdown 等满 ShutdownTimeout。
// 把 Start 传给 httpx.ServeOptions.OnDraining。
type Drain struct {
	once sync.Once
	ch   chan struct{}
}

func NewDrain() *Drain { return &Drain{ch: make(chan struct{})} }

func (d *Drain) Start()                { d.once.Do(func() { close(d.ch) }) }
func (d *Drain) Done() <-chan struct{} { return d.ch }

// Context 返回在 parent 结束或排空开始时取消的 context。
func (d *Drain) Context(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	go func() {
		select {
		case <-d.ch:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}
