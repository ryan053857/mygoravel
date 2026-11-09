package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
)

// 输出格式。
const (
	FormatJSON = "json"
	FormatText = "text"
)

// 标准关联字段名。
const (
	AttrService   = "service"
	AttrVersion   = "version"
	AttrComponent = "component"
	AttrRequestID = "request_id"
	AttrTraceID   = "trace_id"
	AttrMessageID = "message_id"
	AttrAttempt   = "attempt"
)

// Config 是日志配置，对应配置文件的 logs 段；零值即 Info 级别的 JSON 输出。
type Config struct {
	// Level 支持 debug、info、warn、error，以及 info+2 这类偏移写法。
	Level slog.Level `yaml:"level"`
	// Format 为 json 或 text，空值视为 json。
	Format string `yaml:"format"`
	// AddSource 在日志中附加源码位置。
	AddSource bool `yaml:"add_source"`
}

// DefaultConfig 返回默认日志配置：Info 级别、JSON 格式。
func DefaultConfig() Config {
	return Config{Level: slog.LevelInfo, Format: FormatJSON}
}

// SetDefaults 实现 config.Defaulter，使 config.Load 自动填充默认值。
func (c *Config) SetDefaults() { *c = DefaultConfig() }

// Validate 检查输出格式。
func (c Config) Validate() error {
	switch c.Format {
	case "", FormatJSON, FormatText:
		return nil
	default:
		return fmt.Errorf("format must be %q or %q", FormatJSON, FormatText)
	}
}

// Options 是 New 的运行时依赖。
type Options struct {
	// Writer 是输出目标，nil 表示 os.Stdout。
	Writer io.Writer
	// Attrs 是进程级静态字段，例如 service / version。
	Attrs []slog.Attr
}

// New 按 cfg 构造 *slog.Logger；Handler 已包装，会输出 WithAttrs 挂在 context 上的字段。
func New(cfg Config, opts Options) *slog.Logger {
	w := opts.Writer
	if w == nil {
		w = os.Stdout
	}
	hopts := &slog.HandlerOptions{
		AddSource: cfg.AddSource,
		Level:     cfg.Level,
	}
	var h slog.Handler
	if cfg.Format == FormatText {
		h = slog.NewTextHandler(w, hopts)
	} else {
		h = slog.NewJSONHandler(w, hopts)
	}
	logger := slog.New(NewContextHandler(h))
	if len(opts.Attrs) > 0 {
		args := make([]any, 0, len(opts.Attrs))
		for _, a := range opts.Attrs {
			args = append(args, a)
		}
		logger = logger.With(args...)
	}
	return logger
}
