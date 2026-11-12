package schedule_test

import (
	"context"
	"crypto/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/internal/redistest"
	"github.com/ryan053857/mygoravel/lock"
	"github.com/ryan053857/mygoravel/lock/redislock"
	"github.com/ryan053857/mygoravel/schedule"
)

// 多个实例共用 Redis 锁时，每次触发只执行一次。
func TestRedisLockRunsOncePerTick(t *testing.T) {
	rdb := redistest.Open(t)
	name := "test:" + rand.Text() // 锁名包含任务名，随机的任务名让重复运行的测试互不影响
	var n atomic.Int64
	scheds := make([]*schedule.Scheduler, 3)
	for i := range scheds {
		scheds[i] = schedule.New(lock.New(redislock.New(rdb)), schedule.DefaultConfig(), nil)
		scheds[i].Add(name, "@everysecond", counter(&n))
	}
	for tick := range 3 {
		at := base.Add(time.Duration(tick) * time.Second)
		var wg sync.WaitGroup
		for _, s := range scheds {
			wg.Go(func() {
				if err := s.RunDue(context.Background(), at); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
	}
	if n.Load() != 3 {
		t.Fatalf("ran %d times, want 3", n.Load())
	}
}
