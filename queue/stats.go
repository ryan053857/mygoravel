package queue

import (
	"context"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/uptrace/bun"
)

// Unknown 表示驱动无法给出 TopicStats 中的这一项。
const Unknown = -1

// TopicStats 是一个 topic 当前的任务数。驱动无法给出的计数为 Unknown。
type TopicStats struct {
	Topic string
	// Ready 是已经到期、等待领取的任务，包括租约已过期、等待重新领取的任务。
	Ready int
	// Delayed 是还没到执行时间、从未执行过的任务。
	Delayed int
	// Retrying 是失败后等待重试的任务。
	Retrying int
	// Running 是正在处理、租约未过期的任务。
	Running int
	// OldestReadyAt 是 Ready 中最早到期的时间，没有到期的任务或驱动无法给出时为零值。
	// 当前时间减去它就是积压最久的任务已经等了多久：worker 跟不上时它会持续变大。
	OldestReadyAt time.Time
}

// StatsReporter 由能报告队列状态的驱动实现。框架的三个驱动都实现了它。
type StatsReporter interface {
	Stats(ctx context.Context) ([]TopicStats, error)
}

// ErrStatsNotSupported 表示驱动没有实现 StatsReporter。
var ErrStatsNotSupported = errors.New("queue: driver does not report stats")

// Stats 返回驱动 d 中每个 topic 的任务数，按 topic 排序。只有还有任务的 topic 会出现。
// 统计会扫描队列中的任务，适合后台页面和命令，不适合高频抓取；高频的指标请用 WithObserver 记录。
func Stats(ctx context.Context, d Driver) ([]TopicStats, error) {
	r, ok := d.(StatsReporter)
	if !ok {
		return nil, ErrStatsNotSupported
	}
	return r.Stats(ctx)
}

// Stats 实现 StatsReporter。
func (d *dbDriver) Stats(ctx context.Context) ([]TopicStats, error) {
	now := time.Now().UTC()
	var rows []struct {
		Topic    string       `bun:"topic"`
		Ready    int          `bun:"ready_jobs"`
		Delayed  int          `bun:"delayed_jobs"`
		Retrying int          `bun:"retrying_jobs"`
		Running  int          `bun:"running_jobs"`
		Oldest   bun.NullTime `bun:"oldest_ready_at"`
	}
	err := d.db.NewSelect().Model((*job)(nil)).
		Column("j.topic").
		ColumnExpr("SUM(CASE WHEN j.available_at <= ? THEN 1 ELSE 0 END) AS ready_jobs", now).
		ColumnExpr("SUM(CASE WHEN j.available_at > ? AND j.reserved_at IS NULL AND j.attempts = 0 THEN 1 ELSE 0 END) AS delayed_jobs", now).
		ColumnExpr("SUM(CASE WHEN j.available_at > ? AND j.reserved_at IS NULL AND j.attempts > 0 THEN 1 ELSE 0 END) AS retrying_jobs", now).
		ColumnExpr("SUM(CASE WHEN j.available_at > ? AND j.reserved_at IS NOT NULL THEN 1 ELSE 0 END) AS running_jobs", now).
		ColumnExpr("MIN(CASE WHEN j.available_at <= ? THEN j.available_at END) AS oldest_ready_at", now).
		Group("j.topic").Order("j.topic").
		Scan(ctx, &rows)
	if err != nil {
		return nil, errors.Wrap(err, "queue: stats")
	}
	stats := make([]TopicStats, len(rows))
	for i, r := range rows {
		stats[i] = TopicStats{Topic: r.Topic, Ready: r.Ready, Delayed: r.Delayed, Retrying: r.Retrying, Running: r.Running, OldestReadyAt: r.Oldest.Time}
	}
	return stats, nil
}
