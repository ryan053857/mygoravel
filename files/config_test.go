package files_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/config"
	"github.com/ryan053857/mygoravel/files"
)

func TestByteSize(t *testing.T) {
	for text, want := range map[string]files.ByteSize{
		"0": 0, "1048576": files.MiB, "512KiB": 512 * files.KiB, "16 MiB": 16 * files.MiB,
		"1GiB": files.GiB, "2TiB": 2 * files.TiB, "10B": 10,
	} {
		var got files.ByteSize
		if err := got.UnmarshalText([]byte(text)); err != nil || got != want {
			t.Errorf("%q = %d, %v; want %d", text, got, err, want)
		}
	}
	for _, text := range []string{"", "1.5GiB", "-1", "1GB", "MiB", "99999999999TiB"} {
		var got files.ByteSize
		if err := got.UnmarshalText([]byte(text)); err == nil {
			t.Errorf("%q accepted as %d", text, got)
		}
	}
	if s := (1536 * files.MiB).String(); s != "1536MiB" {
		t.Errorf("String = %s", s)
	}
}

func TestConfigLoad(t *testing.T) {
	type root struct {
		Files files.Config `yaml:"files"`
	}
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cfg, err := config.Load[root](write("empty.yaml", "files: {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Files != (files.Config{DefaultDisk: "local", MaxUploadSize: files.GiB, TempTTL: 24 * time.Hour}) {
		t.Fatalf("defaults = %+v", cfg.Files)
	}
	cfg, err = config.Load[root](write("set.yaml", "files:\n  default_disk: s3\n  max_upload_size: 2GiB\n  temp_ttl: 1h\n"))
	if err != nil || cfg.Files.MaxUploadSize != 2*files.GiB || cfg.Files.DefaultDisk != "s3" {
		t.Fatalf("cfg = %+v err=%v", cfg.Files, err)
	}
	cfg, err = config.Load[root](write("int.yaml", "files:\n  max_upload_size: 1048576\n"))
	if err != nil || cfg.Files.MaxUploadSize != files.MiB {
		t.Fatalf("integer size = %+v err=%v", cfg.Files, err)
	}
	if _, err := config.Load[root](write("bad.yaml", "files:\n  max_upload_size: 0\n  temp_ttl: 1s\n")); err == nil {
		t.Fatal("invalid config accepted")
	}
}

func TestPartSize(t *testing.T) {
	for size, want := range map[int64]int64{
		1:                    16 << 20,
		1 << 30:              16 << 20,
		160 << 30:            17 << 20, // 160GiB / 10000 片向上取整到 MiB
		int64(5 * files.TiB): 525 << 20,
	} {
		if got := files.PartSize(size); got != want {
			t.Errorf("PartSize(%d) = %d, want %d", size, got, want)
		}
		if parts := (size + files.PartSize(size) - 1) / files.PartSize(size); parts > 10000 {
			t.Errorf("size %d needs %d parts", size, parts)
		}
	}
}
