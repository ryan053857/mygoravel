// Package realtimetest 提供测试用的内存在线记录存储，以及 realtime.PresenceStore 实现的契约测试。
package realtimetest

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/realtime"
)

var (
	alice = identity.Ref{Type: "user", ID: "alice"}
	bob   = identity.Ref{Type: "user", ID: "bob"}
	// staffAlice 与 alice 的 ID 相同，属于另一套账号体系，是另一个主体。
	staffAlice = identity.Ref{Type: "staff", ID: "alice"}
)

// TestPresenceStore 检查 PresenceStore 的共同行为。newStore 每次返回一个空的存储，
// 或者使用独立命名空间的存储，测试之间互不干扰。
func TestPresenceStore(t *testing.T, newStore func(t *testing.T) realtime.PresenceStore) {
	ctx := context.Background()
	add := func(t *testing.T, s realtime.PresenceStore, e realtime.PresenceEntry, ttl time.Duration) {
		t.Helper()
		if err := s.Add(ctx, e, ttl); err != nil {
			t.Fatal(err)
		}
	}
	list := func(t *testing.T, s realtime.PresenceStore, channel string) []realtime.PresenceEntry {
		t.Helper()
		entries, err := s.List(ctx, channel)
		if err != nil {
			t.Fatal(err)
		}
		slices.SortFunc(entries, func(a, b realtime.PresenceEntry) int { return strings.Compare(a.Client, b.Client) })
		return entries
	}

	t.Run("lists the connections of a channel", func(t *testing.T) {
		s := newStore(t)
		add(t, s, realtime.PresenceEntry{Channel: "room:1", Client: "c1", Subject: alice, Info: []byte(`{"name":"Alice"}`)}, time.Minute)
		add(t, s, realtime.PresenceEntry{Channel: "room:1", Client: "c2", Subject: alice}, time.Minute)
		add(t, s, realtime.PresenceEntry{Channel: "room:1", Client: "c3", Subject: staffAlice}, time.Minute)
		add(t, s, realtime.PresenceEntry{Channel: "room:2", Client: "c1", Subject: alice}, time.Minute)

		got := list(t, s, "room:1")
		if len(got) != 3 {
			t.Fatalf("entries = %+v", got)
		}
		if e := got[0]; e.Channel != "room:1" || e.Client != "c1" || e.Subject != alice || string(e.Info) != `{"name":"Alice"}` {
			t.Fatalf("first entry = %+v", e)
		}
		if e := got[1]; e.Client != "c2" || e.Subject != alice || len(e.Info) != 0 {
			t.Fatalf("entry without info = %+v", e)
		}
		if e := got[2]; e.Client != "c3" || e.Subject != staffAlice {
			t.Fatalf("entry of another account system = %+v", e)
		}
		if got := list(t, s, "room:2"); len(got) != 1 || got[0].Client != "c1" {
			t.Fatalf("room:2 = %+v", got)
		}
		if got := list(t, s, "room:3"); len(got) != 0 {
			t.Fatalf("empty channel = %+v", got)
		}
	})

	t.Run("adding again replaces the entry", func(t *testing.T) {
		s := newStore(t)
		add(t, s, realtime.PresenceEntry{Channel: "room:1", Client: "c1", Subject: alice, Info: []byte(`{"v":1}`)}, time.Minute)
		add(t, s, realtime.PresenceEntry{Channel: "room:1", Client: "c1", Subject: alice, Info: []byte(`{"v":2}`)}, time.Minute)
		if got := list(t, s, "room:1"); len(got) != 1 || string(got[0].Info) != `{"v":2}` {
			t.Fatalf("entries = %+v", got)
		}
	})

	t.Run("removes one connection", func(t *testing.T) {
		s := newStore(t)
		add(t, s, realtime.PresenceEntry{Channel: "room:1", Client: "c1", Subject: alice}, time.Minute)
		add(t, s, realtime.PresenceEntry{Channel: "room:1", Client: "c2", Subject: alice}, time.Minute)
		add(t, s, realtime.PresenceEntry{Channel: "room:2", Client: "c1", Subject: alice}, time.Minute)
		for range 2 { // 重复删除无害
			if err := s.Remove(ctx, "room:1", "c1"); err != nil {
				t.Fatal(err)
			}
		}
		if got := list(t, s, "room:1"); len(got) != 1 || got[0].Client != "c2" {
			t.Fatalf("room:1 = %+v", got)
		}
		if got := list(t, s, "room:2"); len(got) != 1 {
			t.Fatalf("room:2 = %+v", got)
		}
	})

	t.Run("entries expire unless renewed", func(t *testing.T) {
		s := newStore(t)
		add(t, s, realtime.PresenceEntry{Channel: "room:1", Client: "gone", Subject: alice}, 150*time.Millisecond)
		add(t, s, realtime.PresenceEntry{Channel: "room:1", Client: "kept", Subject: bob}, 150*time.Millisecond)
		add(t, s, realtime.PresenceEntry{Channel: "room:1", Client: "kept", Subject: bob}, time.Minute) // 续期
		time.Sleep(300 * time.Millisecond)
		if got := list(t, s, "room:1"); len(got) != 1 || got[0].Client != "kept" {
			t.Fatalf("entries after expiry = %+v", got)
		}
		// 过期之后可以重新加入。
		add(t, s, realtime.PresenceEntry{Channel: "room:1", Client: "gone", Subject: alice}, time.Minute)
		if got := list(t, s, "room:1"); len(got) != 2 {
			t.Fatalf("entries after rejoining = %+v", got)
		}
	})
}
