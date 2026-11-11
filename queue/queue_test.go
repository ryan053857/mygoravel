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

// recorder 是记录入队任务的驱动。
type recorder struct {
	ctx  context.Context
	jobs []queue.Job
}

func (r *recorder) Name() string { return "recorder" }

func (r *recorder) Enqueue(ctx context.Context, jobs ...queue.Job) error {
	r.ctx = ctx
	r.jobs = append(r.jobs, jobs...)
	return nil
}

func (r *recorder) Claim(context.Context, queue.ClaimRequest) ([]queue.Claim, error) { return nil, nil }

func (r *recorder) Stats(context.Context) ([]queue.TopicStats, error) { return nil, nil }

func TestPublish(t *testing.T) {
	type welcome struct {
		Email string `json:"email"`
	}
	ctx := logging.WithAttrs(context.Background(),
		slog.String(logging.AttrRequestID, "req-1"), slog.String("other", "x"))
	at := time.Date(2030, 1, 2, 3, 4, 5, 6000, time.FixedZone("CST", 8*3600))

	var d recorder
	if err := queue.Publish(ctx, &d, "mail.send", welcome{Email: "a@example.com"}, queue.At(at)); err != nil {
		t.Fatal(err)
	}
	if len(d.jobs) != 1 || d.ctx != ctx {
		t.Fatalf("enqueued %d", len(d.jobs))
	}
	j := d.jobs[0]
	if j.Topic != "mail.send" || j.ID.Version() != 7 || !j.AvailableAt.Equal(at) {
		t.Fatalf("job %+v", j)
	}
	if string(j.Payload) != `{"email":"a@example.com"}` {
		t.Fatalf("payload %s", j.Payload)
	}
	if len(j.Metadata) != 1 || j.Metadata[logging.AttrRequestID] != "req-1" {
		t.Fatalf("metadata %v", j.Metadata)
	}

	if err := queue.Publish(ctx, &d, "x", func() {}); err == nil {
		t.Fatal("expected encode error")
	}
	if err := queue.Publish(ctx, &d, "", 1); err == nil {
		t.Fatal("expected empty topic error")
	}
}

func TestPublisher(t *testing.T) {
	var d recorder
	pub := queue.NewPublisher(&d)
	msg := message.NewMessage(uuid.NewString(), []byte("x"))
	msg.Metadata.Set("k", "v")
	msg.Metadata.Set("_queue_available_at", "2030-01-01T19:04:05.000006Z")
	if err := pub.Publish("t", msg); err != nil {
		t.Fatal(err)
	}
	j := d.jobs[0]
	if j.ID.String() != msg.UUID || j.Topic != "t" || j.Metadata["k"] != "v" || len(j.Metadata) != 1 ||
		!j.AvailableAt.Equal(time.Date(2030, 1, 1, 19, 4, 5, 6000, time.UTC)) {
		t.Fatalf("job %+v", j)
	}
	if err := pub.Publish("", message.NewMessage(uuid.NewString(), nil)); err == nil {
		t.Fatal("expected empty topic error")
	}
	if err := pub.Publish("t", message.NewMessage("not-a-uuid", nil)); err == nil {
		t.Fatal("expected invalid UUID error")
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

func TestRetryAfter(t *testing.T) {
	if queue.RetryAfter(nil, time.Second) != nil {
		t.Fatal("RetryAfter(nil) != nil")
	}
	base := errors.New("rate limited")
	err := errors.Wrap(queue.RetryAfter(base, time.Second), "send")
	if !errors.Is(err, base) || err.Error() != "send: rate limited" || queue.IsPermanent(err) {
		t.Fatalf("err = %v", err)
	}
	if !queue.IsPermanent(queue.Permanent(queue.RetryAfter(base, time.Second))) {
		t.Fatal("Permanent(RetryAfter(...)) is not permanent")
	}
}

func TestAttemptFromContextOutsideJob(t *testing.T) {
	if n, final := queue.AttemptFromContext(context.Background()); n != 0 || final {
		t.Fatalf("AttemptFromContext = %d, %t", n, final)
	}
}
