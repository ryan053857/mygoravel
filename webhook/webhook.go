package webhook

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/go-tenon/tenon/identity"
	"github.com/go-tenon/tenon/queue"
)

// Deliver 是投递任务的 topic，payload 是投递记录的 ID。由 Event.Send 和 Resend 发布，Deliverer 处理。
var Deliver = queue.Topic[uuid.UUID]("webhook.deliver")

// body 是请求体，字段顺序与 Standard Webhooks 的示例一致。
type body struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Data      any       `json:"data"`
}

// Status 是投递记录的状态。
type Status string

const (
	StatusPending   Status = "pending"   // 等待投递或等待重试
	StatusSucceeded Status = "succeeded" // 端点返回了 2xx
	StatusFailed    Status = "failed"    // 重试次数用完，或者端点已被停用
)

// Delivery 是一个事件发往一个端点的投递记录，也是 webhook_deliveries 表的 Bun 模型。
type Delivery struct {
	bun.BaseModel `bun:"table:webhook_deliveries,alias:wd"`

	ID             uuid.UUID `bun:"id,pk,type:uuid"` // UUIDv7，按时间递增
	Tenant         string    `bun:"tenant,notnull"`
	EndpointID     uuid.UUID `bun:"endpoint_id,notnull,type:uuid"`
	EventID        uuid.UUID `bun:"event_id,notnull,type:uuid"` // 即请求头 webhook-id，同一个事件的所有投递相同
	Event          string    `bun:"event,notnull"`              // 事件类型
	Payload        string    `bun:"payload,notnull"`            // 请求体，每次重试原样发送
	Status         Status    `bun:"status,notnull"`
	Attempts       int       `bun:"attempts,notnull"`        // 已经发送的次数
	ResponseStatus int       `bun:"response_status,notnull"` // 最后一次响应的 HTTP 状态码，0 表示没有收到响应
	LastError      string    `bun:"last_error,notnull"`      // 最后一次失败的原因，包括响应体的开头；成功时为空
	CreatedAt      time.Time `bun:"created_at,notnull"`
	UpdatedAt      time.Time `bun:"updated_at,notnull"`
}

// send 把 typ 事件推送给 subject 所在租户中订阅了它的端点，见 Event.Send。
func send(ctx context.Context, db bun.IDB, subject identity.Subject, typ string, data any) error {
	if err := checkType(typ); err != nil {
		return err
	}
	var endpoints []Endpoint
	err := db.NewSelect().Model(&endpoints).
		Where("tenant = ?", subject.Tenant).Where("disabled = ?", false).OrderExpr("id").Scan(ctx)
	if err != nil {
		return errors.Wrap(err, "webhook: load endpoints")
	}
	now := time.Now().UTC()
	var deliveries []Delivery
	for _, ep := range endpoints {
		if ep.subscribes(typ) {
			deliveries = append(deliveries, Delivery{
				Tenant: subject.Tenant, EndpointID: ep.ID, Event: typ,
				Status: StatusPending, CreatedAt: now, UpdatedAt: now,
			})
		}
	}
	if len(deliveries) == 0 {
		return nil
	}
	payload, err := json.Marshal(body{Type: typ, Timestamp: now, Data: data})
	if err != nil {
		return errors.Wrapf(err, "webhook: encode %q", typ)
	}
	eventID, err := uuid.NewV7()
	if err != nil {
		return errors.Wrap(err, "webhook: new event id")
	}
	for i := range deliveries {
		if deliveries[i].ID, err = uuid.NewV7(); err != nil {
			return errors.Wrap(err, "webhook: new delivery id")
		}
		deliveries[i].EventID, deliveries[i].Payload = eventID, string(payload)
	}
	return enqueue(ctx, db, deliveries)
}

// enqueue 写入投递记录并为每条记录发布投递任务。
func enqueue(ctx context.Context, db bun.IDB, deliveries []Delivery) error {
	if _, err := db.NewInsert().Model(&deliveries).Exec(ctx); err != nil {
		return errors.Wrap(err, "webhook: insert deliveries")
	}
	ids := make([]uuid.UUID, len(deliveries))
	for i, d := range deliveries {
		ids[i] = d.ID
	}
	return errors.Wrap(Deliver.PublishAll(ctx, db, ids), "webhook: publish deliveries")
}

func checkType(t string) error {
	if t == "" || strings.Contains(t, "*") {
		return errors.Newf("webhook: invalid event type %q", t)
	}
	return nil
}
