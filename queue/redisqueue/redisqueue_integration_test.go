package redisqueue_test

import (
	"context"
	"crypto/rand"
	"maps"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/database/dbtest"
	"github.com/ryan053857/mygoravel/queue"
	"github.com/ryan053857/mygoravel/queue/queuetest"
	"github.com/ryan053857/mygoravel/queue/redisqueue"
)

func openRedis(t *testing.T) *redis.Client {
	t.Helper()
	url := os.Getenv("REDIS_TEST_URL")
	if url == "" {
		t.Fatal("REDIS_TEST_URL is required for this test")
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(opts)
	t.Cleanup(func() { rdb.Close() })
	return rdb
}

// newDriver 使用随机前缀，测试之间的键互不影响，不需要清空 Redis。
func newDriver(t *testing.T) (*redisqueue.Driver, *redis.Client, string) {
	rdb := openRedis(t)
	prefix := "test:queue:" + rand.Text()
	return redisqueue.New(rdb, prefix), rdb, prefix
}

func TestDriver(t *testing.T) {
	queuetest.TestDriver(t, queuetest.Options{New: func(t *testing.T) (queue.Driver, *bun.DB) {
		d, _, _ := newDriver(t)
		// 失败任务写入数据库。
		return d, dbtest.Open(t, queue.Migrations())
	}})
}

func TestDuplicateIDIsRejected(t *testing.T) {
	ctx := context.Background()
	d, _, _ := newDriver(t)
	j := queue.Job{ID: uuid.New(), Topic: "jobs", Payload: []byte("{}")}
	if err := d.Enqueue(ctx, j); err != nil {
		t.Fatal(err)
	}
	if err := d.Enqueue(ctx, j); err == nil {
		t.Fatal("duplicate ID accepted")
	}
}

// 任务完成后不在 Redis 中留下任何键。
func TestFinishedJobsLeaveNoKeys(t *testing.T) {
	ctx := context.Background()
	d, rdb, prefix := newDriver(t)
	if err := d.Enqueue(ctx, queue.Job{ID: uuid.New(), Topic: "jobs", Payload: []byte("{}")}); err != nil {
		t.Fatal(err)
	}
	claims, err := d.Claim(ctx, queue.ClaimRequest{Topic: "jobs", Max: 1, Lease: time.Minute, PollInterval: 10 * time.Millisecond})
	if err != nil || len(claims) != 1 {
		t.Fatalf("claims %d err %v", len(claims), err)
	}
	if n, _ := rdb.Exists(ctx, prefix+":{jobs}:leases").Result(); n != 1 {
		t.Fatal("claimed job not recorded in leases")
	}
	// 重试一次再完成，retries 中的记录也要删除。
	if err := claims[0].Retry(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if n, _ := rdb.Exists(ctx, prefix+":{jobs}:retries").Result(); n != 1 {
		t.Fatal("retried job not recorded in retries")
	}
	claims, err = d.Claim(ctx, queue.ClaimRequest{Topic: "jobs", Max: 1, Lease: time.Minute, PollInterval: 10 * time.Millisecond})
	if err != nil || len(claims) != 1 {
		t.Fatalf("claims %d err %v", len(claims), err)
	}
	if err := claims[0].Ack(ctx); err != nil {
		t.Fatal(err)
	}
	// 只剩记录 topic 的集合。
	if keys, _ := rdb.Keys(ctx, prefix+"*").Result(); len(keys) != 1 || keys[0] != prefix+":topics" {
		t.Fatalf("keys left: %v", keys)
	}
}

// 租约过期的任务和到期的重试算作 ready，不再算作 running 和 retrying。
func TestStatsAtBoundaries(t *testing.T) {
	ctx := context.Background()
	d, _, _ := newDriver(t)
	claim := func() queue.Claim {
		t.Helper()
		claims, err := d.Claim(ctx, queue.ClaimRequest{Topic: "jobs", Max: 1, Lease: 200 * time.Millisecond, PollInterval: 10 * time.Millisecond})
		if err != nil || len(claims) != 1 {
			t.Fatalf("claims %d err %v", len(claims), err)
		}
		return claims[0]
	}
	for range 2 {
		if err := d.Enqueue(ctx, queue.Job{ID: uuid.New(), Topic: "jobs", Payload: []byte("{}")}); err != nil {
			t.Fatal(err)
		}
	}
	claim()                                                                          // 租约 200 毫秒后过期
	if err := claim().Retry(ctx, time.Now().Add(200*time.Millisecond)); err != nil { // 200 毫秒后重试
		t.Fatal(err)
	}
	stats, err := d.Stats(ctx)
	if err != nil || len(stats) != 1 || stats[0].Jobs[queue.StateRunning] != 1 || stats[0].Jobs[queue.StateRetrying] != 1 ||
		stats[0].Jobs[queue.StateDelayed] != 0 || stats[0].Jobs[queue.StateReady] != 0 {
		t.Fatalf("before expiry: %+v %v", stats, err)
	}
	time.Sleep(300 * time.Millisecond)
	stats, err = d.Stats(ctx)
	want := map[queue.State]int{queue.StateReady: 2, queue.StateDelayed: 0, queue.StateRetrying: 0, queue.StateRunning: 0}
	if err != nil || len(stats) != 1 || !maps.Equal(stats[0].Jobs, want) {
		t.Fatalf("after expiry: %+v %v", stats, err)
	}
}
