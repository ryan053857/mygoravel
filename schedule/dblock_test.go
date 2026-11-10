package schedule_test

import (
	"context"
	"io/fs"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/database/dbtest"
	"github.com/ryan053857/mygoravel/database/sqlite"
	"github.com/ryan053857/mygoravel/lock"
	"github.com/ryan053857/mygoravel/schedule"
)

// testDrivers 是数据库锁测试运行的数据库，integration 标签追加 PostgreSQL。
var testDrivers = []database.Driver{sqlite.Driver}

// 多个实例共用数据库锁时，每次触发只执行一次。
func TestDatabaseLockRunsOncePerTick(t *testing.T) {
	for _, driver := range testDrivers {
		t.Run(driver.Name(), func(t *testing.T) {
			migrations, err := fs.Sub(lock.Migrations(), driver.Name())
			if err != nil {
				t.Fatal(err)
			}
			db := dbtest.Open(t, driver, migrations)
			var n atomic.Int64
			scheds := make([]*schedule.Scheduler, 3)
			for i := range scheds {
				scheds[i] = schedule.New(lock.New(lock.NewDBStore(db)), nil)
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
		})
	}
}
