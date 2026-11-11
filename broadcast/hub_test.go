package broadcast_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/broadcast"
	"github.com/ryan053857/mygoravel/broadcast/broadcasttest"
)

func TestHubDeliversAcrossInstances(t *testing.T) {
	backend := broadcasttest.NewBackend()
	h := broadcast.New(backend, nil)
	defer h.Close()
	sub := h.Subscribe("room:1")
	if err := h.PublishJSON(context.Background(), "room:1", map[string]string{"type": "message"}); err != nil {
		t.Fatal(err)
	}
	var ev map[string]string
	if err := json.Unmarshal(broadcasttest.Receive(t, sub).Data, &ev); err != nil || ev["type"] != "message" {
		t.Fatalf("event=%v err=%v", ev, err)
	}
	// 同一个 Backend 上的另一个 Hub 相当于另一个实例。
	other := broadcast.New(backend, nil)
	defer other.Close()
	otherSub := other.Subscribe("room:2")
	_ = h.Publish(context.Background(), "room:2", []byte("x"))
	if got := broadcasttest.Receive(t, otherSub); string(got.Data) != "x" {
		t.Fatalf("other instance received %+v", got)
	}
}

func TestBackendContract(t *testing.T) {
	broadcasttest.TestBackend(t, func(*testing.T) (broadcast.Backend, broadcast.Backend) {
		b := broadcasttest.NewBackend()
		return b, b
	})
}

func TestSlowSubscriberEnds(t *testing.T) {
	h := broadcast.New(broadcasttest.NewBackend(), nil)
	defer h.Close()
	broadcasttest.Ready(t, h) // 之后 Publish 在返回之前投递
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
	h := broadcast.New(broadcasttest.NewBackend(), nil)
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
	h := broadcast.New(broadcasttest.NewBackend(), nil)
	_ = h.Close()
	sub := h.Subscribe("room:1")
	if _, ok := <-sub.C(); ok || !errors.Is(sub.Err(), broadcast.ErrClosed) {
		t.Fatalf("err=%v", sub.Err())
	}
	sub.Close() // 重复关闭无害
}

func TestListenReceivesEveryTopic(t *testing.T) {
	h := broadcast.New(broadcasttest.NewBackend(), nil)
	defer h.Close()
	broadcasttest.Ready(t, h) // 之后的 Publish 在返回之前投递
	var got []string
	stop := h.Listen(func(m broadcast.Message) { got = append(got, m.Topic+"="+string(m.Data)) }, nil)
	sub := h.Subscribe("room:1")
	for _, topic := range []string{"room:1", "room:2"} {
		if err := h.Publish(context.Background(), topic, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if want := []string{"room:1=x", "room:2=x"}; !slices.Equal(got, want) {
		t.Fatalf("listener received %v, want %v", got, want)
	}
	broadcasttest.Receive(t, sub) // 订阅照常收到

	stop()
	stop()
	_ = h.Publish(context.Background(), "room:1", []byte("y"))
	if len(got) != 2 {
		t.Fatalf("listener received %v after stop", got)
	}
}

func TestListenIsToldAboutLostMessages(t *testing.T) {
	backend := lossy{lose: make(chan error)}
	h := broadcast.New(backend, nil)
	defer h.Close()
	lost := make(chan error, 1)
	h.Listen(func(broadcast.Message) {}, func(err error) { lost <- err })
	cause := errors.New("connection reset")
	backend.lose <- cause
	select {
	case err := <-lost:
		if !errors.Is(err, cause) {
			t.Fatalf("lost called with %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("lost was not called")
	}
}
