package httpx

import (
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"github.com/cockroachdb/errors"
)

// Config 是 HTTP 配置，对应配置文件的 http 段：顶层是服务器参数，
// 各子段是同名中间件的参数。配置只提供参数，中间件是否启用取决于代码是否安装它。
type Config struct {
	// Addr 是监听地址，格式为 host:port，端口为 0–65535 的数字。
	Addr string `yaml:"addr"`
	// ReadHeaderTimeout、ReadTimeout、WriteTimeout、IdleTimeout 对应 http.Server 的同名字段。
	ReadHeaderTimeout time.Duration `yaml:"read_header_timeout"`
	ReadTimeout       time.Duration `yaml:"read_timeout"`
	WriteTimeout      time.Duration `yaml:"write_timeout"`
	IdleTimeout       time.Duration `yaml:"idle_timeout"`
	// ShutdownTimeout 是停机时排空请求的上限。
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`

	ClientIP  ClientIPConfig  `yaml:"client_ip"`
	Timeout   TimeoutConfig   `yaml:"timeout"`
	BodyLimit BodyLimitConfig `yaml:"body_limit"`
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
		ShutdownTimeout:   10 * time.Second,
		ClientIP:          DefaultClientIPConfig(),
		Timeout:           DefaultTimeoutConfig(),
		BodyLimit:         DefaultBodyLimitConfig(),
		RateLimit:         DefaultRateLimitConfig(),
	}
}

// SetDefaults 实现 config.Defaulter，使 config.Load 自动填充默认值。
func (c *Config) SetDefaults() { *c = DefaultConfig() }

// Validate 检查服务器参数：地址格式合法、各超时为正数。
// 子段由各自的 Validate 检查；config.Load 会逐个调用，中间件构造时也会调用。
func (c Config) Validate() error {
	_, port, err := net.SplitHostPort(c.Addr)
	if err != nil {
		return errors.Newf("addr %q must be host:port", c.Addr)
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return errors.Newf("addr %q: port must be a number in 0-65535", c.Addr)
	}
	for _, f := range []struct {
		name string
		d    time.Duration
	}{
		{"read_header_timeout", c.ReadHeaderTimeout},
		{"read_timeout", c.ReadTimeout},
		{"write_timeout", c.WriteTimeout},
		{"idle_timeout", c.IdleTimeout},
		{"shutdown_timeout", c.ShutdownTimeout},
	} {
		if f.d <= 0 {
			return errors.Newf("%s must be positive", f.name)
		}
	}
	return nil
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

// ClientIPConfig 是 ClientIP 的参数，对应 http.client_ip 段。
type ClientIPConfig struct {
	// TrustedProxies 是可信反向代理网段；为空时不信任任何代理。
	// 每一项必须是网络地址，即主机位全为零，例如 10.0.0.0/8；单个地址写作 10.0.0.5/32。
	TrustedProxies []netip.Prefix `yaml:"trusted_proxies"`
}

// DefaultClientIPConfig 返回不信任任何代理的配置。
func DefaultClientIPConfig() ClientIPConfig {
	return ClientIPConfig{TrustedProxies: []netip.Prefix{}}
}

// SetDefaults 实现 config.Defaulter。
func (c *ClientIPConfig) SetDefaults() { *c = DefaultClientIPConfig() }

// Validate 检查每个网段有效且主机位为零。
func (c ClientIPConfig) Validate() error {
	for i, p := range c.TrustedProxies {
		if !p.IsValid() {
			return errors.Newf("trusted_proxies[%d] is not a valid network", i)
		}
		if m := p.Masked(); m != p {
			return errors.Newf("trusted_proxies[%d]: %s has host bits set; use %s or %s",
				i, p, m, netip.PrefixFrom(p.Addr(), p.Addr().BitLen()))
		}
	}
	return nil
}

// TimeoutConfig 是 Timeout 的参数，对应 http.timeout 段。
type TimeoutConfig struct {
	// Duration 是每个请求 context 的超时。
	Duration time.Duration `yaml:"duration"`
}

// DefaultTimeoutConfig 返回 30 秒超时。
func DefaultTimeoutConfig() TimeoutConfig { return TimeoutConfig{Duration: 30 * time.Second} }

// SetDefaults 实现 config.Defaulter。
func (c *TimeoutConfig) SetDefaults() { *c = DefaultTimeoutConfig() }

// Validate 检查超时为正数。
func (c TimeoutConfig) Validate() error {
	if c.Duration <= 0 {
		return errors.New("duration must be positive")
	}
	return nil
}

// BodyLimitConfig 是 BodyLimit 的参数，对应 http.body_limit 段。
type BodyLimitConfig struct {
	// MaxBytes 是请求体的最大字节数。
	MaxBytes int64 `yaml:"max_bytes"`
}

// DefaultBodyLimitConfig 返回 1 MiB 上限。
func DefaultBodyLimitConfig() BodyLimitConfig { return BodyLimitConfig{MaxBytes: 1 << 20} }

// SetDefaults 实现 config.Defaulter。
func (c *BodyLimitConfig) SetDefaults() { *c = DefaultBodyLimitConfig() }

// Validate 检查上限为正数。
func (c BodyLimitConfig) Validate() error {
	if c.MaxBytes <= 0 {
		return errors.New("max_bytes must be positive")
	}
	return nil
}

// mustValid 在中间件构造时校验参数，不合法时 panic。
func mustValid(middleware string, err error) {
	if err != nil {
		panic(errors.Wrapf(err, "httpx: %s", middleware))
	}
}
