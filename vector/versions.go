package vector

import (
	"context"
	"database/sql"
	"strconv"
	"strings"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// 索引的版本记在 vector_indexes 中，每个版本是一个 collection <prefix><name>_v<版本>。查询只读 version；
// 重建时登记一个更大的 building，同步同时写入两个版本，重建任务把每个 key 写入 building，
// 完成后 building 成为 version，旧版本的 collection 随后删除。
// 版本号只增不减：新的重建总是取比 version 和 building 都大的号，所以小于两者中较大者、又不是 version 的版本
// 一定已经废弃（被取代的重建、切换之前的版本），可以随时删除。没有记录的索引版本为 1。

// state 是 vector_indexes 中的一行。
type state struct {
	Version   int           `bun:"version"`
	Building  sql.NullInt64 `bun:"building"`
	Operation uuid.NullUUID `bun:"operation_id"`
	LastKey   *string       `bun:"last_key"`
}

// indexVersions 返回同步要写入的版本：查询使用的版本，以及正在重建的版本（如果有）。
//
// 它在锁住 key 之后读取，不加锁：一个同步如果在重建登记之前读到没有 building，重建任务处理这个 key 时
// 要等它提交，之后重新读取数据，不会漏掉它的修改；登记之后开始的同步一定读到 building。
func indexVersions(ctx context.Context, db bun.IDB, index string) ([]int, error) {
	st, err := readState(ctx, db, index, false)
	if err != nil {
		return nil, err
	}
	if st.Building.Valid {
		return []int{st.Version, int(st.Building.Int64)}, nil
	}
	return []int{st.Version}, nil
}

// readState 读取索引的状态，没有记录时返回版本 1。lock 为 true 时锁住这一行直到事务结束。
func readState(ctx context.Context, db bun.IDB, index string, lock bool) (state, error) {
	q := `SELECT version, building, operation_id, last_key FROM vector_indexes WHERE name = ?`
	if lock {
		q += ` FOR UPDATE`
	}
	var rows []state
	if err := db.NewRaw(q, index).Scan(ctx, &rows); err != nil {
		return state{}, errors.Wrap(err, "vector: read index state")
	}
	if len(rows) == 0 {
		return state{Version: 1}, nil
	}
	return rows[0], nil
}

// startBuild 登记操作 op 重建的新版本，取代正在进行的重建，返回新版本号。
func startBuild(ctx context.Context, tx bun.IDB, index string, op uuid.UUID) (int, error) {
	// 先确保有这一行并锁住它：并发登记的重建依次取得版本号
	if _, err := tx.NewRaw(`INSERT INTO vector_indexes (name, version) VALUES (?, 1) `+
		`ON CONFLICT (name) DO UPDATE SET version = vector_indexes.version`, index).Exec(ctx); err != nil {
		return 0, errors.Wrap(err, "vector: start reindex")
	}
	st, err := readState(ctx, tx, index, true)
	if err != nil {
		return 0, err
	}
	building := max(st.Version, int(st.Building.Int64)) + 1
	_, err = tx.NewRaw(`UPDATE vector_indexes SET building = ?, operation_id = ?, last_key = NULL WHERE name = ?`,
		building, op, index).Exec(ctx)
	return building, errors.Wrap(err, "vector: start reindex")
}

// currentBuild 返回操作 op 重建的版本和处理到的 key。op 已被后发起的重建取代时返回 errSuperseded。
func currentBuild(ctx context.Context, db bun.IDB, index string, op uuid.UUID, lock bool) (state, error) {
	st, err := readState(ctx, db, index, lock)
	if err != nil {
		return state{}, err
	}
	if !st.Building.Valid || st.Operation.UUID != op {
		return state{}, errSuperseded.New()
	}
	return st, nil
}

// saveProgress 记下操作 op 的重建处理到的 key，与操作的进度在同一个事务中提交。
func saveProgress(ctx context.Context, tx bun.IDB, index string, op uuid.UUID, lastKey string) error {
	if _, err := currentBuild(ctx, tx, index, op, true); err != nil {
		return err
	}
	_, err := tx.NewRaw(`UPDATE vector_indexes SET last_key = ? WHERE name = ?`, lastKey, index).Exec(ctx)
	return errors.Wrap(err, "vector: save reindex progress")
}

// finishBuild 把操作 op 重建的版本切换为查询使用的版本。
func finishBuild(ctx context.Context, tx bun.IDB, index string, op uuid.UUID) error {
	if _, err := currentBuild(ctx, tx, index, op, true); err != nil {
		return err
	}
	_, err := tx.NewRaw(`UPDATE vector_indexes SET version = building, building = NULL, operation_id = NULL, last_key = NULL `+
		`WHERE name = ?`, index).Exec(ctx)
	return errors.Wrap(err, "vector: switch version")
}

// countKeys 返回索引中有文档的 key 的个数，作为重建的预计单位数。
func countKeys(ctx context.Context, db bun.IDB, index string) (int, error) {
	var n int
	err := db.NewRaw(`SELECT COUNT(*) FROM vector_keys WHERE index_name = ?`, index).Scan(ctx, &n)
	return n, errors.Wrap(err, "vector: count keys")
}

// dropStale 删除已经废弃的版本的 collection，见本文件开头的说明。
func (s *Store) dropStale(ctx context.Context, index string) error {
	st, err := readState(ctx, s.db, index, false)
	if err != nil {
		return err
	}
	below := max(st.Version, int(st.Building.Int64))
	names, err := s.client.ListCollections(ctx)
	if err != nil {
		return errors.Wrap(err, "vector: list collections")
	}
	prefix := s.prefix + index + "_v"
	for _, name := range names {
		rest, ok := strings.CutPrefix(name, prefix)
		if !ok {
			continue
		}
		v, err := strconv.Atoi(rest)
		if err != nil || strconv.Itoa(v) != rest || v >= below || v == st.Version {
			continue
		}
		if err := s.client.DeleteCollection(ctx, name); err != nil {
			return errors.Wrapf(err, "vector: delete collection %s", name)
		}
		s.mu.Lock()
		delete(s.ready, name)
		s.mu.Unlock()
	}
	return nil
}
