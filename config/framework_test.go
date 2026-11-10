package config_test

import (
	"log/slog"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/logging"
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
  client_ip:
    trusted_proxies: [10.0.0.0/8]
  timeout:
    duration: 5s
  rate_limit:
    rate: 5
logs:
  level: debug
  format: text
database:
  dsn: postgres://localhost/app
`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.Addr != ":9000" || cfg.HTTP.Timeout.Duration != 5*time.Second || cfg.HTTP.ShutdownTimeout != 10*time.Second ||
		!reflect.DeepEqual(cfg.HTTP.ClientIP.TrustedProxies, []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}) ||
		cfg.HTTP.BodyLimit != httpx.DefaultBodyLimitConfig() ||
		cfg.HTTP.RateLimit != (httpx.RateLimitConfig{Rate: 5, Interval: time.Second, Burst: 20, MaxEntries: 10000}) {
		t.Fatalf("http=%+v", cfg.HTTP)
	}
	if cfg.Logs.Level != slog.LevelDebug || cfg.Logs.Format != logging.FormatText {
		t.Fatalf("logs=%+v", cfg.Logs)
	}
}

func TestFrameworkConfigsValidate(t *testing.T) {
	_, err := load[frameworkConfig](t, "http:\n  addr: ':99999'\n  body_limit:\n    max_bytes: 0\n  rate_limit:\n    burst: 0\n  client_ip:\n    trusted_proxies: [10.0.0.5/8]\nlogs:\n  format: xml\n")
	if err == nil {
		t.Fatal("expected validation errors")
	}
	for _, want := range []string{"http: addr", "http.body_limit: max_bytes", "http.rate_limit: burst", "http.client_ip: trusted_proxies[0]: 10.0.0.5/8 has host bits set", "logs: format", "database: dsn is required"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("missing %q in %v", want, err)
		}
	}
}
