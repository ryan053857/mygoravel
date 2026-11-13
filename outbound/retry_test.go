package outbound_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/go-tenon/tenon/outbound"
	"github.com/go-tenon/tenon/outbound/outboundtest"
)

// retrying 返回经过 Retry 的 fake 客户端。
func retrying(t *testing.T, opts outbound.RetryOptions) (*outboundtest.Transport, *http.Client) {
	fake := outboundtest.New(t)
	return fake, &http.Client{Transport: outbound.Retry(fake, opts)}
}

// timed 发送请求，返回响应的状态码（出错时为 0）、耗时和错误。
func timed(t *testing.T, c *http.Client, req *http.Request) (int, time.Duration, error) {
	t.Helper()
	start := time.Now()
	resp, err := c.Do(req)
	elapsed := time.Since(start)
	if err != nil {
		return 0, elapsed, err
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)
	return resp.StatusCode, elapsed, nil
}

func newRequest(t *testing.T, method, url, body string) *http.Request {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func between(t *testing.T, d, lo, hi time.Duration) {
	t.Helper()
	if d < lo || d > hi {
		t.Fatalf("waited %v, want between %v and %v", d, lo, hi)
	}
}

func TestRetryTemporaryStatus(t *testing.T) {
	for _, status := range []int{429, 502, 503, 504} {
		synctest.Test(t, func(t *testing.T) {
			fake, c := retrying(t, outbound.RetryOptions{})
			fake.On("https://example.com/").Reply(outboundtest.Status(status), outboundtest.Text(200, "ok"))
			code, waited, err := timed(t, c, newRequest(t, "GET", "https://example.com/", ""))
			if err != nil || code != 200 {
				t.Fatalf("%d: %d %v", status, code, err)
			}
			between(t, waited, 100*time.Millisecond, 200*time.Millisecond)
			if n := len(fake.Requests()); n != 2 {
				t.Fatalf("%d: requests = %d", status, n)
			}
		})
	}
}

func TestRetryGivesUp(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake, c := retrying(t, outbound.RetryOptions{Attempts: 4})
		fake.On("https://example.com/").Reply(outboundtest.Text(503, "busy"))
		resp, err := c.Get("https://example.com/")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		// 最后一次的响应原样返回，响应体可以读取。
		if resp.StatusCode != 503 || string(body) != "busy" || len(fake.Requests()) != 4 {
			t.Fatalf("%d %q, requests = %d", resp.StatusCode, body, len(fake.Requests()))
		}
	})
}

func TestRetryBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake, c := retrying(t, outbound.RetryOptions{Attempts: 4})
		fake.On("*").Reply(outboundtest.Status(503))
		_, waited, _ := timed(t, c, newRequest(t, "GET", "https://example.com/", ""))
		// 100–200ms、200–400ms、400–800ms。
		between(t, waited, 700*time.Millisecond, 1400*time.Millisecond)
	})
	synctest.Test(t, func(t *testing.T) {
		fake, c := retrying(t, outbound.RetryOptions{Attempts: 4, Backoff: time.Second, MaxWait: 1500 * time.Millisecond})
		fake.On("*").Reply(outboundtest.Status(503))
		_, waited, _ := timed(t, c, newRequest(t, "GET", "https://example.com/", ""))
		// 0.5–1s，之后不超过 MaxWait：0.75–1.5s 两次。
		between(t, waited, 2000*time.Millisecond, 4000*time.Millisecond)
	})
}

func TestRetryAfter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake, c := retrying(t, outbound.RetryOptions{})
		fake.On("https://example.com/seconds").Reply(outboundtest.Status(429).Header("Retry-After", "2"), outboundtest.Status(200))
		_, waited, _ := timed(t, c, newRequest(t, "GET", "https://example.com/seconds", ""))
		if waited != 2*time.Second {
			t.Fatalf("seconds: waited %v", waited)
		}

		at := time.Now().Add(3 * time.Second).UTC().Format(http.TimeFormat)
		fake.On("https://example.com/date").Reply(outboundtest.Status(503).Header("Retry-After", at), outboundtest.Status(200))
		_, waited, _ = timed(t, c, newRequest(t, "GET", "https://example.com/date", ""))
		if waited != 3*time.Second {
			t.Fatalf("date: waited %v", waited)
		}

		// 超过 MaxWait 时不等待，直接返回。
		fake.On("https://example.com/long").Reply(outboundtest.Status(429).Header("Retry-After", "60"), outboundtest.Status(200))
		code, waited, _ := timed(t, c, newRequest(t, "GET", "https://example.com/long", ""))
		if code != 429 || waited != 0 || len(fake.Sent("https://example.com/long")) != 1 {
			t.Fatalf("long: %d after %v", code, waited)
		}
	})
}

