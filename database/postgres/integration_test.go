//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/ryan053857/mygoravel/database/dbtest"
	"github.com/ryan053857/mygoravel/database/postgres"
	"github.com/ryan053857/mygoravel/internal/dbtesting"
)

func TestConstraintErrors(t *testing.T) {
	db := dbtest.Open(t, postgres.Driver, nil)
	ctx := context.Background()
	for _, q := range []string{
		`CREATE TABLE articles (id text PRIMARY KEY, slug text NOT NULL, n integer CHECK (n > 0),
		    CONSTRAINT articles_slug_key UNIQUE (slug))`,
		`CREATE TABLE comments (id text PRIMARY KEY, article_id text NOT NULL REFERENCES articles (id))`,
		`INSERT INTO articles VALUES ('1', 'hello', 1)`,
		`INSERT INTO comments VALUES ('1', '1')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	exec := func(q string) error {
		_, err := db.ExecContext(ctx, q)
		return err
	}
	if err := exec(`INSERT INTO articles VALUES ('2', 'hello', 1)`); !postgres.IsUniqueViolation(err, "articles_slug_key") || postgres.IsUniqueViolation(err, "articles_pkey") {
		t.Errorf("unique: %v", err)
	}
	if err := exec(`INSERT INTO articles VALUES ('1', 'other', 1)`); !postgres.IsUniqueViolation(err, "articles_pkey") {
		t.Errorf("primary: %v", err)
	}
	if err := exec(`INSERT INTO comments VALUES ('2', 'missing')`); !postgres.IsForeignKeyViolation(err) {
		t.Errorf("foreign key insert: %v", err)
	}
	if err := exec(`DELETE FROM articles WHERE id = '1'`); !postgres.IsForeignKeyViolation(err) {
		t.Errorf("foreign key delete: %v", err)
	}
	if err := exec(`INSERT INTO articles VALUES ('3', 'c', 0)`); !postgres.IsCheckViolation(err) || postgres.IsUniqueViolation(err) {
		t.Errorf("check: %v", err)
	}
}

func TestDeadlock(t *testing.T) {
	db := dbtest.Open(t, postgres.Driver, nil)
	if _, err := db.ExecContext(context.Background(), "CREATE TABLE t (id integer PRIMARY KEY, n integer NOT NULL); INSERT INTO t VALUES (1, 0), (2, 0)"); err != nil {
		t.Fatal(err)
	}
	if err := dbtesting.Deadlock(t, db); !postgres.IsDeadlock(err) || postgres.IsUniqueViolation(err) {
		t.Fatalf("err=%v", err)
	}
}
