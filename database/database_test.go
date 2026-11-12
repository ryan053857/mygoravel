package database_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/database/migrate"
	"github.com/ryan053857/mygoravel/errorx"
)

func TestConfigValidate(t *testing.T) {
	cfg := database.DefaultConfig()
	if err := cfg.Validate(); err == nil {
		t.Fatal("empty dsn should fail")
	}
}

func TestCheckPublishedDetectsModification(t *testing.T) {
	dir := t.TempDir()
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "00001_a.sql"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "00001_a.sql"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := migrate.CheckPublished(dir, base); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "00001_a.sql"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := migrate.CheckPublished(dir, base)
	if err == nil || !strings.Contains(err.Error(), "modified") {
		t.Fatalf("got %v", err)
	}
}

func TestPublishedMigrationsIncludeNestedFiles(t *testing.T) {
	live, baseline := t.TempDir(), t.TempDir()
	for _, dir := range []string{live, baseline} {
		if err := os.MkdirAll(filepath.Join(dir, "schema"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "schema/00001_init.sql"), []byte("SELECT 1;"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(live, "00002_next.sql"), []byte("SELECT 2;"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := migrate.CheckPublished(live, baseline); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(live, "schema/00001_init.sql")
	if err := os.WriteFile(file, []byte("SELECT 3;"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := migrate.CheckPublished(live, baseline); err == nil || !strings.Contains(err.Error(), "modified") {
		t.Fatalf("expected modified migration, got %v", err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := migrate.CheckPublished(live, baseline); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("expected missing migration, got %v", err)
	}
}

func TestOpenRunsConfigurationChecks(t *testing.T) {
	valid := database.DefaultConfig()
	valid.DSN = "postgres://localhost/db"
	with := func(change func(*database.Config)) database.Config {
		c := valid
		change(&c)
		return c
	}
	for _, cfg := range []database.Config{
		{},
		with(func(c *database.Config) { c.MaxConns = -1 }),
		with(func(c *database.Config) { c.MinConns = c.MaxConns + 1 }),
		with(func(c *database.Config) { c.MinConns = -1 }),
		with(func(c *database.Config) { c.MaxConnLifetime = -1 }),
		with(func(c *database.Config) { c.SlowQuery = -1 }),
	} {
		validation := cfg.Validate()
		db, err := database.Open(context.Background(), cfg)
		if db != nil || validation == nil || err == nil || err.Error() != "database: "+validation.Error() {
			t.Fatalf("validation=%v open=%v db=%v", validation, err, db)
		}
	}
}

func TestOpenReportsUnreachableDatabaseAsUnavailable(t *testing.T) {
	// 端口 1 上没有 PostgreSQL，连接会立即被拒绝
	cfg := database.DefaultConfig()
	cfg.DSN = "postgres://postgres@127.0.0.1:1/blog?sslmode=disable&connect_timeout=2"
	db, err := database.Open(context.Background(), cfg)
	if db != nil || !errors.Is(err, database.ErrUnavailable) || errorx.KindOf(err) != errorx.Unavailable {
		t.Fatalf("db=%v err=%v kind=%v", db, err, errorx.KindOf(err))
	}
	if !strings.Contains(err.Error(), "database: ping") {
		t.Fatalf("err = %q, want the ping failure", err)
	}
}

func TestOpenRejectsInvalidDSN(t *testing.T) {
	db, err := database.Open(context.Background(), database.Config{DSN: "postgres://user:pass@host:not-a-port/db"})
	if db != nil || err == nil || !strings.HasPrefix(err.Error(), "database: invalid dsn: ") {
		t.Errorf("db=%v err=%v", db, err)
	}
}

func TestPublishedMigrationsRequireCurrentDirectory(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if err := migrate.CheckPublished(missing, t.TempDir()); err == nil {
		t.Fatal("missing current directory accepted")
	}
}
