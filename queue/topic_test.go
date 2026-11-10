package queue_test

import (
	"context"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ThreeDotsLabs/watermill/pubsub/gochannel"
	"github.com/google/uuid"

	"github.com/ryan053857/mygoravel/queue"
)

func TestTopic(t *testing.T) {
	type welcome struct {
		Email string `json:"email"`
	}
	topic := queue.Topic[welcome]("mail.welcome")

	var d recorder
	if err := topic.Publish(context.Background(), &d, welcome{Email: "a@example.com"}); err != nil {
		t.Fatal(err)
	}
	if len(d.jobs) != 1 || d.jobs[0].Topic != "mail.welcome" || string(d.jobs[0].Payload) != `{"email":"a@example.com"}` {
		t.Fatalf("jobs %+v", d.jobs)
	}

	pubsub := gochannel.NewGoChannel(gochannel.Config{}, watermill.NopLogger{})
	router := queue.NewRouter(nil)
	got := make(chan welcome, 1)
	topic.Handle(router, pubsub, func(_ context.Context, w welcome) error {
		got <- w
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = router.Run(ctx) }()
	<-router.Running()

	if err := pubsub.Publish("mail.welcome", message.NewMessage(uuid.NewString(), d.jobs[0].Payload)); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-got:
		if w.Email != "a@example.com" {
			t.Fatalf("payload %+v", w)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("job not handled")
	}
}