func TestRetryOnlyReplayable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake, c := retrying(t, outbound.RetryOptions{})
		fake.On("*").Reply(outboundtest.Status(503), outboundtest.Status(200))

		for _, method := range []string{"GET", "HEAD", "OPTIONS", "TRACE", "PUT", "DELETE"} {
			if code, _, _ := timed(t, c, newRequest(t, method, "https://example.com/"+method, "")); code != 200 {
				t.Fatalf("%s: %d", method, code)
			}
			fake.On("*").Reply(outboundtest.Status(503), outboundtest.Status(200))
		}

		for _, method := range []string{"POST", "PATCH"} {
			if code, _, _ := timed(t, c, newRequest(t, method, "https://example.com/"+method, `{"a":1}`)); code != 503 {
				t.Fatalf("%s: %d", method, code)
			}
			fake.On("*").Reply(outboundtest.Status(503), outboundtest.Status(200))
		}

		// 带有幂等键的 POST 重试，每次都发送完整的请求体。
		for _, key := range []string{"Idempotency-Key", "X-Idempotency-Key"} {
			req := newRequest(t, "POST", "https://example.com/keyed", `{"amount":500}`)
			req.Header.Set(key, "order-1")
			if code, _, _ := timed(t, c, req); code != 200 {
				t.Fatalf("%s: %d", key, code)
			}
			fake.On("*").Reply(outboundtest.Status(503), outboundtest.Status(200))
		}
		for _, r := range fake.Sent("POST https://example.com/keyed") {
			if string(r.Body) != `{"amount":500}` {
				t.Fatalf("body = %q", r.Body)
			}
		}
		if n := len(fake.Sent("POST https://example.com/keyed")); n != 4 {
			t.Fatalf("keyed = %d", n)
		}

		// 请求体不能重新读取时不重试。
		req := newRequest(t, "PUT", "https://example.com/stream", "data")
		req.GetBody = nil
		if code, _, _ := timed(t, c, req); code != 503 {
			t.Fatalf("stream: %d", code)
		}
	})
}

func TestRetryStatusesNotRetried(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake, c := retrying(t, outbound.RetryOptions{})
		for _, status := range []int{200, 400, 404, 500, 501} {
			fake.On("*").Reply(outboundtest.Status(status), outboundtest.Status(299))
			if code, _, _ := timed(t, c, newRequest(t, "GET", "https://example.com/", "")); code != status {
				t.Fatalf("%d: got %d", status, code)
			}
		}
	})
}

func TestRetryErrors(t *testing.T) {
	temporary := []error{syscall.ECONNREFUSED, syscall.ECONNRESET, io.ErrUnexpectedEOF, &net.DNSError{Err: "timeout", IsTimeout: true}}
	for _, failure := range temporary {
		synctest.Test(t, func(t *testing.T) {
			fake, c := retrying(t, outbound.RetryOptions{})
			fake.On("*").Reply(outboundtest.Fail(failure), outboundtest.Status(200))
			if code, _, err := timed(t, c, newRequest(t, "GET", "https://example.com/", "")); code != 200 {
				t.Fatalf("%v: %d %v", failure, code, err)
			}
		})
	}
	permanent := []error{
		outbound.ErrForbidden,
		&net.DNSError{Err: "no such host", Name: "nope.example", IsNotFound: true},
	}
	for _, failure := range permanent {
		synctest.Test(t, func(t *testing.T) {
			fake, c := retrying(t, outbound.RetryOptions{})
			fake.On("*").Reply(outboundtest.Fail(failure), outboundtest.Status(200))
			if _, _, err := timed(t, c, newRequest(t, "GET", "https://example.com/", "")); !errors.Is(err, failure) {
				t.Fatalf("%v: %v", failure, err)
			}
			if len(fake.Requests()) != 1 {
				t.Fatalf("%v: retried", failure)
			}
		})
	}
}

func TestRetryContext(t *testing.T) {
	// 剩余时间不够等到下一次：直接返回最后一次的结果，不等待。
	synctest.Test(t, func(t *testing.T) {
		fake, c := retrying(t, outbound.RetryOptions{})
		fake.On("*").Reply(outboundtest.Status(429).Header("Retry-After", "3"), outboundtest.Status(200))
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		req := newRequest(t, "GET", "https://example.com/", "").WithContext(ctx)
		if code, waited, err := timed(t, c, req); code != 429 || err != nil || waited != 0 {
			t.Fatalf("%d %v after %v", code, err, waited)
		}
	})
	// 等待期间取消：返回 ctx 的错误。
	synctest.Test(t, func(t *testing.T) {
		fake, c := retrying(t, outbound.RetryOptions{})
		fake.On("*").Reply(outboundtest.Status(429).Header("Retry-After", "3"), outboundtest.Status(200))
		ctx, cancel := context.WithCancel(t.Context())
		time.AfterFunc(time.Second, cancel)
		req := newRequest(t, "GET", "https://example.com/", "").WithContext(ctx)
		if _, waited, err := timed(t, c, req); !errors.Is(err, context.Canceled) || waited != time.Second {
			t.Fatalf("%v after %v", err, waited)
		}
	})
	// http.Client 的 Timeout 限制包括重试在内的总时长。
	synctest.Test(t, func(t *testing.T) {
		fake, c := retrying(t, outbound.RetryOptions{Attempts: 10, Backoff: time.Second})
		c.Timeout = 3 * time.Second
		fake.On("*").Reply(outboundtest.Status(503))
		code, waited, err := timed(t, c, newRequest(t, "GET", "https://example.com/", ""))
		if waited > 3*time.Second || (err == nil && code != 503) {
			t.Fatalf("%d %v after %v", code, err, waited)
		}
	})
}

// 经过真实连接重试：前一次的响应体被关闭，连接可以复用。
func TestRetryOverNetwork(t *testing.T) {
	var calls atomic.Int32
	srv := server(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, strings.Repeat("x", 1000))
			return
		}
		_, _ = io.WriteString(w, "ok")
	})
	client := outbound.New(local)
	client.Transport = outbound.Retry(client.Transport, outbound.RetryOptions{Backoff: time.Millisecond})
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "ok" || calls.Load() != 2 {
		t.Fatalf("%d %q after %d calls", resp.StatusCode, body, calls.Load())
	}
}
