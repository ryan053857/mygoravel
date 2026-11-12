package queue

import (
	"context"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/uptrace/bun"
)

// State 是任务的状态，也是指标 queue.jobs 的 queue.job.state 属性。
type State string

const (
	// StateReady 是已经到期、等待领取的任务，包括租约已过期、等待重新领取的任务。
	StateReady State = "ready"
	// StateDelayed 是还没到执行时间、从未执行过的任务。
	StateDelayed State = "delayed"
	// StateRetrying 是失败后等待重试的任务。
	StateRetrying State = "retrying"
	// StateRunning 是正在处理、租约未过期的任务。
	StateRunning State = "running"
	// StateFailed 是失败任务表中的任务。驱动的 Stats 不报告它，由 Stats 从失败任务表中读取。
	StateFailed State = "failed"
)

// driverStates 是驱动报告的状态。
var driverStates = []State{StateReady, StateDelayed, StateRetrying, StateRunning}

// TopicStats 是一个 topic 当前的任务数。
type TopicStats struct {
	Topic string
	// Jobs 是各状态的任务数。能给出的状态总会出现，即使为 0；驱动给不出的状态不出现。
	Jobs map[State]int
	// OldestReadyAt 是 StateReady 中最早到期的时间，没有到期的任务时为零值。
	// 当前时间减去它就是积压最久的任务已经等了多久：worker 跟不上时它会持续变大。
	// 有到期的任务而它为零值，表示驱动给不出。
	OldestReadyAt time.Time
}

// Stats 返回 d 中每个 topic 当前的任务数，包括 failed 中由 d 转入的失败任务数，按 topic 排序。
// 每个 topic 都有 StateFailed；只有失败任务的 topic，队列中没有它的任务，驱动报告的状态都是 0。
// 统计会扫描队列和失败任务表，适合命令、后台页面和分钟级频率的采集。d 或 failed 为 nil 时 panic。
func Stats(ctx context.Context, d Driver, failed *FailedJobs) ([]TopicStats, error) {
	if d == nil || failed == nil {
		panic("queue: nil driver or failed jobs")
	}
	stats, err := d.Stats(ctx)
	if err != nil {
		return nil, err
	}
	counts, err := failed.count(ctx, d.Name())
	if err != nil {
		return nil, err
	}
	for i := range stats {
		stats[i].Jobs = maps.Clone(stats[i].Jobs)
		stats[i].Jobs[StateFailed] = counts[stats[i].Topic]
		delete(counts, stats[i].Topic)
	}
	for topic, n := range counts {
		jobs := map[State]int{StateFailed: n}
		for _, state := range driverStates {
			jobs[state] = 0
		}
		stats = append(stats, TopicStats{Topic: topic, Jobs: jobs})
	}
	slices.SortFunc(stats, func(a, b TopicStats) int { return strings.Compare(a.Topic, b.Topic) })
	return stats, nil
}

// Stats 实现 Driver。
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
		stats[i] = TopicStats{Topic: r.Topic, OldestReadyAt: r.Oldest.Time, Jobs: map[State]int{
			StateReady: r.Ready, StateDelayed: r.Delayed, StateRetrying: r.Retrying, StateRunning: r.Running,
		}}
	}
	return stats, nil
}
