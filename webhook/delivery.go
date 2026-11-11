package webhook

import (
	"context"
	"database/sql"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/errorx"
)

// ErrDeliveryNotFound 表示投递记录不存在或属于其他租户。
var ErrDeliveryNotFound = errorx.Define(errorx.NotFound, "webhook.delivery_not_found")

// DefaultLimit 是 ListDeliveries 在 DeliveryQuery.Limit 不大于 0 时返回的条数。
const DefaultLimit = 50

// DeliveryQuery 是 ListDeliveries 的条件。Tenant 总是生效，其余条件为空时不限制。
type DeliveryQuery struct {
	Tenant     string
	EndpointID uuid.UUID
	EventID    uuid.UUID
	Status     Status
	Before     uuid.UUID // 只返回 ID 小于它的记录：翻页时传入上一页最后一条的 ID，零值从最新的开始
	Limit      int       // 不大于 0 时为 DefaultLimit
}

// ListDeliveries 按时间从新到旧返回符合 q 的投递记录。
func ListDeliveries(ctx context.Context, db bun.IDB, q DeliveryQuery) ([]Delivery, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	sel := db.NewSelect().Model((*Delivery)(nil)).Where("tenant = ?", q.Tenant)
	if q.EndpointID != uuid.Nil {
		sel = sel.Where("endpoint_id = ?", q.EndpointID)
	}
	if q.EventID != uuid.Nil {
		sel = sel.Where("event_id = ?", q.EventID)
	}
	if q.Status != "" {
		sel = sel.Where("status = ?", q.Status)
	}
	if q.Before != uuid.Nil {
		sel = sel.Where("id < ?", q.Before)
	}
	list := []Delivery{}
	if err := sel.OrderExpr("id DESC").Limit(int64(limit)).Scan(ctx, &list); err != nil {
		return nil, errors.Wrap(err, "webhook: list deliveries")
	}
	return list, nil
}

// GetDelivery 返回租户 tenant 的投递记录，不存在或属于其他租户时返回 ErrDeliveryNotFound。
func GetDelivery(ctx context.Context, db bun.IDB, tenant string, id uuid.UUID) (*Delivery, error) {
	d := new(Delivery)
	err := db.NewSelect().Model(d).Where("tenant = ?", tenant).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDeliveryNotFound.New()
	}
	if err != nil {
		return nil, errors.Wrap(err, "webhook: get delivery")
	}
	return d, nil
}

// Resend 把租户 tenant 的一条投递记录重新发给它的端点：以相同的事件 ID 和请求体写入一条新的投递记录，
// 并通过 queue.Database(db) 发布投递任务，返回新的记录。原记录保持不变。
// 接收方按 webhook-id 去重时会把它当作同一个事件。
func Resend(ctx context.Context, db bun.IDB, tenant string, id uuid.UUID) (*Delivery, error) {
	old, err := GetDelivery(ctx, db, tenant, id)
	if err != nil {
		return nil, err
	}
	newID, err := uuid.NewV7()
	if err != nil {
		return nil, errors.Wrap(err, "webhook: new delivery id")
	}
	now := time.Now().UTC()
	d := Delivery{
		ID: newID, Tenant: old.Tenant, EndpointID: old.EndpointID, EventID: old.EventID, Event: old.Event,
		Payload: old.Payload, Status: StatusPending, CreatedAt: now, UpdatedAt: now,
	}
	if err := enqueue(ctx, db, []Delivery{d}); err != nil {
		return nil, err
	}
	return &d, nil
}

// Cleanup 返回删除 keep 以前创建的投递记录的清理函数，每次调用最多删除 limit 条，交给 schedule.Cleanup：
//
//	sched.Add("webhooks:cleanup", "0 4 * * *", schedule.Cleanup(webhook.Cleanup(db, 30*24*time.Hour), 1000))
func Cleanup(db bun.IDB, keep time.Duration) func(ctx context.Context, limit int) (int, error) {
	if db == nil {
		panic("webhook: nil database")
	}
	if keep <= 0 {
		panic("webhook: keep must be positive")
	}
	return func(ctx context.Context, limit int) (int, error) {
		if limit <= 0 {
			return 0, errors.New("webhook: cleanup limit must be positive")
		}
		old := db.NewSelect().Model((*Delivery)(nil)).Column("id").
			Where("created_at < ?", time.Now().Add(-keep).UTC()).OrderExpr("created_at").Limit(int64(limit))
		res, err := db.NewDelete().Model((*Delivery)(nil)).
			Where("id IN (SELECT id FROM (?) AS old)", old).Exec(ctx)
		if err != nil {
			return 0, errors.Wrap(err, "webhook: cleanup")
		}
		n, err := res.RowsAffected()
		return int(n), errors.Wrap(err, "webhook: cleanup")
	}
}
