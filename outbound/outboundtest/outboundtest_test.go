package outboundtest_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/go-tenon/tenon/outbound"
	"github.com/go-tenon/tenon/outbound/outboundtest"
)

func get(t *testing.T, c *http.Client, u string) (int, string) {
	t.Helper()
	resp, err := c.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

func TestReplyAndRecord(t *testing.T) {
	fake := outboundtest.New(t)
	fake.On("POST https://api.stripe.com/v1/refunds").Reply(outboundtest.JSON(200, map[string]string{"id": "re_1"}))

	req, _ := http.NewRequest(http.MethodPost, "https://api.stripe.com/v1/refunds?expand=charge", strings.NewReader(`{"amount":500}`))
	req.Header.Set("Authorization", "Bearer sk_test")
	resp, err := fake.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/json" || string(body) != `{"id":"re_1"}` {
		t.Fatalf("response = %d %q %s", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}

	sent := fake.Sent("POST https://api.stripe.com/v1/refunds")
	if len(sent) != 1 {
		t.Fatalf("sent = %d", len(sent))
	}
	var in struct{ Amount int }
	if err := sent[0].JSON(&in); err != nil || in.Amount != 500 {
		t.Fatalf("body = %+v, %v", in, err)
	}
	if sent[0].Header.Get("Authorization") != "Bearer sk_test" || sent[0].URL.Query().Get("expand") != "charge" {
		t.Fatalf("request = %+v", sent[0])
	}
	if len(fake.Sent("GET https://api.stripe.com/v1/refunds")) != 0 || len(fake.Requests()) != 1 {
		t.Fatal("method must be part of the match")
	}
}

func TestSequence(t *testing.T) {
	fake := outboundtest.New(t)
	fake.On("api.github.com/users/*").Reply(
		outboundtest.Status(503).Header("Retry-After", "1"),
		outboundtest.Text(200, "octocat"),
	)
	c := fake.Client()

	resp, err := c.Get("https://api.github.com/users/octocat")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 503 || resp.Header.Get("Retry-After") != "1" {
		t.Fatalf("first = %d %v", resp.StatusCode, resp.Header)
	}
	// 用完之后一直返回最后一个。
	for range 2 {
		if code, body := get(t, c, "https://api.github.com/users/octocat"); code != 200 || body != "octocat" {
			t.Fatalf("then = %d %q", code, body)
		}
	}

	// 重新 Reply 从第一个开始。
	fake.On("api.github.com/users/*").Reply(outboundtest.Status(404), outboundtest.Status(200))
	if code, _ := get(t, c, "https://api.github.com/users/x"); code != 404 {
		t.Fatalf("after Reply = %d", code)
	}
}

func TestLaterRouteWins(t *testing.T) {
	fake := outboundtest.New(t)
	fake.On("*").Reply(outboundtest.Text(200, "default"))
	fake.On("GET https://example.com/special").Reply(outboundtest.Text(200, "special").Header("Content-Type", "text/html"))
	c := fake.Client()
	resp, err := c.Get("https://example.com/special")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if ct := resp.Header.Values("Content-Type"); len(ct) != 1 || ct[0] != "text/html" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if _, body := get(t, c, "https://example.com/other"); body != "default" {
		t.Fatal(body)
	}
	// 没有调用 Reply 的登记返回 200。
	fake.On("https://example.com/empty")
	if code, body := get(t, c, "https://example.com/empty"); code != 200 || body != "" {
		t.Fatalf("%d %q", code, body)
	}
}

func TestPatterns(t *testing.T) {
	cases := []struct {
		pattern, method, url string
		want                 bool
	}{
		{"https://example.com/a", "GET", "https://example.com/a", true},
		{"https://example.com/a", "DELETE", "https://example.com/a", true},
		{"GET https://example.com/a", "POST", "https://example.com/a", false},
		{"https://example.com/a", "GET", "http://example.com/a", false},
		{"example.com/a", "GET", "http://example.com/a", true},
		{"https://example.com", "GET", "https://example.com/", true},
		{"https://example.com/", "GET", "https://example.com", true},
		{"example.com", "GET", "https://example.com", true},
		{"https://example.com/a", "GET", "https://example.com/a?page=2", true},
		{"https://example.com/a?page=2", "GET", "https://example.com/a?page=2", true},
		{"https://example.com/a?page=2", "GET", "https://example.com/a?page=3", false},
		{"https://example.com/a?*", "GET", "https://example.com/a?page=3", true},
		{"https://example.com/users/*", "GET", "https://example.com/users/1/posts", true},
		{"https://example.com/users/*", "GET", "https://example.com/users", false},
		{"https://*.example.com/*/v1", "GET", "https://eu.example.com/api/v1", true},
		{"https://*.example.com/*/v1", "GET", "https://eu.example.com/api/v2", false},
		{"https://example.com:8443/*", "GET", "https://example.com:8443/x", true},
		{"https://example.com/*", "GET", "https://example.com:8443/x", false},
		{"https://example.com/a*a", "GET", "https://example.com/a", false},
		{"https://example.com/a*a", "GET", "https://example.com/aa", true},
		{"*", "PATCH", "http://anything/at/all?x=1", true},
	}
	for _, tc := range cases {
		fake := outboundtest.New(t)
		req, err := http.NewRequest(tc.method, tc.url, nil)
		if err != nil {
			t.Fatal(err)
		}
		fake.On("*")
		if _, err := fake.RoundTrip(req); err != nil {
			t.Fatal(err)
		}
		if got := len(fake.Sent(tc.pattern)) == 1; got != tc.want {
			t.Errorf("%q matches %s %s = %v", tc.pattern, tc.method, tc.url, got)
		}
	}
}

func TestFail(t *testing.T) {
	fake := outboundtest.New(t)
	fake.On("https://slow.example.com/*").Reply(outboundtest.Fail(context.DeadlineExceeded))
	fake.On("https://internal.example.com/*").Reply(outboundtest.Fail(outbound.ErrForbidden))
	c := fake.Client()

	_, err := c.Get("https://slow.example.com/x")
	var uerr *url.Error
	if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &uerr) || !uerr.Timeout() {
		t.Fatalf("timeout: %v", err)
	}
	if _, err := c.Get("https://internal.example.com/"); !errors.Is(err, outbound.ErrForbidden) {
		t.Fatalf("forbidden: %v", err)
	}
	if len(fake.Requests()) != 2 {
		t.Fatal("failed requests are recorded too")
	}
}

