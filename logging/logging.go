package logging

import (
	"cmp"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime/debug"
	"time"

	"github.com/lmittmann/tint"
	slogformatter "github.com/samber/slog-formatter"
	slogmulti "github.com/samber/slog-multi"
	"go.opentelemetry.io/otel/log"
	"golang.org/x/term"
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
	AttrSpanID    = "span_id"
	AttrMessageID = "message_id"
	AttrAttempt   = "attempt"
)

// Config 是日志配置，对应配置文件的 logs 段；零值即输出到 stderr 的 Info 级别 JSON 日志。
type Config struct {
	// Level 支持 debug、info、warn、error，以及 info+2 这类偏移写法。
	Level slog.Level `yaml:"level"`
	// Levels 按 component 字段覆盖 Level，例如 {http: warn, billing: debug}；
	// 框架组件的 component 见 docs/logging.md。
	Levels map[string]slog.Level `yaml:"levels"`
	// Format 为 json 或 text，空值视为 json。json 供日志系统采集；
	// text 供本地开发阅读，输出到终端时着色（遵守 NO_COLOR），不是严格的 logfmt。
	Format string `yaml:"format"`
	// AddSource 在日志中附加源码位置。
	AddSource bool `yaml:"add_source"`
	// Service 非空时作为 service 字段附加到每条日志。
	Service string `yaml:"service"`
	// Version 作为 version 字段附加到每条日志；空值取构建信息中的主模块版本，取不到时省略。
	Version string `yaml:"version"`
	// TimeZone 是日志时间使用的时区，例如 UTC、Asia/Shanghai；空值表示本地时区。
	TimeZone string `yaml:"time_zone"`
	// Writer 是基础输出的目标，nil 表示 os.Stderr；只能在代码中设置。
	Writer io.Writer `yaml:"-"`
}

// DefaultConfig 返回默认日志配置：Info 级别、JSON 格式。
func DefaultConfig() Config {
	return Config{Level: slog.LevelInfo, Format: FormatJSON}
}

// SetDefaults 实现 config.Defaulter，使 config.Load 自动填充默认值。
func (c *Config) SetDefaults() { *c = DefaultConfig() }

// Validate 检查输出格式与时区。
func (c Config) Validate() error {
	switch c.Format {
	case "", FormatJSON, FormatText:
	default:
		return fmt.Errorf("format must be %q or %q", FormatJSON, FormatText)
	}
	if _, err := c.location(); err != nil {
		return fmt.Errorf("time_zone: %w", err)
	}
	if _, ok := c.Levels[""]; ok {
		return fmt.Errorf("levels: component name must not be empty")
	}
	return nil
}

// location 返回 TimeZone 对应的时区；空值返回 nil，表示不转换。
func (c Config) location() (*time.Location, error) {
	if c.TimeZone == "" {
		return nil, nil
	}
	return time.LoadLocation(c.TimeZone)
}

// Option 扩展 New 构造的处理链。
type Option func(*options)

type options struct {
	formatters  []slogformatter.Formatter
	middlewares []slogmulti.Middleware
	handlers    []slog.Handler
	export      log.LoggerProvider
}

// Format 追加字段格式化规则，例如 slogformatter.PIIFormatter("user")。
// 格式化在分发之前执行，作用于所有输出。
func Format(formatters ...slogformatter.Formatter) Option {
	return func(o *options) { o.formatters = append(o.formatters, formatters...) }
}

// Pipe 追加 Handler 中间件，例如过滤或采样；按传入顺序由外到内执行，位于格式化之前。
func Pipe(middlewares ...slogmulti.Middleware) Option {
	return func(o *options) { o.middlewares = append(o.middlewares, middlewares...) }
}

// Fanout 追加输出，与基础输出并列接收日志，例如 Slack 告警或 syslog。
// 各输出按自身的 Enabled 过滤级别；按条件分发可传入 slogmulti.Router().Handler()。
func Fanout(handlers ...slog.Handler) Option {
	return func(o *options) { o.handlers = append(o.handlers, handlers...) }
}

