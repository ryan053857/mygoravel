package mailtest_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ryan053857/mygoravel/mail"
	"github.com/ryan053857/mygoravel/mail/mailtest"
)

func TestMailer(t *testing.T) {
	ctx := context.Background()
	m := mailtest.New()
	if _, ok := m.Last(); ok {
		t.Fatal("Last on empty mailer")
	}
	msg := mail.Message{To: []mail.Address{{Email: "a@example.com"}}, Subject: "s", Text: "t"}
	if err := m.Send(ctx, msg); err != nil {
		t.Fatal(err)
	}
	if err := m.Send(ctx, mail.Message{}); !errors.Is(err, mail.ErrInvalid) {
		t.Fatalf("invalid message err = %v", err)
	}
	if last, ok := m.Last(); !ok || last.Subject != "s" || len(m.Sent()) != 1 {
		t.Fatalf("sent = %+v", m.Sent())
	}

	boom := errors.New("boom")
	m.FailWith(boom)
	if err := m.Send(ctx, msg); !errors.Is(err, boom) || len(m.Sent()) != 1 {
		t.Fatalf("FailWith err = %v, sent %d", err, len(m.Sent()))
	}
	m.Reset()
	if err := m.Send(ctx, msg); err != nil || len(m.Sent()) != 1 {
		t.Fatalf("after Reset err = %v, sent %d", err, len(m.Sent()))
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := m.Send(canceled, msg); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled err = %v", err)
	}
}
