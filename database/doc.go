// Package database 用应用选定的驱动打开 Bun 连接池。Open 返回 *bun.DB，查询和事务使用 Bun 的原生 API。
//
// 本包不链接任何数据库驱动：PostgreSQL 在 database/postgres，SQLite 在 database/sqlite，
// 各自提供 Driver 与约束错误的判断函数。
package database
