package filesystem_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/config"
	"github.com/ryan053857/mygoravel/filesystem"
	"github.com/ryan053857/mygoravel/filesystem/local"
	"github.com/ryan053857/mygoravel/filesystem/s3"
)

func TestByteSize(t *testing.T) {
	for text, want := range map[string]filesystem.ByteSize{
		"0": 0, "1048576": filesystem.MiB, "512KiB": 512 * filesystem.KiB, "16 MiB": 16 * filesystem.MiB,
		"1GiB": filesystem.GiB, "2TiB": 2 * filesystem.TiB, "10B": 10,
	} {
		var got filesystem.ByteSize
		if err := got.UnmarshalText([]byte(text)); err != nil || got != want {
			t.Errorf("%q = %d, %v; want %d", text, got, err, want)
		}
	}
	for _, text := range []string{"", "1.5GiB", "-1", "1GB", "MiB", "99999999999TiB"} {
		var got filesystem.ByteSize
		if err := got.UnmarshalText([]byte(text)); err == nil {
			t.Errorf("%q accepted as %d", text, got)
		}
	}
	if s := (1536 * filesystem.MiB).String(); s != "1536MiB" {
		t.Errorf("String = %s", s)
	}
}

// filesystemConfig 是文档推荐的应用配置形状：内联服务配置，并在同一段下嵌套磁盘配置。
type filesystemConfig struct {
	filesystem.Config `yaml:",inline"`
	Disks             struct {
		Local local.Config `yaml:"local"`
		S3    s3.Config    `yaml:"s3"`
	} `yaml:"disks"`
}

type appConfig struct {
	Filesystem filesystemConfig `yaml:"filesystem"`
}

func TestConfigLoad(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	disks := "  disks:\n    local:\n      root: storage/files\n      public_url: /storage\n" +
		"    s3:\n      bucket: b\n      public_url: https://cdn.example.com\n"

	cfg, err := config.Load[appConfig](write("defaults.yaml", "filesystem:\n"+disks))
	if err != nil {
		t.Fatal(err)
	}
	fs := cfg.Filesystem
	if fs.Config != (filesystem.Config{DefaultDisk: "local", MaxUploadSize: filesystem.GiB, TempTTL: 24 * time.Hour}) {
		t.Fatalf("defaults = %+v", fs.Config)
	}
	if fs.Disks.Local.Root != "storage/files" || fs.Disks.S3.PublicURL != "https://cdn.example.com" {
		t.Fatalf("disks = %+v", fs.Disks)
	}

	cfg, err = config.Load[appConfig](write("set.yaml", "filesystem:\n  default_disk: s3\n  max_upload_size: 2GiB\n  temp_ttl: 1h\n"+disks))
	if err != nil || cfg.Filesystem.MaxUploadSize != 2*filesystem.GiB || cfg.Filesystem.DefaultDisk != "s3" {
		t.Fatalf("cfg = %+v err=%v", cfg.Filesystem.Config, err)
	}
	cfg, err = config.Load[appConfig](write("int.yaml", "filesystem:\n  max_upload_size: 1048576\n"+disks))
	if err != nil || cfg.Filesystem.MaxUploadSize != filesystem.MiB {
		t.Fatalf("integer size = %+v err=%v", cfg.Filesystem.Config, err)
	}

	_, err = config.Load[appConfig](write("bad.yaml", "filesystem:\n  max_upload_size: 0\n  disks:\n    local:\n      root: x\n"))
	if err == nil {
		t.Fatal("invalid config accepted")
	}
	for _, want := range []string{"filesystem: max_upload_size", "filesystem.disks.local: public_url", "filesystem.disks.s3: bucket is required"} {
		if strings.Count(err.Error(), want) != 1 {
			t.Errorf("error should mention %q once: %v", want, err)
		}
	}
}

func TestPartSize(t *testing.T) {
	for size, want := range map[int64]int64{
		1:                         16 << 20,
		1 << 30:                   16 << 20,
		160 << 30:                 17 << 20, // 160GiB / 10000 片向上取整到 MiB
		int64(5 * filesystem.TiB): 525 << 20,
	} {
		if got := filesystem.PartSize(size); got != want {
			t.Errorf("PartSize(%d) = %d, want %d", size, got, want)
		}
		if parts := (size + filesystem.PartSize(size) - 1) / filesystem.PartSize(size); parts > 10000 {
			t.Errorf("size %d needs %d parts", size, parts)
		}
	}
}
