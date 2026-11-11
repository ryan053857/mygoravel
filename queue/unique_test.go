package queue_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/internal/dbtesting"
	"github.com/ryan053857/mygoravel/queue"
)

// pending 返回 topic 中排队任务的 payload，按 N 排序。
func pending(t *testing.T, db *bun.DB, topic string) []int {
	t.Helper()
	var payloads [][]byte
	err := db.NewSelect().Table("queue_jobs").Column("payload").Where("topic = ?", topic).Scan(context.Background(), &payloads)
	if err != nil {
		t.Fatal(err)
	}
	var ns []int
	for _, data := range payloads {
		var p payload
		if err := json.Unmarshal(data, &p); err != nil {
			t.Fatal(err)
		}
		ns = append(ns, p.N)
	}
	slices.Sort(ns)
	return ns
}

func TestUniqueKeyValidation(t *testing.T) {
	db := openDB(t, dbtesting.Drivers()[0])
	ctx := context.Background()
	for _, key := range []string{"", strings.Repeat("k", queue.MaxUniqueLen+1)} {
		if err := queue.Publish(ctx, queue.Database(db), "jobs", payload{N: 1}, queue.Unique(key)); err == nil {
			t.Errorf("Unique key of %d bytes accepted", len(key))
		}
	}
	if err := queue.Publish(ctx, queue.Database(db), "jobs", payload{N: 1}, queue.Unique(strings.Repeat("键", queue.MaxUniqueLen/3))); err != nil {
		t.Fatal(err)
	}
}

func TestUniqueInTransactions(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		ctx := context.Background()
		db := openDB(t, driver)
		boom := errors.New("boom")

		// 回滚的事务不占用键。
		err := db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
			if err := queue.Publish(ctx, queue.Database(tx), "jobs", payload{N: 1}, queue.Unique("k")); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatal(err)
		}

		// 事务中合并重复的任务不会让事务中止，之后的语句照常执行。
		err = db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
			for n := 2; n <= 3; n++ {
				if err := queue.Publish(ctx, queue.Database(tx), "jobs", payload{N: n}, queue.Unique("k")); err != nil {
					return err
				}
			}
			return queue.Publish(ctx, queue.Database(tx), "jobs", payload{N: 4})
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := pending(t, db, "jobs"); !slices.Equal(got, []int{2, 4}) {
			t.Fatalf("pending = %v", got)
		}
	})
}

// 两个事务同时发布同一个键：后者等前者结束，前者提交则跳过，回滚则插入。
func TestUniqueConcurrentTransactions(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		for _, commit := range []bool{true, false} {
			ctx := context.Background()
			db := openDB(t, driver)
			first, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := queue.Publish(ctx, queue.Database(first), "jobs", payload{N: 1}, queue.Unique("k")); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				done <- db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
					return queue.Publish(ctx, queue.Database(tx), "jobs", payload{N: 2}, queue.Unique("k"))
				})
			}()
			select {
			case err := <-done:
				t.Fatalf("second transaction did not wait for the first: %v", err)
			case <-time.After(200 * time.Millisecond):
			}
			if commit {
				err = first.Commit()
			} else {
				err = first.Rollback()
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			want := []int{1}
			if !commit {
				want = []int{2}
			}
			if got := pending(t, db, "jobs"); !slices.Equal(got, want) {
				t.Fatalf("commit=%v: pending = %v, want %v", commit, got, want)
			}
		}
	})
}
