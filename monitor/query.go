package monitor

import (
	"context"
	"encoding/json"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/uptrace/bun"
)

// Aggregate 是一组记录的汇总。
type Aggregate struct {
	// Count 是记录的次数，Sum 是记录的值之和，Max 是其中最大的值。
	Count int64
	Sum   float64
	Max   float64
}

// Mean 返回平均值，没有记录时为 0。
func (a Aggregate) Mean() float64 {
	if a.Count == 0 {
		return 0
	}
	return a.Sum / float64(a.Count)
}

// MeanDuration 返回 RecordDuration 记录的平均耗时。
func (a Aggregate) MeanDuration() time.Duration { return seconds(a.Mean()) }

// MaxDuration 返回 RecordDuration 记录的最长耗时。
func (a Aggregate) MaxDuration() time.Duration { return seconds(a.Max) }

// Add 返回 a 与 b 合在一起的汇总，例如把成功和失败的执行合起来计算平均耗时。
func (a Aggregate) Add(b Aggregate) Aggregate {
	switch {
	case a.Count == 0:
		return b
	case b.Count == 0:
		return a
	}
	return Aggregate{Count: a.Count + b.Count, Sum: a.Sum + b.Sum, Max: max(a.Max, b.Max)}
}

// Total 是 metric 中一个 label 在一段时间内的汇总。
type Total struct {
	Metric string
	Label  string
	Aggregate
}

// Point 是时间序列中的一段。
type Point struct {
	// At 是这一段的开始。
	At time.Time
	Aggregate
}

// Value 是 Set 记录的一个最新值。
type Value struct {
	Label string
	// Data 是记录的值编码成的 JSON。
	Data json.RawMessage
	// At 是记录的时间。
	At time.Time
}

// Decode 把 Data 解码到 dst 中。
func (v Value) Decode(dst any) error {
	return errors.Wrapf(json.Unmarshal(v.Data, dst), "monitor: decode %q", v.Label)
}

// aggregateRow 是查询汇总的结果行。
type aggregateRow struct {
	Metric string  `bun:"metric"`
	Label  string  `bun:"label"`
	At     int64   `bun:"at"`
	Count  int64   `bun:"count"`
	Sum    float64 `bun:"sum"`
	Max    float64 `bun:"max"`
}

func (r aggregateRow) aggregate() Aggregate { return Aggregate{Count: r.Count, Sum: r.Sum, Max: r.Max} }

// Totals 返回 metrics 中每个 metric、每个 label 在 from 到 to 之间的汇总，按 label、metric 排序，
// 没有记录的不出现。一个组件的几个 metric 一次查询，例如成功和失败的次数。
// 记录按分钟聚合：计入从 from 所在的那一分钟起、在 to 之前开始的每一分钟。没有传入 metric 时 panic。
func (m *Monitor) Totals(ctx context.Context, from, to time.Time, metrics ...string) ([]Total, error) {
	if len(metrics) == 0 {
		panic("monitor: no metrics")
	}
	var rows []aggregateRow
	err := m.db.NewRaw(`SELECT metric, label, SUM(count) AS count, SUM(sum) AS sum, MAX(max) AS max
FROM monitor_aggregates WHERE metric IN (?) AND bucket >= ? AND bucket < ?
GROUP BY metric, label ORDER BY label, metric`,
		bun.List(metrics), minute(from), to.Unix()).Scan(ctx, &rows)
	if err != nil {
		return nil, errors.Wrapf(err, "monitor: totals of %q", metrics)
	}
	totals := make([]Total, len(rows))
	for i, r := range rows {
		totals[i] = Total{Metric: r.Metric, Label: r.Label, Aggregate: r.aggregate()}
	}
	return totals, nil
}

// Series 返回 metric 中 label 在 from 到 to 之间按 step 分段的汇总，按时间排序，没有记录的段为零值，
// 可以直接画成趋势图。计入的分钟与 Totals 相同。
// 段按 Unix 时间对齐，例如 step 为一小时时每段从整点开始。step 必须是整分钟，否则 panic。
func (m *Monitor) Series(ctx context.Context, metric, label string, from, to time.Time, step time.Duration) ([]Point, error) {
	if step < time.Minute || step%time.Minute != 0 {
		panic("monitor: step must be a whole number of minutes")
	}
	n := int64(step / time.Second)
	var rows []aggregateRow
	err := m.db.NewRaw(`SELECT bucket - bucket % ? AS at, SUM(count) AS count, SUM(sum) AS sum, MAX(max) AS max
FROM monitor_aggregates WHERE metric = ? AND label = ? AND bucket >= ? AND bucket < ? GROUP BY 1 ORDER BY 1`,
		n, metric, storedLabel(label), minute(from), to.Unix()).Scan(ctx, &rows)
	if err != nil {
		return nil, errors.Wrapf(err, "monitor: series of %q %q", metric, label)
	}
	byAt := make(map[int64]Aggregate, len(rows))
	for _, r := range rows {
		byAt[r.At] = r.aggregate()
	}
	start := minute(from)
	start -= start % n
	var points []Point
	for at := start; at < to.Unix(); at += n {
		points = append(points, Point{At: time.Unix(at, 0), Aggregate: byAt[at]})
	}
	return points, nil
}

// Latest 返回 metric 每个 label 最新的值，按 label 排序。
func (m *Monitor) Latest(ctx context.Context, metric string) ([]Value, error) {
	var rows []struct {
		Label      string `bun:"label"`
		Value      string `bun:"value"`
		RecordedAt int64  `bun:"recorded_at"`
	}
	err := m.db.NewRaw(`SELECT label, value, recorded_at FROM monitor_values WHERE metric = ? ORDER BY label`, metric).
		Scan(ctx, &rows)
	if err != nil {
		return nil, errors.Wrapf(err, "monitor: latest of %q", metric)
	}
	values := make([]Value, len(rows))
	for i, r := range rows {
		values[i] = Value{Label: r.Label, Data: json.RawMessage(r.Value), At: time.UnixMicro(r.RecordedAt)}
	}
	return values, nil
}

// Cleanup 返回删除 keep 以前的汇总记录的清理函数，每次调用最多删除 limit 条，返回删除的条数，交给 schedule.Cleanup：
//
//	sched.Add("monitor:cleanup", "@hourly", schedule.Cleanup(mon.Cleanup(7*24*time.Hour), 1000))
//
// 最新值每个 label 只有一行，不随时间增长，不会被清理。keep 不为正时 panic。
func (m *Monitor) Cleanup(keep time.Duration) func(ctx context.Context, limit int) (int, error) {
	if keep <= 0 {
		panic("monitor: keep must be positive")
	}
	return func(ctx context.Context, limit int) (int, error) {
		if limit <= 0 {
			return 0, errors.New("monitor: cleanup limit must be positive")
		}
		old := m.db.NewRaw(`SELECT metric, label, bucket FROM monitor_aggregates WHERE bucket < ? ORDER BY bucket LIMIT ?`,
			time.Now().Add(-keep).Unix(), limit)
		res, err := m.db.NewRaw(`DELETE FROM monitor_aggregates WHERE (metric, label, bucket) IN
(SELECT metric, label, bucket FROM (?) AS old)`, old).Exec(ctx)
		if err != nil {
			return 0, errors.Wrap(err, "monitor: cleanup")
		}
		n, err := res.RowsAffected()
		return int(n), errors.Wrap(err, "monitor: cleanup")
	}
}

// seconds 把秒数转换为 time.Duration。
func seconds(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }
