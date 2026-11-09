package config_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/errors"

	"github.com/ryan053857/mygoravel/config"
)

type httpConfig struct {
	Addr    string        `yaml:"addr"`
	Port    int           `yaml:"port"`
	Timeout time.Duration `yaml:"timeout"`
	Debug   bool          `yaml:"debug"`
}

type dbConfig struct {
	DSN      string `yaml:"dsn"`
	Password string `yaml:"password"`
}

type appConfig struct {
	HTTP httpConfig `yaml:"http"`
	DB   dbConfig   `yaml:"db"`
}

func defaults() appConfig {
	return appConfig{HTTP: httpConfig{Addr: ":8080", Port: 8080, Timeout: 5 * time.Second}}
}

// writeFiles 在临时目录写入文件并返回目录。
func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// load 把 src 写入临时配置文件后加载到 dst。
func load(t *testing.T, src string, dst any) error {
	t.Helper()
	dir := writeFiles(t, map[string]string{"config.yaml": src})
	return config.Load(filepath.Join(dir, "config.yaml"), dst)
}

func mustLoad(t *testing.T, src string) appConfig {
	t.Helper()
	cfg := defaults()
	if err := load(t, src, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestOverridesDefaults(t *testing.T) {
	cfg := mustLoad(t, "http:\n  port: 9000\n  timeout: 10s\n")
	want := defaults()
	want.HTTP.Port = 9000
	want.HTTP.Timeout = 10 * time.Second
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("got %+v want %+v", cfg, want)
	}
}

func TestMissingFieldsKeepDefaults(t *testing.T) {
	for _, src := range []string{"", "\n", "# comment\n", "db: {}\n"} {
		if cfg := mustLoad(t, src); !reflect.DeepEqual(cfg, defaults()) {
			t.Fatalf("%q: %+v", src, cfg)
		}
	}
}

func TestZeroValuesOverride(t *testing.T) {
	cfg := defaults()
	cfg.HTTP.Debug = true
	if err := load(t, "http:\n  port: 0\n  debug: false\n  addr: \"\"\n", &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.Port != 0 || cfg.HTTP.Debug || cfg.HTTP.Addr != "" {
		t.Fatalf("zero values not applied: %+v", cfg.HTTP)
	}
}

func TestMapMergeSliceReplace(t *testing.T) {
	type wrap struct {
		Tags   []string          `yaml:"tags"`
		Labels map[string]string `yaml:"labels"`
	}
	cfg := wrap{Tags: []string{"default"}, Labels: map[string]string{"team": "core"}}
	if err := load(t, "tags: [a, b]\nlabels:\n  env: prod\n", &cfg); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.Tags, []string{"a", "b"}) ||
		!reflect.DeepEqual(cfg.Labels, map[string]string{"team": "core", "env": "prod"}) {
		t.Fatalf("got %+v", cfg)
	}
}

func TestRejectsUnknownFields(t *testing.T) {
	for src, field := range map[string]string{
		"cache:\n  ttl: 1s\n":             "cache",
		"http:\n  port: 1\n\n  prot: 2\n": "prot",
	} {
		cfg := defaults()
		if err := load(t, src, &cfg); err == nil || !strings.Contains(err.Error(), field) {
			t.Fatalf("%q: want unknown field %s, got %v", src, field, err)
		}
	}
}

func TestRejectsInvalidYAML(t *testing.T) {
	for _, src := range []string{
		"hello",
		"- http",
		"http: {}\nhttp: {}",
		"http:\n  port: 1\n  port: 2",
		"http:\n  1: x",
		"http:\n  port: abc",
	} {
		t.Run(src, func(t *testing.T) {
			cfg := defaults()
			if err := load(t, src, &cfg); err == nil {
				t.Fatal("accepted invalid YAML")
			}
		})
	}
}

func TestMissingFile(t *testing.T) {
	cfg := defaults()
	if err := config.Load(filepath.Join(t.TempDir(), "missing.yaml"), &cfg); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("got %v", err)
	}
}

