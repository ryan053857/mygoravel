package queue

import (
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/hex"

	"github.com/cockroachdb/errors"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/uptrace/bun"
)

const (
	// MessagePrefix 是任务在 WAL 中的逻辑解码消息（pg_logical_emit_message）的前缀，后面接 topic 名，
	// 内容是发给 NSQ 的消息。
	MessagePrefix = "mygoravel.queue:"
	// Publication 是逻辑复制使用的 publication。它不包含任何表，只因为 pgoutput 插件要求指定一个。
	Publication = "mygoravel_queue"
)

// SlotName 返回数据库 database 中转发任务的逻辑复制槽的名字：mygoravel_queue_ 加上数据库名的 MD5 的前 16 位。
// 迁移用同样的规则创建它，监控积压时按这个名字查询 pg_replication_slots。
func SlotName(database string) string {
	sum := md5.Sum([]byte(database))
	return "mygoravel_queue_" + hex.EncodeToString(sum[:])[:16]
}

// slotOf 返回 db 所在数据库的复制槽名字。
func slotOf(ctx context.Context, db bun.IDB) (string, error) {
	var name string
	if err := db.NewRaw("SELECT current_database()").Scan(ctx, &name); err != nil {
		return "", errors.Wrap(err, "queue: current database")
	}
	return SlotName(name), nil
}

// checkSlot 确认复制槽存在并且仍然可用。槽不存在说明没有执行迁移，或者被人删除；
// wal_status 为 lost 说明积压超过了 max_slot_wal_keep_size，槽已失效，其中的任务已经丢失。
func checkSlot(ctx context.Context, db bun.IDB, slot string) error {
	var status sql.NullString
	err := db.NewRaw("SELECT wal_status FROM pg_replication_slots WHERE slot_name = ?", slot).Scan(ctx, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.Newf("queue: replication slot %s does not exist: run the queue migrations", slot)
	}
	if err != nil {
		return errors.Wrap(err, "queue: check replication slot")
	}
	if status.String == "lost" {
		return errors.Newf("queue: replication slot %s is lost (exceeded max_slot_wal_keep_size): "+
			"jobs published since are gone; see docs/deployment.md to recreate it", slot)
	}
	return nil
}

// replicationConfig 返回复制连接的配置：设置了 dsn 时用它，否则复制 db 的连接配置。
func replicationConfig(ctx context.Context, db *bun.DB, dsn string) (*pgconn.Config, error) {
	var cfg *pgconn.Config
	if dsn != "" {
		c, err := pgconn.ParseConfig(dsn)
		if err != nil {
			return nil, errors.Wrap(err, "queue: parse replication_dsn")
		}
		cfg = c
	} else {
		conn, err := db.Conn(ctx)
		if err != nil {
			return nil, errors.Wrap(err, "queue: database connection")
		}
		defer func() { _ = conn.Close() }()
		err = conn.Raw(func(driverConn any) error {
			c, ok := driverConn.(*stdlib.Conn)
			if !ok {
				return errors.Newf("queue: unexpected driver connection %T", driverConn)
			}
			cfg = c.Conn().Config().Config.Copy()
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	cfg.RuntimeParams["replication"] = "database"
	return cfg, nil
}
