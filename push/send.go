package push

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/go-tenon/tenon/notification"
	"github.com/go-tenon/tenon/queue"
)

// Deliver 是推送任务的 topic，由 Send 发布，Deliverer 处理。
var Deliver = queue.Topic[Job]("push.deliver")

// MaxBatch 是一次交给 Provider 的设备数上限，也是一个推送任务的收件人数上限。
const MaxBatch = 1000

// DefaultMaxAttempts 是 Options.MaxAttempts 不大于 0 时一个推送任务最多执行的次数。
// 按队列的退避，最后一次约在第一次之后 6 分钟，仍在阿里云 IdempotentToken 去重的 15 分钟之内，重试不会重复推送。
const DefaultMaxAttempts = 5

// Message 是一条推送通知。文案由应用按收件人的语言生成好再交给 Send。
type Message struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	// Data 随通知带给 App，用户点击通知后 App 据此打开对应的页面，例如 {"article_id": "42"}。
	Data map[string]string `json:"data,omitempty"`
	// Category 是应用自己命名的消息分类，例如 order、chat、promo。Provider 按配置把它换成各厂商要求的分类参数，
	// 见 push/aliyun 的 Config.Categories。为空时使用名为 default 的分类。
	Category string `json:"category,omitempty"`
	// Badge 是 App 图标上的数字，nil 表示不修改。
	Badge *int `json:"badge,omitempty"`
	// TTL 是设备离线时通知保留多久，从 Send 开始计算，超过后不再推送；0 表示使用推送服务的默认值。
	TTL time.Duration `json:"ttl,omitempty"`
}

// Job 是 Deliver 的 payload：一条消息和它的收件人。
type Job struct {
	ID      uuid.UUID                `json:"id"` // UUIDv7，重试时不变
	Message Message                  `json:"message"`
	To      []notification.Recipient `json:"to"`
	SentAt  time.Time                `json:"sent_at"` // 调用 Send 的时间，TTL 从这里开始计算
}

// Send 把 msg 推送给 to 中每个收件人登记的全部设备，收件人与站内通知的相同：某个租户中的一个主体。
//
// db 通常是修改业务数据的事务：Send 在其中发布推送任务，事务提交后才会推送，回滚时一起取消。
// 设备在任务执行时才查询，收件人没有登记设备时什么也不做。重复的收件人只推送一次，to 为空时什么也不做；
// 收件人超过 MaxBatch 个时分成多个任务。
func Send(ctx context.Context, db bun.IDB, msg Message, to ...notification.Recipient) error {
	if msg.Title == "" || msg.Body == "" {
		return errors.New("push: message title and body are required")
	}
	if msg.TTL < 0 {
		return errors.New("push: message TTL must not be negative")
	}
	seen := make(map[notification.Recipient]bool, len(to))
	var recipients []notification.Recipient
	for _, r := range to {
		if err := r.Validate(); err != nil {
			return errors.Wrap(err, "push: recipient")
		}
		if !seen[r] {
			seen[r] = true
			recipients = append(recipients, r)
		}
	}
	if len(recipients) == 0 {
		return nil
	}
	now := time.Now().UTC()
	var jobs []Job
	for chunk := range slices.Chunk(recipients, MaxBatch) {
		jobs = append(jobs, Job{ID: uuid.Must(uuid.NewV7()), Message: msg, To: chunk, SentAt: now})
	}
	return errors.Wrap(Deliver.PublishAll(ctx, db, jobs), "push: publish")
}

// Batch 是交给 Provider 的一次推送：同一个平台的一批设备，不超过 MaxBatch 台。
type Batch struct {
	// Key 标识这一批，同一个任务重试时不变，是 UUID 格式。推送服务支持去重时用它避免重试造成重复推送。
	Key string
	// Job 是所属推送任务的 ID，同一次 Send 的各批相同，可以写入推送服务的回执，方便对照。
	Job      uuid.UUID
	Platform Platform
	Tokens   []string
	Message  Message
	// Expires 是通知的过期时间，由 Message.TTL 算出；零值表示使用推送服务的默认值。
	Expires time.Time
}

// Provider 把一批通知交给推送服务，例如 push/aliyun。返回 queue.Permanent 包装的错误表示重试也不会成功，
// 例如配置错误、参数不合法；其他错误按队列的退避重试。
type Provider interface {
	Push(ctx context.Context, b Batch) error
}

