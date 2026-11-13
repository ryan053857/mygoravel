package httpx

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/rs/cors"
)

// corsExposed 是总是向前端暴露的响应头：请求 ID、语言、限流后的等待时间、新建资源的地址和下载文件名。
var corsExposed = []string{"X-Request-ID", "Content-Language", "Retry-After", "Location", "Content-Disposition"}

// CORSConfig 是 CORS 的参数，对应 http.cors 段。
type CORSConfig struct {
	// AllowedOrigins 是允许跨域调用的来源，例如 https://app.example.com、capacitor://localhost；
	// 一个来源中可以有一个 * 通配，例如 https://*.example.com；单独的 * 允许任何来源。
	AllowedOrigins []string `yaml:"allowed_origins"`
	// ExposeHeaders 是在 X-Request-ID、Content-Language、Retry-After、Location、Content-Disposition
	// 之外还要让前端读取的响应头。
	ExposeHeaders []string `yaml:"expose_headers"`
	// MaxAge 是浏览器缓存预检结果的时间。Chrome 最多缓存 2 小时。
	MaxAge time.Duration `yaml:"max_age"`
}

// DefaultCORSConfig 返回允许任何来源、预检结果缓存 2 小时的配置。
func DefaultCORSConfig() CORSConfig {
	return CORSConfig{AllowedOrigins: []string{"*"}, ExposeHeaders: []string{}, MaxAge: 2 * time.Hour}
}

// SetDefaults 实现 config.Defaulter。
func (c *CORSConfig) SetDefaults() { *c = DefaultCORSConfig() }

// Validate 检查来源列表不为空、每个来源是 * 或者 scheme://host[:port]（最多一个 * 通配），
// 响应头名称合法，max_age 为正数。
func (c CORSConfig) Validate() error {
	if len(c.AllowedOrigins) == 0 {
		return errors.New("allowed_origins is required")
	}
	for i, o := range c.AllowedOrigins {
		if err := validOrigin(o); err != nil {
			return errors.Wrapf(err, "allowed_origins[%d]", i)
		}
	}
	for i, h := range c.ExposeHeaders {
		if h == "" || strings.IndexFunc(h, func(r rune) bool { return r > 127 || !isTokenChar(byte(r)) }) >= 0 {
			return errors.Newf("expose_headers[%d]: %q is not a valid HTTP field name", i, h)
		}
	}
	if c.MaxAge <= 0 {
		return errors.New("max_age must be positive")
	}
	return nil
}

// validOrigin 检查来源是 *，或者没有路径、查询和片段的 scheme://host[:port]，其中最多一个 *。
func validOrigin(o string) error {
	if o == "*" {
		return nil
	}
	if strings.Count(o, "*") > 1 {
		return errors.Newf("%q has more than one *", o)
	}
	u, err := url.Parse(strings.Replace(o, "*", "x", 1))
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return errors.Newf("%q must be scheme://host[:port]", o)
	}
	return nil
}

// CORS 允许其他来源的网页跨域调用 JSON API，例如独立部署的单页应用、桌面或移动端的 WebView。
// 只安装在 API 的路由组上，页面不开放跨域。cfg 不合法时 panic。
//
// API 只用 Bearer token 认证，不使用 Cookie，因此 CORS 从不允许携带凭据
// （Access-Control-Allow-Credentials）：其他网站的网页拿不到用户的 token，允许任何来源（*）也是安全的。
//
// 预检请求直接以 204 应答，不进入路由、认证和限流；请求头按预检请求声明的原样允许（包括 Authorization）；
// 方法为 GET、HEAD、POST、PUT、PATCH、DELETE。错误响应同样带有 CORS 头，前端能读到错误内容。
func CORS(cfg CORSConfig) func(http.Handler) http.Handler {
	if err := cfg.Validate(); err != nil {
		panic(errors.Wrap(err, "httpx: cors"))
	}
	c := cors.New(cors.Options{
		AllowedOrigins: cfg.AllowedOrigins,
		AllowedMethods: []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete},
		AllowedHeaders: []string{"*"},
		ExposedHeaders: append(append([]string{}, corsExposed...), cfg.ExposeHeaders...),
		MaxAge:         int(cfg.MaxAge / time.Second),
	})
	return c.Handler
}
