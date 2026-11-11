// Package redisqueue 是把任务保存在 Redis 中的队列驱动，实现 queue.Driver。
//
//	d := redisqueue.New(rdb, "blog:queue")
//	err := queue.Publish(ctx, d, "mail.send", msg)
//	sub := queue.NewSubscriber(d, cfg, logger, queue.WithFailedJobs(queue.NewFailedJobs(db, d)))
//
// Redis 不能加入数据库事务：需要与业务数据一起提交的任务请使用 queue.Database。
// 失败任务仍然写入数据库的 queue_failed_jobs，因此需要 WithFailedJobs。
package redisqueue

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/ryan053857/mygoravel/queue"
)

// Name 是驱动的名称，写入 queue_failed_jobs.driver。
const Name = "redis"

// Driver 为每个 topic 使用四个键，键名以 prefix:{topic}: 开头（花括号是哈希标签，同一 topic 的键在同一个集群槽位）：
//
//   - schedule：有序集合，成员是任务 ID，分数是可以领取的时间（Unix 毫秒）；任务被领取后分数是租约到期时间，
//     到期没有确认的任务会被重新领取。
//   - jobs：哈希，任务 ID 到任务内容。
//   - attempts：哈希，任务 ID 到已领取的次数，也是防护令牌：任务被重新领取后旧的领取不再生效。
//   - reserved：哈希，处理中的任务 ID 到领取时间。
//
// 每个操作是一次脚本调用，时间取 Redis 服务端时钟。任务完成后四个键中的记录都被删除，不需要清理。
// 另有一个集合 prefix:topics 记录出现过的 topic，供 Stats 使用。
type Driver struct {
	rdb    redis.UniversalClient
	prefix string
}

var _ queue.Driver = (*Driver)(nil)

// New 创建 Redis 队列驱动。prefix 是键名前缀，多个应用共用一个 Redis 时用它区分。
// rdb 为 nil 或 prefix 为空时 panic。
func New(rdb redis.UniversalClient, prefix string) *Driver {
	switch {
	case rdb == nil:
		panic("redisqueue: nil client")
	case prefix == "":
		panic("redisqueue: empty prefix")
	}
	return &Driver{rdb: rdb, prefix: prefix}
}

// Name 实现 queue.Driver。
func (d *Driver) Name() string { return Name }

func (d *Driver) keys(topic string) []string {
	base := d.prefix + ":{" + topic + "}:"
	return []string{base + "schedule", base + "jobs", base + "attempts", base + "reserved"}
}

