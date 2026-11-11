package activity

import (
	"context"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/logging"
)

// maxKey 是字符串列的最大长度，与 MySQL 迁移中 VARCHAR(191) 的列宽一致。
const maxKey = 191

// DefaultLimit 是 List 在 Query.Limit 不大于 0 时返回的条数。
const DefaultLimit = 50

// Activity 是一条操作记录，也是 activity_log 表的 Bun 模型，List 不能满足的查询可以直接用 Bun 编写。
type Activity struct {
	bun.BaseModel `bun:"table:activity_log,alias:al"`

	ID             uuid.UUID            `bun:"id,pk,type:uuid"` // UUIDv7，按时间递增
	Tenant         string               `bun:"tenant,notnull"`  // 操作所在的租户，"" 为平台本身
	ActorType      identity.SubjectType `bun:"actor_type,notnull"`
	ActorID        string               `bun:"actor_id,notnull"`        // 操作人，可以为空
	ImpersonatorID string               `bun:"impersonator_id,notnull"` // 代为登录时实际操作的人，见 identity.Subject.Impersonator；可以为空
	Event          string               `bun:"event,notnull"`           // 发生了什么，例如 article.published
	TargetType     string               `bun:"target_type,notnull"`
	TargetID       string               `bun:"target_id,notnull"`  // 被操作的对象，例如 article 和文章 ID；可以为空
	Properties     map[string]any       `bun:"properties,notnull"` // 展示这条记录需要的其他数据，保存为 JSON
	RequestID      string               `bun:"request_id,notnull"` // 产生这条记录的请求，队列任务中是发布任务的请求
	CreatedAt      time.Time            `bun:"created_at,notnull"`
}

// Record 在 db 中写入一条操作记录。db 通常是业务修改所在的事务，记录与修改一起提交或回滚。
//
// a 只需要填写 Event、TargetType、TargetID 和 Properties，其余字段由 Record 填写：
// 操作人和租户来自 actor（它的 ID、Type、Impersonator 和 Tenant），队列和定时任务传入代表系统的主体；
// RequestID 来自 ctx 中的 request_id 日志字段，HTTP 请求和由它发布的队列任务都带有这个字段。
func Record(ctx context.Context, db bun.IDB, actor identity.Subject, a Activity) error {
	id, err := uuid.NewV7()
	if err != nil {
		return errors.Wrap(err, "activity: new id")
	}
	a.ID, a.CreatedAt = id, time.Now().UTC()
	a.Tenant, a.ActorType, a.ActorID, a.ImpersonatorID = actor.Tenant, actor.Type, actor.ID, actor.Impersonator
	a.RequestID = requestID(ctx)
	if a.Properties == nil {
		a.Properties = map[string]any{}
	}
	if err := check(a); err != nil {
		return err
	}
	_, err = db.NewInsert().Model(&a).Exec(ctx)
	return errors.Wrap(err, "activity: record")
}

func check(a Activity) error {
	if a.Event == "" {
		return errors.New("activity: empty event")
	}
	if a.TargetID != "" && a.TargetType == "" {
		return errors.Newf("activity: target id %q without target type", a.TargetID)
	}
	for field, v := range map[string]string{
		"tenant": a.Tenant, "actor type": string(a.ActorType), "actor id": a.ActorID,
		"impersonator id": a.ImpersonatorID, "event": a.Event,
		"target type": a.TargetType, "target id": a.TargetID, "request id": a.RequestID,
	} {
		if len(v) > maxKey {
			return errors.Newf("activity: %s longer than %d bytes", field, maxKey)
		}
	}
	return nil
}

func requestID(ctx context.Context) string {
	for _, attr := range logging.ContextAttrs(ctx) {
		if attr.Key == logging.AttrRequestID {
			return attr.Value.String()
		}
	}
	return ""
}

// Query 是 List 的条件。Tenant 总是生效，其余条件为空时不限制。
type Query struct {
	Tenant     string // 只查询这个租户中的记录，"" 为平台本身
	TargetType string
	TargetID   string // 需要同时设置 TargetType
	ActorID    string
	Event      string
	Before     uuid.UUID // 只返回 ID 小于它的记录：翻页时传入上一页最后一条的 ID，零值从最新的开始
	Limit      int       // 不大于 0 时为 DefaultLimit
}

// List 按时间从新到旧返回符合 q 的记录。
func List(ctx context.Context, db bun.IDB, q Query) ([]Activity, error) {
	if q.TargetID != "" && q.TargetType == "" {
		return nil, errors.New("activity: query target id without target type")
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	sel := db.NewSelect().Model((*Activity)(nil)).Where("tenant = ?", q.Tenant)
	for _, f := range [...]struct{ column, value string }{
		{"target_type", q.TargetType}, {"target_id", q.TargetID}, {"actor_id", q.ActorID}, {"event", q.Event},
	} {
		if f.value != "" {
			sel = sel.Where("? = ?", bun.Ident(f.column), f.value)
		}
	}
	if q.Before != uuid.Nil {
		sel = sel.Where("id < ?", q.Before)
	}
	activities := []Activity{}
	if err := sel.OrderExpr("id DESC").Limit(int64(limit)).Scan(ctx, &activities); err != nil {
		return nil, errors.Wrap(err, "activity: list")
	}
	return activities, nil
}

// Cleanup 返回删除 keep 以前的记录的清理函数，每次调用最多删除 limit 条，交给 schedule.Cleanup：
//
//	sched.Add("activity:cleanup", "0 4 * * *", schedule.Cleanup(activity.Cleanup(db, 180*24*time.Hour), 1000))
func Cleanup(db bun.IDB, keep time.Duration) func(ctx context.Context, limit int) (int, error) {
	if db == nil {
		panic("activity: nil database")
	}
	if keep <= 0 {
		panic("activity: keep must be positive")
	}
	return func(ctx context.Context, limit int) (int, error) {
		if limit <= 0 {
			return 0, errors.New("activity: cleanup limit must be positive")
		}
		old := db.NewSelect().Model((*Activity)(nil)).Column("id").
			Where("created_at < ?", time.Now().Add(-keep).UTC()).OrderExpr("created_at").Limit(int64(limit))
		res, err := db.NewDelete().Model((*Activity)(nil)).
			Where("id IN (SELECT id FROM (?) AS old)", old).Exec(ctx)
		if err != nil {
			return 0, errors.Wrap(err, "activity: cleanup")
		}
		n, err := res.RowsAffected()
		return int(n), errors.Wrap(err, "activity: cleanup")
	}
}
