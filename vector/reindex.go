package vector

import (
	"context"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/operation"
	"github.com/ryan053857/mygoravel/queue"
)

// errSuperseded 表示重建已被之后发起的重建取代，操作变为 failed，Error 是 vector.reindex_superseded。
var errSuperseded = errorx.Define(errorx.Conflict, "vector.reindex_superseded")

// ReindexBatch 是重建时 ScanFunc 一次返回的 key 数，每处理完一批保存一次进度。
const ReindexBatch = 500

// ScanFunc 按应用自己的稳定顺序（例如主键）返回 after 之后的最多 limit 个 key；after 为空时从头开始，
// 返回空时结束。重建用它列出所有可能有文档的 key，每个 key 再用 LoadFunc 读取，读不到文档的 key 不会写入。
type ScanFunc func(ctx context.Context, db bun.IDB, after string, limit int) ([]string, error)

// Reindex 登记 owner 发起的重建，在 tx 中发布重建任务，返回 pending 状态的操作，操作的类型是 vector.reindex.<name>。
// 换了模型、修改了计算向量的文本或者文档类型的字段之后用它重建索引，
// 也可以用来修复绕过 Sync 修改数据造成的不一致。
//
// 重建用 Store 的模型（New 的 model）建立索引的一个新版本，查询在完成之前仍然使用原来的版本：
//
//   - 事务提交后，同步同时写入原来的版本和新版本，各用建立它的模型计算向量，重建期间的修改不会丢失；
//   - 重建任务用 ScanFunc 列出所有的 key，逐个读取并写入新版本，进度记在操作中，任务重试时从上次保存的地方继续；
//   - 全部写入后新版本成为查询使用的版本，原来版本的 collection 随后删除。
//
// 新版本中文本相同的文档不能沿用原来版本的向量，每个文档都要重新计算一次。
// 正在进行的重建被新的重建取代，它的操作变为 failed，Error 是 vector.reindex_superseded。
// 文档无法建索引的 key 计为失败的单位，不影响其他 key。
func (ix *Index[D]) Reindex(ctx context.Context, tx bun.IDB, ops *operation.Service, owner identity.Subject) (*operation.Operation, error) {
	op, err := ops.Start(ctx, tx, owner, "vector.reindex."+ix.name)
	if err != nil {
		return nil, err
	}
	if _, err := startBuild(ctx, tx, ix.name, op.ID); err != nil {
		return nil, err
	}
	if err := ix.reindex.Publish(ctx, tx, op.ID); err != nil {
		return nil, errors.Wrapf(err, "vector: publish %s reindex", ix.name)
	}
	return op, nil
}

// rebuild 执行操作 id 的重建。
func (ix *Index[D]) rebuild(ctx context.Context, s *Store, ops *operation.Service, load LoadFunc[D], scan ScanFunc, id uuid.UUID) error {
	// 先清理之前被取代或者切换后没有删完的版本
	if err := s.dropStale(ctx, ix.name); err != nil {
		return err
	}
	err := ops.Run(ctx, id, func(ctx context.Context, r *operation.Run) error {
		b, err := currentBuild(ctx, s.db, ix.name, id, false)
		if err != nil {
			return err
		}
		version, after := int(b.Building.Int64), ""
		if b.LastKey != nil {
			after = *b.LastKey
		}
		// 先用这个 worker 的模型建立新版本：之后的同步写入新版本时沿用它
		if _, _, err := s.open(ctx, s.collection(ix.name, version), ix.schema, true); err != nil {
			return err
		}
		op := r.Operation()
		total, processed := op.Total, op.Processed
		if after == "" {
			if total, err = countKeys(ctx, s.db, ix.name); err != nil {
				return err
			}
			r.SetTotal(total)
		}
		for {
			keys, err := scan(ctx, s.db, after, ReindexBatch)
			if err != nil {
				return errors.Wrapf(err, "vector: scan %s", ix.name)
			}
			if len(keys) == 0 {
				break
			}
			if keys[len(keys)-1] == after {
				return queue.Permanent(errors.Newf("vector: scan of %s returned %q again after it", ix.name, after))
			}
			failed := 0
			for _, key := range keys {
				if err := checkKey(key); err != nil {
					return queue.Permanent(err)
				}
				err := ix.refresh(ctx, s, load, key, version)
				if errors.Is(err, errInvalid) {
					failed++
				} else if err != nil {
					return err
				}
			}
			after = keys[len(keys)-1]
			processed += len(keys)
			r.Add(len(keys), failed)
			if processed > total {
				total = processed // 预计的个数来自原来的索引，可能偏少
				r.SetTotal(total)
			}
			err = r.Save(ctx, func(ctx context.Context, tx database.Tx) error {
				return saveProgress(ctx, tx, ix.name, id, after)
			})
			if err != nil {
				return err
			}
		}
		return r.Complete(ctx, func(ctx context.Context, tx database.Tx) error {
			return finishBuild(ctx, tx, ix.name, id)
		})
	}, nil)
	if err != nil {
		return err
	}
	// 切换之后删除原来的版本；失败时任务重试，Run 不再执行已经结束的操作，直接回到这里
	return s.dropStale(ctx, ix.name)
}
