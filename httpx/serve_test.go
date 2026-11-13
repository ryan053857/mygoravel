package httpx_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-tenon/tenon/cleanup"
	"github.com/go-tenon/tenon/httpx"
)

func TestServeDrainsBeforeCleanup(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var dbClosed atomic.Bool
	var sawRequest atomic.Bool
	release := make(chan struct{})

	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		sawRequest.Store(true)
		<-release
		_, _ = io.WriteString(w, "pong")
	})

	var stack cleanup.Stack
	_ = stack.Add("db", func(context.Context) error {
		dbClosed.Store(true)
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		err := httpx.Serve(ctx, shutdownIn(time.Second), ln, mux, httpx.ServeOptions{})
		errCh <- stack.CloseWith(context.Background(), err)
	}()

	// 发起慢请求
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String() + "/ping")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()

	deadline := time.Now().Add(time.Second)
	for !sawRequest.Load() {
		if time.Now().After(deadline) {
			t.Fatal("request not seen")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if dbClosed.Load() {
		t.Fatal("db closed before shutdown started")
	}

	cancel()
	time.Sleep(30 * time.Millisecond)
	if dbClosed.Load() {
		t.Fatal("db must stay open until Shutdown finishes draining")
	}
	close(release)

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serve did not stop")
	}
	if !dbClosed.Load() {
		t.Fatal("db should close after serve returns")
	}
}

// 长连接监听 DrainStarted 主动结束后，Serve 不必等满 ShutdownTimeout。
func TestDrainStartedEndsLongRequests(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-httpx.DrainStarted(r.Context()):
		case <-time.After(10 * time.Second):
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- httpx.Serve(ctx, shutdownIn(5*time.Second), ln, handler, httpx.ServeOptions{}) }()
	go func() {
		if resp, err := http.Get("http://" + ln.Addr().String()); err == nil {
			resp.Body.Close()
		}
	}()
	<-entered
	began := time.Now()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if took := time.Since(began); took > time.Second {
		t.Fatalf("Serve took %v to stop", took)
	}
	if httpx.DrainStarted(context.Background()) != nil {
		t.Fatal("DrainStarted outside Serve should be nil")
	}
}

// 连接在排空之前已经被接收、请求在排空开始之后才到：请求照常处理，而不是被 net/http 直接关闭。
// 不停机升级时旧进程直到交接那一刻都在接收连接。
func TestServeAnswersRequestsOnAcceptedConns(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan struct{})
	counted := &countingListener{Listener: ln, accepted: accepted}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- httpx.Serve(ctx, httpx.DefaultConfig(), counted, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "ok")
		}), httpx.ServeOptions{})
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	<-accepted
	cancel()
	time.Sleep(50 * time.Millisecond) // 排空已经开始

	if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: test\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(resp), "HTTP/1.1 200 OK") || !strings.HasSuffix(string(resp), "ok") {
		t.Fatalf("response = %q, want 200 ok", resp)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("Serve = %v", err)
	}
}

type countingListener struct {
	net.Listener
	accepted chan struct{}
	once     atomic.Bool
}

func (l *countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil && l.once.CompareAndSwap(false, true) {
		close(l.accepted)
	}
	return c, err
}
