// Package schedule 按 cron 表达式定时执行任务，精度到秒。
//
// Scheduler 每秒检查一次到期的任务并在独立的 goroutine 中执行。部署多个实例时，
// 同一个任务的同一次触发只在一个实例上执行；任务上一次还没结束时跳过本次。
// 两者都通过 lock.Locker 实现，单实例部署使用 lock.NewMemoryStore 即可。
//
//	sched := schedule.New(locker, logger)
//	sched.Add("sessions:cleanup", "*/10 * * * *", schedule.Cleanup(sessions.Cleanup, 1000))
//	sched.Add("metrics:flush", "@every 15s", metrics.Flush)
//	err := sched.Run(ctx)
//
// 任务失败只记录日志，不会重试；需要重试的工作应在任务中发布到队列。
package schedule
