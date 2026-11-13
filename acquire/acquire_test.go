package acquire_test

import (
	"context"
	"testing"
	"time"

	"github.com/go-tenon/tenon/acquire"
)

func TestTimeout(t *testing.T) {
	ctx := context.Background()
	if _, ok := acquire.Timeout(ctx); ok {
		t.Fatal("timeout without WithTimeout")
	}
	limited := acquire.WithTimeout(ctx, time.Second)
	if d, ok := acquire.Timeout(limited); !ok || d != time.Second {
		t.Fatalf("d=%v ok=%v", d, ok)
	}
	// 内层可以取消外层设置的时限，例如请求中启动的长任务。
	if _, ok := acquire.Timeout(acquire.WithTimeout(limited, 0)); ok {
		t.Fatal("zero did not clear the timeout")
	}
}
