// Package operation 记录用户在后台发起的长操作，例如导入、导出和批量处理：状态、进度和失败的原因。
//
// 操作在发起请求的事务中用 Start 登记，同一个事务发布队列任务；任务用 Run 执行，期间报告进度：
//
//	op, err := ops.Start(ctx, tx, subject, "articles.reindex")
//	err = ReindexJob.Publish(ctx, queue.Database(tx), op.ID)
//
//	// 队列任务
//	err := ops.Run(ctx, id, func(ctx context.Context, r *operation.Run) error {
//		r.SetTotal(len(ids))
//		for _, id := range ids {
//			// 处理一个单位……
//			r.Add(1, 0)
//		}
//		return nil
//	}, saved)
//
// 用户用 Get 查看一个操作的进度，用 List 列出自己的操作；Cleanup 删除旧的操作。
// imports 和 exports 包建立在它之上，导入和导出也是操作，出现在同一个列表中。
package operation

import (
	"context"
	"database/sql"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/i18n"
	"github.com/ryan053857/mygoravel/identity"
)

// ErrNotFound 表示操作不存在，或者不是这个主体发起的。
var ErrNotFound = errorx.Define(errorx.NotFound, "operation.not_found")

// Status 是操作的状态。
type Status string

const (
	StatusPending   Status = "pending"   // 已登记，等待任务执行
	StatusRunning   Status = "running"   // 正在执行
	StatusCompleted Status = "completed" // 每个单位都处理过了，其中可能有失败的单位
	StatusFailed    Status = "failed"    // 整个操作无法完成，或者任务重试用完；原因见 Error
)

// Operation 是一个后台操作，也是 operations 表的 Bun 模型。
//
// 进度以单位计算，单位是什么由操作决定：导入和导出是行，批量处理是一个任务。
type Operation struct {
	bun.BaseModel `bun:"table:operations,alias:op"`

	ID        uuid.UUID            `bun:"id,pk,type:uuid"    json:"id"` // UUIDv7，按时间递增
	Tenant    string               `bun:"tenant,notnull"     json:"-"`
	OwnerType identity.SubjectType `bun:"owner_type,notnull" json:"-"`
	OwnerID   string               `bun:"owner_id,notnull"   json:"-"`
	Kind      string               `bun:"kind,notnull"       json:"kind"` // 操作的类型，由应用定义，例如 articles.import
	Status    Status               `bun:"status,notnull"     json:"status"`
	Total     int                  `bun:"total,notnull"      json:"total"`     // 预计的单位数，0 表示还不知道
	Processed int                  `bun:"processed,notnull"  json:"processed"` // 已经处理的单位数，包括失败的单位
	Failed    int                  `bun:"failed,notnull"     json:"failed"`    // 失败的单位数
	Version   int                  `bun:"version,notnull"    json:"-"`         // 每次保存加一
	// Error 是 failed 时的错误码，ErrorData 是翻译它的模板数据，见 ErrorMessage。
	Error       string         `bun:"error,notnull"      json:"error,omitempty"`
	ErrorData   map[string]any `bun:"error_data,notnull" json:"-"`
	CreatedAt   time.Time      `bun:"created_at,notnull" json:"created_at"`
	UpdatedAt   time.Time      `bun:"updated_at,notnull" json:"updated_at"`
	CompletedAt *time.Time     `bun:"completed_at"       json:"completed_at,omitempty"` // 变为 completed 或 failed 的时间
}

// Owner 返回发起操作的主体。它只有 ID、Type 和 Tenant，没有权限；需要权限时由应用重新加载。
func (op *Operation) Owner() identity.Subject {
	return identity.Subject{ID: op.OwnerID, Type: op.OwnerType, Tenant: op.Tenant}
}

// Finished 报告操作是否已经结束（completed 或 failed）。
func (op *Operation) Finished() bool {
	return op.Status == StatusCompleted || op.Status == StatusFailed
}

// ErrorMessage 按 ctx 中的语言翻译 Error；没有错误时返回空字符串。
func (op *Operation) ErrorMessage(ctx context.Context) string {
	if op.Error == "" {
		return ""
	}
	return i18n.T(ctx, op.Error, op.ErrorData)
}

