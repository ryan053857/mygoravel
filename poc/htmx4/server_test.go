package htmx4_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/httpx"
)

// S1：httpx.Timeout 会在期限到达时切断流；流式路由不装 Timeout 后保持连接。
func TestStreamAndTimeoutMiddleware(t *testing.T) {
	short := func(c *httpx.Config) { c.Timeout.Duration = time.Second }
	for _, tc := range []struct {
		name        string
		withTimeout bool
		wantClosed  bool
	}{
		{"stream route with Timeout", true, true},
		{"stream route without Timeout", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := start(t, serverOptions{configure: short, streamsUseTimeout: tc.withTimeout})
			lines := streamLines(t, s.client(t, "alice@example.com"), s.URL+"/rooms/lobby/events")
			closed := closedWithin(lines, 2500*time.Millisecond)
			if closed != tc.wantClosed {
				t.Fatalf("closed within 2.5s = %v, want %v", closed, tc.wantClosed)
			}
		})
	}
}

// S2：http.Server.WriteTimeout 会切断流；web.NewStream 用 ResponseController 取消本请求的写超时。
func TestStreamAndWriteTimeout(t *testing.T) {
	s := start(t, serverOptions{configure: func(c *httpx.Config) { c.WriteTimeout = time.Second }})
	c := s.client(t, "alice@example.com")
	lines := streamLines(t, c, s.URL+"/rooms/lobby/events")
	time.Sleep(2 * time.Second)
	s.App.Hub.Post("lobby", "bob@example.com", "after write timeout")
	if !received(lines, "after write timeout", 2*time.Second) {
		t.Fatal("stream did not survive WriteTimeout")
	}

	// 对照：不取消写超时的 Flush 循环，约 1 秒后被切断。
	cfg := httpx.DefaultConfig()
	cfg.WriteTimeout = time.Second
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go httpx.Serve(ctx, cfg, ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := range 10 {
			if _, err := fmt.Fprintf(w, "data: %d\n\n", i); err != nil {
				return
			}
			http.NewResponseController(w).Flush()
			time.Sleep(300 * time.Millisecond)
		}
	}), httpx.ServeOptions{})
	raw := streamLines(t, http.DefaultClient, "http://"+ln.Addr().String())
	var got int
	for l := range raw {
		if strings.HasPrefix(l, "data:") {
			got++
		}
	}
	t.Logf("without clearing the deadline the stream delivered %d/10 events", got)
	if got >= 10 {
		t.Fatal("expected WriteTimeout to cut the stream")
	}
}

func received(lines <-chan string, text string, d time.Duration) bool {
	deadline := time.After(d)
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				return false
			}
			if strings.Contains(l, text) {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

// S3：有打开的流时停机。没有 Drain 时 Shutdown 要等满 ShutdownTimeout；有 Drain 时立即结束。
func TestShutdownWithOpenStreams(t *testing.T) {
	for _, tc := range []struct {
		name    string
		noDrain bool
	}{{"without drain", true}, {"with drain", false}} {
		t.Run(tc.name, func(t *testing.T) {
			s := start(t, serverOptions{noDrain: tc.noDrain, configure: func(c *httpx.Config) { c.ShutdownTimeout = 3 * time.Second }})
			lines := streamLines(t, s.client(t, "alice@example.com"), s.URL+"/rooms/lobby/events")
			_ = lines
			took := s.Stop()
			t.Logf("Serve returned after %v", took.Round(10*time.Millisecond))
			if tc.noDrain && took < 2900*time.Millisecond || !tc.noDrain && took > 500*time.Millisecond {
				t.Fatalf("took %v", took)
			}
			if !closedWithin(lines, time.Second) {
				t.Fatal("stream still open after Serve returned")
			}
		})
	}
}

// S4：流结束后访问日志才写出，带完整时长。
func TestStreamAccessLog(t *testing.T) {
	s := start(t, serverOptions{})
	lines := streamLines(t, s.client(t, "alice@example.com"), s.URL+"/rooms/lobby/events")
	time.Sleep(300 * time.Millisecond)
	if strings.Contains(s.Logs.String(), `"/rooms/lobby/events"`) {
		t.Fatal("access log written while stream open")
	}
	s.App.Hub.KickAll()
	closedWithin(lines, time.Second)
	time.Sleep(100 * time.Millisecond)
	if !strings.Contains(s.Logs.String(), "/rooms/lobby/events") {
		t.Fatalf("no access log for stream:\n%s", s.Logs.String())
	}
}

// S5：页面与 API 的 404、跨站请求、页面认证。
func TestRoutingBasics(t *testing.T) {
	s := start(t, serverOptions{})
	c := s.client(t, "alice@example.com")
	check := func(req *http.Request, status int, header, contains string) {
		t.Helper()
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != status || !strings.Contains(resp.Header.Get(header), contains) {
			t.Fatalf("%s %s: %d %s=%q", req.Method, req.URL.Path, resp.StatusCode, header, resp.Header.Get(header))
		}
	}
	req := func(method, path string, h ...string) *http.Request {
		r, _ := http.NewRequest(method, s.URL+path, nil)
		for i := 0; i < len(h); i += 2 {
			r.Header.Set(h[i], h[i+1])
		}
		return r
	}
	check(req("GET", "/nope"), 404, "Content-Type", "text/html")
	check(req("GET", "/api/nope"), 404, "Content-Type", "application/problem+json")
	check(req("POST", "/rooms/lobby/messages", "Sec-Fetch-Site", "cross-site"), 403, "Content-Type", "")
	check(req("GET", "/admin/only"), 403, "Content-Type", "text/html")
	check(req("GET", "/admin/only", "HX-Request", "true", "HX-Request-Type", "partial"), 403, "HX-Retarget", "#flash")

	anon := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	c = anon
	check(req("GET", "/admin/articles?x=1"), 303, "Location", "/login?next=%2Fadmin%2Farticles%3Fx%3D1")
	check(req("POST", "/rooms/lobby/messages", "HX-Request", "true", "HX-Current-URL", s.URL+"/rooms/lobby"), 204, "HX-Redirect", "/login?next=%2Frooms%2Flobby")
}
