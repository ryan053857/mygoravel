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
  request_timeout: 5s
  trusted_proxies: [10.0.0.0/8]
logs:
  level: debug
  format: text
database:
  dsn: postgres://localhost/app
`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.Addr != ":9000" || cfg.HTTP.RequestTimeout != 5*time.Second || cfg.HTTP.ShutdownTimeout != 10*time.Second ||
		!reflect.DeepEqual(cfg.HTTP.TrustedProxies, []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}) {
		t.Fatalf("http=%+v", cfg.HTTP)
	}
	if cfg.Logs.Level != slog.LevelDebug || cfg.Logs.Format != logging.FormatText {
		t.Fatalf("logs=%+v", cfg.Logs)
	}
}

func TestFrameworkConfigsValidate(t *testing.T) {
	_, err := load[frameworkConfig](t, "http:\n  max_body_bytes: 0\nlogs:\n  format: xml\n")
	if err == nil {
		t.Fatal("expected validation errors")
	}
	for _, want := range []string{"http: max_body_bytes", "logs: format", "database: dsn is required"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("missing %q in %v", want, err)
		}
	}
}
