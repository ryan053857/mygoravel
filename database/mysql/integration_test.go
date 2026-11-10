//go:build integration

package mysql_test

import (
	"context"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/database/dbtest"
	"github.com/ryan053857/mygoravel/database/migrate"
	"github.com/ryan053857/mygoravel/database/mysql"
)

func TestSessionSettings(t *testing.T) {
	db := dbtest.Open(t, mysql.Driver, nil)
	var tz, version string
	if err := db.QueryRowContext(context.Background(), "SELECT @@session.time_zone, VERSION()").Scan(&tz, &version); err != nil {
		t.Fatal(err)
	}
	if tz != "+00:00" {
		t.Fatalf("time_zone=%q", tz)
	}
	t.Logf("MySQL %s", version)
}

// 任意时区的时间都以 UTC 写入，读回后时刻不变；CURRENT_TIMESTAMP 也是 UTC。
func TestTimesAreUTC(t *testing.T) {
	db := dbtest.Open(t, mysql.Driver, nil)
	ctx := context.Background()
	type event struct {
		bun.BaseModel `bun:"table:events"`
		ID            uuid.UUID `bun:"id,pk"`
		At            time.Time `bun:"at,notnull"`
	}
	if _, err := db.ExecContext(ctx, "CREATE TABLE events (id CHAR(36) PRIMARY KEY, at DATETIME(6) NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 11, 17, 0, 0, 123456000, time.FixedZone("CST", 8*3600))
	e := &event{ID: uuid.Must(uuid.NewV7()), At: at}
	if _, err := db.NewInsert().Model(e).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := db.QueryRowContext(ctx, "SELECT CAST(at AS CHAR) FROM events").Scan(&stored); err != nil || stored != "2026-11-11 09:00:00.123456" {
		t.Fatalf("stored=%q err=%v", stored, err)
	}
	got := new(event)
	if err := db.NewSelect().Model(got).Where("id = ?", e.ID).Scan(ctx); err != nil || !got.At.Equal(at) || got.At.Location() != time.UTC {
		t.Fatalf("at=%v err=%v", got.At, err)
	}
	var now time.Time
	if err := db.QueryRowContext(ctx, "SELECT CURRENT_TIMESTAMP(6)").Scan(&now); err != nil || time.Since(now).Abs() > time.Minute {
		t.Fatalf("now=%v err=%v", now, err)
	}
}

// RowsAffected 是匹配的行数：值没有变化的 UPDATE 也算 1 行，与 PostgreSQL 和 SQLite 一致。
func TestRowsAffectedCountsMatchedRows(t *testing.T) {
	db := dbtest.Open(t, mysql.Driver, nil)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, "CREATE TABLE t (id INT PRIMARY KEY, n INT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO t VALUES (1, 1)"); err != nil {
		t.Fatal(err)
	}
	res, err := db.ExecContext(ctx, "UPDATE t SET n = 1 WHERE id = 1")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := res.RowsAffected(); n != 1 || err != nil {
		t.Fatalf("affected=%d err=%v", n, err)
	}
}

func TestConstraintErrors(t *testing.T) {
	db := dbtest.Open(t, mysql.Driver, nil)
	ctx := context.Background()
	for _, q := range []string{
		`CREATE TABLE articles (id CHAR(36) PRIMARY KEY, slug VARCHAR(100) NOT NULL, n INT CHECK (n > 0),
		    CONSTRAINT articles_slug_key UNIQUE (slug))`,
		`CREATE TABLE comments (id CHAR(36) PRIMARY KEY, article_id CHAR(36) NOT NULL,
		    CONSTRAINT comments_article FOREIGN KEY (article_id) REFERENCES articles (id))`,
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
	if err := exec(`INSERT INTO articles VALUES ('2', 'hello', 1)`); !mysql.IsUniqueViolation(err, "articles_slug_key") {
		t.Errorf("unique: %v", err)
	}
	if err := exec(`INSERT INTO articles VALUES ('1', 'other', 1)`); !mysql.IsUniqueViolation(err, "PRIMARY") || mysql.IsUniqueViolation(err, "articles_slug_key") {
		t.Errorf("primary: %v", err)
	}
	if err := exec(`INSERT INTO comments VALUES ('2', 'missing')`); !mysql.IsForeignKeyViolation(err) {
		t.Errorf("foreign key insert: %v", err)
	}
	if err := exec(`DELETE FROM articles WHERE id = '1'`); !mysql.IsForeignKeyViolation(err) {
		t.Errorf("foreign key delete: %v", err)
	}
	if err := exec(`INSERT INTO articles VALUES ('3', 'c', 0)`); !mysql.IsCheckViolation(err) {
		t.Errorf("check: %v", err)
	}
}

// 迁移在执行前取得 GET_LOCK：其他连接持有同名锁时等待，释放后继续。
func TestMigrateWaitsForLock(t *testing.T) {
	db := dbtest.Open(t, mysql.Driver, nil)
	ctx := context.Background()
	holder, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	var got int
	if err := holder.QueryRowContext(ctx, "SELECT GET_LOCK(CONCAT('goose:', DATABASE()), 1)").Scan(&got); err != nil || got != 1 {
		t.Fatalf("got=%d err=%v", got, err)
	}

	migrations := fstest.MapFS{
		"00001_create_notes.sql": {Data: []byte("-- +goose Up\nCREATE TABLE notes (id INT PRIMARY KEY);\n-- +goose Down\nDROP TABLE notes;\n")},
	}
	p, err := migrate.NewProvider(db, migrations)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- migrate.Up(ctx, p) }()
	select {
	case err := <-done:
		t.Fatalf("migration did not wait for the lock: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if _, err := holder.ExecContext(ctx, "DO RELEASE_LOCK(CONCAT('goose:', DATABASE()))"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if ver, err := p.GetDBVersion(ctx); err != nil || ver != 1 {
		t.Fatalf("version=%d err=%v", ver, err)
	}
}
