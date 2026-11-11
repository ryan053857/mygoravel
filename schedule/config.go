package schedule

import (
	"time"

	"github.com/cockroachdb/errors"
)

// Config 是调度器的配置，对应配置文件的 schedule 段。
type Config struct {
	// ShutdownTimeout 是停止时等待执行中的任务结束的最长时间，超时后取消它们的 ctx。
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
}

// DefaultConfig 返回默认配置：停止时最多等待 30 秒。
func DefaultConfig() Config {
	return Config{ShutdownTimeout: 30 * time.Second}
}

// SetDefaults 实现 config.Defaulter。
func (c *Config) SetDefaults() { *c = DefaultConfig() }

// Validate 检查 shutdown_timeout 不是负数；为 0 时停止时立即取消执行中的任务。
func (c Config) Validate() error {
	if c.ShutdownTimeout < 0 {
		return errors.New("shutdown_timeout must not be negative")
	}
	return nil
}
