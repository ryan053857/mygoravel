package database

import (
	"strings"
	"testing"
)

func TestSanitize(t *testing.T) {
	for in, want := range map[string]string{
		`SELECT "a"."id", "a"."title" FROM "articles" AS "a" WHERE ("a"."id" = 42) LIMIT 1`: `SELECT "a"."id", "a"."title" FROM "articles" AS "a" WHERE ("a"."id" = ?) LIMIT ?`,
		`INSERT INTO "users" ("email", "hash") VALUES ('a@b.com', 'it''s secret')`:          `INSERT INTO "users" ("email", "hash") VALUES (?, ?)`,
		`UPDATE t SET v = E'line\nbreak \' quote', n = -3.5e+2 WHERE t2.c1 = 7`:             `UPDATE t SET v = ?, n = -? WHERE t2.c1 = ?`,
		`SELECT * FROM t WHERE id IN (1, 2, 3) AND tags = '{a,b}'::text[]`:                  `SELECT * FROM t WHERE id IN (?, ?, ?) AND tags = ?::text[]`,
		`SELECT $1, $2 FROM t`:                `SELECT $1, $2 FROM t`,
		`SELECT $$it's $1$$ || $body$x$body$`: `SELECT ? || ?`,
		`SELECT 1 -- note 'x' 5` + "\nFROM t": `SELECT ? -- note 'x' 5` + "\nFROM t",
		`SELECT /* 'x' 5 */ v2 FROM "t 1"`:    `SELECT /* 'x' 5 */ v2 FROM "t 1"`,
		`SELECT '中文' AS "名字", 3`:              `SELECT ? AS "名字", ?`,
		`SELECT 'unterminated`:                `SELECT ?`,
		`SELECT "unterminated`:                `SELECT "unterminated`,
		`SELECT x FROM t WHERE created_at < '2026-11-13 08:00:00+00:00' AND e = 'E'`: `SELECT x FROM t WHERE created_at < ? AND e = ?`,
		`COMMIT`: `COMMIT`,
	} {
		if got := sanitize(in); got != want {
			t.Errorf("sanitize(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestSanitizeTruncates(t *testing.T) {
	long := "SELECT " + strings.Repeat("col, ", 1000) + "x FROM t"
	got := sanitize(long)
	if len(got) != maxQueryText+3 || !strings.HasSuffix(got, "...") || !strings.HasPrefix(got, "SELECT col, ") {
		t.Fatalf("len %d, %q", len(got), got[len(got)-10:])
	}
	// 截断不会切开多字节字符。
	got = sanitize("SELECT " + strings.Repeat("名", 1000))
	if !strings.HasSuffix(got, "...") || strings.ContainsRune(got, '�') {
		t.Fatalf("%q", got[len(got)-10:])
	}
}
