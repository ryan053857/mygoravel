package command

import (
	"context"
	"sync"

	"github.com/cockroachdb/errors"
)

// ErrStoppedEarly 表示 Concurrently 中的一个函数在收到停止信号之前就返回了 nil。
var ErrStoppedEarly = errors.New("command: stopped before shutdown")

// Concurrently 在同一个进程中同时运行 runs，例如 HTTP 服务、队列 worker 和定时任务，直到全部返回。
//
// 每个函数都收到同一个派生的 ctx：ctx 被取消（通常是收到停止信号），或者任何一个函数返回时，
// 派生的 ctx 随之取消，其余函数据此平滑停止。在停止之前就返回 nil 的函数视为异常退出，
// 返回值包含 ErrStoppedEarly，进程以非 0 状态退出，交给 systemd、Kubernetes 等重启。
// 停止过程中返回的 context.Canceled 不计为错误。
//
// 返回值合并了全部错误；没有错误时返回 nil。函数中的 panic 不会被恢复。
func Concurrently(ctx context.Context, runs ...func(context.Context) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errs := make([]error, len(runs))
	var wg sync.WaitGroup
	for i, run := range runs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer cancel()
			err := run(ctx)
			switch {
			case err == nil && ctx.Err() == nil:
				errs[i] = ErrStoppedEarly
			case errors.Is(err, context.Canceled) && ctx.Err() != nil:
			default:
				errs[i] = err
			}
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}
