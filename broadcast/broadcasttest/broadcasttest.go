// Package broadcasttest 提供 broadcast.Backend 实现的契约测试。
package broadcasttest

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/broadcast"
)

// TestBackend 检查 Backend 的共同行为。newBackends 返回连到同一个后端（同一张表、同一个频道）的
// 两个新 Backend，模拟两个实例；每次调用都应当使用独立的命名空间，测试之间互不干扰。
func TestBackend(t *testing.T, newBackends func(t *testing.T) (a, b broadcast.Backend)) {
	t.Run("delivers to every instance", func(t *testing.T) {
		a, b := hubs(t, newBackends)
		subA, subB := a.Subscribe("room:1"), b.Subscribe("room:1")
		other := b.Subscribe("room:2")
		Ready(t, a, b)
		publish(t, a, "room:1", "hello")
		for name, sub := range map[string]*broadcast.Subscription{"publisher": subA, "other instance": subB} {
			if got := Receive(t, sub); got.Topic != "room:1" || string(got.Data) != "hello" {
				t.Fatalf("%s received %+v", name, got)
			}
		}
		select {
		case m := <-other.C():
			t.Fatalf("other topic received %+v", m)
		case <-time.After(200 * time.Millisecond):
		}
	})

	t.Run("keeps publish order", func(t *testing.T) {
		a, b := hubs(t, newBackends)
		sub := b.Subscribe("room:1", "room:2")
		Ready(t, a, b)
		for i := range 40 {
			publish(t, a, fmt.Sprintf("room:%d", i%2+1), fmt.Sprint(i))
		}
		for i := range 40 {
			if got := Receive(t, sub); string(got.Data) != fmt.Sprint(i) {
				t.Fatalf("message %d = %q", i, got.Data)
			}
		}
	})

	t.Run("closed subscriptions stop", func(t *testing.T) {
		a, b := hubs(t, newBackends)
		sub := b.Subscribe("room:1")
		Ready(t, a, b)
		sub.Close()
		publish(t, a, "room:1", "after close")
		if _, ok := <-sub.C(); ok || !errors.Is(sub.Err(), broadcast.ErrClosed) {
			t.Fatalf("closed subscription: err=%v", sub.Err())
		}
		kept := b.Subscribe("room:1")
		if err := b.Close(); err != nil {
			t.Fatal(err)
		}
		for range kept.C() { // 关闭前已到达的消息仍可读出，读完后通道关闭
		}
		if !errors.Is(kept.Err(), broadcast.ErrClosed) {
			t.Fatalf("hub close: err=%v", kept.Err())
		}
	})
}

func hubs(t *testing.T, newBackends func(t *testing.T) (a, b broadcast.Backend)) (*broadcast.Hub, *broadcast.Hub) {
	t.Helper()
	ba, bb := newBackends(t)
	a, b := broadcast.New(ba, nil), broadcast.New(bb, nil)
	t.Cleanup(func() { _, _ = a.Close(), b.Close() })
	return a, b
}

func publish(t *testing.T, h *broadcast.Hub, topic, data string) {
	t.Helper()
	if err := h.Publish(context.Background(), topic, []byte(data)); err != nil {
		t.Fatal(err)
	}
}

// Receive 在 5 秒内从 sub 读取一条消息。
func Receive(t *testing.T, sub *broadcast.Subscription) broadcast.Message {
	t.Helper()
	select {
	case m, ok := <-sub.C():
		if !ok {
			t.Fatalf("subscription ended: %v", sub.Err())
		}
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("no message within 5s")
	}
	return broadcast.Message{}
}

// Ready 等到 hubs 都已经开始接收：反复从第一个 Hub 发布探测消息，直到每个 Hub 都收到一条。
// Backend 在后台开始监听，刚创建的 Hub 可能还收不到消息；重新订阅之前 Backend 报告的丢失会结束探测订阅，这时换一个新的。
func Ready(t *testing.T, hubs ...*broadcast.Hub) {
	t.Helper()
	for _, h := range hubs {
		probe := h.Subscribe("broadcasttest:ready")
		deadline := time.Now().Add(10 * time.Second)
		for ready := false; !ready; {
			if time.Now().After(deadline) {
				t.Fatal("hub did not start receiving within 10s")
			}
			publish(t, hubs[0], "broadcasttest:ready", "")
			select {
			case _, ok := <-probe.C():
				if ok {
					ready = true
				} else {
					probe = h.Subscribe("broadcasttest:ready")
				}
			case <-time.After(100 * time.Millisecond):
			}
		}
		probe.Close()
	}
}
