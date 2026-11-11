// Package queue 提供持久化的异步任务队列，基于 Watermill 的消息模型。
//
// 任务由 Driver 保存。Database 驱动把任务保存在 PostgreSQL、MySQL 或 SQLite 的 queue_jobs 表中，
// 表结构由 Migrations 提供，传入 bun.Tx 时任务与业务写入在同一个事务里提交。
// Subscriber 从驱动领取到期的任务交给 Watermill 的 Router 处理，多个实例竞争领取；
// 处理失败的任务按退避时间重试，次数用完后转入 queue_failed_jobs。重试、失败任务、并发和平滑停止与驱动无关。
//
//	// 发布（也可以用 Topic 把 topic 名称和 payload 类型声明在一起）
//	err := queue.Publish(ctx, queue.Database(tx), "mail.send", msg)
//
//	// 处理
//	sub := queue.NewSubscriber(queue.Database(db), cfg, logger)
//	defer sub.Close()
//	router := queue.NewRouter(logger)
//	queue.Handle(router, sub, "mail.send", mailer.Send)
//	err := router.Run(ctx)
//
// 每个任务至少处理一次：进程崩溃或租约过期时任务会被重新处理，处理函数需要能安全地重复执行。
// 任务之间不保证顺序。发布时用 Unique 指定键，可以合并同一个 topic 中还没开始执行的重复任务。
package queue
