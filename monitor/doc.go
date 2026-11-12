// Package monitor 把运行统计保存在应用数据库中，供命令和后台页面查看：
// 多个实例的记录汇总在一起，任何进程都能查询，包括刚启动的命令。
//
// 统计有两种。Record 和 RecordDuration 按分钟汇总次数、总和与最大值，例如每类邮件发了多少、耗时多少；
// Set 只保留最新的值，例如每个定时任务上一次执行的结果。记录在内存中累加，每 10 秒批量写入一次；
// Totals、Series、Latest 查询它们，Cleanup 清理过期的汇总。表结构由 Migrations 提供。
//
//	mon := monitor.New(db, nil)
//	defer mon.Close(ctx)
//
//	mon.RecordDuration("mail.sent", "welcome", elapsed)
//	totals, err := mon.Totals(ctx, time.Now().Add(-time.Hour), time.Now(), "mail.sent", "mail.failed")
//
// 它与 OpenTelemetry 指标互不依赖：指标推送到监控系统，用于告警和长期保存；
// monitor 留在应用自己的数据库中，用于应用内查看。
package monitor
