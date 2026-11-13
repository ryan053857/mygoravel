package notification

import (
	"context"
	"database/sql"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/go-tenon/tenon/errorx"
	"github.com/go-tenon/tenon/identity"
)

// DefaultLimit 是 List 在 Query.Limit 不大于 0 时返回的条数。
const DefaultLimit = 50

// ErrNotFound 表示通知不存在或不属于该收件人。
var ErrNotFound = errorx.Define(errorx.NotFound, "notification.not_found")

// Recipient 是通知的收件人：某个租户中的一个主体。
type Recipient struct {
	Tenant string // 收件人所在的租户，"" 为平台本身
	identity.Ref
}

// RecipientOf 返回主体在它当前租户中的收件人，用于读取当前登录用户的通知。
func RecipientOf(s identity.Subject) Recipient {
	return Recipient{Tenant: s.Tenant, Ref: s.Ref()}
}

// Notification 是发给一个收件人的一条通知，也是 notifications 表的 Bun 模型，
// 本包的函数不能满足的查询可以直接用 Bun 编写。
type Notification struct {
	bun.BaseModel `bun:"table:notifications,alias:n"`

	ID            uuid.UUID            `bun:"id,pk,type:uuid"` // UUIDv7，按时间递增
	Tenant        string               `bun:"tenant,notnull"`
	RecipientType identity.SubjectType `bun:"recipient_type,notnull"`
	RecipientID   string               `bun:"recipient_id,notnull"`
	Event         string               `bun:"event,notnull"` // 通知的是什么，例如 article.published
	Data          map[string]any       `bun:"data,notnull"`  // 展示这条通知需要的数据，保存为 JSON
	ReadAt        time.Time            `bun:"read_at,nullzero"`
	CreatedAt     time.Time            `bun:"created_at,notnull"`
}

// Recipient 返回这条通知的收件人。
func (n Notification) Recipient() Recipient {
	return Recipient{Tenant: n.Tenant, Type: n.RecipientType, ID: n.RecipientID}
}

// Read 报告这条通知是否已读。
func (n Notification) Read() bool { return !n.ReadAt.IsZero() }

// Send 在 db 中为每个收件人写入一条通知，返回写入的通知。db 通常是业务修改所在的事务，
// 通知与修改一起提交或回滚。
//
// n 只需要填写 Event 和 Data，其余字段由 Send 填写。重复的收件人只写入一条，没有收件人时什么也不做。
func Send(ctx context.Context, db bun.IDB, n Notification, to ...Recipient) ([]Notification, error) {
	if n.Event == "" {
		return nil, errors.New("notification: empty event")
	}
	if n.Data == nil {
		n.Data = map[string]any{}
	}
	now := time.Now().UTC()
	seen := make(map[Recipient]bool, len(to))
	rows := make([]Notification, 0, len(to))
	for _, r := range to {
		if seen[r] {
			continue
		}
		seen[r] = true
		if err := check(r); err != nil {
			return nil, err
		}
		id, err := uuid.NewV7()
		if err != nil {
			return nil, errors.Wrap(err, "notification: new id")
		}
		row := n
		row.ID, row.CreatedAt, row.ReadAt = id, now, time.Time{}
		row.Tenant, row.RecipientType, row.RecipientID = r.Tenant, r.Type, r.ID
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	if _, err := db.NewInsert().Model(&rows).Exec(ctx); err != nil {
		return nil, errors.Wrap(err, "notification: send")
	}
	return rows, nil
}

func check(r Recipient) error {
	return errors.Wrap(r.Validate(), "notification: recipient")
}

// of 把查询限定在 r 的通知上。
func of[Q interface{ Where(string, ...any) Q }](q Q, r Recipient) Q {
	return q.Where("tenant = ?", r.Tenant).Where("recipient_type = ?", r.Type).Where("recipient_id = ?", r.ID)
}

// Query 是 List 的条件。
type Query struct {
	Recipient Recipient
	Unread    bool      // 只返回未读的通知
	Before    uuid.UUID // 只返回 ID 小于它的通知：翻页时传入上一页最后一条的 ID，零值从最新的开始
	Limit     int       // 不大于 0 时为 DefaultLimit
}

// List 按时间从新到旧返回 q.Recipient 的通知。
func List(ctx context.Context, db bun.IDB, q Query) ([]Notification, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	sel := of(db.NewSelect().Model((*Notification)(nil)), q.Recipient)
	if q.Unread {
		sel = sel.Where("read_at IS NULL")
	}
	if q.Before != uuid.Nil {
		sel = sel.Where("id < ?", q.Before)
	}
	list := []Notification{}
	if err := sel.OrderExpr("id DESC").Limit(int64(limit)).Scan(ctx, &list); err != nil {
		return nil, errors.Wrap(err, "notification: list")
	}
	return list, nil
}

// Get 返回 r 的一条通知。通知不存在或属于其他收件人时返回 ErrNotFound。
func Get(ctx context.Context, db bun.IDB, r Recipient, id uuid.UUID) (*Notification, error) {
	n := new(Notification)
	err := of(db.NewSelect().Model(n), r).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound.New()
	}
	if err != nil {
		return nil, errors.Wrap(err, "notification: get")
	}
	return n, nil
}

