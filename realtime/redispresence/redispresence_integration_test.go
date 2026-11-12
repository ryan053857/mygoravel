//go:build integration

package redispresence_test

import (
	"context"
	"crypto/rand"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/realtime"
	"github.com/ryan053857/mygoravel/realtime/realtimetest"
	"github.com/ryan053857/mygoravel/realtime/redispresence"
)

func openRedis(t *testing.T) *redis.Client {
	t.Helper()
	url := os.Getenv("REDIS_TEST_URL")
	if url == "" {
		t.Fatal("REDIS_TEST_URL is required for this test")
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(opts)
	t.Cleanup(func() { rdb.Close() })
	return rdb
}

func TestStore(t *testing.T) {
	rdb := openRedis(t)
	realtimetest.TestPresenceStore(t, func(*testing.T) realtime.PresenceStore {
		return redispresence.New(rdb, "test:presence:"+rand.Text()+":")
	})
}

// 没有成员的频道不留下键：两个键都带过期时间，过期的连接在读取时从哈希中删除。
func TestStoreLeavesNoKeysBehind(t *testing.T) {
	ctx := context.Background()
	rdb := openRedis(t)
	prefix := "test:presence:" + rand.Text() + ":"
	s := redispresence.New(rdb, prefix)
	for _, client := range []string{"gone", "kept"} {
		if err := s.Add(ctx, realtime.PresenceEntry{Channel: "room:1", Client: client, Subject: identity.Ref{Type: "user", ID: "u"}}, 100*time.Millisecond); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Add(ctx, realtime.PresenceEntry{Channel: "room:1", Client: "kept", Subject: identity.Ref{Type: "user", ID: "u"}}, 400*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if got, err := s.List(ctx, "room:1"); err != nil || len(got) != 1 {
		t.Fatalf("entries = %+v, %v", got, err)
	}
	if fields := rdb.HKeys(ctx, prefix+"{room:1}:data").Val(); len(fields) != 1 || fields[0] != "kept" {
		t.Fatalf("hash fields after listing = %v", fields)
	}
	time.Sleep(400 * time.Millisecond)
	if keys := rdb.Keys(ctx, prefix+"*").Val(); len(keys) != 0 {
		t.Fatalf("keys left behind: %v", keys)
	}
}
