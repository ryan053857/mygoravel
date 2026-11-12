// Package ratelimit 按 key 限制操作频率，与传输方式无关：既用于 HTTP 中间件
// （httpx.RateLimit），也可以在处理器、命令或任务中直接调用。
//
// 算法是 GCRA，等价于令牌桶：每个 key 平均每 Interval 允许 Rate 次，最多连续 Burst 次，
// 被拒绝的调用不消耗配额。Memory 在进程内计数；子包 redislimit 存在 Redis 中，多实例共享。
//
//	logins := redislimit.New(rdb, "blog:login", ratelimit.Config{Rate: 5, Interval: time.Minute, Burst: 5})
//	if err := logins.Allow(ctx, email); err != nil {
//		return nil, err // 超出时为 ErrExceeded，HTTP 边界返回 429 和 Retry-After
//	}
package ratelimit
