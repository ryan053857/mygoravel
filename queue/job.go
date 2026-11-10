package queue

import (
	"database/sql"
	"math"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// driverDatabase 是 queue_failed_jobs.driver 中数据库驱动的名称。
const driverDatabase = "database"

// metadataAvailableAt 是 Publish 传递可执行时间的内部元数据，写入时转为 available_at 列。
const metadataAvailableAt = "_queue_available_at"

type job struct {
	bun.BaseModel `bun:"table:queue_jobs,alias:j"`

	ID          uuid.UUID         `bun:"id,pk,type:uuid"`
	Topic       string            `bun:"topic,notnull"`
	Payload     []byte            `bun:"payload,notnull"`
	Metadata    map[string]string `bun:"metadata,type:jsonb,notnull"`
	Attempts    int               `bun:"attempts,notnull"`
	AvailableAt time.Time         `bun:"available_at,notnull"`
	ReservedAt  time.Time         `bun:"reserved_at,nullzero"`
	CreatedAt   time.Time         `bun:"created_at,notnull"`
}

type failedJob struct {
	bun.BaseModel `bun:"table:queue_failed_jobs,alias:f"`

	ID       uuid.UUID         `bun:"id,pk,type:uuid"`
	Driver   string            `bun:"driver,notnull"`
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

// txOptions 返回队列事务的选项。MySQL 默认的可重复读会在“删除可能不存在的行再插入”时加间隙锁，
// 两个任务同时转入失败表就会死锁；读已提交下不加间隙锁。其他数据库使用默认选项。
func txOptions(db bun.IDB) *sql.TxOptions {
	if db.Dialect().Name() == dialect.MySQL {
		return &sql.TxOptions{Isolation: sql.LevelReadCommitted}
	}
	return nil
}
