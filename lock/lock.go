package lock

import (
	"context"
	"crypto/rand"
	"time"

	"github.com/cockroachdb/errors"
)

var (
	// ErrNotAcquired 表示锁正被其他持有者占用。
	ErrNotAcquired = errors.New("lock: not acquired")
	// ErrNotHeld 表示锁已过期并被释放或被其他持有者取得。
	ErrNotHeld = errors.New("lock: not held")
)

// Store 保存锁。每个方法都必须是原子的：
// Acquire 仅在 name 未被持有或已过期时取得锁；Refresh 和 Release 仅在 owner 仍持有锁时生效。
// 返回值表示操作是否生效。Held 报告 name 当前是否被持有（未过期）。
type Store interface {
	Acquire(ctx context.Context, name, owner string, ttl time.Duration) (bool, error)
	Refresh(ctx context.Context, name, owner string, ttl time.Duration) (bool, error)
	Release(ctx context.Context, name, owner string) (bool, error)
	Held(ctx context.Context, name string) (bool, error)
}

// Locker 在 Store 之上取得和管理锁。
type Locker struct {
	store Store
}

// New 创建 Locker。
func New(store Store) *Locker {
	if store == nil {
		panic("lock: nil store")
	}
	return &Locker{store: store}
}

// Lock 是一次成功取得的锁，只能由取得它的 Locker 释放。
type Lock struct {
	store Store
	name  string
	owner string
}

// Acquire 尝试取得 name 上的锁，ttl 后自动过期。锁被占用时立即返回 ErrNotAcquired。
func (l *Locker) Acquire(ctx context.Context, name string, ttl time.Duration) (*Lock, error) {
	if name == "" {
		return nil, errors.New("lock: empty name")
	}
	if ttl <= 0 {
		return nil, errors.New("lock: ttl must be positive")
	}
	owner := rand.Text()
	ok, err := l.store.Acquire(ctx, name, owner, ttl)
	if err != nil {
		return nil, errors.Wrapf(err, "lock: acquire %q", name)
	}
	if !ok {
		return nil, ErrNotAcquired
	}
	return &Lock{store: l.store, name: name, owner: owner}, nil
}

// pollInterval 是 Wait 重试取得锁的间隔。
const pollInterval = 100 * time.Millisecond

// Wait 与 Acquire 相同，但锁被占用时最多等待 timeout，期间按固定间隔重试。
// 超时返回 ErrNotAcquired；ctx 取消时返回 ctx 的错误。
func (l *Locker) Wait(ctx context.Context, name string, ttl, timeout time.Duration) (*Lock, error) {
	deadline := time.Now().Add(timeout)
	for {
		lk, err := l.Acquire(ctx, name, ttl)
		if !errors.Is(err, ErrNotAcquired) {
			return lk, err
		}
		wait := min(pollInterval, time.Until(deadline))
		if wait <= 0 {
			return nil, ErrNotAcquired
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
}

// Do 取得 name 上的锁后执行 fn，结束后释放锁。锁被占用时不执行 fn，返回 ErrNotAcquired。
//
// fn 执行时间超过 ttl 时锁可能已被他人取得，此时返回值包含 ErrNotHeld。
// 释放不受 ctx 取消影响。
func (l *Locker) Do(ctx context.Context, name string, ttl time.Duration, fn func(ctx context.Context) error) error {
	lk, err := l.Acquire(ctx, name, ttl)
	if err != nil {
		return err
	}
	fnErr := fn(ctx)
	return errors.Join(fnErr, lk.Release(context.WithoutCancel(ctx)))
}

// Held 报告 name 上的锁当前是否被持有，不取得也不影响它。结果只反映查询的那一刻，
// 用于展示状态，不能代替 Acquire 判断能否执行。
func (l *Locker) Held(ctx context.Context, name string) (bool, error) {
	ok, err := l.store.Held(ctx, name)
	return ok, errors.Wrapf(err, "lock: check %q", name)
}

// Name 返回锁的名称。
func (lk *Lock) Name() string { return lk.name }

// Refresh 把锁的过期时间重设为从现在起 ttl，用于延长长任务持有的锁。
// 锁已不再由本持有者持有时返回 ErrNotHeld。
func (lk *Lock) Refresh(ctx context.Context, ttl time.Duration) error {
	if ttl <= 0 {
		return errors.New("lock: ttl must be positive")
	}
	ok, err := lk.store.Refresh(ctx, lk.name, lk.owner, ttl)
	if err != nil {
		return errors.Wrapf(err, "lock: refresh %q", lk.name)
	}
	if !ok {
		return ErrNotHeld
	}
	return nil
}

// Release 释放锁。锁已不再由本持有者持有时返回 ErrNotHeld，不影响当前持有者。
func (lk *Lock) Release(ctx context.Context) error {
	ok, err := lk.store.Release(ctx, lk.name, lk.owner)
	if err != nil {
		return errors.Wrapf(err, "lock: release %q", lk.name)
	}
	if !ok {
		return ErrNotHeld
	}
	return nil
}
