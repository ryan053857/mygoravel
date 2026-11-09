package httpx_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/cleanup"
	"github.com/ryan053857/mygoravel/httpx"
)

func TestServeDrainsBeforeCleanup(t *testing.T) {
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
		err := httpx.Serve(ctx, httpx.Config{ShutdownTimeout: time.Second}, ln, mux, httpx.ServeOptions{})
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
