package httpx

import (
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/cockroachdb/errors"

	"github.com/go-tenon/tenon/ratelimit"
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
	// AcquireTimeout 是请求等待共享资源（例如空闲的数据库连接）的最长时间，超过时请求以 503 快速失败，见 acquire 包。
	// 0 表示不限制，等到请求超时为止。由 Defaults 中的 AcquireTimeout 中间件使用。
	AcquireTimeout time.Duration `yaml:"acquire_timeout"`

	RequestID RequestIDConfig `yaml:"request_id"`
	ClientIP  ClientIPConfig  `yaml:"client_ip"`
	AccessLog AccessLogConfig `yaml:"access_log"`
	Timeout   TimeoutConfig   `yaml:"timeout"`
	BodyLimit BodyLimitConfig `yaml:"body_limit"`
	// RateLimit 是全局限流的参数，通常传给 ratelimit.NewMemory 后安装 RateLimit 中间件。
	RateLimit ratelimit.Config `yaml:"rate_limit"`
	CORS      CORSConfig       `yaml:"cors"`
	// AppVersion 是各平台 App 的最低版本，安装 AppVersion 中间件后生效。
	AppVersion AppVersionConfig `yaml:"app_version"`
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
		AcquireTimeout:    time.Second,
		RequestID:         DefaultRequestIDConfig(),
		ClientIP:          DefaultClientIPConfig(),
		AccessLog:         DefaultAccessLogConfig(),
		Timeout:           DefaultTimeoutConfig(),
		BodyLimit:         DefaultBodyLimitConfig(),
		RateLimit:         ratelimit.Config{Rate: 10, Interval: time.Second, Burst: 20},
		CORS:              DefaultCORSConfig(),
		AppVersion:        DefaultAppVersionConfig(),
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
	if c.AcquireTimeout < 0 {
		return errors.New("acquire_timeout must not be negative")
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

// RequestIDConfig 是 AssignRequestID 的参数，对应 http.request_id 段。
type RequestIDConfig struct {
	// Header 是读取入站请求 ID 的请求头，例如反向代理写入的 X-Amzn-Trace-Id。
	// 响应始终通过 X-Request-ID 回写。
	Header string `yaml:"header"`
}

// DefaultRequestIDConfig 返回读取 X-Request-ID 的配置。
func DefaultRequestIDConfig() RequestIDConfig { return RequestIDConfig{Header: "X-Request-ID"} }

// SetDefaults 实现 config.Defaulter。
func (c *RequestIDConfig) SetDefaults() { *c = DefaultRequestIDConfig() }

// Validate 检查请求头名称是合法的 HTTP 字段名。
func (c RequestIDConfig) Validate() error {
	if c.Header == "" {
		return errors.New("header is required")
	}
	for _, ch := range []byte(c.Header) {
		if !isTokenChar(ch) {
			return errors.Newf("header %q is not a valid HTTP field name", c.Header)
		}
	}
	return nil
}

// isTokenChar 报告 c 是否属于 RFC 9110 的 tchar。
func isTokenChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
}

// AccessLogConfig 是 AccessLog 的参数，对应 http.access_log 段。
// 请求头记录中 Authorization、Cookie 等敏感头始终隐藏。
type AccessLogConfig struct {
	// IgnorePaths 中的路径（精确匹配）不记录访问日志，例如健康检查。
	IgnorePaths []string `yaml:"ignore_paths"`
	// UserAgent 记录 User-Agent。
	UserAgent bool `yaml:"user_agent"`
	// RequestHeader、ResponseHeader 记录请求头与响应头。
	RequestHeader  bool `yaml:"request_header"`
	ResponseHeader bool `yaml:"response_header"`
	// RequestBody、ResponseBody 记录请求体与响应体，各最多 64 KiB。
	RequestBody  bool `yaml:"request_body"`
	ResponseBody bool `yaml:"response_body"`
}

// DefaultAccessLogConfig 返回只记录基本字段的配置。
func DefaultAccessLogConfig() AccessLogConfig { return AccessLogConfig{IgnorePaths: []string{}} }

// SetDefaults 实现 config.Defaulter。
func (c *AccessLogConfig) SetDefaults() { *c = DefaultAccessLogConfig() }

// Validate 检查忽略的路径以 / 开头。
func (c AccessLogConfig) Validate() error {
	for i, p := range c.IgnorePaths {
		if !strings.HasPrefix(p, "/") {
			return errors.Newf("ignore_paths[%d]: %q must start with /", i, p)
		}
	}
	return nil
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
