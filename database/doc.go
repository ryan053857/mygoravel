// Package database 打开 PostgreSQL 的 Bun 连接池（pgx），并识别约束冲突和死锁。
// Open 返回 *bun.DB，查询和事务使用 Bun 的原生 API；RunInTx 在事务提交后执行 AfterCommit 登记的操作。
//
//	db, err := database.Open(ctx, cfg.Database, database.WithTelemetry(tel.Meters, tel.Tracers))
//
// 超过 Config.SlowQuery 的语句记录为慢查询日志；WithTelemetry 报告每条语句的耗时指标和链路。
// 日志和链路中的 SQL 都把字面量换成了 ?，不带参数的值。
package database
