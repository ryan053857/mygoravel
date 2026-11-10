package logging

import (
	"cmp"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime/debug"
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

// Config 是日志配置，对应配置文件的 logs 段；零值即输出到 stderr 的 Info 级别 JSON 日志。
type Config struct {
	// Level 支持 debug、info、warn、error，以及 info+2 这类偏移写法。
	Level slog.Level `yaml:"level"`
	// Format 为 json 或 text，空值视为 json。
	Format string `yaml:"format"`
	// AddSource 在日志中附加源码位置。
	AddSource bool `yaml:"add_source"`
	// Service 非空时作为 service 字段附加到每条日志。
	Service string `yaml:"service"`
	// Version 作为 version 字段附加到每条日志；空值取构建信息中的主模块版本，取不到时省略。
	Version string `yaml:"version"`
	// Writer 是输出目标，nil 表示 os.Stderr；只能在代码中设置。
	Writer io.Writer `yaml:"-"`
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

// New 按 cfg 构造 *slog.Logger；Handler 已包装，会输出 WithAttrs 挂在 context 上的字段。
// New 不修改 slog.Default，通常在入口处用 slog.SetDefault 安装它。
func New(cfg Config) *slog.Logger {
	w := cfg.Writer
	if w == nil {
		w = os.Stderr
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
	var attrs []slog.Attr
	if cfg.Service != "" {
		attrs = append(attrs, slog.String(AttrService, cfg.Service))
	}
	if v := cmp.Or(cfg.Version, buildVersion()); v != "" {
		attrs = append(attrs, slog.String(AttrVersion, v))
	}
	if len(attrs) > 0 {
		h = h.WithAttrs(attrs)
	}
	return slog.New(NewContextHandler(h))
}

// Or 返回 l，l 为 nil 时返回当前的 slog.Default()；结果的 Handler 已用 NewContextHandler 包装。
// 框架组件用它解析可选的 Logger 参数。
func Or(l *slog.Logger) *slog.Logger {
	if l == nil {
		l = slog.Default()
	}
	return slog.New(NewContextHandler(l.Handler()))
}

// buildVersion 返回主模块版本；本地构建的 (devel) 视为无版本。
func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Version == "(devel)" {
		return ""
	}
	return info.Main.Version
}
