package redisbroadcast_test

import (
	"context"
	"crypto/rand"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/broadcast"
	"github.com/ryan053857/mygoravel/broadcast/broadcasttest"
	"github.com/ryan053857/mygoravel/broadcast/redisbroadcast"
	"github.com/ryan053857/mygoravel/internal/redistest"
)

func TestBackend(t *testing.T) {
	broadcasttest.TestBackend(t, func(t *testing.T) (broadcast.Backend, broadcast.Backend) {
		channel := "test:broadcast:" + rand.Text()
		return redisbroadcast.New(redistest.Open(t), channel), redisbroadcast.New(redistest.Open(t), channel)
	})
}

// 订阅连接被断开后，订阅以 ErrLost 结束；Backend 重新订阅，新的订阅照常收到消息。
func TestConnectionLossEndsSubscriptions(t *testing.T) {
	defer redisbroadcast.SetPingInterval(200 * time.Millisecond)()
	name := "test-" + rand.Text()
	rdb := redistest.OpenNamed(t, name)
	channel := "test:broadcast:" + rand.Text()
	h := broadcast.New(redisbroadcast.New(rdb, channel), nil)
	defer h.Close()
	sub := h.Subscribe("room:1")
	broadcasttest.Ready(t, h)

	if err := killPubSub(t, name); err != nil {
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
		"empty channel": func() { redisbroadcast.New(redistest.Open(t), "") },
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

// killPubSub 断开名为 name 的客户端的订阅连接，不影响并行运行的其他测试。
func killPubSub(t *testing.T, name string) error {
	rdb := redistest.Open(t)
	ctx := context.Background()
	list, err := rdb.Do(ctx, rdb.B().ClientList().TypePubsub().Build()).ToString()
	if err != nil {
		return err
	}
	killed := 0
	list = strings.TrimPrefix(list, "txt:") // RESP3 的 verbatim string 带格式前缀
	for line := range strings.Lines(list) {
		fields := map[string]string{}
		for f := range strings.FieldsSeq(line) {
			if k, v, ok := strings.Cut(f, "="); ok {
				fields[k] = v
			}
		}
		if fields["name"] != name {
			continue
		}
		id, err := strconv.ParseInt(fields["id"], 10, 64)
		if err != nil {
			return err
		}
		if err := rdb.Do(ctx, rdb.B().ClientKill().Id(id).Build()).Error(); err != nil {
			return err
		}
		killed++
	}
	if killed == 0 {
		return errors.New("no subscription connection to kill")
	}
	return nil
}
