// Package natsqueue 是把任务保存在 NATS JetStream 中的队列驱动，实现 queue.Driver。
//
//	d := natsqueue.New(nc, "BLOG_QUEUE")
//	err := queue.Publish(ctx, d, "mail.send", msg)
//	sub := queue.NewSubscriber(d, cfg, logger, queue.WithFailedJobs(queue.NewFailedJobs(db, d)))
//
// 需要 NATS 2.12 或更高版本并开启 JetStream，延迟任务使用服务器的定时消息；版本过低时发布和领取都返回错误。
// NATS 不能加入数据库事务：需要与业务数据一起提交的任务请使用 queue.Database。
// 失败任务仍然写入数据库的 queue_failed_jobs，因此需要 WithFailedJobs。
package natsqueue

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/ryan053857/mygoravel/queue"
)

// Name 是驱动的名称，写入 queue_failed_jobs.driver。
const Name = "nats"

// headerID 是保存任务 ID 的消息头。
const headerID = "Queue-Job-Id"

// minPoll 是一次拉取请求最短的等待时间。
const minPoll = 100 * time.Millisecond

// Driver 把任务保存在名为 stream 的 JetStream 流中，流由驱动在第一次使用时创建或更新：
// 任务发布到主题 stream.jobs.<topic>，延迟任务先作为定时消息发布到 stream.schedules.<任务 ID>，
// 到期后由服务器转发。流使用工作队列保留策略，任务确认后即被删除。
//
// 每个 topic 一个持久的拉取消费者，确认等待时间（AckWait）就是租约，取自进程中第一次领取时的 Config.Lease，
// 修改配置后重启即可生效；处理期间用 InProgress 续租，
// 重试用带延迟的 Nak，执行次数取自投递次数。NATS 无法拒绝已过期租约的确认，
// 租约过期后被重新投递的任务，原来的持有者仍能确认它；停止超时释放的任务也会计入一次投递。
type Driver struct {
	js       jetstream.JetStream
	stream   string
	replicas int

	mu        sync.Mutex
	ready     bool
	consumers map[string]jetstream.Consumer
}

var _ queue.Driver = (*Driver)(nil)

// Option 调整 New 创建的驱动。
type Option func(*Driver)

// Replicas 设置流的副本数，默认 1。集群部署时建议 3。
func Replicas(n int) Option { return func(d *Driver) { d.replicas = n } }

// New 创建 NATS 队列驱动。stream 是流的名称，例如 BLOG_QUEUE，同一个应用的实例必须相同，
// 只能包含字母、数字、- 和 _。连接由应用创建和关闭。nc 为 nil 或 stream 不合法时 panic。
func New(nc *nats.Conn, stream string, opts ...Option) *Driver {
	if nc == nil {
		panic("natsqueue: nil connection")
	}
	if stream == "" || strings.Trim(stream, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_") != "" {
		panic(fmt.Sprintf("natsqueue: invalid stream name %q", stream))
	}
	js, err := jetstream.New(nc)
	if err != nil {
		panic("natsqueue: " + err.Error())
	}
	d := &Driver{js: js, stream: stream, replicas: 1, consumers: map[string]jetstream.Consumer{}}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// Name 实现 queue.Driver。
func (d *Driver) Name() string { return Name }

// validTopic 检查 topic 可以用作 NATS 主题的一部分：不为空，不含空白和通配符，各段不为空。
func validTopic(topic string) error {
	if topic == "" || strings.ContainsAny(topic, " \t\r\n*>") || strings.HasPrefix(topic, ".") ||
		strings.HasSuffix(topic, ".") || strings.Contains(topic, "..") {
		return errors.Newf("natsqueue: invalid topic %q", topic)
	}
	return nil
}

func (d *Driver) subject(topic string) string { return d.stream + ".jobs." + topic }

// ensureStream 创建或更新流，成功一次后不再重复。
func (d *Driver) ensureStream(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ready {
		return nil
	}
	stream, err := d.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:              d.stream,
		Subjects:          []string{d.stream + ".jobs.>", d.stream + ".schedules.>"},
		Retention:         jetstream.WorkQueuePolicy,
		Storage:           jetstream.FileStorage,
		Replicas:          d.replicas,
		AllowMsgSchedules: true,
	})
	if err != nil {
		return errors.Wrapf(err, "natsqueue: create stream %s", d.stream)
	}
	// 不支持定时消息的服务器（2.12 之前）会忽略这个设置，延迟任务将永远不会被投递。
	if !stream.CachedInfo().Config.AllowMsgSchedules {
		return errors.Newf("natsqueue: NATS server does not support scheduled messages (requires 2.12 or later)")
	}
	d.ready = true
	return nil
}

