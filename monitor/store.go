package monitor

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// 名称的最大字节数，与表结构一致。
const (
	MaxMetricLength = 100
	MaxLabelLength  = 255
)

// batchSize 是一条 INSERT 最多写入的行数。
const batchSize = 500

// Store 读写 monitor_aggregates 和 monitor_values 两张表：Recorder 通过它写入，命令和后台页面通过它查询。
type Store struct {
	db bun.IDB
}

// NewStore 创建 Store。db 必须已执行 Migrations 中的迁移；db 为 nil 时 panic。
func NewStore(db bun.IDB) *Store {
	if db == nil {
		panic("monitor: nil database")
	}
	return &Store{db: db}
}

// Total 是一个 label 在一段时间内的汇总。
type Total struct {
	Label string
	// Count 是记录的次数，Sum 是记录的值之和，Max 是其中最大的值。
	Count int64
	Sum   float64
	Max   float64
}

// Mean 返回平均值，没有记录时为 0。
func (t Total) Mean() float64 {
	if t.Count == 0 {
		return 0
	}
	return t.Sum / float64(t.Count)
}

// Point 是时间序列中的一段。
type Point struct {
	// At 是这一段的开始。
	At    time.Time
	Count int64
	Sum   float64
	Max   float64
}

// Value 是 Recorder.Set 记录的一个最新值。
type Value struct {
	Label string
	// Data 是记录的值编码成的 JSON。
	Data json.RawMessage
	// At 是记录的时间。
	At time.Time
}

// Decode 把 Data 解码到 v 中。
func (v Value) Decode(dst any) error {
	return errors.Wrapf(json.Unmarshal(v.Data, dst), "monitor: decode %q", v.Label)
}

// Totals 返回 metric 每个 label 在 from 到 to 之间的汇总，按 label 排序。
// 记录按分钟聚合：计入从 from 所在的那一分钟起、在 to 之前开始的每一分钟。
func (s *Store) Totals(ctx context.Context, metric string, from, to time.Time) ([]Total, error) {
	var totals []Total
	err := s.db.NewRaw(`SELECT label, SUM(count) AS count, SUM(sum) AS sum, MAX(max) AS max FROM monitor_aggregates
WHERE metric = ? AND bucket >= ? AND bucket < ? GROUP BY label ORDER BY label`,
		metric, minute(from), to.Unix()).Scan(ctx, &totals)
	return totals, errors.Wrapf(err, "monitor: totals of %q", metric)
}

// Series 返回 metric 中 label 在 from 到 to 之间按 step 分段的汇总，按时间排序，没有记录的段不出现。
// 计入的分钟与 Totals 相同。
// 段按 Unix 时间对齐，例如 step 为一小时时每段从整点开始。step 必须是整分钟，否则 panic。
func (s *Store) Series(ctx context.Context, metric, label string, from, to time.Time, step time.Duration) ([]Point, error) {
	if step < time.Minute || step%time.Minute != 0 {
		panic("monitor: step must be a whole number of minutes")
	}
	n := int64(step / time.Second)
	var rows []struct {
		At    int64   `bun:"at"`
		Count int64   `bun:"count"`
		Sum   float64 `bun:"sum"`
		Max   float64 `bun:"max"`
	}
	err := s.db.NewRaw(`SELECT bucket - bucket % ? AS at, SUM(count) AS count, SUM(sum) AS sum, MAX(max) AS max
FROM monitor_aggregates WHERE metric = ? AND label = ? AND bucket >= ? AND bucket < ? GROUP BY 1 ORDER BY 1`,
		n, metric, label, minute(from), to.Unix()).Scan(ctx, &rows)
	if err != nil {
		return nil, errors.Wrapf(err, "monitor: series of %q %q", metric, label)
	}
	points := make([]Point, len(rows))
	for i, r := range rows {
		points[i] = Point{At: time.Unix(r.At, 0), Count: r.Count, Sum: r.Sum, Max: r.Max}
	}
	return points, nil
}

