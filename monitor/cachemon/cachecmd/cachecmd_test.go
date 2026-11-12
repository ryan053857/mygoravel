package cachecmd_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ryan053857/mygoravel/database/sqlite"
	"github.com/ryan053857/mygoravel/internal/dbtesting"
	"github.com/ryan053857/mygoravel/monitor"
	"github.com/ryan053857/mygoravel/monitor/cachemon"
	"github.com/ryan053857/mygoravel/monitor/cachemon/cachecmd"
)

func TestStats(t *testing.T) {
	ctx := context.Background()
	stats := monitor.NewStore(dbtesting.Open(t, sqlite.Driver, monitor.Migrations()))
	rec := monitor.NewRecorder(stats, nil)
	t.Cleanup(func() { _ = rec.Close(ctx) })
	for range 3 {
		rec.Record(cachemon.MetricHit, "articles", 0.001)
	}
	rec.Record(cachemon.MetricMiss, "articles", 0.001)
	rec.Record(cachemon.MetricError, "sessions", 0.5)
	if err := rec.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	cmd := cachecmd.New(stats)
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
	want := []string{"最近 24h", "CACHE HITS MISSES ERRORS HIT RATE", "articles 3 1 0 75.0%", "sessions 0 0 1 -"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s", strings.Join(got, "\n"))
	}
}
