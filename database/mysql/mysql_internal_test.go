package mysql

import (
	"testing"
	"time"

	"github.com/cockroachdb/errors"
	driver "github.com/go-sql-driver/mysql"
)

func TestParseFixesTimeAndRowsAffected(t *testing.T) {
	cfg, err := parse("mysql://app:p%40ss%2Fword@db.internal/blog?parseTime=false&loc=Local&clientFoundRows=false&tls=skip-verify&time_zone=%27SYSTEM%27")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.User != "app" || cfg.Passwd != "p@ss/word" || cfg.Net != "tcp" || cfg.Addr != "db.internal:3306" || cfg.DBName != "blog" {
		t.Fatalf("cfg=%+v", cfg)
	}
	if !cfg.ParseTime || cfg.Loc != time.UTC || !cfg.ClientFoundRows || cfg.Params["time_zone"] != "'+00:00'" || cfg.TLSConfig != "skip-verify" {
		t.Fatalf("forced settings not applied: %+v", cfg)
	}
	if cfg, err := parse("mysql://root@[::1]:3307/"); err != nil || cfg.Addr != "[::1]:3307" || cfg.DBName != "" {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
}

func TestParseRejectsOtherFormats(t *testing.T) {
	for _, dsn := range []string{
		"root:root@tcp(localhost:3306)/blog",
		"postgres://localhost/blog",
		"mysql:///blog",
		"mysql://localhost/blog?parseTime=maybe",
	} {
		if _, err := parse(dsn); err == nil {
			t.Errorf("%q accepted", dsn)
		}
	}
}

func TestConstraintErrors(t *testing.T) {
	dup := errors.Wrap(&driver.MySQLError{Number: 1062, Message: "Duplicate entry 'hello' for key 'articles.articles_slug_key'"}, "insert")
	if !IsUniqueViolation(dup) || !IsUniqueViolation(dup, "other", "articles_slug_key") || IsUniqueViolation(dup, "articles") {
		t.Error("unique key name not matched")
	}
	pk := &driver.MySQLError{Number: 1062, Message: "Duplicate entry '1' for key 'articles.PRIMARY'"}
	if !IsUniqueViolation(pk, "PRIMARY") {
		t.Error("primary key not matched")
	}
	for _, n := range []uint16{1451, 1452} {
		if err := (&driver.MySQLError{Number: n}); !IsForeignKeyViolation(err) || IsUniqueViolation(err) {
			t.Errorf("foreign key %d", n)
		}
	}
	if err := (&driver.MySQLError{Number: 3819}); !IsCheckViolation(err) || IsForeignKeyViolation(err) {
		t.Error("check")
	}
	if IsUniqueViolation(errors.New("Duplicate entry 'x' for key 't.k'")) {
		t.Error("plain error matched")
	}
}