// body 是保存在 jobs 哈希中的任务内容。
type body struct {
	Payload  []byte            `json:"payload"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// enqueue 写入一个任务；ID 已存在时返回 0。
var enqueue = redis.NewScript(`
if redis.call("HSETNX", KEYS[2], ARGV[1], ARGV[3]) == 0 then
	return 0
end
local at = tonumber(ARGV[2])
if at == 0 then
	local t = redis.call("TIME")
	at = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
end
redis.call("ZADD", KEYS[1], at, ARGV[1])
redis.call("HSET", KEYS[3], ARGV[1], 0)
return 1
`)

// Enqueue 实现 queue.Driver。同一个 ID 重复入队返回错误。
func (d *Driver) Enqueue(ctx context.Context, jobs ...queue.Job) error {
	for _, j := range jobs {
		data, err := json.Marshal(body{Payload: j.Payload, Metadata: j.Metadata})
		if err != nil {
			return err
		}
		var at int64
		if !j.AvailableAt.IsZero() {
			at = j.AvailableAt.UnixMilli()
		}
		added, err := enqueue.Run(ctx, d.rdb, d.keys(j.Topic), j.ID.String(), at, data).Int()
		if err != nil {
			return errors.Wrapf(err, "redisqueue: enqueue %s", j.ID)
		}
		if added == 0 {
			return errors.Newf("redisqueue: job %s already exists", j.ID)
		}
		// topic 集合与任务的键不在同一个集群槽位，单独写入；写入失败只影响 Stats。
		if err := d.rdb.SAdd(ctx, d.prefix+":topics", j.Topic).Err(); err != nil {
			return errors.Wrapf(err, "redisqueue: record topic %q", j.Topic)
		}
	}
	return nil
}

// stats 返回 {ready, delayed, retrying, running, 最早到期的分数或 -1}。
var stats = redis.NewScript(`
local t = redis.call("TIME")
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local ready = redis.call("ZCOUNT", KEYS[1], "-inf", now)
local oldest = redis.call("ZRANGEBYSCORE", KEYS[1], "-inf", now, "WITHSCORES", "LIMIT", 0, 1)
local delayed, retrying, running = 0, 0, 0
for _, id in ipairs(redis.call("ZRANGEBYSCORE", KEYS[1], "(" .. now, "+inf")) do
	if redis.call("HEXISTS", KEYS[4], id) == 1 then
		running = running + 1
	elseif tonumber(redis.call("HGET", KEYS[3], id) or "0") > 0 then
		retrying = retrying + 1
	else
		delayed = delayed + 1
	end
end
return {ready, delayed, retrying, running, oldest[2] or "-1"}
`)

// Stats 实现 queue.Driver。每个 topic 一次脚本调用，会遍历未到期的任务。
func (d *Driver) Stats(ctx context.Context) ([]queue.TopicStats, error) {
	topics, err := d.rdb.SMembers(ctx, d.prefix+":topics").Result()
	if err != nil {
		return nil, errors.Wrap(err, "redisqueue: stats")
	}
	slices.Sort(topics)
	var out []queue.TopicStats
	for _, topic := range topics {
		res, err := stats.Run(ctx, d.rdb, d.keys(topic)).Slice()
		if err != nil {
			return nil, errors.Wrapf(err, "redisqueue: stats of %q", topic)
		}
		jobs := map[queue.State]int{
			queue.StateReady: int(res[0].(int64)), queue.StateDelayed: int(res[1].(int64)),
			queue.StateRetrying: int(res[2].(int64)), queue.StateRunning: int(res[3].(int64)),
		}
		if jobs[queue.StateReady]+jobs[queue.StateDelayed]+jobs[queue.StateRetrying]+jobs[queue.StateRunning] == 0 {
			continue
		}
		s := queue.TopicStats{Topic: topic, Jobs: jobs}
		if ms, _ := strconv.ParseInt(res[4].(string), 10, 64); ms >= 0 {
			s.OldestReadyAt = time.UnixMilli(ms).UTC()
		}
		out = append(out, s)
	}
	return out, nil
}

// claim 领取最多 ARGV[1] 个到期的任务，租约 ARGV[2] 毫秒；返回 {id, attempts, body, 领取前的分数, ...}。
var claim = redis.NewScript(`
local t = redis.call("TIME")
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local due = redis.call("ZRANGEBYSCORE", KEYS[1], "-inf", now, "WITHSCORES", "LIMIT", 0, tonumber(ARGV[1]))
local out = {}
for i = 1, #due, 2 do
	local id = due[i]
	local data = redis.call("HGET", KEYS[2], id)
	if data then
		redis.call("ZADD", KEYS[1], now + tonumber(ARGV[2]), id)
		local attempts = redis.call("HINCRBY", KEYS[3], id, 1)
		redis.call("HSET", KEYS[4], id, now)
		table.insert(out, id)
		table.insert(out, attempts)
		table.insert(out, data)
		table.insert(out, due[i + 1])
	else
		redis.call("ZREM", KEYS[1], id)
	end
end
return out
`)

// Claim 实现 queue.Driver：每 req.PollInterval 查询一次，直到领取到任务。
// 单次领取不受 ctx 打断，ctx 只用于结束等待。
func (d *Driver) Claim(ctx context.Context, req queue.ClaimRequest) ([]queue.Claim, error) {
	for {
		claims, err := d.claim(req)
		if err != nil || len(claims) > 0 {
			return claims, err
		}
		t := time.NewTimer(req.PollInterval)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-t.C:
		}
	}
}

func (d *Driver) claim(req queue.ClaimRequest) ([]queue.Claim, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := claim.Run(ctx, d.rdb, d.keys(req.Topic), req.Max, req.Lease.Milliseconds()).Slice()
	if err != nil {
		return nil, errors.Wrapf(err, "redisqueue: claim %q", req.Topic)
	}
	claims := make([]queue.Claim, 0, len(res)/4)
	for i := 0; i+3 < len(res); i += 4 {
		id, err := uuid.Parse(res[i].(string))
		if err != nil {
			return claims, errors.Wrapf(err, "redisqueue: job id %v", res[i])
		}
		var b body
		if err := json.Unmarshal([]byte(res[i+2].(string)), &b); err != nil {
			return claims, errors.Wrapf(err, "redisqueue: decode job %s", id)
		}
		job := queue.Job{ID: id, Topic: req.Topic, Payload: b.Payload, Metadata: b.Metadata, Attempt: int(res[i+1].(int64))}
		if ms, err := strconv.ParseInt(res[i+3].(string), 10, 64); err == nil {
			job.AvailableAt = time.UnixMilli(ms).UTC()
		}
		claims = append(claims, &claimed{d: d, keys: d.keys(req.Topic), id: id.String(), attempts: res[i+1].(int64), job: job})
	}
	return claims, nil
}

// claimed 是一次领取。下面的脚本都先比较 attempts：任务已被重新领取或已删除时返回 0。
type claimed struct {
	d        *Driver
	keys     []string
	id       string
	attempts int64
	job      queue.Job
}

// held 检查 attempts 后按 ARGV[3] 执行：ack 删除任务；schedule 把分数设为 ARGV[4]
// （0 表示 Redis 当前时间加 ARGV[5] 毫秒），ARGV[6] 为 1 时清除领取标记，ARGV[7] 为 1 时 attempts 减一。
var held = redis.NewScript(`
if redis.call("HGET", KEYS[3], ARGV[1]) ~= ARGV[2] then
	return 0
end
if ARGV[3] == "ack" then
	redis.call("ZREM", KEYS[1], ARGV[1])
	redis.call("HDEL", KEYS[2], ARGV[1])
	redis.call("HDEL", KEYS[3], ARGV[1])
	redis.call("HDEL", KEYS[4], ARGV[1])
	return 1
end
local at = tonumber(ARGV[4])
if at == 0 then
	local t = redis.call("TIME")
	at = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000) + tonumber(ARGV[5])
