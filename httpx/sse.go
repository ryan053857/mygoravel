package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// heartbeatInterval 是 SSE 心跳（一行注释）的间隔，用于保持代理连接、尽早发现断开的客户端。
var heartbeatInterval = 15 * time.Second

// credentialInterval 是长连接期间重新校验凭据的间隔，见 CredentialCheck。
var credentialInterval = 30 * time.Second

// credentialTimeout 是一次凭据校验的时限，超时视为暂时无法判断。
const credentialTimeout = 10 * time.Second

// SSE 以 text/event-stream 响应请求，并在 fn 返回前保持连接。
//
// 长连接不受请求时限约束：它取消本请求的写超时（http.Server.WriteTimeout），也不受 Timeout
// 中间件的超时影响，所以流式路由可以和普通路由装在同一组中间件下。它设置 X-Accel-Buffering: no
// 让 nginx 不缓冲，并在 fn 运行期间每 15 秒发送一次心跳。
//
// 认证中间件安装了 CredentialCheck 时（rest.RequireAuth、web 的页面路由），每 30 秒重新校验一次凭据：
// 会话或 token 被撤销、用户被停用或失去所需的 scope 后，连接最多 30 秒内结束，客户端重连时按普通请求重新认证。
//
// fn 应当在 Stream.Done 关闭时返回：客户端断开、服务器开始排空（见 DrainStarted），或凭据失效。
//
// web.SSE 和 rest.SSE 分别在它之上发送 HTML 片段和 JSON。
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

	// 流已经开始，请求的处理时限不再适用：摆脱 Timeout 的超时，客户端断开时仍然结束。
	ctx, cancel := withoutTimeout(r.Context())
	defer cancel()
	if drain := DrainStarted(r.Context()); drain != nil {
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
		check := time.NewTicker(credentialInterval)
		defer check.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if s.write(": ping\n\n") != nil {
					cancel()
					return
				}
			case <-check.C:
				cctx, done := context.WithTimeout(ctx, credentialTimeout)
				err := CheckCredential(cctx)
				done()
				if err != nil {
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
	// Name 是事件名，客户端按它分发；为空时是默认的 message 事件。
	Name string
	// ID 在重连时由客户端通过 Last-Event-ID 请求头发回，用于补发断线期间的消息。
	ID string
	// Retry 覆盖客户端的重连延迟。
	Retry time.Duration
}

// Context 返回在客户端断开、服务器开始排空或凭据失效时取消的 context。
func (s *Stream) Context() context.Context { return s.ctx }

// Done 在客户端断开、服务器开始排空或凭据失效时关闭。
func (s *Stream) Done() <-chan struct{} { return s.ctx.Done() }

// LastEventID 返回重连时客户端发回的最后一个事件 ID，首次连接时为空。
func (s *Stream) LastEventID() string { return s.lastEventID }

// Send 发出一条消息，data 中的换行拆成多个 data 行。
func (s *Stream) Send(ev Event, data string) error {
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
	for line := range strings.SplitSeq(data, "\n") {
		b.WriteString("data: " + strings.TrimSuffix(line, "\r") + "\n")
	}
	b.WriteString("\n")
	return s.write(b.String())
}

// SendJSON 把 v 编码为一行 JSON 发出。
func (s *Stream) SendJSON(ev Event, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.Send(ev, string(b))
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

var errStreamClosed = errors.New("httpx: stream closed")

func oneLine(s string) string { return strings.NewReplacer("\n", " ", "\r", " ").Replace(s) }
