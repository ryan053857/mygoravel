package database_test

import (
	"context"
	"testing"
	"time"

	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/database/dbtest"
)

func TestConstraintErrors(t *testing.T) {
	db := dbtest.Open(t, nil)
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
	if err := exec(`INSERT INTO articles VALUES ('2', 'hello', 1)`); !database.IsUniqueViolation(err, "articles_slug_key") || database.IsUniqueViolation(err, "articles_pkey") {
		t.Errorf("unique: %v", err)
	}
	if err := exec(`INSERT INTO articles VALUES ('1', 'other', 1)`); !database.IsUniqueViolation(err, "articles_pkey") {
		t.Errorf("primary: %v", err)
	}
	if err := exec(`INSERT INTO comments VALUES ('2', 'missing')`); !database.IsForeignKeyViolation(err) {
		t.Errorf("foreign key insert: %v", err)
	}
	if err := exec(`DELETE FROM articles WHERE id = '1'`); !database.IsForeignKeyViolation(err) {
		t.Errorf("foreign key delete: %v", err)
	}
	if err := exec(`INSERT INTO articles VALUES ('3', 'c', 0)`); !database.IsCheckViolation(err) || database.IsUniqueViolation(err) {
		t.Errorf("check: %v", err)
	}
}

func TestDeadlock(t *testing.T) {
	db := dbtest.Open(t, nil)
	if _, err := db.ExecContext(context.Background(), "CREATE TABLE t (id integer PRIMARY KEY, n integer NOT NULL); INSERT INTO t VALUES (1, 0), (2, 0)"); err != nil {
		t.Fatal(err)
	}
	if err := deadlock(t, db); !database.IsDeadlock(err) || database.IsUniqueViolation(err) {
		t.Fatalf("err=%v", err)
	}
}

// deadlock 制造一次死锁并返回被数据库中止的事务的错误：两个事务各锁住一行，再以相反的顺序请求对方的行。
func deadlock(t *testing.T, db *bun.DB) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	locked := make(chan struct{}, 2)
	proceed := make(chan struct{})
	errs := make(chan error, 2)
	for _, order := range [][2]int{{1, 2}, {2, 1}} {
		go func() {
			errs <- db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
				if _, err := tx.ExecContext(ctx, "UPDATE t SET n = n + 1 WHERE id = ?", order[0]); err != nil {
					return err
				}
				locked <- struct{}{}
				<-proceed
				_, err := tx.ExecContext(ctx, "UPDATE t SET n = n + 1 WHERE id = ?", order[1])
				return err
			})
		}()
	}
	<-locked
	<-locked
	close(proceed)
	first, second := <-errs, <-errs
	if (first == nil) == (second == nil) {
		t.Fatalf("want exactly one aborted transaction, got %v and %v", first, second)
	}
	if first != nil {
		return first
	}
	return second
}