func TestHandler(t *testing.T) {
	fake := outboundtest.New(t)
	fake.On("POST https://hooks.example.com/*").Reply(outboundtest.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, "hello %s from %s", r.PostForm.Get("name"), r.URL.Path)
	}))
	resp, err := fake.Client().PostForm("https://hooks.example.com/in", url.Values{"name": {"ann"}})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 201 || string(body) != "hello ann from /in" {
		t.Fatalf("%d %q", resp.StatusCode, body)
	}
	form, err := fake.Requests()[0].Form()
	if err != nil || form.Get("name") != "ann" {
		t.Fatalf("form = %v, %v", form, err)
	}
}

func TestHandlerSeesContext(t *testing.T) {
	fake := outboundtest.New(t)
	fake.On("https://slow.example.com/").Reply(outboundtest.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	ctx, cancel := context.WithCancel(t.Context())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://slow.example.com/", nil)
	go cancel()
	if _, err := fake.Client().Do(req); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestCanceledBeforeSend(t *testing.T) {
	fake := outboundtest.New(t)
	fake.On("*")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.com/", nil)
	if _, err := fake.Client().Do(req); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestRedirect(t *testing.T) {
	fake := outboundtest.New(t)
	fake.On("https://example.com/old").Reply(outboundtest.Status(http.StatusFound).Header("Location", "/new"))
	fake.On("https://example.com/new").Reply(outboundtest.Text(200, "new"))
	if code, body := get(t, fake.Client(), "https://example.com/old"); code != 200 || body != "new" {
		t.Fatalf("%d %q", code, body)
	}
	if len(fake.Requests()) != 2 {
		t.Fatal("both hops are recorded")
	}
}

// recorder 记录 Errorf 和 Cleanup，用来检查没有匹配的请求让测试失败。
type recorder struct {
	testing.TB
	errors   []string
	cleanups []func()
}

func (r *recorder) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}
func (r *recorder) Cleanup(f func()) { r.cleanups = append(r.cleanups, f) }

func TestUnexpectedRequest(t *testing.T) {
	rec := &recorder{TB: t}
	fake := outboundtest.New(rec)
	fake.On("https://example.com/known")

	if _, err := fake.Client().Get("https://example.com/unknown"); err == nil || !strings.Contains(err.Error(), "no response registered for GET https://example.com/unknown") {
		t.Fatalf("err = %v", err)
	}
	if len(rec.errors) != 0 {
		t.Fatal("reported before cleanup")
	}
	for _, f := range rec.cleanups {
		f()
	}
	if len(rec.errors) != 1 || rec.errors[0] != "outboundtest: unexpected request GET https://example.com/unknown" {
		t.Fatalf("errors = %q", rec.errors)
	}
	if len(fake.Sent("https://example.com/unknown")) != 1 {
		t.Fatal("unexpected requests are recorded")
	}
}

func TestConcurrent(t *testing.T) {
	fake := outboundtest.New(t)
	fake.On("https://example.com/*").Reply(outboundtest.Status(500), outboundtest.Status(200))
	c := fake.Client()
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Go(func() {
			resp, err := c.Post(fmt.Sprintf("https://example.com/%d", i), "text/plain", strings.NewReader("x"))
			if err != nil {
				t.Error(err)
				return
			}
			resp.Body.Close()
		})
		if i == 25 {
			fake.On("https://example.com/25*").Reply(outboundtest.Status(204))
		}
	}
	wg.Wait()
	if n := len(fake.Sent("POST example.com/*")); n != 50 {
		t.Fatalf("sent = %d", n)
	}
}
