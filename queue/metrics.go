package queue

import (
	"context"
	"sync"
	"time"

	"github.com/cockroachdb/errors"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// meterName 是队列指标的 instrumentation scope。
const meterName = "github.com/ryan053857/mygoravel/queue"

// 指标的属性名。
const (
	attrDriver  = attribute.Key("queue.driver")
	attrTopic   = attribute.Key("queue.topic")
	attrState   = attribute.Key("queue.job.state")
	attrOutcome = attribute.Key("queue.job.outcome")
)

// secondBuckets 是等待时长和处理耗时的直方图分桶（秒），从 5 毫秒到 1 小时。
var secondBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 300, 900, 3600}

// WithMeterProvider 让订阅者把每个任务记录为 OpenTelemetry 指标，属性 queue.driver 和 queue.topic：
//
//   - queue.job.wait（直方图，秒）：任务从可以被领取到开始处理等了多久。驱动给不出可以被领取的时间时不记录，
//     例如 NATS 重新投递的任务。
//   - queue.job.duration（直方图，秒）：处理耗时，属性 queue.job.outcome 是结果：
//     succeeded、retrying、failed、released、lease_lost。按结果计数就是吞吐和失败率。
//
// 队列中的任务数见 ObserveJobs。mp 为 nil 时 panic。
func WithMeterProvider(mp metric.MeterProvider) SubscriberOption {
	if mp == nil {
		panic("queue: nil meter provider")
	}
	meter := mp.Meter(meterName)
	wait, err := meter.Float64Histogram("queue.job.wait", metric.WithUnit("s"),
		metric.WithDescription("Time a job waited between becoming available and starting."),
		metric.WithExplicitBucketBoundaries(secondBuckets...))
	if err != nil {
		panic(errors.Wrap(err, "queue: create queue.job.wait"))
	}
	duration, err := meter.Float64Histogram("queue.job.duration", metric.WithUnit("s"),
		metric.WithDescription("Time spent handling a job, by outcome."),
		metric.WithExplicitBucketBoundaries(secondBuckets...))
	if err != nil {
		panic(errors.Wrap(err, "queue: create queue.job.duration"))
	}
	return WithObserver(func(ctx context.Context, e Event) {
		job := attribute.NewSet(attrDriver.String(e.Driver), attrTopic.String(e.Job.Topic))
		if e.Kind == JobStarted {
			if !e.Job.AvailableAt.IsZero() {
				wait.Record(ctx, max(time.Since(e.Job.AvailableAt), 0).Seconds(), metric.WithAttributeSet(job))
			}
			return
		}
		attrs := append(job.ToSlice(), attrOutcome.String(e.Kind.String()))
		duration.Record(ctx, e.Duration.Seconds(), metric.WithAttributes(attrs...))
	})
}

// statsMaxAge 是 ObserveJobs 复用上一次读取结果的时间：多个采集器或频繁的采集不会反复扫描队列。
var statsMaxAge = 5 * time.Second

// ObserveJobs 把队列中的任务数注册为 OpenTelemetry 指标，在采集时读取 d.Stats 和 failed.Count，
// 属性 queue.driver 和 queue.topic：
//
//   - queue.jobs（gauge）：各状态的任务数，属性 queue.job.state 是 State 的取值。驱动给不出的状态不报告；
//     failed 为 nil 时不报告 failed。
//   - queue.job.oldest_ready_age（gauge，秒）：积压最久的任务已经等了多久，没有积压时为 0。
//     worker 跟不上时它会持续变大，适合作为告警条件。驱动给不出时不报告。
//
// 每次读取都统计整个队列（数据库驱动按 topic 分组统计 queue_jobs 表），每个实例读到的也都是同一份全局数据：
// 只在实例最少的一种进程中注册，例如调度器；汇总多个实例时取最大值而不是求和。
// 出现过的 topic 在任务清空后报告 0，而不是消失。5 秒内的多次采集复用同一次读取。
// 不再需要时调用返回值的 Unregister。
func ObserveJobs(mp metric.MeterProvider, d Driver, failed *FailedJobs) (metric.Registration, error) {
	if mp == nil || d == nil {
		panic("queue: nil meter provider or driver")
	}
	meter := mp.Meter(meterName)
	jobs, err := meter.Int64ObservableGauge("queue.jobs", metric.WithUnit("{job}"),
		metric.WithDescription("Jobs in the queue, by state."))
	if err != nil {
		return nil, errors.Wrap(err, "queue: create queue.jobs")
	}
	age, err := meter.Float64ObservableGauge("queue.job.oldest_ready_age", metric.WithUnit("s"),
		metric.WithDescription("How long the oldest ready job has been waiting."))
	if err != nil {
		return nil, errors.Wrap(err, "queue: create queue.job.oldest_ready_age")
	}
	s := &jobsSnapshot{driver: d, failed: failed, seen: map[string]map[State]bool{}}
	reg, err := meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		return s.observe(ctx, o, jobs, age)
	}, jobs, age)
	return reg, errors.Wrap(err, "queue: register queue metrics")
}

// jobsSnapshot 缓存最近一次读取的任务数，并记住出现过的 topic 和状态。
type jobsSnapshot struct {
	driver Driver
	failed *FailedJobs

	mu     sync.Mutex
	readAt time.Time
	stats  map[string]TopicStats
	seen   map[string]map[State]bool // 出现过的 topic 和它的状态，清空后报告 0
}

func (s *jobsSnapshot) observe(ctx context.Context, o metric.Observer, jobs metric.Int64Observable, age metric.Float64Observable) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Since(s.readAt) >= statsMaxAge {
		if err := s.read(ctx); err != nil {
			return err
		}
	}
	driver := attrDriver.String(s.driver.Name())
	for topic, states := range s.seen {
		st := s.stats[topic]
		attrs := []attribute.KeyValue{driver, attrTopic.String(topic)}
		for state := range states {
			o.ObserveInt64(jobs, int64(st.Jobs[state]), metric.WithAttributes(append(attrs, attrState.String(string(state)))...))
		}
		if !states[StateReady] {
			continue
		}
		switch {
		case st.Jobs[StateReady] == 0:
			o.ObserveFloat64(age, 0, metric.WithAttributes(attrs...))
		case !st.OldestReadyAt.IsZero():
			o.ObserveFloat64(age, max(time.Since(st.OldestReadyAt), 0).Seconds(), metric.WithAttributes(attrs...))
		}
	}
	return nil
}

// read 读取驱动和失败任务表，结果并入 s.stats 和 s.seen。
func (s *jobsSnapshot) read(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	stats, err := s.driver.Stats(ctx)
	if err != nil {
		return err
	}
	byTopic := make(map[string]TopicStats, len(stats))
	for _, st := range stats {
		byTopic[st.Topic] = st
	}
	if s.failed != nil {
		counts, err := s.failed.Count(ctx)
		if err != nil {
			return err
		}
		for topic, n := range counts {
			st := byTopic[topic]
			jobs := make(map[State]int, len(st.Jobs)+1)
			for k, v := range st.Jobs {
				jobs[k] = v
			}
			jobs[StateFailed] = n
			st.Topic, st.Jobs = topic, jobs
			byTopic[topic] = st
		}
	}
	for topic, st := range byTopic {
		if s.seen[topic] == nil {
			s.seen[topic] = map[State]bool{}
			if s.failed != nil { // 失败任务清空后 Count 不再返回这个 topic，仍要报告 0
				s.seen[topic][StateFailed] = true
			}
		}
		for state := range st.Jobs {
			s.seen[topic][state] = true
		}
	}
	s.stats, s.readAt = byTopic, time.Now()
	return nil
}
