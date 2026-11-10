package apitoken

import (
	"time"

	"github.com/cockroachdb/errors"
)

// Config 是登录 token 的有效期配置，对应配置文件的 api_token 段。个人访问令牌的有效期由创建者指定，不受它影响。
//
// 登录 token 的截止时间是“创建时间 + Lifetime”与“最近使用时间 + 空闲时长”中较早的一个，
// 与会话的 lifetime、idle_timeout 含义相同。
type Config struct {
	// Lifetime 是登录 token 从签发起的最长有效期，到期后必须重新登录。
	Lifetime time.Duration `yaml:"lifetime"`
	// IdleTimeout 是登录 token 的最长空闲时间，0 表示不限制。
	IdleTimeout time.Duration `yaml:"idle_timeout"`
	// RememberIdleTimeout 是登录时选择“记住我”的 token 的最长空闲时间，0 表示不限制。
	RememberIdleTimeout time.Duration `yaml:"remember_idle_timeout"`
}

// SetDefaults 实现 config.Defaulter：最长 30 天，空闲 1 天失效，“记住我”空闲 7 天失效。
func (c *Config) SetDefaults() {
	*c = Config{
		Lifetime:            30 * 24 * time.Hour,
		IdleTimeout:         24 * time.Hour,
		RememberIdleTimeout: 7 * 24 * time.Hour,
	}
}

// Validate 检查有效期。
func (c Config) Validate() error {
	var errs []error
	if c.Lifetime <= 0 {
		errs = append(errs, errors.New("lifetime must be positive"))
	}
	if c.IdleTimeout < 0 || c.RememberIdleTimeout < 0 {
		errs = append(errs, errors.New("idle_timeout and remember_idle_timeout must not be negative"))
	}
	return errors.Join(errs...)
}

// idle 返回登录 token 的空闲时长。
func (c Config) idle(remember bool) time.Duration {
	if remember {
		return c.RememberIdleTimeout
	}
	return c.IdleTimeout
}

// deadline 返回登录 token 在 now 使用后的截止时间。
func (c Config) deadline(created, now time.Time, remember bool) time.Time {
	end := created.Add(c.Lifetime)
	if idle := c.idle(remember); idle > 0 && now.Add(idle).Before(end) {
		return now.Add(idle)
	}
	return end
}
