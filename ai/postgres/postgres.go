// Package postgres 把对话保存在 PostgreSQL 中，表由 ai.Migrations 创建。
package postgres

import (
	"trpc.group/trpc-go/trpc-agent-go/session"
	tpg "trpc.group/trpc-go/trpc-agent-go/session/postgres"
)

// NewSessions 创建保存对话的 session.Service，连接 dsn 指向的数据库。
//
// 它使用 trpc-agent-go 的 session/postgres，表名前缀为 agent_，不自动建表。
// opts 原样传入，例如 tpg.WithSessionTTL 让对话过期。它有自己的连接池，用完后调用 Close。
func NewSessions(dsn string, opts ...tpg.ServiceOpt) (session.Service, error) {
	base := []tpg.ServiceOpt{
		tpg.WithPostgresClientDSN(dsn),
		tpg.WithTablePrefix("agent"),
		tpg.WithSkipDBInit(true),
	}
	return tpg.NewService(append(base, opts...)...)
}
