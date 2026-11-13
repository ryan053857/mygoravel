package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cockroachdb/errors"

	"github.com/go-tenon/tenon/config"
)

type Limits struct {
	Size int `yaml:"size"`
}

func (l *Limits) SetDefaults() { l.Size = 10 }

func (l Limits) Validate() error {
	if l.Size <= 0 {
		return errors.New("size must be positive")
	}
	return nil
}

type disk struct {
	Root string `yaml:"root"`
}

func (d disk) Validate() error {
	if d.Root == "" {
		return errors.New("root is required")
	}
	return nil
}

// storageConfig 用 inline 嵌入框架配置，并在同一段下增加嵌套的磁盘配置。
type storageConfig struct {
	Limits `yaml:",inline"`
	Disks  struct {
		Local disk `yaml:"local"`
	} `yaml:"disks"`
}

type inlineRoot struct {
	Storage storageConfig `yaml:"storage"`
}

func TestInlineEmbeddedConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.yaml")
	write := func(body string) {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("storage:\n  disks:\n    local:\n      root: /data\n")
	cfg, err := config.Load[inlineRoot](path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Storage.Size != 10 || cfg.Storage.Disks.Local.Root != "/data" {
		t.Fatalf("cfg = %+v", cfg.Storage)
	}

	write("storage:\n  size: 0\n  disks:\n    local: {}\n")
	_, err = config.Load[inlineRoot](path)
	if err == nil {
		t.Fatal("invalid config accepted")
	}
	// 提升到外层的 Validate 只执行一次，错误不会重复。
	if n := strings.Count(err.Error(), "size must be positive"); n != 1 {
		t.Fatalf("size error reported %d times: %v", n, err)
	}
	if !strings.Contains(err.Error(), "storage.disks.local: root is required") {
		t.Fatalf("missing nested error: %v", err)
	}
}

type overridden struct {
	Limits `yaml:",inline"`
}

// Validate 覆盖内嵌字段的方法，只调用外层。
func (overridden) Validate() error { return errors.New("outer validate") }

func TestInlineOverrideCallsOuterOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.yaml")
	if err := os.WriteFile(path, []byte("o:\n  size: 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.Load[struct {
		O overridden `yaml:"o"`
	}](path)
	if err == nil || !strings.Contains(err.Error(), "outer validate") || strings.Contains(err.Error(), "size must be positive") {
		t.Fatalf("err = %v", err)
	}
}