func TestEnvExpansion(t *testing.T) {
	t.Setenv("MYGORAVEL_TEST_PORT", "9100")
	t.Setenv("MYGORAVEL_TEST_EMPTY", "")
	t.Setenv("MYGORAVEL_TEST_PASSWORD", "p@ss #word: {x}")
	cfg := mustLoad(t, `
http:
  port: ${MYGORAVEL_TEST_PORT}
  addr: ${MYGORAVEL_TEST_EMPTY:-:7000}
db:
  dsn: ${MYGORAVEL_TEST_UNSET:-}
  password: ${MYGORAVEL_TEST_PASSWORD}
`)
	if cfg.HTTP.Port != 9100 || cfg.HTTP.Addr != ":7000" || cfg.DB.DSN != "" || cfg.DB.Password != "p@ss #word: {x}" {
		t.Fatalf("got %+v password=%q", cfg, cfg.DB.Password)
	}
}

func TestMissingEnvFails(t *testing.T) {
	cfg := defaults()
	err := load(t, "db:\n  dsn: ${MYGORAVEL_TEST_UNSET}\n", &cfg)
	if err == nil || !strings.Contains(err.Error(), "MYGORAVEL_TEST_UNSET") {
		t.Fatalf("got %v", err)
	}
}

func TestEnvCannotChangeStructure(t *testing.T) {
	t.Setenv("MYGORAVEL_TEST_EVIL", "x\nport: 1")
	cfg := mustLoad(t, "http:\n  addr: ${MYGORAVEL_TEST_EVIL}\n")
	if cfg.HTTP.Port != 8080 || cfg.HTTP.Addr != "x\nport: 1" {
		t.Fatalf("structure changed: %+v", cfg.HTTP)
	}
}

func TestEscapedReference(t *testing.T) {
	cfg := mustLoad(t, "http:\n  addr: $${TOKEN}\n")
	if cfg.HTTP.Addr != "${TOKEN}" {
		t.Fatalf("addr=%q", cfg.HTTP.Addr)
	}
}

func TestAliasesExpandOnce(t *testing.T) {
	t.Setenv("MYGORAVEL_TEST_PORT", "123")
	type section struct {
		Port int `yaml:"port"`
	}
	var cfg struct {
		One section `yaml:"one"`
		Two section `yaml:"two"`
	}
	if err := load(t, "one: &shared\n  port: ${MYGORAVEL_TEST_PORT}\ntwo: *shared\n", &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.One.Port != 123 || cfg.Two.Port != 123 {
		t.Fatalf("got %+v", cfg)
	}
}

type portConfig struct {
	Port int `yaml:"port"`
}

func (c portConfig) Validate() error {
	if c.Port < 0 {
		return errors.New("port must be >= 0")
	}
	return nil
}

type rootConfig struct {
	API     portConfig            `yaml:"api"`
	Admin   *portConfig           `yaml:"admin"`
	Workers []portConfig          `yaml:"workers"`
	Extra   map[string]portConfig `yaml:"extra"`
	calls   *int
}

func (c *rootConfig) Validate() error {
	*c.calls++
	if c.API.Port == c.Admin.Port {
		return errors.New("api and admin ports must differ")
	}
	return nil
}

func TestValidateWalksNestedFields(t *testing.T) {
	calls := 0
	cfg := rootConfig{Admin: &portConfig{}, calls: &calls}
	err := load(t, `
api: {port: -1}
admin: {port: -1}
workers: [{port: 1}, {port: -2}]
extra: {a: {port: -3}}
`, &cfg)
	if err == nil {
		t.Fatal("expected validation error")
	}
	for _, want := range []string{"invalid configuration", "api: port", "admin: port", "workers[1]: port", "extra.a: port", "must differ"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("missing %q in %v", want, err)
		}
	}
	if calls != 1 {
		t.Fatalf("root Validate called %d times", calls)
	}
}
