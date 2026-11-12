package queue

import (
	"time"

	"github.com/cockroachdb/errors"
)

// Config 是 Worker 的配置，对应配置文件的 queue 段。Concurrency 和 MaxAttempts 是各 topic 的默认值，
// 注册处理函数时可以用同名的 HandleOption 覆盖。
type Config struct {
	// NSQD 是 nsqd 的 TCP 地址，转发任务时发布到这里；没有设置 Lookupd 时也从这里消费。
	NSQD string `yaml:"nsqd"`
	// Lookupd 是 nsqlookupd 的 HTTP 地址。设置后从它们发现所有 nsqd 并消费，多个 nsqd 的任务都会被处理。
	Lookupd []string `yaml:"lookupd"`
	// Prefix 加在每个 topic 前面，成为 NSQ 中的 topic 名，例如 "blog."；多个应用或环境共用一个 nsqd 时用它区分。
	// 加上前缀后的 topic 名最长 64 个字符。
	Prefix string `yaml:"prefix"`
	// Channel 是 NSQ 的 channel 名。同一个应用的 worker 使用同一个 channel，共同消费每个 topic，每个任务只处理一次。
	Channel string `yaml:"channel"`
	// Concurrency 是每个 topic 同时处理的任务数上限。
	Concurrency int `yaml:"concurrency"`
	// MaxAttempts 是任务最多处理的次数，用完后转入失败任务表。
	MaxAttempts int `yaml:"max_attempts"`
	// Lease 是 NSQ 等待处理结果的时间（msg_timeout）。处理期间每 Lease/3 自动延长；
	// 进程崩溃时任务在 Lease 之后被重新投递。不能超过 nsqd 的 --max-msg-timeout（默认 15 分钟）。
	Lease time.Duration `yaml:"lease"`
	// MaxDefer 是 NSQ 延迟消息的最长延迟，必须不超过 nsqd 的 --max-req-timeout（默认 1 小时）。
	// 更晚执行的任务和重试先留在 queue_outbox 中，到时间再转发。
	MaxDefer time.Duration `yaml:"max_defer"`
	// PollInterval 是检查 queue_outbox 的最长间隔。新发布的任务提交时会通知 worker，立即转发，不必等它。
	PollInterval time.Duration `yaml:"poll_interval"`
	// ShutdownTimeout 是停止时等待处理中的任务完成的最长时间，超时的任务立即放回 NSQ。
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
}

// DefaultConfig 返回默认配置：本机 nsqd，channel 为 worker，并发 10，最多处理 10 次，租约 30 秒，
// 最长延迟 1 小时，每秒检查一次 outbox，停止时最多等待 30 秒。
func DefaultConfig() Config {
	return Config{
		NSQD:            "127.0.0.1:4150",
		Channel:         "worker",
		Concurrency:     10,
		MaxAttempts:     10,
		Lease:           30 * time.Second,
		MaxDefer:        time.Hour,
		PollInterval:    time.Second,
		ShutdownTimeout: 30 * time.Second,
	}
}

// SetDefaults 实现 config.Defaulter。
func (c *Config) SetDefaults() { *c = DefaultConfig() }

// Validate 检查各项为正数，shutdown_timeout 可以为 0。
func (c Config) Validate() error {
	var errs []error
	if c.NSQD == "" {
		errs = append(errs, errors.New("nsqd is required"))
	}
	if c.Prefix != "" && !validName(c.Prefix) {
		errs = append(errs, errors.Newf("prefix %q must be letters, digits, '.', '_' or '-'", c.Prefix))
	}
	if !validName(c.Channel) {
		errs = append(errs, errors.Newf("channel %q must be 1 to 64 letters, digits, '.', '_' or '-'", c.Channel))
	}
	if c.Concurrency <= 0 {
		errs = append(errs, errors.New("concurrency must be positive"))
	}
	if c.MaxAttempts <= 0 {
		errs = append(errs, errors.New("max_attempts must be positive"))
	}
	if c.Lease < time.Second {
		errs = append(errs, errors.New("lease must be at least 1s"))
	}
	if c.MaxDefer <= 0 {
		errs = append(errs, errors.New("max_defer must be positive"))
	}
	if c.PollInterval <= 0 {
		errs = append(errs, errors.New("poll_interval must be positive"))
	}
	if c.ShutdownTimeout < 0 {
		errs = append(errs, errors.New("shutdown_timeout must not be negative"))
	}
	return errors.Join(errs...)
}

// validName 报告 s 是否是合法的 NSQ topic 或 channel 名：1 到 64 个字母、数字、'.'、'_' 或 '-'。
func validName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}
