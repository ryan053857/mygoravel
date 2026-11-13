package captcha_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/go-tenon/tenon/captcha"
	"github.com/go-tenon/tenon/errorx"
	"github.com/go-tenon/tenon/outbound/outboundtest"
)

const siteverify = "POST " + captcha.Turnstile

func newVerifier(t *testing.T, cfg captcha.Config) (*captcha.Verifier, *outboundtest.Transport) {
	t.Helper()
	fake := outboundtest.New(t)
	if cfg.Secret == "" {
		cfg.Secret = "s3cret"
	}
	v, err := captcha.New(cfg, fake.Client())
	if err != nil {
		t.Fatal(err)
	}
	return v, fake
}

func TestVerifySuccess(t *testing.T) {
	v, fake := newVerifier(t, captcha.Config{Hostnames: []string{"blog.example.com"}})
	fake.On(siteverify).Reply(outboundtest.JSON(200, map[string]any{
		"success": true, "hostname": "blog.example.com", "action": "signup",
	}))
	if err := v.Verify(context.Background(), "tok", "signup", "203.0.113.7"); err != nil {
		t.Fatal(err)
	}
	sent := fake.Sent(siteverify)
	if len(sent) != 1 {
		t.Fatalf("sent %d requests", len(sent))
	}
	if ct := sent[0].Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
		t.Fatalf("content type %q", ct)
	}
	form, err := sent[0].Form()
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"secret": "s3cret", "response": "tok", "remoteip": "203.0.113.7"} {
		if got := form.Get(k); got != want {
			t.Errorf("%s=%q, want %q", k, got, want)
		}
	}
}

func TestVerifySkipsOptionalChecks(t *testing.T) {
	v, fake := newVerifier(t, captcha.Config{})
	fake.On(siteverify).Reply(outboundtest.JSON(200, map[string]any{"success": true, "hostname": "other.example.com"}))
	if err := v.Verify(context.Background(), "tok", "", ""); err != nil {
		t.Fatal(err)
	}
	form, _ := fake.Sent(siteverify)[0].Form()
	if form.Has("remoteip") {
		t.Fatal("remoteip sent although empty")
	}
}

func TestVerifyFailed(t *testing.T) {
	cases := map[string]struct {
		reply  map[string]any
		action string
	}{
		"rejected":       {reply: map[string]any{"success": false, "error-codes": []string{"invalid-input-response"}}},
		"duplicate":      {reply: map[string]any{"success": false, "error-codes": []string{"timeout-or-duplicate"}}},
		"other hostname": {reply: map[string]any{"success": true, "hostname": "evil.example.com", "action": "signup"}},
		"other action":   {reply: map[string]any{"success": true, "hostname": "blog.example.com", "action": "contact"}, action: "signup"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			v, fake := newVerifier(t, captcha.Config{Hostnames: []string{"blog.example.com"}})
			fake.On(siteverify).Reply(outboundtest.JSON(200, c.reply))
			err := v.Verify(context.Background(), "tok", c.action, "")
			if !errors.Is(err, captcha.ErrFailed) || errorx.KindOf(err) != errorx.InvalidArgument {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestVerifyRejectsTokenWithoutRequest(t *testing.T) {
	v, fake := newVerifier(t, captcha.Config{})
	for _, token := range []string{"", strings.Repeat("x", 2049)} {
		if err := v.Verify(context.Background(), token, "", ""); !errors.Is(err, captcha.ErrFailed) {
			t.Fatalf("len %d: err=%v", len(token), err)
		}
	}
	if n := len(fake.Requests()); n != 0 {
		t.Fatalf("sent %d requests", n)
	}
}

func TestVerifyUnavailable(t *testing.T) {
	cases := map[string]outboundtest.Response{
		"connection":     outboundtest.Fail(errors.New("connection refused")),
		"server error":   outboundtest.Status(http.StatusBadGateway),
		"not json":       outboundtest.Text(200, "<html>"),
		"internal-error": outboundtest.JSON(200, map[string]any{"success": false, "error-codes": []string{"internal-error"}}),
	}
	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			v, fake := newVerifier(t, captcha.Config{})
			fake.On(siteverify).Reply(reply)
			err := v.Verify(context.Background(), "tok", "", "")
			if !errors.Is(err, captcha.ErrUnavailable) || errorx.KindOf(err) != errorx.Unavailable {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestVerifyBadSecretIsNotUserError(t *testing.T) {
	v, fake := newVerifier(t, captcha.Config{})
	fake.On(siteverify).Reply(outboundtest.JSON(200, map[string]any{"success": false, "error-codes": []string{"invalid-input-secret"}}))
	err := v.Verify(context.Background(), "tok", "", "")
	if err == nil || errors.Is(err, captcha.ErrFailed) || errors.Is(err, captcha.ErrUnavailable) {
		t.Fatalf("err=%v", err)
	}
}

func TestEndpoint(t *testing.T) {
	fake := outboundtest.New(t)
	v, err := captcha.New(captcha.Config{Secret: "s", Endpoint: captcha.HCaptcha}, fake.Client())
	if err != nil {
		t.Fatal(err)
	}
	fake.On("POST " + captcha.HCaptcha).Reply(outboundtest.JSON(200, map[string]any{"success": true}))
	if err := v.Verify(context.Background(), "tok", "", ""); err != nil {
		t.Fatal(err)
	}
}

func TestConfigValidate(t *testing.T) {
	cases := map[string]captcha.Config{
		"missing secret": {},
		"http endpoint":  {Secret: "s", Endpoint: "http://challenges.cloudflare.com/turnstile/v0/siteverify"},
	}
	for name, cfg := range cases {
		if _, err := captcha.New(cfg, nil); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	if _, err := captcha.New(captcha.Config{Secret: "s"}, nil); err != nil {
		t.Fatal(err)
	}
}
