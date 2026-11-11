package webhook

import (
	"context"
	"database/sql"
	"net/url"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/errorx"
)

var (
	// ErrEndpointNotFound 表示端点不存在或属于其他租户。
	ErrEndpointNotFound = errorx.Define(errorx.NotFound, "webhook.endpoint_not_found")
	// ErrInvalidURL 表示端点地址不是 http 或 https 的绝对地址。
	ErrInvalidURL = errorx.Define(errorx.InvalidArgument, "webhook.invalid_url")
	// ErrInvalidEvents 表示端点没有订阅任何事件，或者事件的写法不合法。
	ErrInvalidEvents = errorx.Define(errorx.InvalidArgument, "webhook.invalid_events")
)

// PreviousSecretTTL 是 RotateSecret 之后旧密钥继续参与签名的时间，接收方在这段时间内换上新密钥。
const PreviousSecretTTL = 24 * time.Hour

// Endpoint 是租户配置的接收地址，也是 webhook_endpoints 表的 Bun 模型。
type Endpoint struct {
	bun.BaseModel `bun:"table:webhook_endpoints,alias:we"`

	ID          uuid.UUID `bun:"id,pk,type:uuid"`
	Tenant      string    `bun:"tenant,notnull"`
	URL         string    `bun:"url,notnull"`
	Description string    `bun:"description,notnull"`
	// Events 是订阅的事件类型：完整的类型如 article.published，以 .* 结尾的前缀如 article.*，或者 * 表示全部。
	Events   []string `bun:"events,notnull"`
	Secret   string   `bun:"secret,notnull"` // 签名密钥，whsec_ 开头；创建时生成，交给接收方校验签名
	Disabled bool     `bun:"disabled,notnull"`
	// PreviousSecret 是轮换前的密钥，在 PreviousSecretExpiresAt 之前与 Secret 一起签名。
	PreviousSecret          string    `bun:"previous_secret,notnull"`
	PreviousSecretExpiresAt time.Time `bun:"previous_secret_expires_at,nullzero"`
	CreatedAt               time.Time `bun:"created_at,notnull"`
	UpdatedAt               time.Time `bun:"updated_at,notnull"`
}

// subscribes 报告端点是否订阅了事件类型 t。
func (ep Endpoint) subscribes(t string) bool {
	for _, p := range ep.Events {
		if p == "*" || p == t {
			return true
		}
		if prefix, ok := strings.CutSuffix(p, "*"); ok && strings.HasPrefix(t, prefix) {
			return true
		}
	}
	return false
}

// secrets 返回在 now 时参与签名的密钥。
func (ep Endpoint) secrets(now time.Time) []string {
	if ep.PreviousSecret != "" && now.Before(ep.PreviousSecretExpiresAt) {
		return []string{ep.Secret, ep.PreviousSecret}
	}
	return []string{ep.Secret}
}

// EndpointInput 是创建和修改端点时提交的内容。
type EndpointInput struct {
	URL         string
	Events      []string
	Description string
	Disabled    bool // 停用的端点不再收到新事件，已有的投递在下次尝试时标记为失败
}

