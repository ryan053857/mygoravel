package resend_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	resendsdk "github.com/resend/resend-go/v3"

	"github.com/ryan053857/mygoravel/mail"
	"github.com/ryan053857/mygoravel/mail/resend"
)

type request struct {
	header http.Header
	body   map[string]any
}

func newMailer(t *testing.T, status int, reply string) (*resend.Mailer, *[]request) {
	var got []request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/emails" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		data, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(data, &body); err != nil {
			t.Errorf("body: %v", err)
		}
		got = append(got, request{header: r.Header.Clone(), body: body})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	client := resendsdk.NewClient("re_test")
	client.BaseURL, _ = url.Parse(srv.URL + "/")
	m, err := resend.NewWithClient(client, mail.Address{Name: "Blog", Email: "no-reply@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	return m, &got
}

func TestSend(t *testing.T) {
	m, got := newMailer(t, http.StatusOK, `{"id":"email_1"}`)
	ctx := resend.WithIdempotencyKey(context.Background(), "welcome/42")
	err := m.Send(ctx, mail.Message{
		To:          []mail.Address{{Name: "Ada", Email: "ada@example.com"}},
		Cc:          []mail.Address{{Email: "cc@example.com"}},
		Bcc:         []mail.Address{{Email: "bcc@example.com"}},
		ReplyTo:     mail.Address{Email: "support@example.com"},
		Subject:     "Welcome",
		HTML:        "<p>Hi</p>",
		Text:        "Hi",
		Attachments: []mail.Attachment{{Filename: "a.txt", ContentType: "text/plain", Content: []byte("hello")}},
		Headers:     map[string]string{"X-Campaign": "welcome"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 {
		t.Fatalf("requests = %d", len(*got))
	}
	r := (*got)[0]
	if r.header.Get("Authorization") != "Bearer re_test" || r.header.Get("Idempotency-Key") != "welcome/42" {
		t.Errorf("headers = %v", r.header)
	}
	for key, want := range map[string]string{
		"from":     "Blog <no-reply@example.com>",
		"to":       `["Ada <ada@example.com>"]`,
		"cc":       `["cc@example.com"]`,
		"bcc":      `["bcc@example.com"]`,
		"reply_to": "support@example.com",
		"subject":  "Welcome",
		"html":     "<p>Hi</p>",
		"text":     "Hi",
		"headers":  `{"X-Campaign":"welcome"}`,
	} {
		if got := field(r.body[key]); got != want {
			t.Errorf("%s = %s, want %s", key, got, want)
		}
	}
	att := r.body["attachments"].([]any)[0].(map[string]any)
	if att["filename"] != "a.txt" || att["content_type"] != "text/plain" || att["content"] == nil {
		t.Errorf("attachment = %v", att)
	}
}

func TestSendWithoutIdempotencyKey(t *testing.T) {
	m, got := newMailer(t, http.StatusOK, `{"id":"email_1"}`)
	msg := mail.Message{From: mail.Address{Email: "team@example.com"}, To: []mail.Address{{Email: "a@example.com"}}, Subject: "s", Text: "t"}
	if err := m.Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	r := (*got)[0]
	if r.header.Get("Idempotency-Key") != "" || r.body["from"] != "team@example.com" {
		t.Errorf("header=%v from=%v", r.header.Get("Idempotency-Key"), r.body["from"])
	}
}

func TestSendErrors(t *testing.T) {
	msg := mail.Message{To: []mail.Address{{Email: "a@example.com"}}, Subject: "s", Text: "t"}

	m, _ := newMailer(t, http.StatusUnprocessableEntity, `{"statusCode":422,"name":"validation_error","message":"domain is not verified"}`)
	if err := m.Send(context.Background(), msg); err == nil || !strings.Contains(err.Error(), "domain is not verified") {
		t.Errorf("422 err = %v", err)
	}

	m, _ = newMailer(t, http.StatusTooManyRequests, `{"message":"slow down"}`)
	if err := m.Send(context.Background(), msg); !errors.Is(err, resendsdk.ErrRateLimit) {
		t.Errorf("429 err = %v", err)
	}

	m, got := newMailer(t, http.StatusOK, `{}`)
	if err := m.Send(context.Background(), mail.Message{}); !errors.Is(err, mail.ErrInvalid) || len(*got) != 0 {
		t.Errorf("invalid message err = %v, requests %d", err, len(*got))
	}
}

func TestNew(t *testing.T) {
	if _, err := resend.New(resend.Config{}); err == nil {
		t.Error("empty config accepted")
	}
	m, err := resend.New(resend.Config{From: mail.Address{Email: "a@example.com"}, APIKey: "re_x"})
	if err != nil || m.Client() == nil {
		t.Errorf("New = %v, %v", m, err)
	}
	if _, err := resend.NewWithClient(nil, mail.Address{Email: "a@example.com"}); err == nil {
		t.Error("nil client accepted")
	}
}

// field 把字符串原样返回，其他值编码为不转义 HTML 的 JSON。
func field(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
	return strings.TrimSpace(b.String())
}
