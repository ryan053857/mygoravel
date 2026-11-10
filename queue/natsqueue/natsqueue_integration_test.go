//go:build integration

package natsqueue_test

import (
	"crypto/rand"
	"os"
	"strings"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/database/sqlite"
	"github.com/ryan053857/mygoravel/internal/dbtesting"
	"github.com/ryan053857/mygoravel/internal/queuetesting"
	"github.com/ryan053857/mygoravel/queue"
	"github.com/ryan053857/mygoravel/queue/natsqueue"
)

func connect(t *testing.T) *nats.Conn {
	t.Helper()
	url := os.Getenv("NATS_TEST_URL")
	if url == "" {
		t.Fatal("NATS_TEST_URL is required for this test")
	}
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	return nc
}

func TestDriver(t *testing.T) {
	queuetesting.Run(t, queuetesting.Backend{
		New: func(t *testing.T) (queue.Driver, *bun.DB) {
			// 每个场景使用独立的流，测试之间互不影响。
			stream := "TEST_QUEUE_" + strings.ToUpper(rand.Text())
			return natsqueue.New(connect(t), stream), dbtesting.Open(t, sqlite.Driver, queue.Migrations())
		},
		NoFencing:    true,
		PartialStats: true,
	})
}
