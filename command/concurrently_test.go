package command_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/errors"

	"github.com/ryan053857/mygoravel/command"
)

// blocking 一直运行到 ctx 取消，然后返回 result，并记录是否被取消。
func blocking(stopped *atomic.Int64, result error) func(context.Context) error {
	return func(ctx context.Context) error {
		<-ctx.Done()
		stopped.Add(1)
		return result
	}
}

func TestConcurrentlyStopsAllOnCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	var stopped atomic.Int64
	done := make(chan error, 1)
	go func() {
		done <- command.Concurrently(ctx, blocking(&stopped, nil), blocking(&stopped, ctx.Err()), blocking(&stopped, context.Canceled))
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Concurrently did not return")
	}
	if stopped.Load() != 3 {
		t.Fatalf("stopped %d of 3", stopped.Load())
	}
}

func TestConcurrentlyErrorStopsOthers(t *testing.T) {
	t.Parallel()
	boom := errors.New("listen: address in use")
	var stopped atomic.Int64
	err := command.Concurrently(context.Background(),
		blocking(&stopped, nil),
		func(context.Context) error { return boom },
		blocking(&stopped, context.Canceled),
	)
	if !errors.Is(err, boom) || errors.Is(err, command.ErrStoppedEarly) || errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if stopped.Load() != 2 {
		t.Fatalf("stopped %d of 2", stopped.Load())
	}
}

func TestConcurrentlyEarlyReturnIsError(t *testing.T) {
	t.Parallel()
	var stopped atomic.Int64
	err := command.Concurrently(context.Background(),
		blocking(&stopped, nil),
		func(context.Context) error { return nil },
	)
	if !errors.Is(err, command.ErrStoppedEarly) || stopped.Load() != 1 {
		t.Fatalf("err = %v, stopped %d", err, stopped.Load())
	}
	if command.ExitCode(err) == 0 {
		t.Fatal("early return should exit non-zero")
	}
}

func TestConcurrentlyJoinsErrors(t *testing.T) {
	t.Parallel()
	a, b := errors.New("a"), errors.New("b")
	release := make(chan struct{})
	err := command.Concurrently(context.Background(),
		func(context.Context) error { return a },
		func(ctx context.Context) error {
			<-ctx.Done()
			close(release)
			return b // 停止时出错，例如关闭连接失败
		},
	)
	<-release
	if !errors.Is(err, a) || !errors.Is(err, b) {
		t.Fatalf("err = %v", err)
	}
}

func TestConcurrentlyWithoutRuns(t *testing.T) {
	t.Parallel()
	if err := command.Concurrently(context.Background()); err != nil {
		t.Fatalf("err = %v", err)
	}
}
