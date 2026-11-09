package config_test

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v4"

	"github.com/ryan053857/mygoravel/config"
)

type httpConfig struct {
	Addr    string        `yaml:"addr"`
	Port    int           `yaml:"port"`
	Timeout time.Duration `yaml:"timeout"`
	Debug   bool          `yaml:"debug"`
}

type dbConfig struct {
	DSN      config.Secret `yaml:"dsn"`
	Password config.Secret `yaml:"password"`
}

func defaultHTTP() httpConfig {
	return httpConfig{Addr: ":8080", Port: 8080, Timeout: 5 * time.Second}
}

func lookupEnv(m map[string]string) config.LookupFunc {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func parse(t *testing.T, yamlText string, sections ...string) *config.Document {
	t.Helper()

	doc, err := config.Parse([]byte(yamlText),
		config.WithSections(sections...),
		config.WithLookup(lookupEnv(nil)),
	)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestDecodeOverridesDefaults(t *testing.T) {
	doc := parse(t, `
http:
  port: 9000
  timeout: 10s
`, "http")
	cfg := defaultHTTP()
	if err := doc.Decode("http", &cfg); err != nil {
		t.Fatal(err)
	}
	want := defaultHTTP()
	want.Port = 9000
	want.Timeout = 10 * time.Second
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("got %+v want %+v", cfg, want)
	}
}

func TestMissingSectionKeepsDefaults(t *testing.T) {
	doc := parse(t, `database: {}`, "http", "database")
	cfg := defaultHTTP()
	if err := doc.Decode("http", &cfg); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg, defaultHTTP()) {
		t.Fatalf("defaults changed: %+v", cfg)
	}
}

func TestEmptyDocumentKeepsDefaults(t *testing.T) {
	for _, text := range []string{"", "\n", "# comment\n"} {
		doc, err := config.Parse([]byte(text), config.WithSections("http"), config.WithLookup(lookupEnv(nil)))
		if err != nil {
			t.Fatalf("%q: %v", text, err)
		}
		cfg := defaultHTTP()
		if err := doc.Decode("http", &cfg); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(cfg, defaultHTTP()) {
			t.Fatalf("%q: %+v", text, cfg)
		}
	}
}

func TestRejectUnknownSection(t *testing.T) {
	_, err := config.Parse([]byte("cache:\n  ttl: 1s\n"), config.WithSections("http"), config.WithLookup(lookupEnv(nil)))
	if err == nil || !strings.Contains(err.Error(), "cache") {
		t.Fatalf("want unknown section error, got %v", err)
	}
}

func TestRejectUnknownField(t *testing.T) {
	doc := parse(t, "http:\n  port: 1\n\n\n  prot: 2\n", "http")
	cfg := defaultHTTP()
	err := doc.Decode("http", &cfg)
	if err == nil || !strings.Contains(err.Error(), "prot") {
		t.Fatalf("want unknown field error, got %v", err)
	}
}

func TestRejectNull(t *testing.T) {
	_, err := config.Parse([]byte("http:\n  addr: null\n"), config.WithSections("http"), config.WithLookup(lookupEnv(nil)))
	if err == nil || !strings.Contains(err.Error(), "null") {
		t.Fatalf("want null rejection, got %v", err)
	}
}

func TestRejectMultipleDocuments(t *testing.T) {
	_, err := config.Parse([]byte("http: {}\n---\nhttp: {}\n"), config.WithSections("http"), config.WithLookup(lookupEnv(nil)))
	if err == nil || !strings.Contains(err.Error(), "single YAML document") {
		t.Fatalf("want multi-doc error, got %v", err)
	}
}

