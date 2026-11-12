package schedule_test

import (
	"context"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/database/dbtest"
	"github.com/ryan053857/mygoravel/lock"
	"github.com/ryan053857/mygoravel/schedule"
)

func openLockDB(t *testing.T) *lock.Locker {
	t.Helper()
	return lock.New(lock.NewDatabase(dbtest.Open(t, lock.Migrations())))
}

// 最长的任务名在最大的触发时刻生成的锁名，也不超过数据库锁存储的上限。
func TestLongestNameFitsDatabaseLock(t *testing.T) {
	var n atomic.Int64
	s := schedule.New(openLockDB(t), schedule.DefaultConfig(), nil)
	s.Add(strings.Repeat("x", schedule.MaxNameLength), "@every 1s", counter(&n))
	for _, at := range []time.Time{time.Unix(math.MaxInt64, 0), time.Unix(math.MinInt64+1, 0)} {
		if err := s.RunDue(context.Background(), at); err != nil {
			t.Fatalf("%d: %v", at.Unix(), err)
		}
	}
	if n.Load() != 2 {
		t.Fatalf("ran %d times", n.Load())
	}
}

// 多个实例共用数据库锁时，每次触发只执行一次。
func TestDatabaseLockRunsOncePerTick(t *testing.T) {
	db := dbtest.Open(t, lock.Migrations())
	var n atomic.Int64
	scheds := make([]*schedule.Scheduler, 3)
	for i := range scheds {
		scheds[i] = schedule.New(lock.New(lock.NewDatabase(db)), schedule.DefaultConfig(), nil)
		scheds[i].Add("cleanup", "@everysecond", counter(&n))
	}
	for tick := range 3 {
		at := base.Add(time.Duration(tick) * time.Second)
		var wg sync.WaitGroup
		for _, s := range scheds {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := s.RunDue(context.Background(), at); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
	}
	if n.Load() != 3 {
		t.Fatalf("ran %d times, want 3", n.Load())
	}
}
