package monitor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/logging"
)

// 名称的最大字节数，与表结构一致。
const (
	MaxMetricLength = 100
	MaxLabelLength  = 255
)

// flushInterval 是 Monitor 定期写入的间隔，flushTimeout 是每次写入的时长上限。
var (
	flushInterval = 10 * time.Second
	flushTimeout  = 10 * time.Second
)

// Monitor 记录和查询运行统计。记录只修改内存，每 10 秒批量写入数据库，可以在任何热路径上调用；
// 查询读取数据库，任何进程都能看到所有实例的记录。
//
// 写入失败时这一批记录被丢弃并记录一条 error 日志：统计只用于查看，不值得为它阻塞或无限积压。
// 每个进程创建一个 Monitor，进程退出前调用 Close 写入剩余的记录。
type Monitor struct {
	db     bun.IDB
	logger *slog.Logger
	now    func() time.Time

	mu         sync.Mutex
	closed     bool
	aggregates map[aggregateKey]*aggregate
	values     map[valueKey]value

	stop chan struct{}
	done chan struct{}
}

type aggregateKey struct {
	metric, label string
	bucket        int64
}

type valueKey struct{ metric, label string }

// New 创建 Monitor 并开始定期写入。db 必须已执行 Migrations 中的迁移；logger 为 nil 时使用 slog.Default()。
// db 为 nil 时 panic。
func New(db bun.IDB, logger *slog.Logger) *Monitor {
	if db == nil {
		panic("monitor: nil database")
	}
	m := &Monitor{
		db:         db,
		logger:     logging.Or(logger).With(logging.AttrComponent, "monitor"),
		now:        time.Now,
		aggregates: map[aggregateKey]*aggregate{},
		values:     map[valueKey]value{},
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}
	go m.loop()
	return m
}

// Record 把 v 计入 metric 中 label 当前这一分钟的汇总：次数加 1，v 计入总和与最大值。
// 只关心次数时 v 传 1，例如缓存命中；记录耗时用 RecordDuration。
//
// metric 是记录的种类，例如 "mail.sent"；label 是其中的一项，例如邮件类型。
// label 的取值要有限，不要用 ID 这类不断增长的值；超过 MaxLabelLength 字节的 label 以 SHA-256 摘要保存。
// metric 为空或超过 MaxMetricLength 字节时 panic。Close 之后的记录被丢弃。
func (m *Monitor) Record(metric, label string, v float64) {
	checkMetric(metric)
	k := aggregateKey{metric, storedLabel(label), minute(m.now())}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	a := m.aggregates[k]
	if a == nil {
		m.aggregates[k] = &aggregate{metric: k.metric, label: k.label, bucket: k.bucket, count: 1, sum: v, max: v}
		return
	}
	a.count++
	a.sum += v
	a.max = max(a.max, v)
}

// RecordDuration 以秒记录耗时 d，查询时用 Aggregate.MeanDuration 和 MaxDuration 读取。
func (m *Monitor) RecordDuration(metric, label string, d time.Duration) {
	m.Record(metric, label, d.Seconds())
}

// Set 把 metric 中 label 的最新值设为 v，例如定时任务上一次执行的结果。v 编码为 JSON 保存，
// 多个实例先后记录同一个 label 时，以记录时间最晚的为准。
// 名称的要求与 Record 相同；v 无法编码为 JSON 时记录一条 error 日志并丢弃。
func (m *Monitor) Set(metric, label string, v any) {
	checkMetric(metric)
	data, err := json.Marshal(v)
	if err != nil {
		m.logger.Error("encode monitor value failed", "metric", metric, "label", label, "error", err)
		return
	}
	k := valueKey{metric, storedLabel(label)}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.values[k] = value{metric: k.metric, label: k.label, data: data, at: m.now().UnixMicro()}
}

// Flush 立即写入内存中的记录。通常不需要调用：Monitor 定期写入，Close 时写入剩余的记录；测试中用它等待写入。
func (m *Monitor) Flush(ctx context.Context) error {
	m.mu.Lock()
	aggregates, values := m.aggregates, m.values
	m.aggregates, m.values = map[aggregateKey]*aggregate{}, map[valueKey]value{}
	m.mu.Unlock()

	rows := make([]aggregate, 0, len(aggregates))
	for _, a := range aggregates {
		rows = append(rows, *a)
	}
	latest := make([]value, 0, len(values))
	for _, v := range values {
		latest = append(latest, v)
	}
	return errors.Join(m.addAggregates(ctx, rows), m.setValues(ctx, latest))
}

// Close 停止定期写入，写入剩余的记录。之后的记录被丢弃，查询照常可用。可以登记到 cleanup.Stack。
func (m *Monitor) Close(ctx context.Context) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.mu.Unlock()
	close(m.stop)
	<-m.done
	return m.Flush(ctx)
}

// loop 每隔 flushInterval 写入一次，直到 Close。
func (m *Monitor) loop() {
	defer close(m.done)
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
			if err := m.Flush(ctx); err != nil {
				m.logger.Error("write monitor records failed", "error", err)
			}
			cancel()
		}
	}
}

// checkMetric 检查 metric 符合表结构。metric 由代码给出，不合法是编程错误。
func checkMetric(metric string) {
	switch {
	case metric == "":
		panic("monitor: empty metric")
	case len(metric) > MaxMetricLength:
		panic(errors.Newf("monitor: metric %.20q… is longer than %d bytes", metric, MaxMetricLength))
	}
}

// storedLabel 返回 label 在表中保存的形式：不超过 MaxLabelLength 时原样保存，否则保存 SHA-256 摘要。
// label 往往来自运行时的数据，例如 topic，超长时不能 panic。
func storedLabel(label string) string {
	if len(label) <= MaxLabelLength {
		return label
	}
	sum := sha256.Sum256([]byte(label))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// minute 返回 t 所在分钟的开始，Unix 秒。
func minute(t time.Time) int64 {
	s := t.Unix()
	return s - ((s%60)+60)%60
}
