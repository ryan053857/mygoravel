package htmx4_test

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/poc/htmx4/app"
)

// server 是一个运行在随机端口上的演示应用。
type server struct {
	URL  string
	App  *app.App
	Logs *syncBuffer
	stop context.CancelFunc
	done chan error
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type serverOptions struct {
	configure         func(*httpx.Config)
	streamsUseTimeout bool
	noDrain           bool
}

func start(t *testing.T, o serverOptions) *server {
	t.Helper()
	cfg := httpx.DefaultConfig()
	if o.configure != nil {
		o.configure(&cfg)
	}
	logs := &syncBuffer{}
	logger := slog.New(slog.NewJSONHandler(logs, nil))
	a := app.New(app.Options{HTTP: cfg, Logger: logger, StreamsUseTimeout: o.streamsUseTimeout})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	s := &server{URL: "http://" + ln.Addr().String(), App: a, Logs: logs, stop: stop, done: make(chan error, 1)}
	opts := httpx.ServeOptions{Logger: logger, OnDraining: a.Drain.Start}
	if o.noDrain {
		opts.OnDraining = nil
	}
	go func() { s.done <- httpx.Serve(ctx, cfg, ln, a.Handler, opts) }()
	t.Cleanup(func() { s.Stop() })
	return s
}

// Stop 停止服务器并返回 Serve 退出所用的时间。
func (s *server) Stop() time.Duration {
	if s.stop == nil {
		return 0
	}
	began := time.Now()
	s.stop()
	s.stop = nil
	<-s.done
	return time.Since(began)
}

// client 返回已登录的 HTTP 客户端，不跟随跳转。
func (s *server) client(t *testing.T, email string) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.PostForm(s.URL+"/login", url.Values{"email": {email}, "password": {"secret"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login status %d", resp.StatusCode)
	}
	return c
}

// streamLines 打开 path 的 SSE 流，把收到的行发到通道，流结束时关闭通道。
func streamLines(t *testing.T, c *http.Client, url string) <-chan string {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream status %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	ch := make(chan string, 64)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		buf := make([]byte, 4096)
		var pending string
		for {
			n, err := resp.Body.Read(buf)
			pending += string(buf[:n])
			for {
				i := strings.IndexByte(pending, '\n')
				if i < 0 {
					break
				}
				ch <- pending[:i]
				pending = pending[i+1:]
			}
			if err != nil {
				return
			}
		}
	}()
	return ch
}

// closedWithin 等待流结束，返回是否在 d 内结束。
func closedWithin(ch <-chan string, d time.Duration) bool {
	deadline := time.After(d)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return true
			}
		case <-deadline:
			return false
		}
	}
}