// Latest 返回 metric 每个 label 最新的值，按 label 排序。
func (s *Store) Latest(ctx context.Context, metric string) ([]Value, error) {
	var rows []struct {
		Label      string `bun:"label"`
		Value      string `bun:"value"`
		RecordedAt int64  `bun:"recorded_at"`
	}
	err := s.db.NewRaw(`SELECT label, value, recorded_at FROM monitor_values WHERE metric = ? ORDER BY label`, metric).
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

// Cleanup 返回删除 keep 以前的聚合记录的清理函数，每次调用最多删除 limit 条，返回删除的条数，交给 schedule.Cleanup：
//
//	sched.Add("monitor:cleanup", "@hourly", schedule.Cleanup(store.Cleanup(7*24*time.Hour), 1000))
//
// 最新值每个 label 只有一行，不随时间增长，不会被清理。keep 不为正时 panic。
func (s *Store) Cleanup(keep time.Duration) func(ctx context.Context, limit int) (int, error) {
	if keep <= 0 {
		panic("monitor: keep must be positive")
	}
	return func(ctx context.Context, limit int) (int, error) {
		if limit <= 0 {
			return 0, errors.New("monitor: cleanup limit must be positive")
		}
		// 外层派生表让带 LIMIT 的子查询在 MySQL 中也能用于 IN。
		old := s.db.NewRaw(`SELECT metric, label, bucket FROM monitor_aggregates WHERE bucket < ? ORDER BY bucket LIMIT ?`,
			time.Now().Add(-keep).Unix(), limit)
		res, err := s.db.NewRaw(`DELETE FROM monitor_aggregates WHERE (metric, label, bucket) IN
(SELECT metric, label, bucket FROM (?) AS old)`, old).Exec(ctx)
		if err != nil {
			return 0, errors.Wrap(err, "monitor: cleanup")
		}
		n, err := res.RowsAffected()
		return int(n), errors.Wrap(err, "monitor: cleanup")
	}
}

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
func (s *Store) addAggregates(ctx context.Context, rows []aggregate) error {
	insert := `INSERT INTO monitor_aggregates (metric, label, bucket, count, sum, max) VALUES `
	upsert := ` ON CONFLICT (metric, label, bucket) DO UPDATE SET count = monitor_aggregates.count + excluded.count,
sum = monitor_aggregates.sum + excluded.sum, max = ` + s.greatest("monitor_aggregates.max", "excluded.max")
	if s.db.Dialect().Name() == dialect.MySQL {
		upsert = ` AS new ON DUPLICATE KEY UPDATE count = monitor_aggregates.count + new.count,
sum = monitor_aggregates.sum + new.sum, max = GREATEST(monitor_aggregates.max, new.max)`
	}
	for chunk := range chunks(rows) {
		args := make([]any, 0, len(chunk)*6)
		for _, r := range chunk {
			args = append(args, r.metric, r.label, r.bucket, r.count, r.sum, r.max)
		}
		q := insert + placeholders(len(chunk), 6) + upsert
		if _, err := s.db.NewRaw(q, args...).Exec(ctx); err != nil {
			return errors.Wrap(err, "monitor: write aggregates")
		}
	}
	return nil
}

// setValues 写入 rows，表中已有更新的值时保留它：多个实例各自写入时，以记录时间最晚的为准。
func (s *Store) setValues(ctx context.Context, rows []value) error {
	insert := `INSERT INTO monitor_values (metric, label, value, recorded_at) VALUES `
	upsert := ` ON CONFLICT (metric, label) DO UPDATE SET value = excluded.value, recorded_at = excluded.recorded_at
WHERE monitor_values.recorded_at <= excluded.recorded_at`
	if s.db.Dialect().Name() == dialect.MySQL {
		// 赋值从左到右进行，value 先比较原来的 recorded_at。
		upsert = ` AS new ON DUPLICATE KEY UPDATE
value = IF(new.recorded_at >= monitor_values.recorded_at, new.value, monitor_values.value),
recorded_at = GREATEST(monitor_values.recorded_at, new.recorded_at)`
	}
	for chunk := range chunks(rows) {
		args := make([]any, 0, len(chunk)*4)
		for _, r := range chunk {
			args = append(args, r.metric, r.label, string(r.data), r.at)
		}
		q := insert + placeholders(len(chunk), 4) + upsert
		if _, err := s.db.NewRaw(q, args...).Exec(ctx); err != nil {
			return errors.Wrap(err, "monitor: write values")
		}
	}
	return nil
}

// greatest 返回取两个值中较大者的表达式：SQLite 用多参数的 max，其他数据库用 GREATEST。
func (s *Store) greatest(a, b string) string {
	if s.db.Dialect().Name() == dialect.SQLite {
		return "max(" + a + ", " + b + ")"
	}
	return "GREATEST(" + a + ", " + b + ")"
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

// minute 返回 t 所在分钟的开始，Unix 秒。
func minute(t time.Time) int64 {
	s := t.Unix()
	return s - ((s%60)+60)%60
}
