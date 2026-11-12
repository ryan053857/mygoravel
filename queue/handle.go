package queue

import (
	"context"
	"time"

	"github.com/cockroachdb/errors"
)

// HandleOption 调整 Topic.Handle 注册的处理函数，没有指定的沿用 Worker 的 Config。
type HandleOption func(*handler)

// MaxAttempts 设置这个 topic 的任务最多处理的次数，覆盖 Config.MaxAttempts。n 小于 1 时 panic。
func MaxAttempts(n int) HandleOption {
	if n < 1 {
		panic("queue: max attempts must be positive")
	}
	return func(h *handler) { h.maxAttempts = n }
}

// Concurrency 设置这个 topic 同时处理的任务数上限，覆盖 Config.Concurrency。n 小于 1 时 panic。
func Concurrency(n int) HandleOption {
	if n < 1 {
		panic("queue: concurrency must be positive")
	}
	return func(h *handler) { h.concurrency = n }
}

type attemptKey struct{}

type attemptInfo struct{ n, max int }

// AttemptFromContext 返回处理中的任务本次是第几次执行（从 1 开始），以及这是否是最后一次：
// 本次返回错误时不再重试，任务转入失败任务表。处理函数据此在放弃之前记下最终结果，例如把投递记录标记为失败。
// 返回 Permanent 包装的错误时无论第几次都不再重试，不需要再看 final。ctx 不是任务处理函数的 context 时返回 0 和 false。
func AttemptFromContext(ctx context.Context) (attempt int, final bool) {
	a, ok := ctx.Value(attemptKey{}).(attemptInfo)
	if !ok {
		return 0, false
	}
	return a.n, a.n >= a.max
}

// Permanent 标记不应重试的错误，例如参数非法或数据已被删除。err 为 nil 时返回 nil。
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{err}
}

type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// IsPermanent 报告 err 是否由 Permanent 标记。
func IsPermanent(err error) bool {
	var p *permanentError
	return errors.As(err, &p)
}

// RetryAfter 标记应当在 d 之后重试的错误，代替按执行次数计算的退避时间，
// 例如第三方接口返回 429 并给出了 Retry-After。d 不大于 0 时立即重试。
// 重试仍然计入执行次数，次数用完时照常转入失败任务表；同时被 Permanent 包装时不再重试。err 为 nil 时返回 nil。
func RetryAfter(err error, d time.Duration) error {
	if err == nil {
		return nil
	}
	return &retryAfterError{err, max(d, 0)}
}

type retryAfterError struct {
	err   error
	delay time.Duration
}

func (e *retryAfterError) Error() string { return e.err.Error() }
func (e *retryAfterError) Unwrap() error { return e.err }

// retryDelay 返回第 attempts 次处理以 err 失败后到下一次处理的等待时间。
func retryDelay(err error, attempts int) time.Duration {
	var r *retryAfterError
	if errors.As(err, &r) {
		return r.delay
	}
	return backoff(attempts)
}
