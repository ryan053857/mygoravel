package ratelimit

import (
	"context"

	"github.com/cockroachdb/errors"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// scopeName 是限流指标的 instrumentation scope。
const scopeName = "github.com/go-tenon/tenon/ratelimit"

// 指标的属性名。
const (
	attrName   = attribute.Key("ratelimit.name")
	attrResult = attribute.Key("ratelimit.result")
)

// Observe 包装 l，把每次 Allow 的结果记录为 OpenTelemetry 计数器 ratelimit.requests，
// 属性 ratelimit.name 是 name，ratelimit.result 是结果：
//
//   - allowed：放行；
//   - exceeded：配额用完，返回 ErrExceeded；
//   - error：存储出错，例如 Redis 不可用。
//
// ctx 被取消或超时导致的错误不记录，它们来自调用方而不是限流器。
// 按结果计数就能看出拒绝的比例和存储故障；key 不进入属性，否则指标序列会随客户端数量增长。
//
//	logins := redislimit.New(rdb, "blog:login", cfg)
//	attempts := ratelimit.Observe(meters, "login", logins)
//
// mp 或 l 为 nil、name 为空时 panic。
func Observe(mp metric.MeterProvider, name string, l Limiter) Limiter {
	switch {
	case mp == nil:
		panic("ratelimit: nil meter provider")
	case name == "":
		panic("ratelimit: empty limiter name")
	case l == nil:
		panic("ratelimit: nil limiter")
	}
	requests, err := mp.Meter(scopeName).Int64Counter("ratelimit.requests", metric.WithUnit("{request}"),
		metric.WithDescription("Rate limiter decisions, by result."))
	if err != nil {
		panic(errors.Wrap(err, "ratelimit: create ratelimit.requests"))
	}
	results := map[string]metric.MeasurementOption{}
	for _, r := range []string{"allowed", "exceeded", "error"} {
		results[r] = metric.WithAttributeSet(attribute.NewSet(attrName.String(name), attrResult.String(r)))
	}
	return &observed{l: l, requests: requests, results: results}
}

type observed struct {
	l        Limiter
	requests metric.Int64Counter
	results  map[string]metric.MeasurementOption
}

// Reset 不记录指标。
func (o *observed) Reset(ctx context.Context, key string) error { return o.l.Reset(ctx, key) }

func (o *observed) Allow(ctx context.Context, key string) error {
	err := o.l.Allow(ctx, key)
	result := "allowed"
	switch {
	case err == nil:
	case errors.Is(err, ErrExceeded):
		result = "exceeded"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	default:
		result = "error"
	}
	o.requests.Add(ctx, 1, o.results[result])
	return err
}
