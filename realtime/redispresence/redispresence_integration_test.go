package redispresence_test

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/redis/rueidis"

	"github.com/go-tenon/tenon/identity"
	"github.com/go-tenon/tenon/internal/redistest"
	"github.com/go-tenon/tenon/realtime"
	"github.com/go-tenon/tenon/realtime/realtimetest"
	"github.com/go-tenon/tenon/realtime/redispresence"
)

func TestStore(t *testing.T) {
	for _, srv := range redistest.Servers {
		t.Run(srv.Name, func(t *testing.T) {
			rdb := srv.Open(t)
			realtimetest.TestPresenceStore(t, func(*testing.T) realtime.PresenceStore {
				return redispresence.New(rdb, "test:presence:"+rand.Text()+":")
			})
		})
	}
}

// 没有成员的频道不留下键：两个键都带过期时间，过期的连接在读取时从哈希中删除。
func TestStoreLeavesNoKeysBehind(t *testing.T) {
	for _, srv := range redistest.Servers {
		t.Run(srv.Name, func(t *testing.T) { testStoreLeavesNoKeysBehind(t, srv.Open(t)) })
	}
}

func testStoreLeavesNoKeysBehind(t *testing.T, rdb rueidis.Client) {
	ctx := context.Background()
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
	if fields, _ := rdb.Do(ctx, rdb.B().Hkeys().Key(prefix+"{room:1}:data").Build()).AsStrSlice(); len(fields) != 1 || fields[0] != "kept" {
		t.Fatalf("hash fields after listing = %v", fields)
	}
	time.Sleep(400 * time.Millisecond)
	// 两个键在同一个槽，集群中也能用一条 EXISTS 检查。
	if n, err := rdb.Do(ctx, rdb.B().Exists().Key(prefix+"{room:1}:exp", prefix+"{room:1}:data").Build()).AsInt64(); err != nil || n != 0 {
		t.Fatalf("keys left behind: %d, %v", n, err)
	}
}
