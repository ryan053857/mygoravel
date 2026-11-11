// Package mysql 把对话保存在 MySQL 中，表由 chat.Migrations 创建。
package mysql

import (
	"context"
	"database/sql"
	"errors"
	"sync"

	"github.com/uptrace/bun"
	"trpc.group/trpc-go/trpc-agent-go/session"
	tmysql "trpc.group/trpc-go/trpc-agent-go/session/mysql"
	storage "trpc.group/trpc-go/trpc-agent-go/storage/mysql"
)

// NewSessions 创建保存对话的 session.Service，使用应用的数据库连接池。
//
// 它使用 trpc-agent-go 的 session/mysql，表名前缀为 agent_，不自动建表。
// opts 原样传入，例如 tmysql.WithSessionTTL 让对话过期。Close 不关闭 db。
func NewSessions(db *bun.DB, opts ...tmysql.ServiceOpt) (session.Service, error) {
	install.Do(installBuilder)
	base := []tmysql.ServiceOpt{
		tmysql.WithExtraOptions(shared{db.DB}),
		tmysql.WithTablePrefix("agent"),
		tmysql.WithSkipDBInit(true),
	}
	return tmysql.NewService(append(base, opts...)...)
}

type shared struct{ db *sql.DB }

var install sync.Once

// installBuilder 包装 trpc-agent-go 的全局 client builder，见 ai/chat/postgres 中的同名函数。
func installBuilder() {
	prev := storage.GetClientBuilder()
	storage.SetClientBuilder(func(opts ...storage.ClientBuilderOpt) (storage.Client, error) {
		var o storage.ClientBuilderOpts
		for _, opt := range opts {
			opt(&o)
		}
		for _, x := range o.ExtraOptions {
			if s, ok := x.(shared); ok {
				return client(s), nil
			}
		}
		return prev(opts...)
	})
}

// client 在应用的连接池上实现 storage.Client，行为与 trpc-agent-go 的默认实现相同，只是 Close 不关闭连接池。
type client struct{ db *sql.DB }

func (c client) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return c.db.ExecContext(ctx, query, args...)
}

func (c client) Query(ctx context.Context, next storage.NextFunc, query string, args ...any) error {
	rows, err := c.db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := next(rows); err != nil {
			if errors.Is(err, storage.ErrBreak) {
				break
			}
			return err
		}
	}
	return rows.Err()
}

func (c client) QueryRow(ctx context.Context, dest []any, query string, args ...any) error {
	return c.db.QueryRowContext(ctx, query, args...).Scan(dest...)
}

func (c client) Transaction(ctx context.Context, fn storage.TxFunc, opts ...storage.TxOption) error {
	var txOpts sql.TxOptions
	for _, opt := range opts {
		opt(&txOpts)
	}
	tx, err := c.db.BeginTx(ctx, &txOpts)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (client) Close() error { return nil }
