package queue_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/logging"
	"github.com/ryan053857/mygoravel/queue"
)

func TestConfig(t *testing.T) {
	var cfg queue.Config
	cfg.SetDefaults()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}
	cfg.ShutdownTimeout = 0
	if err := cfg.Validate(); err != nil {
		t.Fatalf("zero shutdown_timeout: %v", err)
	}
	bad := queue.Config{ShutdownTimeout: -1}
	err := bad.Validate()
	for _, field := range []string{"concurrency", "poll_interval", "lease", "max_attempts", "shutdown_timeout"} {
		if err == nil || !strings.Contains(err.Error(), field) {
			t.Errorf("error %v does not mention %s", err, field)
		}
	}
}

// nilDB 满足 bun.IDB，用于在访问数据库之前就返回的校验。
type nilDB struct{ bun.IDB }

type recorder struct {
	topic string
	msgs  []*message.Message
}

func (r *recorder) Publish(topic string, msgs ...*message.Message) error {
	r.topic = topic
	r.msgs = append(r.msgs, msgs...)
	return nil
}

func (r *recorder) Close() error { return nil }

func TestPublish(t *testing.T) {
	type welcome struct {
		Email string `json:"email"`
	}
	ctx := logging.WithAttrs(context.Background(),
		slog.String(logging.AttrRequestID, "req-1"), slog.String("other", "x"))
	at := time.Date(2030, 1, 2, 3, 4, 5, 6000, time.FixedZone("CST", 8*3600))

	var pub recorder
	if err := queue.Publish(ctx, &pub, "mail.send", welcome{Email: "a@example.com"}, queue.At(at)); err != nil {
		t.Fatal(err)
	}
	if pub.topic != "mail.send" || len(pub.msgs) != 1 {
		t.Fatalf("published %q %d", pub.topic, len(pub.msgs))
	}
	msg := pub.msgs[0]
	id, err := uuid.Parse(msg.UUID)
	if err != nil || id.Version() != 7 {
		t.Fatalf("uuid %q version %v err %v", msg.UUID, id.Version(), err)
	}
	if string(msg.Payload) != `{"email":"a@example.com"}` {
		t.Fatalf("payload %s", msg.Payload)
	}
	if msg.Metadata.Get(logging.AttrRequestID) != "req-1" || msg.Metadata.Get("other") != "" {
		t.Fatalf("metadata %v", msg.Metadata)
	}
	if msg.Context() != ctx {
		t.Fatal("message context not set")
	}
	if got := msg.Metadata.Get("_queue_available_at"); got != "2030-01-01T19:04:05.000006Z" {
		t.Fatalf("available at %q", got)
	}

	if err := queue.Publish(ctx, &pub, "x", func() {}); err == nil {
		t.Fatal("expected encode error")
	}
}

func TestPermanent(t *testing.T) {
	if queue.Permanent(nil) != nil {
		t.Fatal("Permanent(nil) != nil")
	}
	base := errors.New("gone")
	err := errors.Wrap(queue.Permanent(base), "handle")
	if !queue.IsPermanent(err) || !errors.Is(err, base) || err.Error() != "handle: gone" {
		t.Fatalf("err = %v", err)
	}
	if queue.IsPermanent(base) {
		t.Fatal("plain error reported permanent")
	}
}

func TestPublisherRejectsInvalidMessages(t *testing.T) {
	pub := queue.NewPublisher(nilDB{})
	if err := pub.Publish("", message.NewMessage(uuid.NewString(), nil)); err == nil {
		t.Fatal("expected empty topic error")
	}
	if err := pub.Publish("t", message.NewMessage("not-a-uuid", nil)); err == nil {
		t.Fatal("expected invalid UUID error")
	}
}