end
redis.call("ZADD", KEYS[1], at, ARGV[1])
if ARGV[6] == "1" then
	redis.call("HDEL", KEYS[4], ARGV[1])
end
if ARGV[7] == "1" then
	redis.call("HINCRBY", KEYS[3], ARGV[1], -1)
end
return 1
`)

func (c *claimed) run(ctx context.Context, op string, at int64, after time.Duration, unreserve, uncount bool) error {
	flag := func(b bool) string {
		if b {
			return "1"
		}
		return "0"
	}
	ok, err := held.Run(ctx, c.d.rdb, c.keys, c.id, strconv.FormatInt(c.attempts, 10), op, at, after.Milliseconds(), flag(unreserve), flag(uncount)).Int()
	if err != nil {
		return errors.Wrapf(err, "redisqueue: %s job %s", op, c.id)
	}
	if ok == 0 {
		return queue.ErrLeaseLost
	}
	return nil
}

func (c *claimed) Job() queue.Job { return c.job }

func (c *claimed) Extend(ctx context.Context, lease time.Duration) error {
	return c.run(ctx, "schedule", 0, lease, false, false)
}

func (c *claimed) Ack(ctx context.Context) error {
	return c.run(ctx, "ack", 0, 0, false, false)
}

func (c *claimed) Retry(ctx context.Context, at time.Time) error {
	return c.run(ctx, "schedule", max(at.UnixMilli(), 1), 0, true, false)
}

func (c *claimed) Release(ctx context.Context) error {
	return c.run(ctx, "schedule", 0, 0, true, true)
}
