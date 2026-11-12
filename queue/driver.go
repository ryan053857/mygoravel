package queue

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Driver 保存和领取任务，是队列中与存储有关的部分。重试策略、失败任务、并发控制、续租时机和平滑停止
// 由 Worker 负责，与驱动无关。框架提供 Database，
// 以及子包 redisqueue 和 natsqueue。
//
// 每个任务至少交付一次：持有任务的实例在租约到期前没有确认、放回或续租，任务会被重新领取。
type Driver interface {
	// Name 是驱动的名称，写入 queue_failed_jobs.driver，重新入队时据此找回驱动。
	Name() string
	// Enqueue 保存任务，任务在 Job.AvailableAt 之后可以被领取。Job.Attempt 被忽略。
	// Job.Unique 非空时，同一 topic 中已有同键、还没被领取过的任务，就跳过这个任务，不返回错误；
	// 同一批中键相同的任务只保留第一个。任务第一次被领取时释放它的键。不支持的驱动返回错误。
	Enqueue(ctx context.Context, jobs ...Job) error
	// Claim 领取 req.Topic 中最多 req.Max 个到期的任务，每个任务的租约为 req.Lease。
	// 没有到期的任务时一直等待，直到领取到至少一个或 ctx 结束（此时返回 ctx 的错误）；
	// 需要轮询的驱动按 req.PollInterval 查询。
	Claim(ctx context.Context, req ClaimRequest) ([]Claim, error)
	// Stats 返回每个还有任务的 topic 中各状态的任务数，按 topic 排序。
	Stats(ctx context.Context) ([]TopicStats, error)
}

// Job 是一个任务。
type Job struct {
	ID       uuid.UUID
	Topic    string
	Payload  []byte
	Metadata map[string]string
	// AvailableAt 是任务最早可以被领取的时间，零值表示立即。领取到的任务中，它是本次可以被领取的时间：
	// 首次执行是发布时指定的时间，重试是退避后的时间，租约过期被重新领取是租约到期的时间；
	// 驱动给不出时为零值。
	AvailableAt time.Time
	// Attempt 是领取到的任务本次是第几次执行，从 1 开始。
	Attempt int
	// Unique 是发布时用 queue.Unique 指定的键，见 Driver.Enqueue。领取到的任务中为空。
	Unique string
}

// ClaimRequest 是 Driver.Claim 的参数。
type ClaimRequest struct {
	Topic        string
	Max          int
	Lease        time.Duration
	PollInterval time.Duration
}

// Claim 是领取到的一个任务及其租约。除 Job 外的方法都会结束或延长租约；
// 租约已经过期、任务可能已被其他实例领取时，它们返回 ErrLeaseLost（驱动无法判断时可以不返回）。
type Claim interface {
	Job() Job
	// Extend 把租约延长到从现在起 lease。
	Extend(ctx context.Context, lease time.Duration) error
	// Ack 确认任务已完成，删除它。
	Ack(ctx context.Context) error
	// Retry 把任务放回，在 at 之后可以再次领取；本次执行计入次数。
	Retry(ctx context.Context, at time.Time) error
	// Release 把任务放回，立即可以再次领取；本次执行不计入次数。
	Release(ctx context.Context) error
}
