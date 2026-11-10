package mail_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/cockroachdb/errors"

	"github.com/ryan053857/mygoravel/config"
	"github.com/ryan053857/mygoravel/mail"
)

func TestAddressText(t *testing.T) {
	for text, want := range map[string]mail.Address{
		"ada@example.com":                   {Email: "ada@example.com"},
		"Ada <ada@example.com>":             {Name: "Ada", Email: "ada@example.com"},
		`"Lovelace, Ada" <ada@example.com>`: {Name: "Lovelace, Ada", Email: "ada@example.com"},
		"博客 <no-reply@example.com>":         {Name: "博客", Email: "no-reply@example.com"},
	} {
		var got mail.Address
		if err := got.UnmarshalText([]byte(text)); err != nil || got != want {
			t.Errorf("%q = %+v, %v", text, got, err)
		}
		var back mail.Address
		if err := back.UnmarshalText([]byte(got.String())); err != nil || back != want {
			t.Errorf("round trip %q via %q = %+v, %v", text, got.String(), back, err)
		}
	}
	var zero mail.Address
	if err := zero.UnmarshalText([]byte(" ")); err != nil || !zero.IsZero() {
		t.Errorf("blank = %+v, %v", zero, err)
	}
	if _, err := mail.ParseAddress("not an address"); !errors.Is(err, mail.ErrInvalid) {
		t.Errorf("invalid address err = %v", err)
	}
}

func TestConfigAddress(t *testing.T) {
	type root struct {
		From mail.Address `yaml:"from"`
	}
	p := filepath.Join(t.TempDir(), "app.yaml")
	if err := os.WriteFile(p, []byte("from: Blog <no-reply@example.com>\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load[root](p)
	if err != nil || cfg.From != (mail.Address{Name: "Blog", Email: "no-reply@example.com"}) {
		t.Fatalf("cfg = %+v, %v", cfg, err)
	}
}

func valid() mail.Message {
	return mail.Message{
		From:    mail.Address{Name: "Blog", Email: "no-reply@example.com"},
		To:      []mail.Address{{Name: "Ada", Email: "ada@example.com"}},
		Cc:      []mail.Address{{Email: "cc@example.com"}},
		ReplyTo: mail.Address{Email: "support@example.com"},
		Subject: "Welcome",
		HTML:    "<p>Hi</p>",
		Text:    "Hi",
		Attachments: []mail.Attachment{
			{Filename: "a.txt", ContentType: "text/plain", Content: []byte("hello")},
		},
		Headers: map[string]string{"X-Campaign": "welcome"},
	}
}

func TestMessageJSONRoundTrip(t *testing.T) {
	msg := valid()
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	var back mail.Message
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, msg) {
		t.Fatalf("round trip = %+v\njson %s", back, data)
	}
	data, _ = json.Marshal(mail.Message{To: []mail.Address{{Email: "a@example.com"}}, Subject: "s", Text: "t"})
	if want := `{"to":["a@example.com"],"subject":"s","text":"t"}`; string(data) != want {
		t.Fatalf("minimal json = %s", data)
	}
}

func TestMessageValidate(t *testing.T) {
	if err := valid().Validate(); err != nil {
		t.Fatalf("valid: %v", err)
	}
	noFrom := valid()
	noFrom.From = mail.Address{}
	if err := noFrom.Validate(); err != nil {
		t.Fatalf("empty from should be allowed: %v", err)
	}
	for name, mutate := range map[string]func(*mail.Message){
		"no recipient":      func(m *mail.Message) { m.To = nil },
		"empty subject":     func(m *mail.Message) { m.Subject = " " },
		"multiline subject": func(m *mail.Message) { m.Subject = "a\nBcc: x@example.com" },
		"no body":           func(m *mail.Message) { m.HTML, m.Text = "", "" },
		"bad to":            func(m *mail.Message) { m.To[0].Email = "Ada <ada@example.com>" },
		"bad bcc":           func(m *mail.Message) { m.Bcc = []mail.Address{{Email: "nope"}} },
		"bad from":          func(m *mail.Message) { m.From.Email = "x" },
		"newline in name":   func(m *mail.Message) { m.To[0].Name = "a\r\nb" },
		"attachment name":   func(m *mail.Message) { m.Attachments[0].Filename = "" },
		"header key":        func(m *mail.Message) { m.Headers = map[string]string{"X Bad": "v"} },
		"header value":      func(m *mail.Message) { m.Headers = map[string]string{"X-A": "v\r\nBcc: x"} },
	} {
		msg := valid()
		mutate(&msg)
		if err := msg.Validate(); !errors.Is(err, mail.ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestWithDefaultFrom(t *testing.T) {
	def := mail.Address{Email: "default@example.com"}
	if got := (mail.Message{}).WithDefaultFrom(def).From; got != def {
		t.Errorf("empty from = %+v", got)
	}
	if got := valid().WithDefaultFrom(def).From; got == def {
		t.Error("explicit from overridden")
	}
}
