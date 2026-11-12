package telemetry

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/log"
	lognoop "go.opentelemetry.io/otel/log/noop"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

// Config 是推送配置，对应配置文件的 telemetry 段；Endpoint 为空时不推送。
type Config struct {
	// Endpoint 是 OTLP/HTTP 接收端的基础地址，例如 http://openobserve:5080/api/default；
	// 日志、指标、链路分别发往其下的 /v1/logs、/v1/metrics、/v1/traces。
	Endpoint string `yaml:"endpoint"`
	// Headers 附加到每个请求，通常是认证信息，例如 {Authorization: "Basic ${O2_TOKEN}"}。
	Headers map[string]string `yaml:"headers"`
	// Service 是 service.name；空值取可执行文件名。
	Service string `yaml:"service"`
	// Version 是 service.version；空值取构建信息中的主模块版本，取不到时省略。
	Version string `yaml:"version"`
	// SampleRatio 是新链路的采样比例，0 到 1；接在调用方链路上的 span 跟随调用方的决定。
	SampleRatio float64 `yaml:"sample_ratio"`
}

// DefaultConfig 返回默认配置：不推送，开启时记录全部链路。
func DefaultConfig() Config {
	return Config{SampleRatio: 1}
}

// SetDefaults 实现 config.Defaulter，使 config.Load 自动填充默认值。
func (c *Config) SetDefaults() { *c = DefaultConfig() }

// Validate 检查接收地址与采样比例。
func (c Config) Validate() error {
	if c.SampleRatio < 0 || c.SampleRatio > 1 {
		return fmt.Errorf("sample_ratio must be between 0 and 1")
	}
	if c.Endpoint == "" {
		return nil
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil {
		return fmt.Errorf("endpoint: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("endpoint must be an http or https URL")
	}
	return nil
}

// Providers 是交给框架组件和日志的三个 Provider。
type Providers struct {
	Meters  metric.MeterProvider
	Tracers trace.TracerProvider
	Logs    log.LoggerProvider

	shutdown []func(context.Context) error
}

// Open 按 cfg 创建推送日志、指标和链路的 Provider，三者共用一个 resource：
// service.name、service.version、service.instance.id（每个进程不同）、主机名和进程号。
// 用完后调用 Shutdown 发出缓冲中的数据。
//
// cfg.Endpoint 为空时返回不记录也不推送的 Provider，调用方不需要区分。
//
// 开启推送时，Open 设置 OpenTelemetry 的全局错误处理：导出失败用 slog.Default() 记录为 component=telemetry 的错误日志。
func Open(ctx context.Context, cfg Config) (*Providers, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("telemetry: %w", err)
	}
	if cfg.Endpoint == "" {
		return &Providers{
			Meters:  metricnoop.NewMeterProvider(),
			Tracers: tracenoop.NewTracerProvider(),
			Logs:    lognoop.NewLoggerProvider(),
		}, nil
	}

	res, err := newResource(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("telemetry: resource: %w", err)
	}
	base := strings.TrimSuffix(cfg.Endpoint, "/")

	traces, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpointURL(base+"/v1/traces"),
		otlptracehttp.WithHeaders(cfg.Headers),
		otlptracehttp.WithCompression(otlptracehttp.GzipCompression))
	if err != nil {
		return nil, fmt.Errorf("telemetry: trace exporter: %w", err)
	}
	metrics, err := otlpmetrichttp.New(ctx,
		otlpmetrichttp.WithEndpointURL(base+"/v1/metrics"),
		otlpmetrichttp.WithHeaders(cfg.Headers),
		otlpmetrichttp.WithCompression(otlpmetrichttp.GzipCompression))
	if err != nil {
		return nil, fmt.Errorf("telemetry: metric exporter: %w", err)
	}
	logs, err := otlploghttp.New(ctx,
		otlploghttp.WithEndpointURL(base+"/v1/logs"),
		otlploghttp.WithHeaders(cfg.Headers),
		otlploghttp.WithCompression(otlploghttp.GzipCompression))
	if err != nil {
		return nil, fmt.Errorf("telemetry: log exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(traces),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRatio))),
	)
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metrics)),
	)
	lp := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logs)),
	)
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		slog.Default().Error("telemetry export failed", "component", "telemetry", "error", err)
	}))

	// 日志最后关闭，前两者关闭时的错误还能推送出去。
	return &Providers{
		Meters:   mp,
		Tracers:  tp,
		Logs:     lp,
		shutdown: []func(context.Context) error{tp.Shutdown, mp.Shutdown, lp.Shutdown},
	}, nil
}

// Shutdown 发出缓冲中的数据并停止推送；ctx 到期时放弃剩余的数据。没有开启推送时什么也不做。
func (p *Providers) Shutdown(ctx context.Context) error {
	var errs []error
	for _, fn := range p.shutdown {
		errs = append(errs, fn(ctx))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("telemetry: shutdown: %w", err)
	}
	return nil
}

func newResource(ctx context.Context, cfg Config) (*resource.Resource, error) {
	attrs := []resource.Option{
		resource.WithAttributes(
			semconv.ServiceName(cmp.Or(cfg.Service, executableName())),
			semconv.ServiceInstanceID(uuid.NewString()),
		),
		resource.WithHost(),
		resource.WithProcessPID(),
		resource.WithTelemetrySDK(),
	}
	if v := cmp.Or(cfg.Version, buildVersion()); v != "" {
		attrs = append(attrs, resource.WithAttributes(semconv.ServiceVersion(v)))
	}
	return resource.New(ctx, attrs...)
}

// executableName 返回可执行文件名，作为未配置 Service 时的服务名。
func executableName() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Base(exe)
	}
	return filepath.Base(os.Args[0])
}

// buildVersion 返回主模块版本；本地构建的 (devel) 视为无版本。
func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Version == "(devel)" {
		return ""
	}
	return info.Main.Version
}
