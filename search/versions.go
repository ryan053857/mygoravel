package search

import (
	"context"
	"database/sql"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// 索引的版本记在 search_indexes 中。查询只读 version；重建时登记一个更大的 building，
// 同步同时写入两个版本，重建任务把每个 key 写入 building，完成后 building 成为 version，旧版本的数据随后删除。
// 版本号只增不减：新的重建总是取比 version 和 building 都大的号，所以小于两者中较大者、又不是 version 的版本
// 一定已经废弃（被取代的重建、切换之前的版本），可以随时删除。没有记录的索引版本为 1。

// state 是 search_indexes 中的一行。
type state struct {
	Version   int           `bun:"version"`
	Building  sql.NullInt64 `bun:"building"`
	Operation uuid.NullUUID `bun:"operation_id"`
	LastKey   *string       `bun:"last_key"`
}

// versions 返回同步要写入的版本：查询使用的版本，以及正在重建的版本（如果有）。
//
// 它在锁住 key 之后读取，不加锁：一个同步如果在重建登记之前读到没有 building，重建任务处理这个 key 时
// 要等它提交，之后重新读取数据，不会漏掉它的修改；登记之后开始的同步一定读到 building。
func (s store) versions(ctx context.Context, db bun.IDB, index string) ([]int, error) {
	st, err := s.state(ctx, db, index, false)
	if err != nil {
		return nil, err
	}
	if st.Building.Valid {
		return []int{st.Version, int(st.Building.Int64)}, nil
	}
	return []int{st.Version}, nil
}

// state 读取索引的状态，没有记录时返回版本 1。lock 为 true 时锁住这一行直到事务结束。
func (s store) state(ctx context.Context, db bun.IDB, index string, lock bool) (state, error) {
	q := `SELECT version, building, operation_id, last_key FROM search_indexes WHERE name = ?`
	if lock && s.dialect != dialect.SQLite {
		q += ` FOR UPDATE` // SQLite 的写事务已经独占数据库
	}
	var rows []state
	if err := db.NewRaw(q, index).Scan(ctx, &rows); err != nil {
		return state{}, errors.Wrap(err, "search: read index state")
	}
	if len(rows) == 0 {
		return state{Version: 1}, nil
	}
	return rows[0], nil
}

// startBuild 登记操作 op 重建的新版本，取代正在进行的重建，返回新版本号。
func (s store) startBuild(ctx context.Context, tx bun.IDB, index string, op uuid.UUID) (int, error) {
	// 先确保有这一行并锁住它：并发登记的重建依次取得版本号
	q := `INSERT INTO search_indexes (name, version) VALUES (?, 1) ON CONFLICT (name) DO UPDATE SET version = search_indexes.version`
	if s.dialect == dialect.MySQL {
		q = `INSERT INTO search_indexes (name, version) VALUES (?, 1) ON DUPLICATE KEY UPDATE version = version`
	}
	if _, err := tx.NewRaw(q, index).Exec(ctx); err != nil {
		return 0, errors.Wrap(err, "search: start reindex")
	}
	st, err := s.state(ctx, tx, index, true)
	if err != nil {
		return 0, err
	}
	building := max(st.Version, int(st.Building.Int64)) + 1
	_, err = tx.NewRaw(`UPDATE search_indexes SET building = ?, operation_id = ?, last_key = NULL WHERE name = ?`,
		building, op, index).Exec(ctx)
	return building, errors.Wrap(err, "search: start reindex")
}

// build 返回操作 op 重建的版本和处理到的 key。op 已被后发起的重建取代时返回 errSuperseded。
func (s store) build(ctx context.Context, db bun.IDB, index string, op uuid.UUID, lock bool) (state, error) {
	st, err := s.state(ctx, db, index, lock)
	if err != nil {
		return state{}, err
	}
	if !st.Building.Valid || st.Operation.UUID != op {
		return state{}, errSuperseded.New()
	}
	return st, nil
}

// saveProgress 记下操作 op 的重建处理到的 key，与操作的进度在同一个事务中提交。
func (s store) saveProgress(ctx context.Context, tx bun.IDB, index string, op uuid.UUID, lastKey string) error {
	if _, err := s.build(ctx, tx, index, op, true); err != nil {
		return err
	}
	_, err := tx.NewRaw(`UPDATE search_indexes SET last_key = ? WHERE name = ?`, lastKey, index).Exec(ctx)
	return errors.Wrap(err, "search: save reindex progress")
}

// finishBuild 把操作 op 重建的版本切换为查询使用的版本。
func (s store) finishBuild(ctx context.Context, tx bun.IDB, index string, op uuid.UUID) error {
	if _, err := s.build(ctx, tx, index, op, true); err != nil {
		return err
	}
	_, err := tx.NewRaw(`UPDATE search_indexes SET version = building, building = NULL, operation_id = NULL, last_key = NULL `+
		`WHERE name = ?`, index).Exec(ctx)
	return errors.Wrap(err, "search: switch version")
}

// countKeys 返回索引中有文档的 key 的个数，作为重建的预计单位数。
func (s store) countKeys(ctx context.Context, db bun.IDB, index string) (int, error) {
	var n int
	err := db.NewRaw(`SELECT COUNT(*) FROM search_keys WHERE index_name = ?`, index).Scan(ctx, &n)
	return n, errors.Wrap(err, "search: count keys")
}

// dropBatch 是 dropStale 一个事务删除的文档数。
const dropBatch = 1000

// dropStale 删除已经废弃的版本中的文档，见本文件开头的说明。
func (s store) dropStale(ctx context.Context, db bun.IDB, index string) error {
	st, err := s.state(ctx, db, index, false)
	if err != nil {
		return err
	}
	below := max(st.Version, int(st.Building.Int64))
	for {
		var ids []int64
		err := db.NewRaw(`SELECT id FROM search_entries WHERE index_name = ? AND version < ? AND version <> ? LIMIT ?`,
			index, below, st.Version, dropBatch).Scan(ctx, &ids)
		if err != nil {
			return errors.Wrap(err, "search: find stale entries")
		}
		if len(ids) == 0 {
			return nil
		}
		err = db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
			deletes := []string{`DELETE FROM search_values WHERE entry_id IN (?)`}
			if s.dialect == dialect.SQLite {
				deletes = append(deletes, `DELETE FROM search_fts WHERE rowid IN (?)`)
			}
			deletes = append(deletes, `DELETE FROM search_entries WHERE id IN (?)`)
			for _, q := range deletes {
				if _, err := tx.NewRaw(q, bun.List(ids)).Exec(ctx); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return errors.Wrap(err, "search: delete stale entries")
		}
	}
}
