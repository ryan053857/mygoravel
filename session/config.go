package session

import (
	"net/http"
	"time"

	"github.com/cockroachdb/errors"
)

// Config 是会话配置，对应配置文件的 session 段。
type Config struct {
	// Lifetime 是会话从创建（或登录）起的最长有效期。
	Lifetime time.Duration `yaml:"lifetime"`
	// IdleTimeout 是会话的最长空闲时间，0 表示不限制。
	IdleTimeout time.Duration `yaml:"idle_timeout"`
	Cookie      CookieConfig  `yaml:"cookie"`
}

// CookieConfig 是会话 cookie 的属性。cookie 始终为 HttpOnly。
type CookieConfig struct {
	Name   string `yaml:"name"`
	Domain string `yaml:"domain"`
	Path   string `yaml:"path"`
	// Secure 要求只通过 HTTPS 发送；浏览器对 http://localhost 同样接受 Secure cookie。
	Secure bool `yaml:"secure"`
	// SameSite 取 lax、strict 或 none；none 要求 Secure。
	SameSite string `yaml:"same_site"`
	// Persist 为 true 时 cookie 在浏览器关闭后保留，直到会话过期。
	Persist bool `yaml:"persist"`
}

// SetDefaults 实现 config.Defaulter：24 小时有效期，不限空闲，
// 名为 session、路径为 /、Secure、SameSite=Lax 的持久 cookie。
func (c *Config) SetDefaults() {
	*c = Config{
		Lifetime: 24 * time.Hour,
		Cookie: CookieConfig{
			Name:     "session",
			Path:     "/",
			Secure:   true,
			SameSite: "lax",
			Persist:  true,
		},
	}
}

// Validate 检查有效期与 cookie 属性。
func (c Config) Validate() error {
	var errs []error
	if c.Lifetime <= 0 {
		errs = append(errs, errors.New("lifetime must be positive"))
	}
	if c.IdleTimeout < 0 {
		errs = append(errs, errors.New("idle_timeout must not be negative"))
	}
	if err := (&http.Cookie{Name: c.Cookie.Name, Value: "x"}).Valid(); c.Cookie.Name == "" || err != nil {
		errs = append(errs, errors.Newf("cookie.name %q is not a valid cookie name", c.Cookie.Name))
	}
	if _, err := c.Cookie.sameSite(); err != nil {
		errs = append(errs, err)
	} else if c.Cookie.SameSite == "none" && !c.Cookie.Secure {
		errs = append(errs, errors.New("cookie.same_site none requires cookie.secure"))
	}
	return errors.Join(errs...)
}

func (c CookieConfig) sameSite() (http.SameSite, error) {
	switch c.SameSite {
	case "lax":
		return http.SameSiteLaxMode, nil
	case "strict":
		return http.SameSiteStrictMode, nil
	case "none":
		return http.SameSiteNoneMode, nil
	default:
		return 0, errors.Newf("cookie.same_site %q must be lax, strict or none", c.SameSite)
	}
}
