package redislimit_test

import (
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/internal/redistest"
	"github.com/ryan053857/mygoravel/ratelimit"
	"github.com/ryan053857/mygoravel/ratelimit/redislimit"
)

func TestNewRejectsInvalidArguments(t *testing.T) {
	rdb := redistest.Open(t)
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
