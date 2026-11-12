package queue

import (
	"encoding/json"
	"math"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// Job 是一个任务，处理函数之外通过 Event 和失败任务看到它。
type Job struct {
	ID       uuid.UUID
	Topic    string
	Payload  []byte
	Metadata map[string]string
	// AvailableAt 是任务这次可以开始执行的时间：首次执行是发布时指定的时间，经由 outbox 的重试是退避后的时间；
	// NSQ 重新投递的任务给不出，为零值。
	AvailableAt time.Time
	// Attempt 是本次是第几次执行，从 1 开始。
	Attempt int
}

// envelope 是发给 NSQ 的消息体。
type envelope struct {
	ID          uuid.UUID         `json:"id"`
	Payload     json.RawMessage   `json:"payload"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	AvailableAt time.Time         `json:"available_at"`
	// Attempts 是这条消息之前已经执行的次数：经由 outbox 重试的任务从这里接着计数。
	Attempts int `json:"attempts,omitempty"`
}

// outboxRow 是 queue_outbox 中的一行。
type outboxRow struct {
	bun.BaseModel `bun:"table:queue_outbox,alias:o"`

	ID          int64     `bun:"id,pk,autoincrement"`
	Topic       string    `bun:"topic,notnull"`
	Body        []byte    `bun:"body,notnull"`
	AvailableAt time.Time `bun:"available_at,notnull"`
	CreatedAt   time.Time `bun:"created_at,notnull"`
}

type failedJob struct {
	bun.BaseModel `bun:"table:queue_failed_jobs,alias:f"`

	ID       uuid.UUID         `bun:"id,pk,type:uuid"`
	Topic    string            `bun:"topic,notnull"`
	Payload  []byte            `bun:"payload,notnull"`
	Metadata map[string]string `bun:"metadata,type:jsonb,notnull"`
	Attempts int               `bun:"attempts,notnull"`
	Error    string            `bun:"error,notnull"`
	FailedAt time.Time         `bun:"failed_at,notnull"`
}

// maxBackoff 是两次重试之间的最长等待时间。
const maxBackoff = 24 * time.Hour

// backoff 返回第 attempts 次处理失败后到下一次处理的等待时间：约 attempts⁴ 秒，上下浮动 10%，最长 24 小时。
func backoff(attempts int) time.Duration {
	// 先按秒封顶再转换，否则次数很大时 float64 转 Duration 会溢出。
	seconds := min(math.Pow(float64(max(attempts, 1)), 4)*(0.9+0.2*rand.Float64()), maxBackoff.Seconds())
	return time.Duration(seconds * float64(time.Second))
}
