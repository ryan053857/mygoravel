// Package postgres 把对话保存在 PostgreSQL 中，表由 chat.Migrations 创建。
package postgres

import (
	"context"
	"database/sql"
	"sync"

	"github.com/uptrace/bun"
	"trpc.group/trpc-go/trpc-agent-go/session"
	tpg "trpc.group/trpc-go/trpc-agent-go/session/postgres"
	storage "trpc.group/trpc-go/trpc-agent-go/storage/postgres"
)

// NewSessions 创建保存对话的 session.Service，使用应用的数据库连接池。
//
// 它使用 trpc-agent-go 的 session/postgres，表名前缀为 agent_，不自动建表。
// opts 原样传入，例如 tpg.WithSessionTTL 让对话过期。Close 不关闭 db。
func NewSessions(db *bun.DB, opts ...tpg.ServiceOpt) (session.Service, error) {
	install.Do(installBuilder)
	base := []tpg.ServiceOpt{
		tpg.WithExtraOptions(shared{db.DB}),
		tpg.WithTablePrefix("agent"),
		tpg.WithSkipDBInit(true),
	}
	return tpg.NewService(append(base, opts...)...)
}

// shared 通过 ExtraOptions 把应用的连接池交给 client builder。
type shared struct{ db *sql.DB }

var install sync.Once

// installBuilder 包装 trpc-agent-go 的全局 client builder：参数中带有 shared 时使用应用的连接池，
// 否则交给原来的 builder，不影响应用中其他直接使用 trpc-agent-go 的地方。
func installBuilder() {
	prev := storage.GetClientBuilder()
	storage.SetClientBuilder(func(ctx context.Context, opts ...storage.ClientBuilderOpt) (storage.Client, error) {
		var o storage.ClientBuilderOpts
		for _, opt := range opts {
			opt(&o)
		}
		for _, x := range o.ExtraOptions {
			if s, ok := x.(shared); ok {
				return client{s.db}, nil
			}
		}
		return prev(ctx, opts...)
	})
}

// client 在应用的连接池上实现 storage.Client，行为与 trpc-agent-go 的默认实现相同，只是 Close 不关闭连接池。
type client struct{ db *sql.DB }

func (c client) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return c.db.ExecContext(ctx, query, args...)
}

func (c client) Query(ctx context.Context, handler storage.HandlerFunc, query string, args ...any) error {
	rows, err := c.db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	if err := handler(rows); err != nil {
		return err
	}
	return rows.Err()
}

func (c client) Transaction(ctx context.Context, fn storage.TxFunc) (err error) {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (client) Close() error { return nil }
