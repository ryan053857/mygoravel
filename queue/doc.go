// Package queue 提供持久化的异步任务队列，基于 Watermill 的消息模型。
//
// 任务保存在 PostgreSQL 的 queue_jobs 表中，表结构由 Migrations 提供。Publisher 把任务写入表中，
// 传入 bun.Tx 时与业务写入在同一个事务里提交；Subscriber 领取到期的任务交给 Watermill 的 Router 处理，
// 多个实例通过 SKIP LOCKED 竞争领取，处理失败的任务按退避时间重试，次数用完后转入 queue_failed_jobs。
//
//	// 发布
//	err := queue.Publish(ctx, queue.NewPublisher(tx), "mail.send", msg)
//
//	// 处理
//	sub := queue.NewSubscriber(db, cfg, logger)
//	defer sub.Close()
//	router := queue.NewRouter(logger)
//	queue.Handle(router, sub, "mail.send", mailer.Send)
//	err := router.Run(ctx)
//
// 每个任务至少处理一次：进程崩溃或租约过期时任务会被重新处理，处理函数需要能安全地重复执行。
// 任务之间不保证顺序。
package queue