// Service 登记、执行和查询操作。
type Service struct {
	db *bun.DB
}

// New 返回操作服务。
func New(db *bun.DB) *Service {
	if db == nil {
		panic("operation: nil database")
	}
	return &Service{db: db}
}

// Start 登记 owner 发起的一个 kind 操作，返回 pending 状态的操作。
// tx 通常是发布任务的事务，在同一个事务中用 queue.Database(tx) 发布任务，事务回滚时两者都不会留下。
func (s *Service) Start(ctx context.Context, tx bun.IDB, owner identity.Subject, kind string) (*Operation, error) {
	if kind == "" {
		return nil, errors.New("operation: empty kind")
	}
	now := time.Now().UTC()
	op := &Operation{
		ID: uuid.Must(uuid.NewV7()), Tenant: owner.Tenant, OwnerType: owner.Type, OwnerID: owner.ID,
		Kind: kind, Status: StatusPending, ErrorData: map[string]any{}, CreatedAt: now, UpdatedAt: now,
	}
	_, err := tx.NewInsert().Model(op).Exec(ctx)
	return op, errors.Wrap(err, "operation: start")
}

// Get 返回 owner 发起的操作；不存在或者不是 owner 发起的时返回 ErrNotFound。
func (s *Service) Get(ctx context.Context, owner identity.Subject, id uuid.UUID) (*Operation, error) {
	op := new(Operation)
	err := owned(s.db.NewSelect().Model(op), owner).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound.New()
	}
	return op, errors.Wrap(err, "operation: get")
}

// owned 把查询限定为 owner 发起的操作。
func owned(q *bun.SelectQuery, owner identity.Subject) *bun.SelectQuery {
	return q.Where("tenant = ?", owner.Tenant).Where("owner_type = ?", owner.Type).Where("owner_id = ?", owner.ID)
}

// DefaultLimit 是 List 在 Query.Limit 不大于 0 时返回的条数。
const DefaultLimit = 50

// Query 是 List 的条件。
type Query struct {
	Kind   string    // 只返回这一类操作，空字符串不限制
	Before uuid.UUID // 只返回 ID 小于它的操作：翻页时传入上一页最后一个的 ID，零值从最新的开始
	Limit  int       // 不大于 0 时为 DefaultLimit
}

// List 按时间从新到旧返回 owner 发起的、符合 q 的操作，供“任务中心”这样的页面使用。
func (s *Service) List(ctx context.Context, owner identity.Subject, q Query) ([]Operation, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	sel := owned(s.db.NewSelect().Model((*Operation)(nil)), owner)
	if q.Kind != "" {
		sel = sel.Where("kind = ?", q.Kind)
	}
	if q.Before != uuid.Nil {
		sel = sel.Where("id < ?", q.Before)
	}
	list := []Operation{}
	err := sel.OrderExpr("id DESC").Limit(int64(limit)).Scan(ctx, &list)
	return list, errors.Wrap(err, "operation: list")
}

// Cleanup 返回删除创建时间早于 keep 之前的操作的函数，每次最多删除 limit 个，返回删除的个数。
// 引用操作的表（例如导入的失败行）通过外键级联删除。可以直接交给 schedule.Cleanup。
func (s *Service) Cleanup(keep time.Duration) func(ctx context.Context, limit int) (int, error) {
	if keep <= 0 {
		panic("operation: keep must be positive")
	}
	return func(ctx context.Context, limit int) (int, error) {
		if limit <= 0 {
			return 0, errors.New("operation: cleanup limit must be positive")
		}
		old := s.db.NewSelect().Model((*Operation)(nil)).Column("id").
			Where("created_at < ?", time.Now().Add(-keep).UTC()).OrderExpr("created_at").Limit(int64(limit))
		res, err := s.db.NewDelete().Model((*Operation)(nil)).Where("id IN (?)", old).Exec(ctx)
		if err != nil {
			return 0, errors.Wrap(err, "operation: cleanup")
		}
		n, err := res.RowsAffected()
		return int(n), errors.Wrap(err, "operation: cleanup")
	}
}
