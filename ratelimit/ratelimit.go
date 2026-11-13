package ratelimit

import (
	"context"
	"time"

	"github.com/cockroachdb/errors"

	"github.com/go-tenon/tenon/errorx"
)

// ErrExceeded 表示 key 的配额已用完。错误实例带 errorx.RetryAfter，
// 即下一次调用可以成功的最短等待时间。
var ErrExceeded = errorx.Define(errorx.ResourceExhausted, "rate_limit.exceeded")

// Limiter 按 key 限制操作频率。
type Limiter interface {
	// Allow 为 key 消耗一次配额。配额用完时返回 ErrExceeded，存储故障时返回其他错误。
	Allow(ctx context.Context, key string) error
	// Reset 让 key 回到配额全满的状态，例如登录成功后清零失败计数：
	// 每次尝试前调用 Allow，成功后调用 Reset，于是只有连续的失败受到限制。
	Reset(ctx context.Context, key string) error
}

// Config 是限流参数：每个 key 平均每 Interval 允许 Rate 次，最多连续 Burst 次。
type Config struct {
	// Rate 是每个 Interval 补充的配额。
	Rate int `yaml:"rate"`
	// Interval 是补充 Rate 个配额的周期。
	Interval time.Duration `yaml:"interval"`
	// Burst 是允许连续使用的最大配额，即令牌桶容量。
	Burst int `yaml:"burst"`
}

// Validate 检查各字段为正数，且每个配额的补充间隔不小于 1 微秒。
func (c Config) Validate() error {
	switch {
	case c.Rate <= 0:
		return errors.New("rate must be positive")
	case c.Interval <= 0:
		return errors.New("interval must be positive")
	case c.Burst <= 0:
		return errors.New("burst must be positive")
	case c.Interval/time.Duration(c.Rate) < time.Microsecond:
		return errors.New("interval / rate must be at least 1µs")
	}
	return nil
}

// gcra 是 Config 换算出的 GCRA 参数。
type gcra struct {
	// step 是每次调用把理论到达时间推后的量，即补充一个配额的间隔。
	step time.Duration
	// ahead 是理论到达时间最多可以领先当前时间的量，超出即拒绝。
	ahead time.Duration
}

func newGCRA(name string, cfg Config) gcra {
	if err := cfg.Validate(); err != nil {
		panic("ratelimit: " + name + ": " + err.Error())
	}
	step := cfg.Interval / time.Duration(cfg.Rate)
	return gcra{step: step, ahead: step * time.Duration(cfg.Burst-1)}
}

// take 为 now 时刻的一次调用做出决定。tat 是 key 的理论到达时间，没有记录时为零值。
// 放行时返回新的 tat；拒绝时返回下一次调用可以成功的最短等待时间，tat 保持不变。
func (g gcra) take(tat, now time.Time) (next time.Time, wait time.Duration) {
	if tat.Before(now) {
		tat = now
	}
	if wait := tat.Sub(now) - g.ahead; wait > 0 {
		return tat, wait
	}
	return tat.Add(g.step), 0
}

func exceeded(wait time.Duration) error {
	return ErrExceeded.New(errorx.RetryAfter(wait))
}
