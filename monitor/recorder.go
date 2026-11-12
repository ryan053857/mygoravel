package monitor

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/cockroachdb/errors"

	"github.com/ryan053857/mygoravel/logging"
)

// flushInterval 是 Recorder 定期写入的间隔，flushTimeout 是每次写入的时长上限。
var (
	flushInterval = 10 * time.Second
	flushTimeout  = 10 * time.Second
)

// Recorder 在内存中累加记录，每 10 秒把它们批量写入 Store。Record 和 Set 只修改内存，可以在任何热路径上调用。
//
// 写入失败时这一批记录被丢弃并记录一条 error 日志：统计只用于查看，不值得为它阻塞或无限积压。
// 每个进程创建一个 Recorder，进程退出前调用 Close 写入剩余的记录。
type Recorder struct {
	store  *Store
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

// NewRecorder 创建 Recorder 并开始定期写入 store。logger 为 nil 时使用 slog.Default()；store 为 nil 时 panic。
func NewRecorder(store *Store, logger *slog.Logger) *Recorder {
	if store == nil {
		panic("monitor: nil store")
	}
	r := &Recorder{
		store:      store,
		logger:     logging.Or(logger).With(logging.AttrComponent, "monitor"),
		now:        time.Now,
		aggregates: map[aggregateKey]*aggregate{},
		values:     map[valueKey]value{},
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}
	go r.loop()
	return r
}

// Record 把 v 计入 metric 中 label 当前这一分钟的汇总：次数加 1，v 计入总和与最大值。
// 只关心次数时 v 传 1，例如缓存命中；关心大小时传大小，例如耗时的秒数。
//
// metric 是记录的种类，例如 "queue.processed"；label 是其中的一项，例如 topic。
// label 的取值要有限，例如 topic、任务名、缓存名，不要用 ID 这类不断增长的值。
// metric 为空、metric 或 label 超过 MaxMetricLength、MaxLabelLength 字节时 panic。Close 之后的记录被丢弃。
func (r *Recorder) Record(metric, label string, v float64) {
	checkNames(metric, label)
	k := aggregateKey{metric, label, minute(r.now())}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	a := r.aggregates[k]
	if a == nil {
		r.aggregates[k] = &aggregate{metric: metric, label: label, bucket: k.bucket, count: 1, sum: v, max: v}
		return
	}
	a.count++
	a.sum += v
	a.max = max(a.max, v)
}

// Set 把 metric 中 label 的最新值设为 v，例如定时任务上一次执行的结果。v 编码为 JSON 保存，
// 多个实例先后记录同一个 label 时，以记录时间最晚的为准。
// 名称的要求与 Record 相同；v 无法编码为 JSON 时记录一条 error 日志并丢弃。
func (r *Recorder) Set(metric, label string, v any) {
	checkNames(metric, label)
	data, err := json.Marshal(v)
	if err != nil {
		r.logger.Error("encode monitor value failed", "metric", metric, "label", label, "error", err)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.values[valueKey{metric, label}] = value{metric: metric, label: label, data: data, at: r.now().UnixMicro()}
}

// Flush 立即写入内存中的记录。通常不需要调用：Recorder 定期写入，Close 时写入剩余的记录；测试中用它等待写入。
func (r *Recorder) Flush(ctx context.Context) error {
	r.mu.Lock()
	aggregates, values := r.aggregates, r.values
	r.aggregates, r.values = map[aggregateKey]*aggregate{}, map[valueKey]value{}
	r.mu.Unlock()

	rows := make([]aggregate, 0, len(aggregates))
	for _, a := range aggregates {
		rows = append(rows, *a)
	}
	latest := make([]value, 0, len(values))
	for _, v := range values {
		latest = append(latest, v)
	}
	return errors.Join(r.store.addAggregates(ctx, rows), r.store.setValues(ctx, latest))
}

// Close 停止定期写入，写入剩余的记录。之后的 Record 和 Set 被丢弃。可以登记到 cleanup.Stack。
func (r *Recorder) Close(ctx context.Context) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.mu.Unlock()
	close(r.stop)
	<-r.done
	return r.Flush(ctx)
}

// loop 每隔 flushInterval 写入一次，直到 Close。
func (r *Recorder) loop() {
	defer close(r.done)
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
			if err := r.Flush(ctx); err != nil {
				r.logger.Error("write monitor records failed", "error", err)
			}
			cancel()
		}
	}
}

// checkNames 检查 metric 和 label 符合表结构。
func checkNames(metric, label string) {
	switch {
	case metric == "":
		panic("monitor: empty metric")
	case len(metric) > MaxMetricLength:
		panic(errors.Newf("monitor: metric %.20q… is longer than %d bytes", metric, MaxMetricLength))
	case len(label) > MaxLabelLength:
		panic(errors.Newf("monitor: label %.20q… is longer than %d bytes", label, MaxLabelLength))
	}
}
