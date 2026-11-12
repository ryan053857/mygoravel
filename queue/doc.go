// Package queue 是基于 NSQ 的异步任务队列，任务和业务数据在同一个事务中发布。
//
// 发布：Topic.Publish 把任务写入 PostgreSQL 的 queue_outbox 表。传入事务时任务随事务提交，回滚时一起取消；
// 提交后 Worker 把任务转发到 NSQ 并从表中删除。表结构由 Migrations 提供，失败任务保存在 queue_failed_jobs 中。
//
// 处理：Worker 消费已注册 topic 的任务，交给处理函数；同一个应用的 Worker 使用同一个 channel，每个任务只处理一次。
// 处理失败的任务按退避时间重试，不超过 Config.MaxDefer 的由 NSQ 延迟投递，更晚的写回 outbox 等待；
// 次数用完后转入 queue_failed_jobs。
//
// Topic 把 topic 名称和 payload 类型声明在一起，发布和处理都通过它：
//
//	var SendMail = queue.Topic[mail.Message]("blog.mail.send")
//
//	// 发布
//	err := SendMail.Publish(ctx, tx, msg)
//
//	// 处理
//	w := queue.NewWorker(db, cfg, logger)
//	SendMail.Handle(w, mailer.Send)
//	err := w.Run(ctx)
//
// 每个任务至少处理一次：进程崩溃、租约过期或转发后提交失败时任务会被重新处理，处理函数需要能安全地重复执行。
// 任务之间不保证顺序。
package queue
