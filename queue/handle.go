package queue

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ThreeDotsLabs/watermill/message/router/middleware"
	"github.com/cockroachdb/errors"

	"github.com/ryan053857/mygoravel/logging"
)

// NewRouter 创建 Watermill 路由器，安装两个中间件：Recoverer 把处理函数的 panic 转为错误；
// 外层记录处理函数返回的错误，Subscriber 据此决定重试或转入失败任务表，并记录日志。
// Watermill 自身的日志降为 debug，任务失败由 Subscriber 以 warn 和 error 级别记录。logger 为 nil 时使用 slog.Default()。
//
// 不要再安装 Watermill 的 Retry 和 PoisonQueue 中间件：重试和失败任务由 Subscriber 持久化处理。
func NewRouter(logger *slog.Logger) *message.Router {
	logger = logging.Or(logger).With(logging.AttrComponent, "queue")
	r, err := message.NewRouter(message.RouterConfig{}, watermill.NewSlogLoggerWithLevelMapping(logger, map[slog.Level]slog.Level{
		slog.LevelInfo:  slog.LevelDebug,
		slog.LevelError: slog.LevelDebug,
	}))
	if err != nil {
		panic(errors.Wrap(err, "queue: new router"))
	}
	r.AddMiddleware(recordError, middleware.Recoverer)
	return r
}

// Handle 把 fn 注册为 topic 的处理函数，处理器名就是 topic。任务的 payload 按 JSON 解码为 T，
// 解码失败视为永久失败。fn 返回错误时任务按退避时间重试，返回 RetryAfter 包装的错误时在指定的时间之后重试，
// 返回 Permanent 包装的错误时直接转入失败任务表。
// 返回的 *message.Handler 可以继续添加只作用于它的中间件。
func Handle[T any](r *message.Router, sub message.Subscriber, topic string, fn func(ctx context.Context, payload T) error) *message.Handler {
	return r.AddConsumerHandler(topic, topic, sub, func(msg *message.Message) error {
		var payload T
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			return Permanent(errors.Wrapf(err, "queue: decode %q payload", topic))
		}
		return fn(msg.Context(), payload)
	})
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

// outcome 记录处理函数返回的错误。Subscriber 把它放进消息的 context，
// recordError 在 Nack 之前写入，Subscriber 在收到 Nack 之后读取。
type outcome struct {
	err error
}

type outcomeKey struct{}

func recordError(h message.HandlerFunc) message.HandlerFunc {
	return func(msg *message.Message) ([]*message.Message, error) {
		out, err := h(msg)
		if err != nil {
			if o, ok := msg.Context().Value(outcomeKey{}).(*outcome); ok {
				o.err = err
			}
		}
		return out, err
	}
}