// body 是消息内容。
type body struct {
	Payload  []byte            `json:"payload"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// Enqueue 实现 queue.Driver。延迟任务按秒向上取整，不会早于 AvailableAt 被领取。
// NATS 不检查任务 ID 是否重复。
func (d *Driver) Enqueue(ctx context.Context, jobs ...queue.Job) error {
	if err := d.ensureStream(ctx); err != nil {
		return err
	}
	for _, j := range jobs {
		if err := validTopic(j.Topic); err != nil {
			return err
		}
		data, err := json.Marshal(body{Payload: j.Payload, Metadata: j.Metadata})
		if err != nil {
			return err
		}
		msg := &nats.Msg{Subject: d.subject(j.Topic), Data: data, Header: nats.Header{}}
		msg.Header.Set(headerID, j.ID.String())
		var opts []jetstream.PublishOpt
		if at := j.AvailableAt; at.After(time.Now()) {
			if at.Truncate(time.Second) != at {
				at = at.Truncate(time.Second).Add(time.Second)
			}
			msg.Subject = d.stream + ".schedules." + j.ID.String()
			opts = append(opts, jetstream.WithScheduleAt(at), jetstream.WithScheduleTarget(d.subject(j.Topic)))
		}
		if _, err := d.js.PublishMsg(ctx, msg, opts...); err != nil {
			return errors.Wrapf(err, "natsqueue: publish %s", j.ID)
		}
	}
	return nil
}

// consumer 返回 topic 的持久消费者。进程中第一次使用时创建或更新它，确认等待时间设为 lease；
// 之后不再修改，避免不同的租约反复改动正在等待确认的任务。
func (d *Driver) consumer(ctx context.Context, topic string, lease time.Duration) (jetstream.Consumer, error) {
	if err := d.ensureStream(ctx); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if c, ok := d.consumers[topic]; ok {
		return c, nil
	}
	h := fnv.New32a()
	h.Write([]byte(topic))
	name := fmt.Sprintf("queue_%s_%08x", strings.ReplaceAll(topic, ".", "_"), h.Sum32())
	c, err := d.js.CreateOrUpdateConsumer(ctx, d.stream, jetstream.ConsumerConfig{
		Durable:       name,
		FilterSubject: d.subject(topic),
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       lease,
		MaxDeliver:    -1,
	})
	if err != nil {
		return nil, errors.Wrapf(err, "natsqueue: create consumer for %q", topic)
	}
	d.consumers[topic] = c
	return c, nil
}

// Claim 实现 queue.Driver：先取已经到达的任务；没有时发出最多等待 PollInterval 的拉取请求，
// 任务到达时立即返回。拉取请求总是等到结束，不会在服务器上留下没人接收的请求，
// 因此 ctx 取消后最多再等一个 PollInterval 才返回。
func (d *Driver) Claim(ctx context.Context, req queue.ClaimRequest) ([]queue.Claim, error) {
	if err := validTopic(req.Topic); err != nil {
		return nil, err
	}
	c, err := d.consumer(ctx, req.Topic, req.Lease)
	if err != nil {
		return nil, err
	}
	for {
		claims, err := d.fetchNoWait(c, req.Topic, req.Max)
		if err != nil || len(claims) > 0 {
			return claims, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		batch, err := c.Fetch(1, jetstream.FetchMaxWait(max(req.PollInterval, minPoll)))
		if err != nil {
			return nil, errors.Wrapf(err, "natsqueue: fetch %q", req.Topic)
		}
		for m := range batch.Messages() {
			claims = append(claims, d.claims(req.Topic, m)...)
		}
		if err := batch.Error(); err != nil && !errors.Is(err, nats.ErrTimeout) && !errors.Is(err, jetstream.ErrNoMessages) {
			return claims, errors.Wrapf(err, "natsqueue: fetch %q", req.Topic)
		}
		if len(claims) > 0 {
			more, err := d.fetchNoWait(c, req.Topic, req.Max-len(claims))
			return append(claims, more...), err
		}
	}
}

// fetchNoWait 取最多 n 个已经到达的任务，不等待。
func (d *Driver) fetchNoWait(c jetstream.Consumer, topic string, n int) ([]queue.Claim, error) {
	if n <= 0 {
		return nil, nil
	}
	batch, err := c.FetchNoWait(n)
	if err != nil {
		return nil, errors.Wrapf(err, "natsqueue: fetch %q", topic)
	}
	var claims []queue.Claim
	for m := range batch.Messages() {
		claims = append(claims, d.claims(topic, m)...)
	}
	if err := batch.Error(); err != nil && !errors.Is(err, nats.ErrTimeout) && !errors.Is(err, jetstream.ErrNoMessages) {
		return claims, errors.Wrapf(err, "natsqueue: fetch %q", topic)
	}
	return claims, nil
}

// claims 把消息转换为领取；无法解析的消息被终止，不再投递。
func (d *Driver) claims(topic string, m jetstream.Msg) []queue.Claim {
	id, err := uuid.Parse(m.Headers().Get(headerID))
	var b body
	if err == nil {
		err = json.Unmarshal(m.Data(), &b)
	}
	meta, merr := m.Metadata()
	if err != nil || merr != nil {
		_ = m.Term()
		return nil
	}
	return []queue.Claim{&claimed{m: m, job: queue.Job{
		ID: id, Topic: topic, Payload: b.Payload, Metadata: b.Metadata, Attempt: int(meta.NumDelivered),
	}}}
}

type claimed struct {
	m   jetstream.Msg
	job queue.Job
}

func (c *claimed) Job() queue.Job { return c.job }

// Extend 重置确认等待时间。租约长度由消费者的 AckWait 决定，lease 只在创建消费者时生效。
func (c *claimed) Extend(context.Context, time.Duration) error { return c.m.InProgress() }

func (c *claimed) Ack(ctx context.Context) error { return c.m.DoubleAck(ctx) }

func (c *claimed) Retry(_ context.Context, at time.Time) error {
	if d := time.Until(at); d > 0 {
		return c.m.NakWithDelay(d)
	}
	return c.m.Nak()
}

func (c *claimed) Release(context.Context) error { return c.m.Nak() }
