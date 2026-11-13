package mail_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cockroachdb/errors"

	"github.com/go-tenon/tenon/logging/logtest"
	"github.com/go-tenon/tenon/mail"
	"github.com/go-tenon/tenon/mail/mailtest"
)

func message() mail.Message {
	return mail.Message{To: []mail.Address{{Email: "alice@example.com"}}, Subject: "验证码", Text: "123456"}
}

func TestFailover(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		primary, backup := mailtest.New(), mailtest.New()
		rec := logtest.New()
		m := mail.Failover([]mail.Named{{Name: "resend", Mailer: primary}, {Name: "ses", Mailer: backup}},
			mail.FailoverOptions{Cooldown: time.Minute, Logger: rec.Logger()})

		// 主发送器正常时只用它。
		if err := m.Send(ctx, message()); err != nil {
			t.Fatal(err)
		}
		if len(primary.Sent()) != 1 || len(backup.Sent()) != 0 || len(rec.Records()) != 0 {
			t.Fatalf("primary %d, backup %d, logs %v", len(primary.Sent()), len(backup.Sent()), rec.Records())
		}

		// 主发送器失败：换用备用的，记录一条 Warn。
		primary.FailWith(errors.New("resend: 503 service unavailable"))
		if err := m.Send(ctx, message()); err != nil {
			t.Fatal(err)
		}
		if len(backup.Sent()) != 1 {
			t.Fatalf("backup %d", len(backup.Sent()))
		}
		r := rec.Records()[0]
		if r.Level != slog.LevelWarn || r.Attr("sent_by") != "ses" || r.Attr("component") != "mail" ||
			!strings.Contains(r.Attr("error.message").(string), "resend: 503") {
			t.Fatalf("record %+v", r)
		}

		// 冷却期内直接用备用的，主发送器即使已经恢复也不试。
		primary.FailWith(nil)
		if err := m.Send(ctx, message()); err != nil {
			t.Fatal(err)
		}
		if len(primary.Sent()) != 1 || len(backup.Sent()) != 2 || len(rec.Records()) != 1 {
			t.Fatalf("during cooldown: primary %d, backup %d, logs %d", len(primary.Sent()), len(backup.Sent()), len(rec.Records()))
		}

		// 冷却期过后重新先试主发送器，它恢复了就继续用，并记录恢复。
		time.Sleep(time.Minute)
		if err := m.Send(ctx, message()); err != nil {
			t.Fatal(err)
		}
		if len(primary.Sent()) != 2 || len(backup.Sent()) != 2 {
			t.Fatalf("after cooldown: primary %d, backup %d", len(primary.Sent()), len(backup.Sent()))
		}
		if r := rec.Records()[1]; r.Level != slog.LevelInfo || r.Message != "mailer recovered" || r.Attr("mailer") != "resend" {
			t.Fatalf("record %+v", r)
		}
	})
}

func TestFailoverAllFail(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		a, b := mailtest.New(), mailtest.New()
		a.FailWith(errors.New("connection refused"))
		b.FailWith(errors.New("quota exceeded"))
		m := mail.Failover([]mail.Named{{Name: "smtp", Mailer: a}, {Name: "ses", Mailer: b}}, mail.FailoverOptions{Logger: slog.New(slog.DiscardHandler)})

		err := m.Send(ctx, message())
		if err == nil || !strings.Contains(err.Error(), "mail: smtp: connection refused") || !strings.Contains(err.Error(), "mail: ses: quota exceeded") {
			t.Fatalf("err %v", err)
		}
		// 都在冷却中时仍然按顺序尝试，恢复的那个照样能发出。
		b.FailWith(nil)
		if err := m.Send(ctx, message()); err != nil || len(b.Sent()) != 1 {
			t.Fatalf("err %v, sent %d", err, len(b.Sent()))
		}
	})
}

func TestFailoverStopsEarly(t *testing.T) {
	ctx := context.Background()
	primary, backup := mailtest.New(), mailtest.New()
	m := mail.Failover([]mail.Named{{Name: "resend", Mailer: primary}, {Name: "ses", Mailer: backup}}, mail.FailoverOptions{})

	// 邮件不合法：换哪个发送器都一样，不再尝试。
	bad := message()
	bad.Subject = ""
	if err := m.Send(ctx, bad); !errors.Is(err, mail.ErrInvalid) {
		t.Fatalf("invalid: %v", err)
	}
	// 调用方已经不再等待：不再尝试。
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := m.Send(canceled, message()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v", err)
	}
	if len(backup.Sent()) != 0 {
		t.Fatalf("backup used %d times", len(backup.Sent()))
	}
	// 这两种失败不让主发送器进入冷却。
	if err := m.Send(ctx, message()); err != nil || len(primary.Sent()) != 1 {
		t.Fatalf("err %v, primary %d", err, len(primary.Sent()))
	}
}

func TestFailoverPanics(t *testing.T) {
	ok := mailtest.New()
	for name, mailers := range map[string][]mail.Named{
		"empty":     nil,
		"no name":   {{Mailer: ok}},
		"nil":       {{Name: "smtp"}},
		"duplicate": {{Name: "smtp", Mailer: ok}, {Name: "smtp", Mailer: ok}},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: no panic", name)
				}
			}()
			mail.Failover(mailers, mail.FailoverOptions{})
		}()
	}
}
