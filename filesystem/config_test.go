package filesystem_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-tenon/tenon/config"
	"github.com/go-tenon/tenon/filesystem"
)

func TestByteSize(t *testing.T) {
	t.Parallel()
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

type appConfig struct {
	Filesystem filesystem.Config `yaml:"filesystem"`
}

func TestConfigLoad(t *testing.T) {
	t.Parallel()
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
	want := filesystem.Config{
		DefaultDisk: "local", MaxUploadSize: filesystem.GiB, TempTTL: 24 * time.Hour,
		Disks: filesystem.DisksConfig{
			Local: filesystem.LocalConfig{Root: "storage/files", PublicURL: "/storage"},
			S3:    filesystem.S3Config{Bucket: "b", PublicURL: "https://cdn.example.com"},
		},
	}
	if cfg.Filesystem != want {
		t.Fatalf("cfg = %+v", cfg.Filesystem)
	}

	cfg, err = config.Load[appConfig](write("set.yaml", "filesystem:\n  default_disk: s3\n  max_upload_size: 2GiB\n  temp_ttl: 1h\n"+disks))
	if err != nil || cfg.Filesystem.MaxUploadSize != 2*filesystem.GiB || cfg.Filesystem.DefaultDisk != "s3" {
		t.Fatalf("cfg = %+v err=%v", cfg.Filesystem, err)
	}
	cfg, err = config.Load[appConfig](write("int.yaml", "filesystem:\n  max_upload_size: 1048576\n"+disks))
	if err != nil || cfg.Filesystem.MaxUploadSize != filesystem.MiB {
		t.Fatalf("integer size = %+v err=%v", cfg.Filesystem, err)
	}

	// 只配置本地磁盘时，S3 保持零值且不校验。
	cfg, err = config.Load[appConfig](write("local.yaml", "filesystem:\n  disks:\n    local:\n      root: x\n      public_url: /s\n"))
	if err != nil || !cfg.Filesystem.Disks.S3.IsZero() {
		t.Fatalf("local only = %+v err=%v", cfg.Filesystem, err)
	}

	for name, c := range map[string]struct{ body, want string }{
		"limits":       {"filesystem:\n  max_upload_size: 0\n" + disks, "filesystem: max_upload_size"},
		"local":        {"filesystem:\n  disks:\n    local:\n      root: x\n", "filesystem.disks.local: public_url"},
		"s3":           {"filesystem:\n  disks:\n    local:\n      root: x\n      public_url: /s\n    s3:\n      region: r\n", "filesystem.disks.s3: bucket is required"},
		"default disk": {"filesystem:\n  default_disk: s3\n  disks:\n    local:\n      root: x\n      public_url: /s\n", `default_disk "s3" must be local or s3 and configured`},
		"no disks":     {"filesystem: {}\n", `default_disk "local"`},
	} {
		_, err := config.Load[appConfig](write(name+".yaml", c.body))
		if err == nil || strings.Count(err.Error(), c.want) != 1 {
			t.Errorf("%s: error should mention %q once: %v", name, c.want, err)
		}
	}
}

func TestDiskConfigValidate(t *testing.T) {
	t.Parallel()
	for _, u := range []string{"/storage", "https://cdn.example.com/files", "http://localhost:8080/storage/"} {
		if err := (filesystem.LocalConfig{Root: "x", PublicURL: u}).Validate(); err != nil {
			t.Errorf("local %s: %v", u, err)
		}
	}
	for _, u := range []string{"", "storage", "ftp://x/y", "https:///nohost", "/s?x=1"} {
		if err := (filesystem.LocalConfig{Root: "x", PublicURL: u}).Validate(); err == nil {
			t.Errorf("local %q accepted", u)
		}
	}
	if err := (filesystem.S3Config{Bucket: "b", PublicURL: "https://cdn.example.com"}).Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []filesystem.S3Config{
		{PublicURL: "https://cdn.example.com"},
		{Bucket: "b"},
		{Bucket: "b", PublicURL: "/storage"},
		{Bucket: "b", PublicURL: "https://x", Endpoint: "minio:9000"},
		{Bucket: "b", PublicURL: "https://x", AccessKey: "k"},
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("s3 %+v accepted", bad)
		}
	}
}

func TestPartSize(t *testing.T) {
	t.Parallel()
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
