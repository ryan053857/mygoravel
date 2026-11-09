package config_test

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cockroachdb/errors"

	"github.com/ryan053857/mygoravel/config"
)

type appConfig struct {
	Level string        `yaml:"level"`
	Token config.Secret `yaml:"token"`
}

// writeFiles 在临时目录中写入文件并返回目录路径。
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

func decodeApp(t *testing.T, doc *config.Document) appConfig {
	t.Helper()
	cfg := appConfig{Level: "default"}
	if err := doc.Decode("app", &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

const envFileConfig = `
envfile: env/.env
app:
  level: ${LOG_LEVEL:-info}
  token: ${TOKEN}
`

func TestEnvFileRelativeToConfigFile(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"config.yaml": envFileConfig,
		"env/.env":    "# comment\nexport LOG_LEVEL=debug\nTOKEN='s3cr$t value'\n",
	})
	doc, err := config.ReadFile(filepath.Join(dir, "config.yaml"),
		config.WithSections("app"), config.WithLookup(lookupEnv(nil)))
	if err != nil {
		t.Fatal(err)
	}
	cfg := decodeApp(t, doc)
	if cfg.Level != "debug" || cfg.Token.Reveal() != "s3cr$t value" {
		t.Fatalf("got level=%q token=%q", cfg.Level, cfg.Token.Reveal())
	}
}

func TestProcessEnvOverridesEnvFile(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"config.yaml": envFileConfig,
		"env/.env":    "LOG_LEVEL=debug\nTOKEN=from-file\n",
	})
	// 进程中设为空字符串同样算已设置，优先于 envfile。
	env := map[string]string{"LOG_LEVEL": "warn", "TOKEN": ""}
	doc, err := config.ReadFile(filepath.Join(dir, "config.yaml"),
		config.WithSections("app"), config.WithLookup(lookupEnv(env)))
	if err != nil {
		t.Fatal(err)
	}
	cfg := decodeApp(t, doc)
	if cfg.Level != "warn" || cfg.Token.Reveal() != "" {
		t.Fatalf("got level=%q token=%q", cfg.Level, cfg.Token.Reveal())
	}
}

func TestEnvFilePathExpandsProcessEnv(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"config.yaml":  "envfile: env/${APP_ENV}.env\napp:\n  level: ${LOG_LEVEL}\n",
		"env/prod.env": "LOG_LEVEL=error\n",
	})
	doc, err := config.ReadFile(filepath.Join(dir, "config.yaml"),
		config.WithSections("app"), config.WithLookup(lookupEnv(map[string]string{"APP_ENV": "prod"})))
	if err != nil {
		t.Fatal(err)
	}
	if cfg := decodeApp(t, doc); cfg.Level != "error" {
		t.Fatalf("level=%q", cfg.Level)
	}
}

func TestParseWithAbsoluteEnvFile(t *testing.T) {
	dir := writeFiles(t, map[string]string{".env": "LOG_LEVEL=trace\n"})
	src := fmt.Sprintf("envfile: %q\napp:\n  level: ${LOG_LEVEL}\n", filepath.Join(dir, ".env"))
	doc, err := config.Parse([]byte(src), config.WithSections("app"), config.WithLookup(lookupEnv(nil)))
	if err != nil {
		t.Fatal(err)
	}
	if cfg := decodeApp(t, doc); cfg.Level != "trace" {
		t.Fatalf("level=%q", cfg.Level)
	}
}

func TestMissingEnvFileFails(t *testing.T) {
	dir := writeFiles(t, map[string]string{"config.yaml": envFileConfig})
	_, err := config.ReadFile(filepath.Join(dir, "config.yaml"),
		config.WithSections("app"), config.WithLookup(lookupEnv(nil)))
	if !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), "envfile") {
		t.Fatalf("want missing envfile error, got %v", err)
	}
}

func TestInvalidEnvFileDoesNotLeak(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"config.yaml": envFileConfig,
		"env/.env":    "BAD-KEY=private-secret-value\n",
	})
	_, err := config.ReadFile(filepath.Join(dir, "config.yaml"),
		config.WithSections("app"), config.WithLookup(lookupEnv(nil)))
	if err == nil || !strings.Contains(err.Error(), "invalid dotenv syntax") ||
		strings.Contains(fmt.Sprintf("%+v", err), "private-secret") {
		t.Fatalf("got %+v", err)
	}
}

func TestEnvFileMustBeNonEmptyString(t *testing.T) {
	for _, src := range []string{
		"envfile: [a]\n",
		"envfile: 1\n",
		"envfile: \"\"\n",
		"envfile: ${EMPTY:-}\n",
		"envfile: ${UNSET}\n",
	} {
		t.Run(src, func(t *testing.T) {
			_, err := config.Parse([]byte(src), config.WithSections("app"), config.WithLookup(lookupEnv(nil)))
			if err == nil || !strings.Contains(err.Error(), "envfile") {
				t.Fatalf("want envfile error, got %v", err)
			}
		})
	}
}

func TestEnvFileIsReserved(t *testing.T) {
	if _, err := config.Parse([]byte("app: {}\n"), config.WithSections("app", "envfile")); err == nil {
		t.Fatal("envfile accepted as a section")
	}
	dir := writeFiles(t, map[string]string{".env": ""})
	doc, err := config.Parse([]byte(fmt.Sprintf("envfile: %q\n", filepath.Join(dir, ".env"))), config.WithSections("app"))
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := doc.Decode("envfile", &v); err == nil {
		t.Fatal("envfile decoded as a section")
	}
}
