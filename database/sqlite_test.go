package database_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/database/dbtest"
)

func TestSQLiteConnectionSettings(t *testing.T) {
	db := dbtest.Open(t, database.SQLite, nil)
	ctx := context.Background()
	for pragma, want := range map[string]string{"journal_mode": "wal", "foreign_keys": "1", "busy_timeout": "5000", "synchronous": "1"} {
		var got string
		if err := db.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&got); err != nil || got != want {
			t.Errorf("%s=%q err=%v, want %q", pragma, got, err, want)
		}
	}
}

func TestSQLiteRequiresExistingDirectory(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "missing", "app.db")
	if _, err := database.Open(context.Background(), database.Config{Driver: database.SQLite, DSN: dsn}); err == nil {
		t.Fatal("missing directory accepted")
	}
}

func TestSQLiteConstraintErrors(t *testing.T) {
	db := dbtest.Open(t, database.SQLite, nil)
	ctx := context.Background()
	exec := func(q string) error {
		_, err := db.ExecContext(ctx, q)
		return err
	}
	for _, q := range []string{
		`CREATE TABLE articles (id TEXT PRIMARY KEY, slug TEXT NOT NULL, title TEXT NOT NULL, n INTEGER CHECK (n > 0),
		    disk TEXT, key TEXT, CONSTRAINT articles_slug_key UNIQUE (slug), UNIQUE (disk, key))`,
		`CREATE UNIQUE INDEX articles_lower_title ON articles (lower(title))`,
		`CREATE TABLE comments (id TEXT PRIMARY KEY, article_id TEXT NOT NULL REFERENCES articles (id))`,
		`INSERT INTO articles VALUES ('1', 'hello', 'Hello', 1, 'local', 'a')`,
	} {
		if err := exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		sql, target string
	}{
		{`INSERT INTO articles VALUES ('1', 'b', 'B', 1, NULL, NULL)`, "articles.id"},
		{`INSERT INTO articles VALUES ('2', 'hello', 'C', 1, NULL, NULL)`, "articles.slug"},
		{`INSERT INTO articles VALUES ('3', 'd', 'D', 1, 'local', 'a')`, "articles.disk, articles.key"},
		{`INSERT INTO articles VALUES ('4', 'e', 'HELLO', 1, NULL, NULL)`, "articles_lower_title"},
	} {
		err := exec(tc.sql)
		if !database.IsUniqueViolation(err) || !database.IsUniqueViolation(err, "other", tc.target) ||
			database.IsUniqueViolation(err, "articles_slug_key") || database.IsCheckViolation(err) {
			t.Errorf("%s: target %q not matched by %v", tc.sql, tc.target, err)
		}
	}
	if err := exec(`INSERT INTO articles VALUES ('5', 'f', 'F', 0, NULL, NULL)`); !database.IsCheckViolation(err) || database.IsUniqueViolation(err) {
		t.Errorf("check: %v", err)
	}
	if err := exec(`INSERT INTO comments VALUES ('1', 'missing')`); !database.IsForeignKeyViolation(err) {
		t.Errorf("foreign key: %v", err)
	}
	if database.IsUniqueViolation(errors.New("UNIQUE constraint failed: articles.slug")) {
		t.Error("plain error matched")
	}
}

// 写事务在 BEGIN 时取得写锁：并发的先读后写事务会排队，而不是在升级锁时以 SQLITE_BUSY 失败。
func TestSQLiteConcurrentReadThenWriteTransactions(t *testing.T) {
	db := dbtest.Open(t, database.SQLite, nil)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, "CREATE TABLE counters (id INTEGER PRIMARY KEY, n INTEGER NOT NULL); INSERT INTO counters VALUES (1, 0)"); err != nil {
		t.Fatal(err)
	}
	const workers = 20
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for range workers {
		wg.Go(func() {
			errs <- db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
				var n int
				if err := tx.QueryRowContext(ctx, "SELECT n FROM counters WHERE id = 1").Scan(&n); err != nil {
					return err
				}
				_, err := tx.ExecContext(ctx, "UPDATE counters SET n = ? WHERE id = 1", n+1)
				return err
			})
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := db.QueryRowContext(ctx, "SELECT n FROM counters").Scan(&n); err != nil || n != workers {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

// 时间经 Bun 以 UTC 文本写入，读回后时刻不变，按文本比较也保持时间顺序。
func TestSQLiteTimesRoundTripAndCompare(t *testing.T) {
	db := dbtest.Open(t, database.SQLite, nil)
	ctx := context.Background()
	type event struct {
		bun.BaseModel `bun:"table:events"`
		ID            uuid.UUID `bun:"id,pk"`
		At            time.Time `bun:"at,notnull"`
	}
	if _, err := db.ExecContext(ctx, "CREATE TABLE events (id TEXT PRIMARY KEY, at TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	shanghai := time.FixedZone("CST", 8*3600)
	base := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	times := []time.Time{base, base.Add(500 * time.Millisecond), base.Add(time.Second).In(shanghai), base.Add(time.Microsecond)}
	for _, at := range times {
		if _, err := db.NewInsert().Model(&event{ID: uuid.Must(uuid.NewV7()), At: at}).Exec(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var got []event
	if err := db.NewSelect().Model(&got).Where("at > ?", base).OrderExpr("at").Scan(ctx); err != nil {
		t.Fatal(err)
	}
	want := []time.Time{base.Add(time.Microsecond), base.Add(500 * time.Millisecond), base.Add(time.Second)}
	if len(got) != len(want) {
		t.Fatalf("got %d rows", len(got))
	}
	for i := range want {
		if !got[i].At.Equal(want[i]) {
			t.Errorf("row %d at=%v want %v", i, got[i].At, want[i])
		}
	}
}
