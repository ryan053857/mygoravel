package natsbroadcast_test

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/ryan053857/mygoravel/broadcast"
	"github.com/ryan053857/mygoravel/broadcast/broadcasttest"
	"github.com/ryan053857/mygoravel/broadcast/natsbroadcast"
)

func connect(t *testing.T) *nats.Conn {
	t.Helper()
	url := os.Getenv("NATS_TEST_URL")
	if url == "" {
		t.Fatal("NATS_TEST_URL is required for this test")
	}
	nc, err := nats.Connect(url, nats.ReconnectWait(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	return nc
}

func prefix() string { return "test.broadcast." + strings.ToLower(rand.Text()) }

func TestBackend(t *testing.T) {
	broadcasttest.TestBackend(t, func(t *testing.T) (broadcast.Backend, broadcast.Backend) {
		p := prefix()
		return natsbroadcast.New(connect(t), p), natsbroadcast.New(connect(t), p)
	})
}

// 连接重连后，订阅以 ErrLost 结束；Backend 重新订阅，新的订阅照常收到消息。
func TestReconnectEndsSubscriptions(t *testing.T) {
	defer natsbroadcast.SetCheckInterval(50 * time.Millisecond)()
	nc := connect(t)
	h := broadcast.New(natsbroadcast.New(nc, prefix()), nil)
	defer h.Close()
	sub := h.Subscribe("room:1")
	broadcasttest.Ready(t, h)

	if err := nc.ForceReconnect(); err != nil {
		t.Fatal(err)
	}
	select {
	case _, ok := <-sub.C():
		if ok || !errors.Is(sub.Err(), broadcast.ErrLost) {
			t.Fatalf("ok=%v err=%v", ok, sub.Err())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("subscription not ended after reconnect")
	}
	again := h.Subscribe("room:1")
	broadcasttest.Ready(t, h)
	if err := h.Publish(context.Background(), "room:1", []byte("back")); err != nil {
		t.Fatal(err)
	}
	if got := broadcasttest.Receive(t, again); string(got.Data) != "back" {
		t.Fatalf("got %q", got.Data)
	}
}

func TestNewPanics(t *testing.T) {
	for name, f := range map[string]func(){
		"nil connection":  func() { natsbroadcast.New(nil, "p") },
		"wildcard prefix": func() { natsbroadcast.New(connect(t), "blog.>") },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected panic")
				}
			}()
			f()
		})
	}
}
