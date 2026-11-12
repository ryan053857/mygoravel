package cachecmd_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/database/sqlite"
	"github.com/ryan053857/mygoravel/internal/dbtesting"
	"github.com/ryan053857/mygoravel/monitor"
	"github.com/ryan053857/mygoravel/monitor/cachemon"
	"github.com/ryan053857/mygoravel/monitor/cachemon/cachecmd"
)

func TestStats(t *testing.T) {
	ctx := context.Background()
	mon := monitor.New(dbtesting.Open(t, sqlite.Driver, monitor.Migrations()), nil)
	t.Cleanup(func() { _ = mon.Close(ctx) })
	for range 3 {
		mon.RecordDuration(cachemon.MetricHit, "articles", time.Millisecond)
	}
	mon.RecordDuration(cachemon.MetricMiss, "articles", 5*time.Millisecond)
	mon.RecordDuration(cachemon.MetricError, "sessions", 500*time.Millisecond)
	if err := mon.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	cmd := cachecmd.New(mon)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"stats", "--since", "24h"})
	if err := cmd.ExecuteContext(ctx); err != nil {
		t.Fatal(err)
	}
	var got []string
	for line := range strings.Lines(out.String()) {
		got = append(got, strings.Join(strings.Fields(line), " "))
	}
	want := []string{"最近 24h", "CACHE HITS MISSES ERRORS HIT RATE AVG GET", "articles 3 1 0 75.0% 2ms", "sessions 0 0 1 - -"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s", strings.Join(got, "\n"))
	}
}
