package queue

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/cockroachdb/errors"
)

func TestBackoff(t *testing.T) {
	for attempts, want := range map[int]time.Duration{1: time.Second, 2: 16 * time.Second, 5: 625 * time.Second} {
		for range 100 {
			got := backoff(attempts)
			if got < want*9/10 || got > want*11/10 {
				t.Fatalf("backoff(%d) = %v, want %v ±10%%", attempts, got, want)
			}
		}
	}
	for _, attempts := range []int{18, 1000, math.MaxInt} {
		if got := backoff(attempts); got != maxBackoff {
			t.Fatalf("backoff(%d) = %v, want %v", attempts, got, maxBackoff)
		}
	}
}

func TestRecordError(t *testing.T) {
	boom := errors.New("boom")
	h := recordError(func(*message.Message) ([]*message.Message, error) { return nil, boom })

	o := &outcome{}
	msg := message.NewMessage("1", nil)
	msg.SetContext(context.WithValue(context.Background(), outcomeKey{}, o))
	if _, err := h(msg); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if !errors.Is(o.err, boom) {
		t.Fatalf("recorded = %v", o.err)
	}

	// 不是 Subscriber 交付的消息没有 outcome，照常返回错误。
	if _, err := h(message.NewMessage("2", nil)); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}
