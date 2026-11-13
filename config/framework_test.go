package config_test

import (
	"log/slog"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-tenon/tenon/database"
	"github.com/go-tenon/tenon/httpx"
	"github.com/go-tenon/tenon/logging"
	"github.com/go-tenon/tenon/ratelimit"
)

type frameworkConfig struct {
	HTTP     httpx.Config    `yaml:"http"`
	Logs     logging.Config  `yaml:"logs"`
	Database database.Config `yaml:"database"`
}

func TestLoadsFrameworkConfigs(t *testing.T) {
	cfg, err := load[frameworkConfig](t, `
http:
  addr: :9000
  request_id:
    header: X-Amzn-Trace-Id
  client_ip:
    trusted_proxies: [10.0.0.0/8]
  access_log:
    ignore_paths: [/healthz]
    user_agent: true
  timeout:
    duration: 5s
  rate_limit:
    rate: 5
logs:
  level: debug
  format: text
  time_zone: UTC
  levels:
    http: warn
database:
  dsn: postgres://localhost/app
`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.Addr != ":9000" || cfg.HTTP.Timeout.Duration != 5*time.Second || cfg.HTTP.ShutdownTimeout != 10*time.Second ||
		!reflect.DeepEqual(cfg.HTTP.ClientIP.TrustedProxies, []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}) ||
		cfg.HTTP.BodyLimit != httpx.DefaultBodyLimitConfig() ||
		cfg.HTTP.RateLimit != (ratelimit.Config{Rate: 5, Interval: time.Second, Burst: 20}) {
		t.Fatalf("http=%+v", cfg.HTTP)
	}
	if cfg.HTTP.RequestID.Header != "X-Amzn-Trace-Id" || !cfg.HTTP.AccessLog.UserAgent ||
		!reflect.DeepEqual(cfg.HTTP.AccessLog.IgnorePaths, []string{"/healthz"}) {
		t.Fatalf("http=%+v", cfg.HTTP)
	}
	if cfg.Logs.Level != slog.LevelDebug || cfg.Logs.Format != logging.FormatText || cfg.Logs.TimeZone != "UTC" || cfg.Logs.Levels["http"] != slog.LevelWarn {
		t.Fatalf("logs=%+v", cfg.Logs)
	}
}

func TestFrameworkConfigsValidate(t *testing.T) {
	_, err := load[frameworkConfig](t, "http:\n  addr: ':99999'\n  body_limit:\n    max_bytes: 0\n  rate_limit:\n    burst: 0\n  client_ip:\n    trusted_proxies: [10.0.0.5/8]\n  request_id:\n    header: ''\n  access_log:\n    ignore_paths: [healthz]\nlogs:\n  time_zone: Mars/Olympus\n")
	if err == nil {
		t.Fatal("expected validation errors")
	}
	for _, want := range []string{"http: addr", "http.body_limit: max_bytes", "http.rate_limit: burst", "http.client_ip: trusted_proxies[0]: 10.0.0.5/8 has host bits set", "http.request_id: header is required", "http.access_log: ignore_paths[0]", "logs: time_zone", "database: dsn is required"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("missing %q in %v", want, err)
		}
	}
}
