package outbound

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"time"
)

// RetryOptions 是 Retry 的选项，零值可用。
type RetryOptions struct {
	// Attempts 是包括第一次在内的最多尝试次数，零值为 3。
	Attempts int
	// Backoff 是第一次重试前的等待时间，之后每次翻倍并加上随机抖动，零值为 200ms。
	Backoff time.Duration
	// MaxWait 是两次尝试之间最长的等待时间，零值为 5s。响应的 Retry-After 超过它时不再重试，直接返回这个响应。
	MaxWait time.Duration
}

// Retry 包装 next（nil 时为 http.DefaultTransport），对可以安全重复的请求在临时失败时重试：
//
//   - 只重试 GET、HEAD、OPTIONS、TRACE、PUT、DELETE，以及带有 Idempotency-Key 或 X-Idempotency-Key 请求头的请求，
//     并且请求体可以重新读取（http.NewRequest 用 bytes、strings 创建的请求体都可以）；
//   - 只在连接失败，或者响应状态码是 429、502、503、504 时重试；ErrForbidden、域名不存在、证书错误不会因为重试而成功，直接返回；
//   - 响应带有 Retry-After 时按它等待；
//   - 等待受请求的 context 限制：剩余时间不够等到下一次时，直接返回最后一次的结果。http.Client 的 Timeout 同样限制包括重试在内的总时长。
//
// 重试让调用方多等，只适合在当前请求中就要结果、并且失败是短暂的场景。要保证送达的请求（例如 webhook、通知第三方），
// 放进队列的任务中，由队列重试。
//
//	client := outbound.New(outbound.Options{Timeout: 10 * time.Second})
//	client.Transport = outbound.Retry(client.Transport, outbound.RetryOptions{})
func Retry(next http.RoundTripper, opts RetryOptions) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	if opts.Attempts <= 0 {
		opts.Attempts = 3
	}
	if opts.Backoff <= 0 {
		opts.Backoff = 200 * time.Millisecond
	}
	if opts.MaxWait <= 0 {
		opts.MaxWait = 5 * time.Second
	}
	return &retryTransport{next: next, opts: opts}
}

type retryTransport struct {
	next http.RoundTripper
	opts RetryOptions
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !replayable(req) {
		return t.next.RoundTrip(req)
	}
	ctx := req.Context()
	attempt := req
	for n := 1; ; n++ {
		resp, err := t.next.RoundTrip(attempt)
		if n >= t.opts.Attempts || ctx.Err() != nil {
			return resp, err
		}
		wait, ok := t.wait(n, resp, err)
		if !ok {
			return resp, err
		}
		if deadline, has := ctx.Deadline(); has && time.Until(deadline) < wait {
			return resp, err // 等不到下一次了
		}
		if resp != nil {
			// 读完一小段再关闭，连接可以复用。
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
			resp.Body.Close()
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		attempt = req.Clone(ctx)
		if req.Body != nil && req.Body != http.NoBody {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			attempt.Body = body
		}
	}
}

// wait 判断第 n 次尝试的结果是否应该重试，返回重试前的等待时间。
func (t *retryTransport) wait(n int, resp *http.Response, err error) (time.Duration, bool) {
	if err != nil {
		if !temporary(err) {
			return 0, false
		}
		return t.backoff(n), true
	}
	switch resp.StatusCode {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
	default:
		return 0, false
	}
	if after, ok := retryAfter(resp.Header.Get("Retry-After")); ok {
		if after > t.opts.MaxWait {
			return 0, false
		}
		return after, true
	}
	return t.backoff(n), true
}

// backoff 返回第 n 次尝试失败后的等待时间：Backoff·2ⁿ⁻¹，不超过 MaxWait，其中后一半随机。
func (t *retryTransport) backoff(n int) time.Duration {
	d := t.opts.MaxWait
	if shift := n - 1; shift < 32 && t.opts.Backoff<<shift < d {
		d = t.opts.Backoff << shift
	}
	return d/2 + rand.N(d/2+1)
}

// retryAfter 解析 Retry-After 的秒数或 HTTP 日期。
func retryAfter(v string) (time.Duration, bool) {
	if v == "" {
		return 0, false
	}
	if s, err := strconv.Atoi(v); err == nil {
		return time.Duration(max(s, 0)) * time.Second, true
	}
	if at, err := http.ParseTime(v); err == nil {
		return max(time.Until(at), 0), true
	}
	return 0, false
}

// replayable 判断请求能否安全地再发一次：方法是 RFC 9110 中幂等的方法，或者带有幂等键（与 net/http 识别的请求头相同）。
func replayable(req *http.Request) bool {
	if req.Body != nil && req.Body != http.NoBody && req.GetBody == nil {
		return false
	}
	switch req.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace, http.MethodPut, http.MethodDelete:
		return true
	}
	_, key := req.Header["Idempotency-Key"]
	_, xkey := req.Header["X-Idempotency-Key"]
	return key || xkey
}

// temporary 判断连接错误是否可能因为重试而成功。
func temporary(err error) bool {
	if errors.Is(err, ErrForbidden) {
		return false
	}
	var dns *net.DNSError
	if errors.As(err, &dns) && dns.IsNotFound {
		return false
	}
	var verify *tls.CertificateVerificationError
	var unknown x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	if errors.As(err, &verify) || errors.As(err, &unknown) || errors.As(err, &hostname) || errors.As(err, &invalid) {
		return false
	}
	return true
}
