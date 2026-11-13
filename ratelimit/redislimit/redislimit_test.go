package redislimit_test

import (
	"testing"
	"time"

	"github.com/go-tenon/tenon/internal/redistest"
	"github.com/go-tenon/tenon/ratelimit"
	"github.com/go-tenon/tenon/ratelimit/redislimit"
)

func TestNewRejectsInvalidArguments(t *testing.T) {
	rdb := redistest.Standalone.Open(t)
	valid := ratelimit.Config{Rate: 1, Interval: time.Second, Burst: 1}
	for name, fn := range map[string]func(){
		"nil client":     func() { redislimit.New(nil, "x", valid) },
		"empty name":     func() { redislimit.New(rdb, "", valid) },
		"invalid config": func() { redislimit.New(rdb, "x", ratelimit.Config{}) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected panic")
				}
			}()
			fn()
		})
	}
}
