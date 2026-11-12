package ses_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	netmail "net/mail"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"

	"github.com/ryan053857/mygoravel/mail"
	"github.com/ryan053857/mygoravel/mail/ses"
)

// request 是假 SES 端点收到的一次 SendEmail 请求。
type request struct {
	Content struct {
		Raw struct {
			Data []byte
		}
	}
	Destination struct {
		ToAddresses, CcAddresses, BccAddresses []string
	}
	ConfigurationSetName string
}

func newServer(t *testing.T, status int, reply string) (string, *[]request) {
	var got []request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v2/email/outbound-emails" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		data, _ := io.ReadAll(r.Body)
		var body request
		if err := json.Unmarshal(data, &body); err != nil {
			t.Errorf("body: %v", err)
		}
		got = append(got, body)
		w.Header().Set("Content-Type", "application/json")
		if status != http.StatusOK {
			w.Header().Set("X-Amzn-ErrorType", "MessageRejected")
		}
		w.WriteHeader(status)
		io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &got
}

func newClient(t *testing.T, status int, reply string) (*sesv2.Client, *[]request) {
	url, got := newServer(t, status, reply)
	client := sesv2.New(sesv2.Options{
		BaseEndpoint: aws.String(url),
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("AKID", "SECRET", ""),
	})
	return client, got
}

var from = mail.Address{Name: "Blog", Email: "no-reply@example.com"}