// Options 是 Deliverer 的选项，零值可用。
type Options struct {
	// MaxAttempts 是一个推送任务最多执行的次数，覆盖 Worker 的 Config.MaxAttempts；不大于 0 时为 DefaultMaxAttempts。
	MaxAttempts int
}

// Deliverer 处理推送任务：查出收件人的设备，按平台分批交给 Provider。
type Deliverer struct {
	db          bun.IDB
	provider    Provider
	maxAttempts int
}

// NewDeliverer 创建推送任务的处理器，db 是设备表所在的数据库。
func NewDeliverer(db bun.IDB, p Provider, opts Options) *Deliverer {
	if db == nil {
		panic("push: nil database")
	}
	if p == nil {
		panic("push: nil provider")
	}
	d := &Deliverer{db: db, provider: p, maxAttempts: opts.MaxAttempts}
	if d.maxAttempts <= 0 {
		d.maxAttempts = DefaultMaxAttempts
	}
	return d
}

// Handle 把 Deliver 的处理函数注册到 w，最多执行 Options.MaxAttempts 次。
//
// 一个任务的各批依次推送。有一批失败时整个任务重试，已经成功的批次用相同的 Batch.Key 再交给 Provider，
// 由推送服务去重；全部失败都是 queue.Permanent 时不再重试。超过 TTL 的任务直接结束。
func (d *Deliverer) Handle(w *queue.Worker) {
	Deliver.Handle(w, d.deliver, queue.MaxAttempts(d.maxAttempts))
}

func (d *Deliverer) deliver(ctx context.Context, job Job) error {
	var expires time.Time
	if job.Message.TTL > 0 {
		expires = job.SentAt.Add(job.Message.TTL)
		if !time.Now().Before(expires) {
			return nil
		}
	}
	devices, err := d.devices(ctx, job.To)
	if err != nil {
		return err
	}
	byPlatform := map[Platform][]string{}
	for _, dev := range devices {
		byPlatform[dev.Platform] = append(byPlatform[dev.Platform], dev.Token)
	}
	var retry, permanent []error
	for _, platform := range slices.Sorted(maps.Keys(byPlatform)) {
		i := 0
		for tokens := range slices.Chunk(byPlatform[platform], MaxBatch) {
			b := Batch{
				Key:      uuid.NewSHA1(job.ID, fmt.Appendf(nil, "%s/%d", platform, i)).String(),
				Job:      job.ID,
				Platform: platform,
				Tokens:   tokens,
				Message:  job.Message,
				Expires:  expires,
			}
			i++
			err := d.provider.Push(ctx, b)
			switch {
			case err == nil:
			case queue.IsPermanent(err):
				permanent = append(permanent, errors.Wrapf(err, "push: %s batch %d", platform, i))
			default:
				retry = append(retry, errors.Wrapf(err, "push: %s batch %d", platform, i))
			}
		}
	}
	if len(retry) > 0 {
		// 只保留永久失败的文字：带着 queue.Permanent 的标记，整个任务会被当作永久失败而不再重试。
		for _, err := range permanent {
			retry = append(retry, errors.Newf("%s", err.Error()))
		}
		return errors.Join(retry...)
	}
	if len(permanent) > 0 {
		return queue.Permanent(errors.Join(permanent...))
	}
	return nil
}

// devices 查出 to 中收件人登记的设备，按平台和登记顺序排列。
func (d *Deliverer) devices(ctx context.Context, to []notification.Recipient) ([]Device, error) {
	tenants, types, ids := make([]string, len(to)), make([]string, len(to)), make([]string, len(to))
	for i, r := range to {
		tenants[i], types[i], ids[i] = r.Tenant, string(r.Type), r.ID
	}
	var devices []Device
	err := d.db.NewSelect().Model(&devices).Column("pd.platform", "pd.token").
		Join("JOIN unnest(?::text[], ?::text[], ?::text[]) AS r(tenant, subject_type, subject_id)",
			pgdialect.Array(tenants), pgdialect.Array(types), pgdialect.Array(ids)).
		JoinOn("r.tenant = pd.tenant AND r.subject_type = pd.subject_type AND r.subject_id = pd.subject_id").
		OrderExpr("pd.platform, pd.id").Scan(ctx)
	return devices, errors.Wrap(err, "push: load devices")
}
