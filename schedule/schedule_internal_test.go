package schedule

import (
	"context"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/lock"
)

func TestRunWaitsThenCancelsTasks(t *testing.T) {
	for _, tc := range []struct {
		name    string
		work    time.Duration
		timeout time.Duration
		cause   error
	}{
		{"finishes", 300 * time.Millisecond, time.Second, nil},
		{"canceled after timeout", time.Hour, 200 * time.Millisecond, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := New(lock.New(lock.NewMemoryStore()), nil)
			s.shutdownTimeout = tc.timeout
			started := make(chan struct{}, 1)
			result := make(chan error, 1)
			s.Add("slow", "@everysecond", func(ctx context.Context) error {
				select {
				case started <- struct{}{}:
				default:
					return nil
				}
				select {
				case <-time.After(tc.work):
					result <- ctx.Err()
				case <-ctx.Done():
					result <- ctx.Err()
				}
				return nil
			})
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- s.Run(ctx) }()
			<-started
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-result:
				if err != tc.cause {
					t.Fatalf("task ctx err = %v, want %v", err, tc.cause)
				}
			default:
				t.Fatal("Run returned before the task finished")
			}
		})
	}
}
