package httpx

import (
	"net/http"
	"net/netip"
	"time"

	"github.com/cockroachdb/errors"
)

// Config 是 HTTP 服务配置，对应配置文件的 http 段。
// 框架函数把零值字段视为未设置，使用 DefaultConfig 中的值。
type Config struct {
	// Addr 是监听地址。
	Addr string `yaml:"addr"`
	// ReadHeaderTimeout、ReadTimeout、WriteTimeout、IdleTimeout 对应 http.Server 的同名字段。
	ReadHeaderTimeout time.Duration `yaml:"read_header_timeout"`
	ReadTimeout       time.Duration `yaml:"read_timeout"`
	WriteTimeout      time.Duration `yaml:"write_timeout"`
	IdleTimeout       time.Duration `yaml:"idle_timeout"`
	// RequestTimeout 是每个请求 context 的超时。
	RequestTimeout time.Duration `yaml:"request_timeout"`
	// ShutdownTimeout 是停机时排空请求的上限。
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
	// MaxBodyBytes 是请求体大小上限。
	MaxBodyBytes int64 `yaml:"max_body_bytes"`
	// TrustedProxies 是可信反向代理网段；为空时不信任 X-Forwarded-For。
	TrustedProxies []netip.Prefix `yaml:"trusted_proxies"`
	// RateLimit 是按客户端限流的配置；零值表示不限流。
	RateLimit RateLimitConfig `yaml:"rate_limit"`
}

// DefaultConfig 返回默认 HTTP 配置。
func DefaultConfig() Config {
	return Config{
		Addr:              ":8080",
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       time.Minute,
		RequestTimeout:    30 * time.Second,
		ShutdownTimeout:   10 * time.Second,
		MaxBodyBytes:      1 << 20,
		TrustedProxies:    []netip.Prefix{},
	}
}

// SetDefaults 实现 config.Defaulter，使 config.Load 自动填充默认值。
func (c *Config) SetDefaults() { *c = DefaultConfig() }

// Validate 检查地址非空、超时与限额为正数。
func (c Config) Validate() error {
	if c.Addr == "" {
		return errors.New("addr is required")
	}
	for _, f := range []struct {
		name string
		d    time.Duration
	}{
		{"read_header_timeout", c.ReadHeaderTimeout},
		{"read_timeout", c.ReadTimeout},
		{"write_timeout", c.WriteTimeout},
		{"idle_timeout", c.IdleTimeout},
		{"request_timeout", c.RequestTimeout},
		{"shutdown_timeout", c.ShutdownTimeout},
	} {
		if f.d <= 0 {
			return errors.Newf("%s must be positive", f.name)
		}
	}
	if c.MaxBodyBytes <= 0 {
		return errors.New("max_body_bytes must be positive")
	}
	return nil
}

// withDefaults 用 DefaultConfig 填充零值字段。
func (c Config) withDefaults() Config {
	d := DefaultConfig()
	if c.Addr == "" {
		c.Addr = d.Addr
	}
	fill := func(v *time.Duration, def time.Duration) {
		if *v <= 0 {
			*v = def
		}
	}
	fill(&c.ReadHeaderTimeout, d.ReadHeaderTimeout)
	fill(&c.ReadTimeout, d.ReadTimeout)
	fill(&c.WriteTimeout, d.WriteTimeout)
	fill(&c.IdleTimeout, d.IdleTimeout)
	fill(&c.RequestTimeout, d.RequestTimeout)
	fill(&c.ShutdownTimeout, d.ShutdownTimeout)
	if c.MaxBodyBytes <= 0 {
		c.MaxBodyBytes = d.MaxBodyBytes
	}
	c.TrustedProxies = append([]netip.Prefix(nil), c.TrustedProxies...)
	return c
}

func (c Config) server(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: c.ReadHeaderTimeout,
		ReadTimeout:       c.ReadTimeout,
		WriteTimeout:      c.WriteTimeout,
		IdleTimeout:       c.IdleTimeout,
	}
}
