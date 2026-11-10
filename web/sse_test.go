package web_test

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/web"
)

// startServer 用 httpx.Serve 运行 handler，返回地址和停止函数（返回 Serve 用时）。
func startServer(t *testing.T, cfg httpx.Config, handler http.Handler) (string, func() time.Duration) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- httpx.Serve(ctx, cfg, ln, handler, httpx.ServeOptions{}) }()
	stopped := false
	stop := func() time.Duration {
		if stopped {
			return 0
		}
		stopped = true
		began := time.Now()
		cancel()
		<-done
		return time.Since(began)
	}
	t.Cleanup(func() { stop() })
	return "http://" + ln.Addr().String(), stop
}

func readLines(t *testing.T, url string, header http.Header) (<-chan string, *http.Response) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	lines := make(chan string, 64)
	go func() {
		defer close(lines)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	return lines, resp
}

func collect(lines <-chan string, n int, d time.Duration) []string {
	var got []string
	deadline := time.After(d)
	for len(got) < n {
		select {
		case l, ok := <-lines:
			if !ok {
				return got
			}
			got = append(got, l)
		case <-deadline:
			return got
		}
	}
	return got
}

func TestSSEFormat(t *testing.T) {
	url, _ := startServer(t, httpx.DefaultConfig(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = web.SSE(w, r, func(s *web.Stream) error {
			if s.LastEventID() != "7" {
				t.Errorf("LastEventID = %q", s.LastEventID())
			}
			_ = s.Send(text("line1\nline2"))
			return s.SendEvent(web.Event{Name: "done\nx", ID: "8", Retry: 2 * time.Second}, text("bye"))
		})
	}))
	lines, resp := readLines(t, url, http.Header{"Last-Event-Id": {"7"}})
	got := collect(lines, 100, 2*time.Second)
	want := []string{"data: line1", "data: line2", "", "event: done x", "id: 8", "retry: 2000", "data: bye", ""}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", got)
	}
	if resp.Header.Get("Content-Type") != "text/event-stream" || resp.Header.Get("X-Accel-Buffering") != "no" || resp.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("headers: %v", resp.Header)
	}
}

func TestSSEInsidePagesHandle(t *testing.T) {
	h := handle(pages(), func(w http.ResponseWriter, r *http.Request) error {
		return web.SSE(w, r, func(s *web.Stream) error { return s.Send(text("hi")) })
	})
	url, _ := startServer(t, httpx.DefaultConfig(), h)
	lines, _ := readLines(t, url, nil)
	if got := collect(lines, 100, time.Second); len(got) == 0 || got[0] != "data: hi" {
		t.Fatalf("got %q", got)
	}
}
