//go:build integration

package redisqueue_test

import (
	"context"
	"crypto/rand"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/database/sqlite"
	"github.com/ryan053857/mygoravel/internal/dbtesting"
	"github.com/ryan053857/mygoravel/internal/queuetesting"
	"github.com/ryan053857/mygoravel/queue"
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
	queuetesting.Run(t, queuetesting.Backend{New: func(t *testing.T) (queue.Driver, *bun.DB) {
		d, _, _ := newDriver(t)
		// 失败任务写入数据库，这里用 SQLite。
		return d, dbtesting.Open(t, sqlite.Driver, queue.Migrations())
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
	if n, _ := rdb.Exists(ctx, prefix+":{jobs}:reserved").Result(); n != 1 {
		t.Fatal("claimed job not marked reserved")
	}
	if err := claims[0].Ack(ctx); err != nil {
		t.Fatal(err)
	}
	if keys, _ := rdb.Keys(ctx, prefix+"*").Result(); len(keys) != 0 {
		t.Fatalf("keys left: %v", keys)
	}
}