// UnreadCount 返回 r 的未读通知数。
func UnreadCount(ctx context.Context, db bun.IDB, r Recipient) (int, error) {
	n, err := of(db.NewSelect().Model((*Notification)(nil)), r).Where("read_at IS NULL").Count(ctx)
	return int(n), errors.Wrap(err, "notification: unread count")
}

// MarkRead 把 r 的这些通知标记为已读。已读的通知保持原来的已读时间，不属于 r 的 ID 被忽略。
func MarkRead(ctx context.Context, db bun.IDB, r Recipient, ids ...uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	return markRead(ctx, db, r, ids)
}

// MarkAllRead 把 r 的全部通知标记为已读。
func MarkAllRead(ctx context.Context, db bun.IDB, r Recipient) error {
	return markRead(ctx, db, r, nil)
}

func markRead(ctx context.Context, db bun.IDB, r Recipient, ids []uuid.UUID) error {
	upd := of(db.NewUpdate().Model((*Notification)(nil)), r).
		Set("read_at = ?", time.Now().UTC()).Where("read_at IS NULL")
	if ids != nil {
		upd = upd.Where("id IN (?)", bun.List(ids))
	}
	_, err := upd.Exec(ctx)
	return errors.Wrap(err, "notification: mark read")
}

// Delete 删除 r 的这些通知，不属于 r 的 ID 被忽略。
func Delete(ctx context.Context, db bun.IDB, r Recipient, ids ...uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := of(db.NewDelete().Model((*Notification)(nil)), r).Where("id IN (?)", bun.List(ids)).Exec(ctx)
	return errors.Wrap(err, "notification: delete")
}

// Cleanup 返回删除 keep 以前的通知（无论是否已读）的清理函数，每次调用最多删除 limit 条，交给 schedule.Cleanup：
//
//	sched.Add("notifications:cleanup", "0 4 * * *", schedule.Cleanup(notification.Cleanup(db, 90*24*time.Hour), 1000))
func Cleanup(db bun.IDB, keep time.Duration) func(ctx context.Context, limit int) (int, error) {
	if db == nil {
		panic("notification: nil database")
	}
	if keep <= 0 {
		panic("notification: keep must be positive")
	}
	return func(ctx context.Context, limit int) (int, error) {
		if limit <= 0 {
			return 0, errors.New("notification: cleanup limit must be positive")
		}
		old := db.NewSelect().Model((*Notification)(nil)).Column("id").
			Where("created_at < ?", time.Now().Add(-keep).UTC()).OrderExpr("created_at").Limit(int64(limit))
		res, err := db.NewDelete().Model((*Notification)(nil)).
			Where("id IN (?)", old).Exec(ctx)
		if err != nil {
			return 0, errors.Wrap(err, "notification: cleanup")
		}
		n, err := res.RowsAffected()
		return int(n), errors.Wrap(err, "notification: cleanup")
	}
}
