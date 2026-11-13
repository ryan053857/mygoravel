package database_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/uptrace/bun"

	"github.com/go-tenon/tenon/database"
	"github.com/go-tenon/tenon/database/dbtest"
	"github.com/go-tenon/tenon/logging/logtest"
)

func openItems(t *testing.T) *bun.DB {
	t.Helper()
	db := dbtest.Open(t, nil)
	if _, err := db.ExecContext(context.Background(), "CREATE TABLE items (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	return db
}

func insert(ctx context.Context, db bun.IDB, id int) error {
	_, err := db.NewRaw("INSERT INTO items (id) VALUES (?)", id).Exec(ctx)
	return err
}

func ids(t *testing.T, db *bun.DB) []int {
	t.Helper()
	var out []int
	if err := db.NewRaw("SELECT id FROM items ORDER BY id").Scan(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// recorder 记录回调的执行顺序。
type recorder struct{ ran []string }

func (r *recorder) hook(name string) func(context.Context) error {
	return func(context.Context) error {
		r.ran = append(r.ran, name)
		return nil
	}
}

func TestRunInTxRunsHooksAfterCommit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openItems(t)
	var r recorder
	err := database.RunInTx(ctx, db, nil, func(ctx context.Context, tx database.Tx) error {
		if err := insert(ctx, tx, 1); err != nil {
			return err
		}
		tx.AfterCommit(func(context.Context) error {
			// 回调执行时数据已经提交，事务外的连接能读到。
			r.ran = append(r.ran, "first")
			if got := ids(t, db); !slices.Equal(got, []int{1}) {
				t.Errorf("hook saw %v, want committed row", got)
			}
			return nil
		})
		tx.AfterCommit(r.hook("second"))
		if len(r.ran) != 0 {
			t.Error("hook ran before commit")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.ran, []string{"first", "second"}) {
		t.Fatalf("ran %v", r.ran)
	}
}

func TestRunInTxDiscardsHooksOnRollback(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openItems(t)
	var r recorder
	boom := errors.New("boom")
	err := database.RunInTx(ctx, db, nil, func(ctx context.Context, tx database.Tx) error {
		if err := insert(ctx, tx, 1); err != nil {
			return err
		}
		tx.AfterCommit(r.hook("discarded"))
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if len(r.ran) != 0 || len(ids(t, db)) != 0 {
		t.Fatalf("ran %v, rows %v", r.ran, ids(t, db))
	}
}

func TestRunInTxSavepoints(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openItems(t)
	var r recorder
	boom := errors.New("boom")
	err := database.RunInTx(ctx, db, nil, func(ctx context.Context, tx database.Tx) error {
		tx.AfterCommit(r.hook("outer"))
		err := database.RunInTx(ctx, tx, nil, func(ctx context.Context, sp database.Tx) error {
			if err := insert(ctx, sp, 1); err != nil {
				return err
			}
			sp.AfterCommit(r.hook("failed savepoint"))
			return boom
		})
		if !errors.Is(err, boom) {
			t.Errorf("savepoint err = %v", err)
		}
		err = database.RunInTx(ctx, tx, nil, func(ctx context.Context, sp database.Tx) error {
			sp.AfterCommit(r.hook("savepoint"))
			return insert(ctx, sp, 2)
		})
		if err != nil {
			return err
		}
		if len(r.ran) != 0 {
			t.Errorf("savepoint hooks ran before the outer commit: %v", r.ran)
		}
		tx.AfterCommit(r.hook("last"))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.ran, []string{"outer", "savepoint", "last"}) {
		t.Fatalf("ran %v", r.ran)
	}
	if got := ids(t, db); !slices.Equal(got, []int{2}) {
		t.Fatalf("rows %v", got)
	}
}

func TestRunInTxOuterRollbackDiscardsSavepointHooks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openItems(t)
	var r recorder
	boom := errors.New("boom")
	err := database.RunInTx(ctx, db, nil, func(ctx context.Context, tx database.Tx) error {
		err := database.RunInTx(ctx, tx, nil, func(ctx context.Context, sp database.Tx) error {
			sp.AfterCommit(r.hook("savepoint"))
			return insert(ctx, sp, 1)
		})
		if err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if len(r.ran) != 0 || len(ids(t, db)) != 0 {
		t.Fatalf("ran %v, rows %v", r.ran, ids(t, db))
	}
}

type ctxKey struct{}

func TestRunInTxContext(t *testing.T) {
	t.Parallel()
	db := openItems(t)
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), ctxKey{}, "outer"))
	defer cancel()
	var hookErr, hookValue any
	err := database.RunInTx(ctx, db, nil, func(ctx context.Context, tx database.Tx) error {
		inner := context.WithValue(ctx, ctxKey{}, "inner")
		err := database.RunInTx(inner, tx, nil, func(ctx context.Context, sp database.Tx) error {
			// 保存点收到调用方的 ctx，而不是外层事务的 ctx。
			if v := ctx.Value(ctxKey{}); v != "inner" {
				t.Errorf("savepoint ctx value = %v", v)
			}
			return nil
		})
		if err != nil {
			return err
		}
		tx.AfterCommit(func(context.Context) error {
			cancel() // 例如请求在提交后结束
			return nil
		})
		tx.AfterCommit(func(ctx context.Context) error {
			hookErr, hookValue = ctx.Err(), ctx.Value(ctxKey{})
			return nil
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if hookErr != nil || hookValue != "outer" {
		t.Fatalf("hook ctx err %v, value %v", hookErr, hookValue)
	}
}

func TestRunInTxLogsHookFailures(t *testing.T) {
	t.Parallel()
	logs := logtest.Capture(t)
	db := openItems(t)
	var r recorder
	err := database.RunInTx(context.Background(), db, nil, func(ctx context.Context, tx database.Tx) error {
		tx.AfterCommit(func(context.Context) error { return errors.New("broadcast failed") })
		tx.AfterCommit(func(context.Context) error { panic("bad hook") })
		tx.AfterCommit(r.hook("after failures"))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.ran, []string{"after failures"}) {
		t.Fatalf("ran %v", r.ran)
	}
	failed, ok := logs.Find("after commit callback failed")
	if !ok || failed.Level != slog.LevelError || !strings.Contains(fmt.Sprint(failed.Attrs), "broadcast failed") {
		t.Fatalf("failure log %+v", failed)
	}
	panicked, ok := logs.Find("after commit callback panicked")
	if !ok || panicked.Level != slog.LevelError || panicked.Attr("panic") != "bad hook" || panicked.Attr("stack") == "" {
		t.Fatalf("panic log %+v", panicked)
	}
}

func TestRunInTxMisuse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openItems(t)
	noop := func(context.Context, database.Tx) error { return nil }

	err := db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		return database.RunInTx(ctx, tx, nil, noop)
	})
	if err == nil {
		t.Error("RunInTx inside a bun.Tx succeeded")
	}

	var leaked database.Tx
	err = database.RunInTx(ctx, db, nil, func(ctx context.Context, tx database.Tx) error {
		leaked = tx
		if database.RunInTx(ctx, tx, &sql.TxOptions{ReadOnly: true}, noop) == nil {
			t.Error("savepoint accepted TxOptions")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	mustPanic(t, "after finish", func() { leaked.AfterCommit(func(context.Context) error { return nil }) })
	mustPanic(t, "zero Tx", func() { database.Tx{}.AfterCommit(func(context.Context) error { return nil }) })
	_ = database.RunInTx(ctx, db, nil, func(ctx context.Context, tx database.Tx) error {
		mustPanic(t, "nil callback", func() { tx.AfterCommit(nil) })
		return nil
	})
}

func mustPanic(t *testing.T, name string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s: no panic", name)
		}
	}()
	fn()
}
