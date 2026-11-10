package logmail_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/ryan053857/mygoravel/mail"
	"github.com/ryan053857/mygoravel/mail/logmail"
)

func TestMailer(t *testing.T) {
	var buf bytes.Buffer
	m := logmail.New(slog.New(slog.NewTextHandler(&buf, nil)))
	err := m.Send(context.Background(), mail.Message{
		To:          []mail.Address{{Name: "Ada", Email: "ada@example.com"}, {Email: "bob@example.com"}},
		Subject:     "Welcome",
		HTML:        "<p>html</p>",
		Text:        "open https://example.com/verify",
		Attachments: []mail.Attachment{{Filename: "a.pdf"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"mail sent", `to="Ada <ada@example.com>, bob@example.com"`, "subject=Welcome", "https://example.com/verify", "a.pdf"} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q: %s", want, out)
		}
	}
	if err := m.Send(context.Background(), mail.Message{}); !errors.Is(err, mail.ErrInvalid) {
		t.Errorf("invalid message err = %v", err)
	}
}
