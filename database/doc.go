// Package database 按配置的 Driver 装配 PostgreSQL（pgx）或 SQLite（modernc）的 Bun 连接池，并识别约束错误。
// Open 返回 *bun.DB，查询和事务使用 Bun 的原生 API。
package database
