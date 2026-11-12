// Package monitor 把运行统计保存在应用数据库中，供命令和后台页面查看：
// 多个实例的记录汇总在一起，任何进程都能查询，包括刚启动的命令。
//
// 统计有两种。Recorder.Record 按分钟聚合次数、总和与最大值，例如每个 topic 处理了多少任务、耗时多少；
// Recorder.Set 只保留最新的值，例如每个定时任务上一次执行的结果。Recorder 在内存中累加，
// 每 10 秒批量写入一次；Store 查询它们，并清理过期的记录。表结构由 Migrations 提供。
//
//	store := monitor.NewStore(db)
//	rec := monitor.NewRecorder(store, nil)
//	defer rec.Close(ctx)
//
//	rec.Record("queue.processed", "mail.send", duration.Seconds())
//	totals, err := store.Totals(ctx, "queue.processed", time.Now().Add(-time.Hour), time.Now())
//
// 它与 OpenTelemetry 指标互不依赖：指标推送到监控系统，用于告警和长期保存；
// monitor 留在应用自己的数据库中，用于应用内查看。
package monitor
