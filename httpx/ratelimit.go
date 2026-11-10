package httpx

import (
	"container/list"
	"math"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/cockroachdb/errors"
	"golang.org/x/time/rate"
)

// RateLimitConfig 是 RateLimit 的参数，对应 http.rate_limit 段。
type RateLimitConfig struct {
	// Rate 是每个 Interval 补充的令牌数。
	Rate int `yaml:"rate"`
	// Interval 是补充 Rate 个令牌的周期。
	Interval time.Duration `yaml:"interval"`
	// Burst 是桶容量，即允许的瞬时突发。
	Burst int `yaml:"burst"`
	// MaxEntries 是同时跟踪的 key 数上限，超出时淘汰最久未访问的 key。
	MaxEntries int `yaml:"max_entries"`
}

// DefaultRateLimitConfig 返回每秒 10 个、突发 20 个、最多跟踪 10000 个 key 的配置。
func DefaultRateLimitConfig() RateLimitConfig {
	return RateLimitConfig{Rate: 10, Interval: time.Second, Burst: 20, MaxEntries: 10000}
}

// SetDefaults 实现 config.Defaulter。
func (c *RateLimitConfig) SetDefaults() { *c = DefaultRateLimitConfig() }

// Validate 检查各字段为正数。
func (c RateLimitConfig) Validate() error {
	switch {
	case c.Rate <= 0:
		return errors.New("rate must be positive")
	case c.Interval <= 0:
		return errors.New("interval must be positive")
	case c.Burst <= 0:
		return errors.New("burst must be positive")
	case c.MaxEntries <= 0:
		return errors.New("max_entries must be positive")
	}
	return nil
}

// KeyFunc 返回请求的限流 key；返回空字符串表示该请求不限流。
type KeyFunc func(*http.Request) string

// KeyByIP 以客户端地址（不含端口）为 key。
// 部署在代理之后时，需要在它之前安装 ClientIP。
func KeyByIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// RateLimit 按 key 返回的值限流，超出时返回 429 并设置 Retry-After。
// cfg 不合法或 key 为 nil 时 panic。
//
// 计数保存在进程内存中，多实例部署时每个实例分别计数。
func RateLimit(cfg RateLimitConfig, key KeyFunc) func(http.Handler) http.Handler {
	mustValid("RateLimit", cfg.Validate())
	if key == nil {
		panic("httpx: RateLimit: nil key function")
	}
	limiters := &limiterCache{
		limit: rate.Limit(float64(cfg.Rate) / cfg.Interval.Seconds()),
		burst: cfg.Burst,
		max:   cfg.MaxEntries,
		items: map[string]*list.Element{},
		order: list.New(),
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			k := key(r)
			if k == "" {
				next.ServeHTTP(w, r)
				return
			}
			res := limiters.get(k).Reserve()
			if delay := res.Delay(); delay > 0 {
				res.Cancel()
				w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(delay.Seconds()))))
				WriteProblem(w, http.StatusTooManyRequests, "too many requests")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// limiterCache 是容量有限的 LRU，保存每个 key 的令牌桶。
type limiterCache struct {
	limit rate.Limit
	burst int
	max   int

	mu    sync.Mutex
	items map[string]*list.Element
	order *list.List // 最近访问的在前
}

type limiterEntry struct {
	key     string
	limiter *rate.Limiter
}

func (c *limiterCache) get(key string) *rate.Limiter {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.order.MoveToFront(el)
		return el.Value.(*limiterEntry).limiter
	}
	if c.order.Len() >= c.max {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.items, oldest.Value.(*limiterEntry).key)
	}
	l := rate.NewLimiter(c.limit, c.burst)
	c.items[key] = c.order.PushFront(&limiterEntry{key: key, limiter: l})
	return l
}
