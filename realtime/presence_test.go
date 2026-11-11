package realtime_test

import (
	"context"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/broadcast"
	"github.com/ryan053857/mygoravel/broadcast/broadcasttest"
	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/internal/dbtesting"
	"github.com/ryan053857/mygoravel/realtime"
	"github.com/ryan053857/mygoravel/realtime/realtimetest"
)

func TestMemoryPresence(t *testing.T) {
	realtimetest.TestPresenceStore(t, func(*testing.T) realtime.PresenceStore { return realtime.NewMemoryPresence() })
}

func TestDatabasePresence(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		realtimetest.TestPresenceStore(t, func(t *testing.T) realtime.PresenceStore {
			return realtime.NewDatabasePresence(dbtesting.Open(t, driver, realtime.Migrations()))
		})
	})
}

func TestDatabasePresenceCleanup(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		ctx := context.Background()
		s := realtime.NewDatabasePresence(dbtesting.Open(t, driver, realtime.Migrations()))
		for _, client := range []string{"a", "b", "c"} {
			if err := s.Add(ctx, realtime.PresenceEntry{Channel: "room:1", Client: client, Subject: "u"}, -time.Second); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.Add(ctx, realtime.PresenceEntry{Channel: "room:1", Client: "live", Subject: "u"}, time.Minute); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Cleanup(ctx, 0); err == nil {
			t.Fatal("cleanup accepted a zero limit")
		}
		for _, want := range []int{2, 1, 0} {
			if n, err := s.Cleanup(ctx, 2); err != nil || n != want {
				t.Fatalf("cleanup = %d, %v; want %d", n, err, want)
			}
		}
		if got, err := s.List(ctx, "room:1"); err != nil || len(got) != 1 || got[0].Client != "live" {
			t.Fatalf("entries after cleanup = %+v, %v", got, err)
		}
	})
}

// editors 是测试用的 presence 规则：carol 不能加入，其他人以自己的名字加入。
func editors(_ context.Context, sub identity.Subject, p realtime.Params) (any, bool, error) {
	if sub.ID == "carol" {
		return nil, false, nil
	}
	return map[string]string{"name": "user " + sub.ID, "article": p["article"]}, true, nil
}

func TestPresenceChannel(t *testing.T) {
	store := &tokens{}
	n := start(t, broadcasttest.NewBackend(), realtime.Options{Authenticate: store.validate}, nil)
	n.rt.Channel("editing:{article}", realtime.Presence(editors))
	n.rt.Channel("articles", realtime.Public())
	ctx := context.Background()

	alice := mustConnect(t, n.url, "alice", nil)
	alice.mustSubscribe("editing:7")
	if kind, m := alice.membership(); kind != "join" || m.User != "alice" || string(m.ChanInfo) != `{"article":"7","name":"user alice"}` {
		t.Fatalf("own join = %s %+v", kind, m)
	}

	// 同一个主体的第二个连接和另一个主体加入：已经在频道中的连接收到 join。
	tab := mustConnect(t, n.url, "alice", nil)
	tab.mustSubscribe("editing:7")
	bob := mustConnect(t, n.url, "bob", nil)
	bob.mustSubscribe("editing:7")
	for _, want := range []string{"alice", "bob"} {
		if kind, m := alice.membership(); kind != "join" || m.User != want {
			t.Fatalf("join = %s %+v, want %s", kind, m, want)
		}
	}

	if users, err := bob.presence("editing:7"); err != nil || len(users) != 2 || users["alice"] != 2 || users["bob"] != 1 {
		t.Fatalf("presence = %v, %+v", users, err)
	}
	members, err := n.rt.Members(ctx, "editing:7")
	if err != nil || len(members) != 2 {
		t.Fatalf("members = %+v, %v", members, err)
	}
	if m := members[0]; m.ID != "alice" || m.Connections != 2 || string(m.Info) != `{"article":"7","name":"user alice"}` {
		t.Fatalf("first member = %+v", m)
	}
	if m := members[1]; m.ID != "bob" || m.Connections != 1 {
		t.Fatalf("second member = %+v", m)
	}
	if members, err := n.rt.Members(ctx, "editing:8"); err != nil || len(members) != 0 {
		t.Fatalf("members of another channel = %+v, %v", members, err)
	}

	// 一个标签页关闭：收到 leave，主体仍然在线；最后一个连接断开后不再在线。
	tab.close()
	if kind, m := bob.membership(); kind != "join" || m.User != "bob" {
		t.Fatalf("bob's own join = %s %+v", kind, m)
	}
	if kind, m := bob.membership(); kind != "leave" || m.User != "alice" {
		t.Fatalf("leave = %s %+v", kind, m)
	}
	alice.close()
	if kind, m := bob.membership(); kind != "leave" || m.User != "alice" {
		t.Fatalf("second leave = %s %+v", kind, m)
	}
	if members, err := n.rt.Members(ctx, "editing:7"); err != nil || len(members) != 1 || members[0].ID != "bob" {
		t.Fatalf("members after alice left = %+v, %v", members, err)
	}

	// 没有加入的连接不能读取成员；规则拒绝的主体和匿名连接不能加入；其他频道没有成员列表。
	carol := mustConnect(t, n.url, "carol", nil)
	if _, err := carol.presence("editing:7"); err == nil || err.Code != 103 {
		t.Fatalf("presence without subscribing: %+v", err)
	}
	if reply := carol.subscribe("editing:7"); reply.Error == nil || reply.Error.Code != 103 {
		t.Fatalf("denied subject subscribed: %+v", reply.Error)
	}
	if reply := mustConnect(t, n.url, "", nil).subscribe("editing:7"); reply.Error == nil || reply.Error.Code != 103 {
		t.Fatalf("anonymous subscribed: %+v", reply.Error)
	}
	carol.mustSubscribe("articles")
	if _, err := carol.presence("articles"); err == nil || err.Code != 103 {
		t.Fatalf("presence of a public channel: %+v", err)
	}
}

