// Package database 打开 PostgreSQL 的 Bun 连接池（pgx），并识别约束冲突和死锁。
// Open 返回 *bun.DB，查询和事务使用 Bun 的原生 API；RunInTx 在事务提交后执行 AfterCommit 登记的操作。
//
//	db, err := database.Open(ctx, cfg.Database)
package database