func TestSend(t *testing.T) {
	client, got := newClient(t, http.StatusOK, `{"MessageId":"m-1"}`)
	m, err := ses.NewWithClient(client, from)
	if err != nil {
		t.Fatal(err)
	}
	err = m.Send(context.Background(), mail.Message{
		To:          []mail.Address{{Name: "Ada", Email: "ada@example.com"}},
		Cc:          []mail.Address{{Email: "cc@example.com"}},
		Bcc:         []mail.Address{{Email: "bcc@example.com"}},
		ReplyTo:     mail.Address{Email: "support@example.com"},
		Subject:     "欢迎",
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
	d := r.Destination
	if !slices.Equal(d.ToAddresses, []string{"ada@example.com"}) ||
		!slices.Equal(d.CcAddresses, []string{"cc@example.com"}) ||
		!slices.Equal(d.BccAddresses, []string{"bcc@example.com"}) {
		t.Errorf("destination = %+v", d)
	}
	if r.ConfigurationSetName != "" {
		t.Errorf("configuration set = %q", r.ConfigurationSetName)
	}

	parsed, err := netmail.ReadMessage(strings.NewReader(string(r.Content.Raw.Data)))
	if err != nil {
		t.Fatalf("raw message: %v", err)
	}
	h := parsed.Header
	for key, want := range map[string]string{
		"From":       `"Blog" <no-reply@example.com>`,
		"To":         `"Ada" <ada@example.com>`,
		"Cc":         "<cc@example.com>",
		"Reply-To":   "<support@example.com>",
		"X-Campaign": "welcome",
	} {
		if h.Get(key) != want {
			t.Errorf("%s = %q, want %q", key, h.Get(key), want)
		}
	}
	if subject, _ := new(netmail.AddressParser).WordDecoder.DecodeHeader(h.Get("Subject")); subject != "欢迎" {
		t.Errorf("subject = %q", subject)
	}
	if h.Get("Bcc") != "" || strings.Contains(string(r.Content.Raw.Data), "bcc@example.com") {
		t.Error("bcc leaked into the raw message")
	}
	if h.Get("Message-Id") == "" || h.Get("Date") == "" {
		t.Errorf("missing Message-Id or Date: %v", h)
	}
	if !strings.Contains(string(r.Content.Raw.Data), `filename="a.txt"`) {
		t.Error("attachment missing")
	}
}

func TestSendUsesMessageFrom(t *testing.T) {
	client, got := newClient(t, http.StatusOK, `{"MessageId":"m-1"}`)
	m, _ := ses.NewWithClient(client, from)
	err := m.Send(context.Background(), mail.Message{
		From:    mail.Address{Email: "news@example.com"},
		To:      []mail.Address{{Email: "ada@example.com"}},
		Subject: "Hi",
		Text:    "Hi",
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := netmail.ReadMessage(strings.NewReader(string((*got)[0].Content.Raw.Data)))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Header.Get("From") != "<news@example.com>" {
		t.Errorf("From = %q", parsed.Header.Get("From"))
	}
}

func TestSendRejectsInvalidMessage(t *testing.T) {
	client, got := newClient(t, http.StatusOK, `{}`)
	m, _ := ses.NewWithClient(client, from)
	err := m.Send(context.Background(), mail.Message{Subject: "Hi", Text: "Hi"})
	if !errors.Is(err, mail.ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
	if len(*got) != 0 {
		t.Errorf("requests = %d", len(*got))
	}
}

func TestSendReturnsServiceError(t *testing.T) {
	client, _ := newClient(t, http.StatusBadRequest, `{"message":"Email address is not verified."}`)
	m, _ := ses.NewWithClient(client, from)
	err := m.Send(context.Background(), mail.Message{
		To: []mail.Address{{Email: "ada@example.com"}}, Subject: "Hi", Text: "Hi",
	})
	if err == nil || !strings.Contains(err.Error(), "ses: send") || !strings.Contains(err.Error(), "not verified") {
		t.Fatalf("err = %v", err)
	}
}

func TestNew(t *testing.T) {
	m, err := ses.New(ses.Config{
		From: from, Region: "eu-west-1", AccessKey: "AKID", SecretKey: "SECRET",
	})
	if err != nil {
		t.Fatal(err)
	}
	opts := m.Client().Options()
	if opts.Region != "eu-west-1" {
		t.Errorf("region = %q", opts.Region)
	}
	creds, err := opts.Credentials.Retrieve(context.Background())
	if err != nil || creds.AccessKeyID != "AKID" {
		t.Errorf("credentials = %+v, %v", creds, err)
	}
}

func TestNewSendsConfigurationSet(t *testing.T) {
	url, got := newServer(t, http.StatusOK, `{"MessageId":"m-1"}`)
	t.Setenv("AWS_ENDPOINT_URL", url)
	m, err := ses.New(ses.Config{
		From: from, Region: "us-east-1", AccessKey: "AKID", SecretKey: "SECRET", ConfigurationSet: "transactional",
	})
	if err != nil {
		t.Fatal(err)
	}
	err = m.Send(context.Background(), mail.Message{
		To: []mail.Address{{Email: "ada@example.com"}}, Subject: "Hi", Text: "Hi",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 || (*got)[0].ConfigurationSetName != "transactional" {
		t.Fatalf("requests = %+v", *got)
	}
}

func TestNewRequiresRegion(t *testing.T) {
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")
	t.Setenv("AWS_CONFIG_FILE", t.TempDir()+"/none")
	t.Setenv("AWS_PROFILE", "")
	if _, err := ses.New(ses.Config{From: from}); err == nil || !strings.Contains(err.Error(), "region is required") {
		t.Fatalf("err = %v", err)
	}
}

func TestConfigValidate(t *testing.T) {
	err := ses.Config{AccessKey: "AKID"}.Validate()
	if err == nil || !strings.Contains(err.Error(), "from") || !strings.Contains(err.Error(), "set together") {
		t.Fatalf("err = %v", err)
	}
	if err := (ses.Config{From: from}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestNewWithClientValidates(t *testing.T) {
	if _, err := ses.NewWithClient(nil, from); err == nil {
		t.Error("nil client accepted")
	}
	client, _ := newClient(t, http.StatusOK, `{}`)
	if _, err := ses.NewWithClient(client, mail.Address{}); err == nil {
		t.Error("empty from accepted")
	}
}