func TestEnvExpansionOnlyOnDecode(t *testing.T) {
	env := map[string]string{"TEST_PORT": "9100", "TEST_PASSWORD": "p@ss #word: {x}", "TEST_EMPTY": ""}
	doc, err := config.Parse([]byte(`
http:
  port: ${TEST_PORT}
  addr: ${TEST_EMPTY:-:7000}
db:
  password: ${TEST_PASSWORD}
`), config.WithSections("http", "db"), config.WithLookup(lookupEnv(env)))
	if err != nil {
		t.Fatal(err)
	}

	// 仅解码 http 段时，保留 db 中尚未展开的变量。
	httpDoc, err := config.Parse([]byte(`
http:
  port: ${TEST_PORT}
db:
  password: ${MISSING_SECRET}
`), config.WithSections("http", "db"), config.WithLookup(lookupEnv(map[string]string{"TEST_PORT": "9200"})))
	if err != nil {
		t.Fatal(err)
	}
	cfg := defaultHTTP()
	if err := httpDoc.Decode("http", &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 9200 {
		t.Fatalf("port=%d", cfg.Port)
	}
	var db dbConfig
	if err := httpDoc.Decode("db", &db); err == nil {
		t.Fatal("expected missing env on db decode")
	}

	cfg = defaultHTTP()
	if err := doc.Decode("http", &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 9100 || cfg.Addr != ":7000" {
		t.Fatalf("got %+v", cfg)
	}
	var db2 dbConfig
	if err := doc.Decode("db", &db2); err != nil {
		t.Fatal(err)
	}
	if db2.Password.Reveal() != "p@ss #word: {x}" {
		t.Fatalf("password=%q", db2.Password.Reveal())
	}
}

func TestEnvCannotChangeStructure(t *testing.T) {
	env := map[string]string{"EVIL": "x\nport: 1"}
	doc, err := config.Parse([]byte("http:\n  addr: ${EVIL}\n"), config.WithSections("http"), config.WithLookup(lookupEnv(env)))
	if err != nil {
		t.Fatal(err)
	}
	cfg := defaultHTTP()
	if err := doc.Decode("http", &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 8080 || cfg.Addr != "x\nport: 1" {
		t.Fatalf("structure changed: %+v", cfg)
	}
}

func TestMapMergeSliceReplace(t *testing.T) {
	type wrap struct {
		Tags   []string          `yaml:"tags"`
		Labels map[string]string `yaml:"labels"`
	}
	doc, err := config.Parse([]byte(`
app:
  tags: [a, b]
  labels:
    env: prod
`), config.WithSections("app"), config.WithLookup(lookupEnv(nil)))
	if err != nil {
		t.Fatal(err)
	}
	cfg := wrap{Tags: []string{"default"}, Labels: map[string]string{"team": "core"}}
	if err := doc.Decode("app", &cfg); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.Tags, []string{"a", "b"}) {
		t.Fatalf("tags=%v", cfg.Tags)
	}
	if !reflect.DeepEqual(cfg.Labels, map[string]string{"team": "core", "env": "prod"}) {
		t.Fatalf("labels=%v", cfg.Labels)
	}
}

func TestZeroValuesPreserved(t *testing.T) {
	doc := parse(t, "http:\n  port: 0\n  debug: false\n  addr: \"\"\n", "http")
	cfg := defaultHTTP()
	cfg.Debug = true
	if err := doc.Decode("http", &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 0 || cfg.Debug || cfg.Addr != "" {
		t.Fatalf("zero values not preserved: %+v", cfg)
	}
}

func TestTwoDocumentsIsolated(t *testing.T) {
	a, err := config.Parse([]byte("http:\n  port: ${PORT}\n"),
		config.WithSections("http"),
		config.WithLookup(lookupEnv(map[string]string{"PORT": "1"})))
	if err != nil {
		t.Fatal(err)
	}
	b, err := config.Parse([]byte("http:\n  port: ${PORT}\n"),
		config.WithSections("http"),
		config.WithLookup(lookupEnv(map[string]string{"PORT": "2"})))
	if err != nil {
		t.Fatal(err)
	}
	var ca, cb httpConfig
	ca.Port, cb.Port = 8080, 8080
	if err := a.Decode("http", &ca); err != nil {
		t.Fatal(err)
	}
	if err := b.Decode("http", &cb); err != nil {
		t.Fatal(err)
	}
	if ca.Port != 1 || cb.Port != 2 {
		t.Fatalf("a=%d b=%d", ca.Port, cb.Port)
	}
}

func TestDefaultLookupIsProcessEnv(t *testing.T) {
	t.Setenv("MYGORAVEL_TEST_PORT", "7777")
	doc, err := config.Parse([]byte("http:\n  port: ${MYGORAVEL_TEST_PORT}\n"), config.WithSections("http"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := defaultHTTP()
	if err := doc.Decode("http", &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 7777 {
		t.Fatalf("port=%d", cfg.Port)
	}
}

func TestRequiresSections(t *testing.T) {
	if _, err := config.Parse([]byte("http: {}\n")); err == nil {
		t.Fatal("expected error")
	}
}

func TestReadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(path, []byte("http:\n  port: 7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := config.ReadFile(path, config.WithSections("http"), config.WithLookup(lookupEnv(nil)))
	if err != nil {
		t.Fatal(err)
	}
	cfg := defaultHTTP()
	if err := doc.Decode("http", &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 7 {
		t.Fatalf("port=%d", cfg.Port)
	}
}

func TestSecretRedaction(t *testing.T) {
	s := config.Secret("super-secret")
	if s.String() != "******" || s.Reveal() != "super-secret" {
		t.Fatalf("string=%q reveal=%q", s.String(), s.Reveal())
	}
	if fmt.Sprintf("%v", s) != "******" || fmt.Sprintf("%#v", s) != `"******"` {
		t.Fatalf("fmt failed: %v %#v", s, s)
	}
	data, err := json.Marshal(s)
	if err != nil || string(data) != `"******"` {
		t.Fatalf("json=%s err=%v", data, err)
	}
	y, err := yaml.Marshal(s)
	if err != nil || !strings.Contains(string(y), "******") || strings.Contains(string(y), "super-secret") {
		t.Fatalf("yaml=%s err=%v", y, err)
	}
	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	logger.Info("x", "password", s)
	if strings.Contains(buf.String(), "super-secret") || !strings.Contains(buf.String(), "******") {
		t.Fatalf("slog=%q", buf.String())
	}
}

type validating struct {
	Port int `yaml:"port"`
}

func (v validating) Validate() error {
	if v.Port < 0 {
		return fmt.Errorf("port must be >= 0")
	}
	return nil
}

func TestValidateHook(t *testing.T) {
	doc := parse(t, "http:\n  port: -1\n", "http")
	var v validating
	err := doc.Decode("http", &v)
	if err == nil || !strings.Contains(err.Error(), "invalid configuration") {
		t.Fatalf("got %v", err)
	}
}
