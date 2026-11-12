// Package redislimit 是基于 rueidis 的限流器，实现 ratelimit.Limiter，多个实例共享配额。
//
//	logins := redislimit.New(rdb, "blog:login", ratelimit.Config{Rate: 5, Interval: time.Minute, Burst: 5})
package redislimit

import (
	"context"
	"strconv"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/redis/rueidis"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/ratelimit"
)

var _ ratelimit.Limiter = (*Limiter)(nil)

// Limiter 把每个 key 的理论到达时间（tat）存为一个 Redis 键，键名是 name:key。
// 算法与 ratelimit.Memory 相同；一次 Allow 是一次脚本调用，时间取 Redis 服务端时钟。
// 键在配额回满时过期，由 Redis 自动删除，不需要清理。
type Limiter struct {
	rdb    rueidis.Client
	prefix string
	// step 是补充一个配额的间隔，ahead 是 tat 最多领先当前时间的量，单位都是微秒。
	step, ahead string
}

// New 创建 Redis 限流器。name 区分不同的限流器，同名的限流器共享配额，因此 name 应与 cfg 一一对应。
// rdb 为 nil、name 为空或 cfg 不合法时 panic。
func New(rdb rueidis.Client, name string, cfg ratelimit.Config) *Limiter {
	switch {
	case rdb == nil:
		panic("redislimit: nil client")
	case name == "":
		panic("redislimit: empty name")
	}
	if err := cfg.Validate(); err != nil {
		panic("redislimit: " + err.Error())
	}
	step := cfg.Interval / time.Duration(cfg.Rate)
	return &Limiter{
		rdb:    rdb,
		prefix: name + ":",
		step:   strconv.FormatInt(step.Microseconds(), 10),
		ahead:  strconv.FormatInt((step * time.Duration(cfg.Burst-1)).Microseconds(), 10),
	}
}

// allow 放行时把 tat 推后 step 并返回 0，拒绝时不修改并返回需要等待的微秒数。
// 时间以微秒计，当前的 Unix 微秒数在 Lua 的双精度数中可以精确表示。
var allow = rueidis.NewLuaScript(`
local t = redis.call("TIME")
local now = tonumber(t[1]) * 1000000 + tonumber(t[2])
local step, ahead = tonumber(ARGV[1]), tonumber(ARGV[2])
local tat = math.max(tonumber(redis.call("GET", KEYS[1]) or now), now)
local wait = tat - now - ahead
if wait > 0 then
	return wait
end
tat = tat + step
redis.call("SET", KEYS[1], string.format("%.0f", tat), "PX", math.ceil((tat - now) / 1000))
return 0`)

// Allow 实现 ratelimit.Limiter。
func (l *Limiter) Allow(ctx context.Context, key string) error {
	wait, err := allow.Exec(ctx, l.rdb, []string{l.prefix + key}, []string{l.step, l.ahead}).AsInt64()
	if err != nil {
		return errors.Wrap(err, "ratelimit: allow")
	}
	if wait > 0 {
		return ratelimit.ErrExceeded.New(errorx.RetryAfter(time.Duration(wait) * time.Microsecond))
	}
	return nil
}

// Reset 实现 ratelimit.Limiter：删除 key 的 Redis 键。
func (l *Limiter) Reset(ctx context.Context, key string) error {
	return errors.Wrap(l.rdb.Do(ctx, l.rdb.B().Del().Key(l.prefix+key).Build()).Error(), "ratelimit: reset")
}
