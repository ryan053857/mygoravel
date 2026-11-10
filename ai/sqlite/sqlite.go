// Package sqlite 把对话保存在 SQLite 中，表由 ai.Migrations 创建。
package sqlite

import (
	"context"

	"github.com/cockroachdb/errors"
	"github.com/uptrace/bun"
	"trpc.group/trpc-go/trpc-agent-go/session"
	tsqlite "trpc.group/trpc-go/trpc-agent-go/session/sqlite"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/database/sqlite"
)

// NewSessions 创建保存对话的 session.Service，对话保存在 db 所在的数据库文件中。
//
// 它使用 trpc-agent-go 的 session/sqlite，表名前缀为 agent_，不自动建表。trpc-agent-go 关闭服务时
// 会关闭交给它的连接，所以这里用框架的 SQLite 驱动对同一个文件另开一组连接，由服务的 Close 关闭；
// 写事务由 SQLite 的文件锁协调，与 db 上的写入互不影响。不支持内存数据库。
func NewSessions(db *bun.DB, opts ...tsqlite.ServiceOpt) (session.Service, error) {
	ctx := context.Background()
	var seq int
	var name, file string
	if err := db.QueryRowContext(ctx, "PRAGMA database_list").Scan(&seq, &name, &file); err != nil {
		return nil, errors.Wrap(err, "ai/sqlite: locate database file")
	}
	if file == "" {
		return nil, errors.New("ai/sqlite: in-memory databases are not supported")
	}
	own, err := database.Open(ctx, sqlite.Driver, database.Config{DSN: file})
	if err != nil {
		return nil, err
	}
	base := []tsqlite.ServiceOpt{
		tsqlite.WithTablePrefix("agent"),
		tsqlite.WithSkipDBInit(true),
	}
	s, err := tsqlite.NewService(own.DB, append(base, opts...)...)
	if err != nil {
		_ = own.Close()
		return nil, err
	}
	return s, nil
}
