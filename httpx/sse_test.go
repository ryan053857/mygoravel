package httpx_test

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/httpx"
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

func TestSSEJSON(t *testing.T) {
	t.Parallel()
	url, _ := startServer(t, httpx.DefaultConfig(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = httpx.SSE(w, r, func(s *httpx.Stream) error {
			_ = s.Send(httpx.Event{}, "line1\nline2")
			return s.SendJSON(httpx.Event{Name: "delta", ID: "3"}, map[string]string{"text": "a\nb"})
		})
	}))
	lines, _ := readLines(t, url, nil)
	got := collect(lines, 100, 2*time.Second)
	want := []string{"data: line1", "data: line2", "", "event: delta", "id: 3", `data: {"text":"a\nb"}`, ""}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", got)
	}
}

func TestSSESurvivesWriteTimeoutAndSendsHeartbeats(t *testing.T) {
	defer httpx.SetHeartbeatInterval(200 * time.Millisecond)()
	cfg := httpx.DefaultConfig()
	cfg.WriteTimeout = 300 * time.Millisecond
	url, _ := startServer(t, cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = httpx.SSE(w, r, func(s *httpx.Stream) error {
			select {
			case <-time.After(time.Second):
				return s.Send(httpx.Event{}, "late")
			case <-s.Done():
				return nil
			}
		})
	}))
	lines, _ := readLines(t, url, nil)
	got := strings.Join(collect(lines, 100, 3*time.Second), "|")
	if !strings.Contains(got, ": ping") || !strings.Contains(got, "data: late") {
		t.Fatalf("got %q", got)
	}
}

func TestSSEEndsWhenServerDrains(t *testing.T) {
	t.Parallel()
	cfg := httpx.DefaultConfig()
	cfg.ShutdownTimeout = 5 * time.Second
	ended := make(chan struct{})
	url, stop := startServer(t, cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = httpx.SSE(w, r, func(s *httpx.Stream) error {
			<-s.Done()
			close(ended)
			return nil
		})
	}))
	lines, _ := readLines(t, url, nil)
	_ = lines
	time.Sleep(100 * time.Millisecond)
	if took := stop(); took > time.Second {
		t.Fatalf("shutdown took %v", took)
	}
	<-ended
}

func TestSSESendAfterReturnFails(t *testing.T) {
	t.Parallel()
	escaped := make(chan *httpx.Stream, 1)
	url, _ := startServer(t, httpx.DefaultConfig(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = httpx.SSE(w, r, func(s *httpx.Stream) error {
			escaped <- s
			return nil
		})
	}))
	lines, _ := readLines(t, url, nil)
	collect(lines, 100, time.Second)
	if err := (<-escaped).Send(httpx.Event{}, "too late"); err == nil {
		t.Fatal("Send after SSE returned should fail")
	}
}

type valueKey struct{}

func TestSSEOutlivesTimeoutButEndsOnDisconnect(t *testing.T) {
	t.Parallel()
	timeout := httpx.Timeout(httpx.TimeoutConfig{Duration: 100 * time.Millisecond})
	ended := make(chan struct{})
	h := timeout(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(context.WithValue(r.Context(), valueKey{}, "kept"))
		_ = httpx.SSE(w, r, func(s *httpx.Stream) error {
			defer close(ended)
			if s.Context().Value(valueKey{}) != "kept" {
				t.Error("stream context lost request values")
			}
			select {
			case <-time.After(400 * time.Millisecond):
			case <-s.Done():
				t.Error("stream canceled by request timeout")
				return nil
			}
			_ = s.Send(httpx.Event{}, "late")
			<-s.Done() // 客户端断开后结束
			return nil
		})
	}))
	url, _ := startServer(t, httpx.DefaultConfig(), h)
	req, _ := http.NewRequest("GET", url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(resp.Body)
	if !sc.Scan() || sc.Text() != "data: late" {
		t.Fatalf("got %q", sc.Text())
	}
	resp.Body.Close()
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not end after client disconnected")
	}
}
