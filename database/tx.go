package database

import (
	"context"
	"database/sql"
	"log/slog"
	"runtime/debug"
	"sync"

	"github.com/cockroachdb/errors"
	"github.com/uptrace/bun"
)

// Tx 是 RunInTx 开启的事务。它嵌入 bun.Tx，可以直接传给接收 bun.IDB 的函数，
// 例如 activity.Record、notification.Send 和 queue.Database；
// 另外可以用 AfterCommit 登记事务提交之后才执行的操作。
type Tx struct {
	bun.Tx
	scope *scope
}

// scope 是一层事务或保存点。同一个最外层事务中的各层共用一个回调列表。
type scope struct {
	hooks *hooks
	done  bool // 由 hooks.mu 保护
}

// hooks 是最外层事务登记的提交后回调。
type hooks struct {
	mu  sync.Mutex
	fns []func(context.Context) error
}

// AfterCommit 登记一个回调，在最外层事务提交成功之后执行；事务或登记它的保存点回滚时丢弃。
//
// 回调按登记顺序在 RunInTx 返回之前同步执行，收到的 ctx 是 RunInTx 的 ctx 去掉取消信号，
// 请求结束不会打断它。回调返回错误或 panic 时用 slog.Default() 记录 Error 日志，其余回调照常执行，
// RunInTx 仍然返回 nil，因为数据已经提交。
//
// 回调只保存在内存中：提交之后、执行之前进程退出，回调就不会执行。
// 适合广播、删除缓存这类尽力而为的操作；必须执行的任务请在事务中用 queue.Database(tx) 发布。
//
// 事务结束之后再调用 AfterCommit 会 panic。
func (tx Tx) AfterCommit(fn func(ctx context.Context) error) {
	if fn == nil {
		panic("database: AfterCommit with nil callback")
	}
	if tx.scope == nil {
		panic("database: AfterCommit on a Tx not started by database.RunInTx")
	}
	h := tx.scope.hooks
	h.mu.Lock()
	defer h.mu.Unlock()
	if tx.scope.done {
		panic("database: AfterCommit after the transaction finished")
	}
	h.fns = append(h.fns, fn)
}

// RunInTx 在事务中执行 fn：fn 返回错误时回滚，返回 nil 时提交，然后执行 AfterCommit 登记的回调。
// 参数与 Bun 的 RunInTx 相同，只是数据库作为第一个参数传入，fn 收到的是 Tx。
//
// db 是 *bun.DB 或 bun.Conn 时开启一个新事务。db 是外层 RunInTx 的 Tx 时以保存点执行：
// 保存点内登记的回调等外层事务提交后才执行，保存点回滚时丢弃；此时 opts 必须为 nil。
// db 不能是不由 RunInTx 开启的 bun.Tx，因为无法知道它何时提交。
func RunInTx(ctx context.Context, db bun.IDB, opts *sql.TxOptions, fn func(ctx context.Context, tx Tx) error) error {
	switch parent := db.(type) {
	case Tx:
		return parent.savepoint(ctx, opts, fn)
	case *Tx:
		return parent.savepoint(ctx, opts, fn)
	case bun.Tx, *bun.Tx:
		return errors.New("database: RunInTx inside a bun.Tx; pass the database.Tx from the outer RunInTx")
	}
	s := &scope{hooks: &hooks{}}
	err := db.RunInTx(ctx, opts, func(_ context.Context, tx bun.Tx) error {
		return fn(ctx, Tx{Tx: tx, scope: s})
	})
	fns := s.finish(-1)
	if err != nil {
		return err
	}
	ctx = context.WithoutCancel(ctx)
	for _, fn := range fns {
		runHook(ctx, fn)
	}
	return nil
}

// savepoint 在 tx 中以保存点执行 fn，回调登记到最外层事务的列表中。
func (tx Tx) savepoint(ctx context.Context, opts *sql.TxOptions, fn func(ctx context.Context, tx Tx) error) error {
	if tx.scope == nil {
		return errors.New("database: RunInTx inside a Tx not started by database.RunInTx")
	}
	if opts != nil {
		return errors.New("database: a savepoint does not take TxOptions")
	}
	h := tx.scope.hooks
	h.mu.Lock()
	mark := len(h.fns)
	h.mu.Unlock()
	s := &scope{hooks: h}
	// Bun 把外层事务的 ctx 传给保存点的回调，这里改为调用方的 ctx。
	err := tx.Tx.RunInTx(ctx, nil, func(_ context.Context, sp bun.Tx) error {
		return fn(ctx, Tx{Tx: sp, scope: s})
	})
	if err != nil {
		s.finish(mark)
		return err
	}
	s.finish(-1)
	return nil
}

// finish 结束这一层：之后不能再登记回调。keep 不小于 0 时只保留前 keep 个回调；
// 返回保留的回调。
func (s *scope) finish(keep int) []func(context.Context) error {
	h := s.hooks
	h.mu.Lock()
	defer h.mu.Unlock()
	s.done = true
	if keep >= 0 && keep < len(h.fns) {
		clear(h.fns[keep:])
		h.fns = h.fns[:keep]
	}
	return h.fns
}

// runHook 执行一个提交后回调，记录它的错误或 panic。
func runHook(ctx context.Context, fn func(context.Context) error) {
	defer func() {
		if r := recover(); r != nil {
			slog.ErrorContext(ctx, "after commit callback panicked", "panic", r, "stack", string(debug.Stack()))
		}
	}()
	if err := fn(ctx); err != nil {
		slog.ErrorContext(ctx, "after commit callback failed", "error", err)
	}
}
