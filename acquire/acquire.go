// Package acquire 在 context 中传递“最多愿意等多久去拿一个共享资源”的时限，例如数据库连接池中的空闲连接。
//
// 设置时限的一方（通常是 HTTP 中间件）不需要知道有哪些资源，持有资源的一方（例如 database 的连接池）
// 只需要读取它：连接用满时，请求在时限后快速失败，而不是排队直到请求超时。没有设置时限的 context
// （队列任务、命令）照常排队，直到 context 结束。
//
//	ctx = acquire.WithTimeout(ctx, time.Second)
//	if d, ok := acquire.Timeout(ctx); ok { ... } // 资源池按 d 限制等待
package acquire

import (
	"context"
	"time"
)

type key struct{}

// WithTimeout 返回一个 ctx：用它向资源池取资源时，最多等待 d。d 不大于 0 时取消之前设置的时限。
// 时限只作用于等待资源，不影响拿到资源之后的使用。
func WithTimeout(ctx context.Context, d time.Duration) context.Context {
	return context.WithValue(ctx, key{}, d)
}

// Timeout 返回 ctx 中的时限；没有设置或不大于 0 时 ok 为 false，资源池应当一直等到 ctx 结束。
func Timeout(ctx context.Context) (d time.Duration, ok bool) {
	d, _ = ctx.Value(key{}).(time.Duration)
	return d, d > 0
}
