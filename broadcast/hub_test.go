package broadcast_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/broadcast"
	"github.com/ryan053857/mygoravel/broadcast/broadcasttest"
)

func TestMemory(t *testing.T) {
	h := broadcast.New(broadcast.Memory(), nil)
	defer h.Close()
	sub := h.Subscribe("room:1")
	if err := h.PublishJSON(context.Background(), "room:1", map[string]string{"type": "message"}); err != nil {
		t.Fatal(err)
	}
	var ev map[string]string
	if err := json.Unmarshal(broadcasttest.Receive(t, sub).Data, &ev); err != nil || ev["type"] != "message" {
		t.Fatalf("event=%v err=%v", ev, err)
	}
	// 不同的 Hub 不互通。
	other := broadcast.New(broadcast.Memory(), nil)
	defer other.Close()
	otherSub := other.Subscribe("room:1")
	_ = h.Publish(context.Background(), "room:1", nil)
	select {
	case m := <-otherSub.C():
		t.Fatalf("separate memory hub received %+v", m)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSlowSubscriberEnds(t *testing.T) {
	h := broadcast.New(broadcast.Memory(), nil)
	defer h.Close()
	slow, fast := h.Subscribe("room:1"), h.Subscribe("room:1")
	ctx := context.Background()
	for range 100 {
		_ = h.Publish(ctx, "room:1", []byte("x"))
		for len(fast.C()) > 0 {
			<-fast.C()
		}
	}
	n := 0
	for range slow.C() {
		n++
	}
	if !errors.Is(slow.Err(), broadcast.ErrSlow) || n != 64 {
		t.Fatalf("slow: err=%v buffered=%d", slow.Err(), n)
	}
	if fast.Err() != nil {
		t.Fatalf("fast subscription ended: %v", fast.Err())
	}
}

// lossy 是一个可以手动报告丢失的 Backend。
type lossy struct {
	lose chan error
}

func (lossy) Publish(context.Context, broadcast.Message) error { return nil }

func (l lossy) Listen(ctx context.Context, _ func(broadcast.Message), lost func(error)) {
	for {
		select {
		case <-ctx.Done():
			return
		case err := <-l.lose:
			lost(err)
		}
	}
}

func TestLostEndsAllSubscriptions(t *testing.T) {
	backend := lossy{lose: make(chan error)}
	h := broadcast.New(backend, nil)
	defer h.Close()
	a, b := h.Subscribe("room:1"), h.Subscribe("room:2", "room:3")
	cause := errors.New("connection reset")
	backend.lose <- cause
	for _, sub := range []*broadcast.Subscription{a, b} {
		if _, ok := <-sub.C(); ok || !errors.Is(sub.Err(), broadcast.ErrLost) {
			t.Fatalf("err=%v", sub.Err())
		}
	}
	// 之后的订阅照常工作。
	if h.Subscribe("room:1").Err() != nil {
		t.Fatal("new subscription ended")
	}
}

func TestTopics(t *testing.T) {
	for _, topic := range []string{"room:42", "room.42", "a_b-c", "x"} {
		if err := broadcast.ValidateTopic(topic); err != nil {
			t.Errorf("%q: %v", topic, err)
		}
	}
	for _, topic := range []string{"", "room 42", "room*", "room>", ".room", "room.", "a..b", "房间", string(make([]byte, 201))} {
		if broadcast.ValidateTopic(topic) == nil {
			t.Errorf("%q accepted", topic)
		}
	}
	h := broadcast.New(broadcast.Memory(), nil)
	defer h.Close()
	if h.Publish(context.Background(), "bad topic", nil) == nil {
		t.Error("Publish accepted an invalid topic")
	}
	for name, f := range map[string]func(){
		"no topics":     func() { h.Subscribe() },
		"invalid topic": func() { h.Subscribe("bad topic") },
		"nil backend":   func() { broadcast.New(nil, nil) },
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

func TestSubscribeAfterClose(t *testing.T) {
	h := broadcast.New(broadcast.Memory(), nil)
	_ = h.Close()
	sub := h.Subscribe("room:1")
	if _, ok := <-sub.C(); ok || !errors.Is(sub.Err(), broadcast.ErrClosed) {
		t.Fatalf("err=%v", sub.Err())
	}
	sub.Close() // 重复关闭无害
}
