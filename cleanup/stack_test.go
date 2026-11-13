package cleanup_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-tenon/tenon/cleanup"
)

func TestLIFOAndIdempotentClose(t *testing.T) {
	var order []string
	var s cleanup.Stack
	for _, name := range []string{"a", "b", "c"} {
		if err := s.Add(name, func(context.Context) error {
			order = append(order, name)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, "") != "cba" {
		t.Fatalf("order=%v", order)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, "") != "cba" {
		t.Fatalf("second close re-ran: %v", order)
	}
}

func TestPartialOpenCleansEarlierResources(t *testing.T) {
	var closed []string
	err := buildWithFailure(&closed)
	if err == nil || !strings.Contains(err.Error(), "open c") {
		t.Fatalf("want open failure, got %v", err)
	}
	if strings.Join(closed, ",") != "b,a" {
		t.Fatalf("closed=%v", closed)
	}
}

func buildWithFailure(closed *[]string) (err error) {
	var s cleanup.Stack
	defer func() { err = s.CloseWith(context.Background(), err) }()

	for _, name := range []string{"a", "b", "c"} {
		if name == "c" {
			return errors.New("open c")
		}
		if err := s.Add(name, func(context.Context) error {
			*closed = append(*closed, name)
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func TestCloseWithPreservesRunErrorAndJoinsCleanup(t *testing.T) {
	var s cleanup.Stack
	cleanErr := errors.New("close x")
	_ = s.Add("x", func(context.Context) error { return cleanErr })
	runErr := errors.New("serve failed")
	err := s.CloseWith(context.Background(), runErr)
	if err == nil {
		t.Fatal("expected combined error")
	}
	if !errors.Is(err, runErr) {
		t.Fatalf("run error lost: %v", err)
	}
	if !errors.Is(err, cleanErr) {
		t.Fatalf("cleanup error not attached: %v", err)
	}
	if !strings.Contains(err.Error(), "serve failed") {
		t.Fatalf("run error text missing: %q", err.Error())
	}
}

func TestCleanupIgnoresCanceledRequestContext(t *testing.T) {
	var s cleanup.Stack
	started := make(chan struct{})
	_ = s.Add("slow", func(ctx context.Context) error {
		close(started)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
			return nil
		}
	})
	req, cancel := context.WithCancel(context.Background())
	cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- s.Close(req) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not start")
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("cleanup should not fail from canceled request: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cleanup hung")
	}
}

func TestNoAddAfterClose(t *testing.T) {
	var s cleanup.Stack
	_ = s.Close(context.Background())
	err := s.Add("x", func(context.Context) error { return nil })
	if err == nil {
		t.Fatal("expected error")
	}
}
