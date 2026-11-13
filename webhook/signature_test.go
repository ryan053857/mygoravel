package webhook_test

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/cockroachdb/errors"

	"github.com/go-tenon/tenon/webhook"
)

// Standard Webhooks 规范附带的测试向量。
func TestSignMatchesSpecVector(t *testing.T) {
	t.Parallel()
	sig, err := webhook.Sign("whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw", "msg_p5jXN8AQM9LWM0D4loKWxJek",
		time.Unix(1614265330, 0), []byte(`{"test": 2432232314}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE="; sig != want {
		t.Fatalf("Sign = %s, want %s", sig, want)
	}
}

func signed(t *testing.T, secret string, ts time.Time, body string, extra ...string) http.Header {
	t.Helper()
	sig, err := webhook.Sign(secret, "msg_1", ts, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	h.Set(webhook.HeaderID, "msg_1")
	h.Set(webhook.HeaderTimestamp, strconv.FormatInt(ts.Unix(), 10))
	for _, s := range extra {
		sig = s + " " + sig
	}
	h.Set(webhook.HeaderSignature, sig)
	return h
}

func TestVerify(t *testing.T) {
	t.Parallel()
	const secret = "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"
	const other = "whsec_dGhpcyBpcyBhbm90aGVyIHNlY3JldCE="
	body := `{"type":"x"}`
	now := time.Now()

	if err := webhook.Verify(secret, signed(t, secret, now, body), []byte(body)); err != nil {
		t.Fatalf("valid: %v", err)
	}
	// 轮换期间请求头带有多个签名，任意一个匹配即可；不带 whsec_ 前缀的密钥同样可用。
	if err := webhook.Verify(secret[len("whsec_"):], signed(t, secret, now, body, "v1,bm90LWEtc2lnbmF0dXJl", "v2,x"), []byte(body)); err != nil {
		t.Fatalf("multiple signatures: %v", err)
	}
	for name, tc := range map[string]struct {
		secret string
		header http.Header
		body   string
	}{
		"wrong secret":  {other, signed(t, secret, now, body), body},
		"modified body": {secret, signed(t, secret, now, body), body + " "},
		"too old":       {secret, signed(t, secret, now.Add(-webhook.Tolerance-time.Minute), body), body},
		"in future":     {secret, signed(t, secret, now.Add(webhook.Tolerance+time.Minute), body), body},
		"no headers":    {secret, http.Header{}, body},
	} {
		if err := webhook.Verify(tc.secret, tc.header, []byte(tc.body)); !errors.Is(err, webhook.ErrInvalidSignature) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if err := webhook.Verify("whsec_!!!", signed(t, secret, now, body), []byte(body)); err == nil || errors.Is(err, webhook.ErrInvalidSignature) {
		t.Fatalf("malformed secret: %v", err)
	}
}
