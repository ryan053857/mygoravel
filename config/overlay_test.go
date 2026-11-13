package config_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cockroachdb/errors"

	"github.com/go-tenon/tenon/config"
)

// loadFiles 在临时目录写入 files，按 order 的顺序加载。
func loadFiles(t *testing.T, files map[string]string, order ...string) (appConfig, error) {
	t.Helper()
	dir := writeFiles(t, files)
	paths := make([]string, len(order))
	for i, name := range order {
		paths[i] = filepath.Join(dir, name)
	}
	return config.Load[appConfig](paths...)
}

func TestLaterFilesOverrideEarlier(t *testing.T) {
	cfg, err := loadFiles(t, map[string]string{
		"app.yaml":   "http:\n  addr: :8000\n  port: 1\ndb:\n  dsn: base\n",
		"prod.yaml":  "http:\n  port: 2\n",
		"local.yaml": "db:\n  password: local\n",
	}, "app.yaml", "prod.yaml", "local.yaml")
	if err != nil {
		t.Fatal(err)
	}
	want := defaults()
	want.HTTP.Addr = ":8000"
	want.HTTP.Port = 2
	want.DB = dbConfig{DSN: "base", Password: "local"}
	if cfg != want {
		t.Fatalf("got %+v want %+v", cfg, want)
	}
}

func TestOverlayMergesMapsAndReplacesSlices(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"app.yaml":  "tags: [a, b]\nlabels:\n  env: dev\n  tier: web\n",
		"prod.yaml": "tags: [c]\nlabels:\n  env: prod\n",
	})
	cfg, err := config.Load[collections](filepath.Join(dir, "app.yaml"), filepath.Join(dir, "prod.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.Tags, ",") != "c" || cfg.Labels["env"] != "prod" || cfg.Labels["tier"] != "web" || cfg.Labels["team"] != "core" {
		t.Fatalf("got %+v", cfg)
	}
}

func TestOverlayErrorsNameTheFile(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"unknown field": {"app.yaml": "http:\n  port: 1\n", "prod.yaml": "http:\n  prot: 2\n"},
		"missing env":   {"app.yaml": "http:\n  port: 1\n", "prod.yaml": "db:\n  dsn: ${TENON_TEST_UNSET}\n"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadFiles(t, files, "app.yaml", "prod.yaml")
			if err == nil || !strings.Contains(err.Error(), "prod.yaml") {
				t.Fatalf("want error naming prod.yaml, got %v", err)
			}
		})
	}
	_, err := loadFiles(t, map[string]string{"app.yaml": ""}, "app.yaml", "prod.yaml")
	if !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), "prod.yaml") {
		t.Fatalf("want missing prod.yaml, got %v", err)
	}
}

func TestValidatesMergedResult(t *testing.T) {
	// 单独看 app.yaml 不合法，prod.yaml 补齐后合法：只校验合并后的结果
	files := map[string]string{
		"app.yaml":  "inner:\n  name: \"\"\n",
		"prod.yaml": "inner:\n  name: ok\n",
	}
	dir := writeFiles(t, files)
	if _, err := config.Load[validated](filepath.Join(dir, "app.yaml"), filepath.Join(dir, "prod.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load[validated](filepath.Join(dir, "app.yaml")); err == nil {
		t.Fatal("invalid config accepted")
	}
}

func TestLoadRequiresAFile(t *testing.T) {
	if _, err := config.Load[appConfig](); err == nil {
		t.Fatal("Load without files succeeded")
	}
}

// unsetEnv 在测试期间清除 keys，测试结束后恢复原值。
func unsetEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, key := range keys {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
}

func TestLoadEnv(t *testing.T) {
	unsetEnv(t, "TENON_TEST_ADDR", "TENON_TEST_PASSWORD")
	t.Setenv("TENON_TEST_KEEP", "") // 已设置的变量（包括空字符串）不被覆盖
	dir := writeFiles(t, map[string]string{
		".env":     "# comment\nexport TENON_TEST_ADDR=:7001\nTENON_TEST_PASSWORD='s3cr$ET value'\nTENON_TEST_KEEP=from-file\n",
		"app.yaml": "http:\n  addr: ${TENON_TEST_ADDR}\ndb:\n  password: ${TENON_TEST_PASSWORD}\n  dsn: x${TENON_TEST_KEEP}\n",
	})
	if err := config.LoadEnv(filepath.Join(dir, ".env")); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load[appConfig](filepath.Join(dir, "app.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.Addr != ":7001" || cfg.DB.Password != "s3cr$ET value" || cfg.DB.DSN != "x" {
		t.Fatalf("got %+v", cfg)
	}
}

func TestLoadEnvErrors(t *testing.T) {
	if err := config.LoadEnv(filepath.Join(t.TempDir(), ".env")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("want ErrNotExist, got %v", err)
	}
	dir := writeFiles(t, map[string]string{".env": "BAD-KEY=value\n"})
	if err := config.LoadEnv(filepath.Join(dir, ".env")); err == nil || !strings.Contains(err.Error(), ".env") {
		t.Fatalf("got %v", err)
	}
}

type named struct {
	Name string `yaml:"name"`
}

func (n named) Validate() error {
	if n.Name == "" {
		return errors.New("name is required")
	}
	return nil
}

type validated struct {
	Inner named `yaml:"inner"`
}
