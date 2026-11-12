package monitor

import (
	"context"
	"strings"

	"github.com/cockroachdb/errors"
)

// batchSize 是一条 INSERT 最多写入的行数。
const batchSize = 500

// aggregate 是一个分钟内的汇总，等待写入。
type aggregate struct {
	metric, label string
	bucket        int64
	count         int64
	sum, max      float64
}

// value 是一个最新值，等待写入。
type value struct {
	metric, label string
	data          []byte
	at            int64
}

// addAggregates 把 rows 累加到表中已有的分钟上。rows 中 metric、label、bucket 不重复。
func (m *Monitor) addAggregates(ctx context.Context, rows []aggregate) error {
	insert := `INSERT INTO monitor_aggregates (metric, label, bucket, count, sum, max) VALUES `
	upsert := ` ON CONFLICT (metric, label, bucket) DO UPDATE SET count = monitor_aggregates.count + excluded.count,
sum = monitor_aggregates.sum + excluded.sum, max = ` + "GREATEST(monitor_aggregates.max, excluded.max)"
	for chunk := range chunks(rows) {
		args := make([]any, 0, len(chunk)*6)
		for _, r := range chunk {
			args = append(args, r.metric, r.label, r.bucket, r.count, r.sum, r.max)
		}
		q := insert + placeholders(len(chunk), 6) + upsert
		if _, err := m.db.NewRaw(q, args...).Exec(ctx); err != nil {
			return errors.Wrap(err, "monitor: write aggregates")
		}
	}
	return nil
}

// setValues 写入 rows，表中已有更新的值时保留它：多个实例各自写入时，以记录时间最晚的为准。
func (m *Monitor) setValues(ctx context.Context, rows []value) error {
	insert := `INSERT INTO monitor_values (metric, label, value, recorded_at) VALUES `
	upsert := ` ON CONFLICT (metric, label) DO UPDATE SET value = excluded.value, recorded_at = excluded.recorded_at
WHERE monitor_values.recorded_at <= excluded.recorded_at`
	for chunk := range chunks(rows) {
		args := make([]any, 0, len(chunk)*4)
		for _, r := range chunk {
			args = append(args, r.metric, r.label, string(r.data), r.at)
		}
		q := insert + placeholders(len(chunk), 4) + upsert
		if _, err := m.db.NewRaw(q, args...).Exec(ctx); err != nil {
			return errors.Wrap(err, "monitor: write values")
		}
	}
	return nil
}

// chunks 把 rows 按 batchSize 分段。
func chunks[T any](rows []T) func(yield func([]T) bool) {
	return func(yield func([]T) bool) {
		for len(rows) > 0 {
			n := min(len(rows), batchSize)
			if !yield(rows[:n]) {
				return
			}
			rows = rows[n:]
		}
	}
}

// placeholders 返回 rows 行、每行 cols 个占位符的 VALUES 列表。
func placeholders(rows, cols int) string {
	row := "(" + strings.TrimSuffix(strings.Repeat("?, ", cols), ", ") + ")"
	return strings.TrimSuffix(strings.Repeat(row+", ", rows), ", ")
}
