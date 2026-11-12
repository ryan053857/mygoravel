package redisbroadcast_test

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/ryan053857/mygoravel/broadcast"
	"github.com/ryan053857/mygoravel/broadcast/broadcasttest"
	"github.com/ryan053857/mygoravel/broadcast/redisbroadcast"
)

func openRedis(t *testing.T) *redis.Client {
	t.Helper()
	url := os.Getenv("REDIS_TEST_URL")
	if url == "" {
		t.Fatal("REDIS_TEST_URL is required for this test")
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(opts)
	t.Cleanup(func() { rdb.Close() })
	return rdb
}

func TestBackend(t *testing.T) {
	broadcasttest.TestBackend(t, func(t *testing.T) (broadcast.Backend, broadcast.Backend) {
		channel := "test:broadcast:" + rand.Text()
		return redisbroadcast.New(openRedis(t), channel), redisbroadcast.New(openRedis(t), channel)
	})
}

// 订阅连接被断开后，订阅以 ErrLost 结束；Backend 重新订阅，新的订阅照常收到消息。
func TestConnectionLossEndsSubscriptions(t *testing.T) {
	defer redisbroadcast.SetPingInterval(200 * time.Millisecond)()
	rdb := openRedis(t)
	channel := "test:broadcast:" + rand.Text()
	h := broadcast.New(redisbroadcast.New(rdb, channel), nil)
	defer h.Close()
	sub := h.Subscribe("room:1")
	broadcasttest.Ready(t, h)

	if err := openRedis(t).ClientKillByFilter(context.Background(), "TYPE", "pubsub").Err(); err != nil {
		t.Fatal(err)
	}
	select {
	case _, ok := <-sub.C():
		if ok || !errors.Is(sub.Err(), broadcast.ErrLost) {
			t.Fatalf("ok=%v err=%v", ok, sub.Err())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("subscription not ended after the connection was killed")
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
		"nil client":    func() { redisbroadcast.New(nil, "c") },
		"empty channel": func() { redisbroadcast.New(openRedis(t), "") },
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
