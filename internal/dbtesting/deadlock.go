package dbtesting

import (
	"context"
	"testing"
	"time"

	"github.com/uptrace/bun"
)

// Deadlock 在 db 上制造一次死锁并返回被数据库中止的事务的错误：两个事务各锁住一行，
// 再以相反的顺序请求对方的行。db 中必须有表 t (id INT PRIMARY KEY, n INT)，包含 id 为 1 和 2 的行。
// 只用于有行锁和死锁检测的数据库（PostgreSQL、MySQL）。
func Deadlock(t *testing.T, db *bun.DB) error {
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
