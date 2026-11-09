package config_test

import (
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cockroachdb/errors"

	"github.com/ryan053857/mygoravel/config"
)

const envFileConfig = `
envfile: env/.env
http:
  addr: ${MYGORAVEL_TEST_ADDR:-:9999}
db:
  password: ${MYGORAVEL_TEST_PASSWORD}
`

func loadDir(t *testing.T, files map[string]string) (appConfig, error) {
	t.Helper()
	dir := writeFiles(t, files)
	return config.Load[appConfig](filepath.Join(dir, "config.yaml"))
}

func TestEnvFileRelativeToConfigFile(t *testing.T) {
	cfg, err := loadDir(t, map[string]string{
		"config.yaml": envFileConfig,
		"env/.env":    "# comment\nexport MYGORAVEL_TEST_ADDR=:7001\nMYGORAVEL_TEST_PASSWORD='s3cr$ET value'\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.Addr != ":7001" || cfg.DB.Password != "s3cr$ET value" {
		t.Fatalf("addr=%q password=%q", cfg.HTTP.Addr, cfg.DB.Password)
	}
}

func TestProcessEnvOverridesEnvFile(t *testing.T) {
	t.Setenv("MYGORAVEL_TEST_ADDR", ":7002")
	t.Setenv("MYGORAVEL_TEST_PASSWORD", "") // 空值也算已设置
	cfg, err := loadDir(t, map[string]string{
		"config.yaml": envFileConfig,
		"env/.env":    "MYGORAVEL_TEST_ADDR=:7001\nMYGORAVEL_TEST_PASSWORD=from-file\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.Addr != ":7002" || cfg.DB.Password != "" {
		t.Fatalf("addr=%q password=%q", cfg.HTTP.Addr, cfg.DB.Password)
	}
}

func TestEnvFilePathExpandsProcessEnv(t *testing.T) {
	t.Setenv("MYGORAVEL_TEST_APP_ENV", "prod")
	cfg, err := loadDir(t, map[string]string{
		"config.yaml":  "envfile: env/${MYGORAVEL_TEST_APP_ENV}.env\nhttp:\n  addr: ${MYGORAVEL_TEST_ADDR}\n",
		"env/prod.env": "MYGORAVEL_TEST_ADDR=:7003\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.Addr != ":7003" {
		t.Fatalf("addr=%q", cfg.HTTP.Addr)
	}
}

func TestMissingEnvFileFails(t *testing.T) {
	_, err := loadDir(t, map[string]string{"config.yaml": envFileConfig})
	if !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), "envfile") {
		t.Fatalf("want missing envfile error, got %v", err)
	}
}

func TestInvalidEnvFileFails(t *testing.T) {
	_, err := loadDir(t, map[string]string{
		"config.yaml": envFileConfig,
		"env/.env":    "BAD-KEY=value\n",
	})
	if err == nil || !strings.Contains(err.Error(), "envfile") {
		t.Fatalf("got %v", err)
	}
}

func TestEnvFileMustBeNonEmptyString(t *testing.T) {
	for _, src := range []string{
		"envfile: [a]\n",
		"envfile: \"\"\n",
		"envfile: ${MYGORAVEL_TEST_UNSET:-}\n",
		"envfile: ${MYGORAVEL_TEST_UNSET}\n",
	} {
		t.Run(src, func(t *testing.T) {
			_, err := loadDir(t, map[string]string{"config.yaml": src})
			if err == nil || !strings.Contains(err.Error(), "envfile") {
				t.Fatalf("want envfile error, got %v", err)
			}
		})
	}
}