func (in EndpointInput) check() error {
	u, err := url.Parse(in.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(in.URL) > 2048 {
		return ErrInvalidURL.New()
	}
	if len(in.Events) == 0 {
		return ErrInvalidEvents.New()
	}
	for _, p := range in.Events {
		if p == "*" {
			continue
		}
		prefix, _ := strings.CutSuffix(p, ".*")
		if checkType(prefix) != nil {
			return ErrInvalidEvents.New(errorx.Public("event", p))
		}
	}
	return nil
}

// CreateEndpoint 在租户 tenant 中创建端点，并生成签名密钥。
func CreateEndpoint(ctx context.Context, db bun.IDB, tenant string, in EndpointInput) (*Endpoint, error) {
	if err := in.check(); err != nil {
		return nil, err
	}
	if len(tenant) > maxKey {
		return nil, errors.Newf("webhook: tenant longer than %d bytes", maxKey)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, errors.Wrap(err, "webhook: new endpoint id")
	}
	now := time.Now().UTC()
	ep := &Endpoint{
		ID: id, Tenant: tenant, URL: in.URL, Description: in.Description, Events: in.Events,
		Secret: newSecret(), Disabled: in.Disabled, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := db.NewInsert().Model(ep).Exec(ctx); err != nil {
		return nil, errors.Wrap(err, "webhook: create endpoint")
	}
	return ep, nil
}

// GetEndpoint 返回租户 tenant 的端点，不存在或属于其他租户时返回 ErrEndpointNotFound。
func GetEndpoint(ctx context.Context, db bun.IDB, tenant string, id uuid.UUID) (*Endpoint, error) {
	ep := new(Endpoint)
	err := db.NewSelect().Model(ep).Where("tenant = ?", tenant).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrEndpointNotFound.New()
	}
	if err != nil {
		return nil, errors.Wrap(err, "webhook: get endpoint")
	}
	return ep, nil
}

// ListEndpoints 按创建时间返回租户 tenant 的全部端点。
func ListEndpoints(ctx context.Context, db bun.IDB, tenant string) ([]Endpoint, error) {
	list := []Endpoint{}
	if err := db.NewSelect().Model(&list).Where("tenant = ?", tenant).OrderExpr("id").Scan(ctx); err != nil {
		return nil, errors.Wrap(err, "webhook: list endpoints")
	}
	return list, nil
}

// UpdateEndpoint 修改租户 tenant 的端点，密钥保持不变。
func UpdateEndpoint(ctx context.Context, db bun.IDB, tenant string, id uuid.UUID, in EndpointInput) (*Endpoint, error) {
	if err := in.check(); err != nil {
		return nil, err
	}
	ep := &Endpoint{URL: in.URL, Description: in.Description, Events: in.Events, Disabled: in.Disabled, UpdatedAt: time.Now().UTC()}
	return update(ctx, db, tenant, id, ep, "url", "description", "events", "disabled", "updated_at")
}

// RotateSecret 为租户 tenant 的端点生成新的签名密钥。旧密钥在 PreviousSecretTTL 内继续签名，
// 请求头中同时带有新旧两个签名，接收方可以先换上新密钥再等旧密钥过期。
func RotateSecret(ctx context.Context, db bun.IDB, tenant string, id uuid.UUID) (*Endpoint, error) {
	old, err := GetEndpoint(ctx, db, tenant, id)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	ep := &Endpoint{
		Secret: newSecret(), PreviousSecret: old.Secret, PreviousSecretExpiresAt: now.Add(PreviousSecretTTL), UpdatedAt: now,
	}
	return update(ctx, db, tenant, id, ep, "secret", "previous_secret", "previous_secret_expires_at", "updated_at")
}

func update(ctx context.Context, db bun.IDB, tenant string, id uuid.UUID, ep *Endpoint, columns ...string) (*Endpoint, error) {
	res, err := db.NewUpdate().Model(ep).Column(columns...).
		Where("tenant = ?", tenant).Where("id = ?", id).Exec(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "webhook: update endpoint")
	}
	if n, err := res.RowsAffected(); err != nil {
		return nil, errors.Wrap(err, "webhook: update endpoint")
	} else if n == 0 {
		return nil, ErrEndpointNotFound.New()
	}
	return GetEndpoint(ctx, db, tenant, id)
}

// DeleteEndpoint 删除租户 tenant 的端点和它的投递记录。尚未完成的投递任务在执行时发现记录不存在，直接结束。
func DeleteEndpoint(ctx context.Context, db bun.IDB, tenant string, id uuid.UUID) error {
	res, err := db.NewDelete().Model((*Endpoint)(nil)).Where("tenant = ?", tenant).Where("id = ?", id).Exec(ctx)
	if err != nil {
		return errors.Wrap(err, "webhook: delete endpoint")
	}
	if n, err := res.RowsAffected(); err != nil {
		return errors.Wrap(err, "webhook: delete endpoint")
	} else if n == 0 {
		return ErrEndpointNotFound.New()
	}
	_, err = db.NewDelete().Model((*Delivery)(nil)).Where("endpoint_id = ?", id).Exec(ctx)
	return errors.Wrap(err, "webhook: delete deliveries")
}