// New 按 cfg 构造 *slog.Logger，处理链由外到内为：
//
//	context 字段 → 组件级别 → service / version → Pipe 中间件 → 格式化 → 基础输出 + Fanout 输出 + Export
//
// 格式化默认包含 ErrorFormatter；cfg.TimeZone 非空时还会把时间值转换到该时区。
// New 不修改 slog.Default，通常在入口处用 slog.SetDefault 安装它。cfg 不合法时 panic。
func New(cfg Config, opts ...Option) *slog.Logger {
	if err := cfg.Validate(); err != nil {
		panic(fmt.Errorf("logging: %w", err))
	}
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	loc, _ := cfg.location()

	h := baseHandler(cfg, loc)
	handlers := o.handlers
	if o.export != nil {
		handlers = append(handlers, newExportHandler(o.export, cfg))
	}
	if len(handlers) > 0 {
		h = slog.NewMultiHandler(append([]slog.Handler{h}, handlers...)...)
	}
	formatters := append([]slogformatter.Formatter{ErrorFormatter()}, o.formatters...)
	if loc != nil {
		formatters = append(formatters, slogformatter.TimezoneConverter(loc))
	}
	h = slogformatter.NewFormatterHandler(formatters...)(h)
	if len(o.middlewares) > 0 {
		h = slogmulti.Pipe(o.middlewares...).Handler(h)
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
	if len(cfg.Levels) > 0 {
		h = &componentHandler{inner: h, root: cfg.Level, levels: cfg.Levels}
	}
	return slog.New(NewContextHandler(h))
}

// minLevel 返回 Level 和各组件级别中最低的一个。输出放行到这个级别，由 componentHandler 负责按组件过滤。
func (c Config) minLevel() slog.Level {
	level := c.Level
	for _, lv := range c.Levels {
		level = min(level, lv)
	}
	return level
}

// baseHandler 按格式写入 cfg.Writer；loc 非空时把记录时间转换到该时区。
func baseHandler(cfg Config, loc *time.Location) slog.Handler {
	w := cfg.Writer
	if w == nil {
		w = os.Stderr
	}
	hopts := &slog.HandlerOptions{AddSource: cfg.AddSource, Level: cfg.minLevel()}
	if loc != nil {
		hopts.ReplaceAttr = func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey && a.Value.Kind() == slog.KindTime {
				a.Value = slog.TimeValue(a.Value.Time().In(loc))
			}
			return a
		}
	}
	if cfg.Format == FormatText {
		return tint.NewTextHandler(w, &tint.Options{
			AddSource:   hopts.AddSource,
			Level:       hopts.Level,
			ReplaceAttr: hopts.ReplaceAttr,
			TimeFormat:  textTimeFormat,
			NoColor:     !colorful(w),
		})
	}
	return slog.NewJSONHandler(w, hopts)
}

// textTimeFormat 是 text 格式的时间格式：带日期，跨天查看或按时间关联日志时没有歧义。
const textTimeFormat = "2006-01-02 15:04:05.000"

// colorful 报告是否为 w 输出颜色：w 是终端且未设置 NO_COLOR（https://no-color.org）。
func colorful(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	f, ok := w.(interface{ Fd() uintptr })
	return ok && term.IsTerminal(int(f.Fd()))
}

// Or 返回 l，l 为 nil 时返回当前的 slog.Default()。框架组件用它解析可选的 Logger 参数。
// 不是 New 构造的日志器会补上 context 字段和 ErrorFormatter。
func Or(l *slog.Logger) *slog.Logger {
	if l == nil {
		l = slog.Default()
	}
	h := l.Handler()
	if _, ok := h.(contextHandler); ok {
		return l
	}
	return slog.New(NewContextHandler(slogformatter.NewFormatterHandler(ErrorFormatter())(h)))
}

// buildVersion 返回主模块版本；本地构建的 (devel) 视为无版本。
func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Version == "(devel)" {
		return ""
	}
	return info.Main.Version
}
