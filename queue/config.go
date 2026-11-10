package queue

import (
	"time"

	"github.com/cockroachdb/errors"
)

// Config 是 Subscriber 的配置，对应配置文件的 queue 段。
type Config struct {
	// Concurrency 是每个 topic 同时处理的任务数上限。
	Concurrency int `yaml:"concurrency"`
	// PollInterval 是没有到期任务时查询的间隔；一次取满时立即再取。
	PollInterval time.Duration `yaml:"poll_interval"`
	// Lease 是领取任务后的租约。处理期间每 Lease/3 自动续期；进程崩溃时任务在租约到期后被重新领取。
	Lease time.Duration `yaml:"lease"`
	// MaxAttempts 是任务最多处理的次数，用完后转入失败任务表。
	MaxAttempts int `yaml:"max_attempts"`
	// ShutdownTimeout 是停止时等待处理中的任务完成的最长时间，超时的任务立即释放给其他实例。
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
}

// DefaultConfig 返回默认配置：并发 10，每秒查询一次，租约 30 秒，最多处理 10 次，停止时最多等待 30 秒。
func DefaultConfig() Config {
	return Config{
		Concurrency:     10,
		PollInterval:    time.Second,
		Lease:           30 * time.Second,
		MaxAttempts:     10,
		ShutdownTimeout: 30 * time.Second,
	}
}

// SetDefaults 实现 config.Defaulter。
func (c *Config) SetDefaults() { *c = DefaultConfig() }

// Validate 检查各项为正数，shutdown_timeout 可以为 0。
func (c Config) Validate() error {
	var errs []error
	if c.Concurrency <= 0 {
		errs = append(errs, errors.New("concurrency must be positive"))
	}
	if c.PollInterval <= 0 {
		errs = append(errs, errors.New("poll_interval must be positive"))
	}
	if c.Lease <= 0 {
		errs = append(errs, errors.New("lease must be positive"))
	}
	if c.MaxAttempts <= 0 {
		errs = append(errs, errors.New("max_attempts must be positive"))
	}
	if c.ShutdownTimeout < 0 {
		errs = append(errs, errors.New("shutdown_timeout must not be negative"))
	}
	return errors.Join(errs...)
}