// 两个实例使用数据库的广播和在线成员存储：成员互相可见，在另一个实例上取消订阅时发出 leave。
// 取消订阅的控制消息在 Hub 的投递中到达，离开事件又要发布到 Hub，这里确认两者不会互相等待。
func TestPresenceAcrossInstancesOnDatabase(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		hubDB := dbtesting.Open(t, driver, broadcast.Migrations())
		presence := realtime.NewDatabasePresence(dbtesting.Open(t, driver, realtime.Migrations()))
		store := &tokens{}
		opts := realtime.Options{Authenticate: store.validate, Presence: presence}
		a := start(t, broadcast.NewDatabase(hubDB, nil), opts, nil)
		b := start(t, broadcast.NewDatabase(hubDB, nil), opts, nil)
		broadcasttest.Ready(t, a.hub, b.hub)
		for _, n := range []*node{a, b} {
			n.rt.Channel("editing:{article}", realtime.Presence(editors))
		}

		alice, bob := mustConnect(t, a.url, "alice", nil), mustConnect(t, b.url, "bob", nil)
		alice.mustSubscribe("editing:7")
		alice.membership() // 自己的 join
		bob.mustSubscribe("editing:7")
		if kind, m := alice.membership(); kind != "join" || m.User != "bob" {
			t.Fatalf("join from the other instance = %s %+v", kind, m)
		}
		if users, err := alice.presence("editing:7"); err != nil || len(users) != 2 {
			t.Fatalf("presence = %v, %+v", users, err)
		}
		if members, err := a.rt.Members(context.Background(), "editing:7"); err != nil || len(members) != 2 {
			t.Fatalf("members = %+v, %v", members, err)
		}

		if err := a.rt.Unsubscribe("bob", "editing:7"); err != nil {
			t.Fatal(err)
		}
		if ch := bob.unsubscribed(); ch != "editing:7" {
			t.Fatalf("unsubscribed from %q", ch)
		}
		if kind, m := alice.membership(); kind != "leave" || m.User != "bob" {
			t.Fatalf("leave from the other instance = %s %+v", kind, m)
		}
		if members, err := b.rt.Members(context.Background(), "editing:7"); err != nil || len(members) != 1 || members[0].ID != "alice" {
			t.Fatalf("members after unsubscribe = %+v, %v", members, err)
		}
	})
}
