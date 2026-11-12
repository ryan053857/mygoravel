package realtime_test

import (
	"context"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/broadcast"
	"github.com/ryan053857/mygoravel/broadcast/broadcasttest"
	"github.com/ryan053857/mygoravel/database/dbtest"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/realtime"
	"github.com/ryan053857/mygoravel/realtime/realtimetest"
)

func TestDatabasePresence(t *testing.T) {
	realtimetest.TestPresenceStore(t, func(t *testing.T) realtime.PresenceStore {
		return realtime.NewDatabasePresence(dbtest.Open(t, realtime.Migrations()))
	})
}

func TestDatabasePresenceCleanup(t *testing.T) {
	ctx := context.Background()
	s := realtime.NewDatabasePresence(dbtest.Open(t, realtime.Migrations()))
	for _, client := range []string{"a", "b", "c"} {
		if err := s.Add(ctx, realtime.PresenceEntry{Channel: "room:1", Client: client, Subject: identity.Ref{Type: "user", ID: "u"}}, -time.Second); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Add(ctx, realtime.PresenceEntry{Channel: "room:1", Client: "live", Subject: identity.Ref{Type: "user", ID: "u"}}, time.Minute); err != nil {
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
	// 不保留在线记录，离开事件立即发出；宽限期见 TestPresenceGrace。
	n := start(t, broadcasttest.NewBackend(), realtime.Options{Authenticate: store.validate, Presence: realtimetest.NewPresence(), PresenceGrace: -1}, nil)
	n.rt.Channel("editing:{article}", realtime.Presence(editors))
	n.rt.Channel("articles", realtime.Public())
	ctx := context.Background()

	alice := mustConnect(t, n.url, "alice", nil)
	alice.mustSubscribe("editing:7")
	if kind, m := alice.membership(); kind != "join" || m.User != "user:alice" || string(m.ChanInfo) != `{"article":"7","name":"user alice"}` {
		t.Fatalf("own join = %s %+v", kind, m)
	}

	// 事件按主体发出：同一个主体的第二个连接加入时没有事件，另一个主体加入时有。
	tab := mustConnect(t, n.url, "alice", nil)
	tab.mustSubscribe("editing:7")
	bob := mustConnect(t, n.url, "bob", nil)
	bob.mustSubscribe("editing:7")
	if kind, m := alice.membership(); kind != "join" || m.User != "user:bob" {
		t.Fatalf("join = %s %+v, want bob", kind, m)
	}
	tab.noMembership(0) // 第二个连接没有收到自己的 join

	if users, err := bob.presence("editing:7"); err != nil || len(users) != 2 || users["user:alice"] != 2 || users["user:bob"] != 1 {
		t.Fatalf("presence = %v, %+v", users, err)
	}
	members, err := n.rt.Members(ctx, "editing:7")
	if err != nil || len(members) != 2 {
		t.Fatalf("members = %+v, %v", members, err)
	}
	if m := members[0]; m.Subject.ID != "alice" || m.Connections != 2 || string(m.Info) != `{"article":"7","name":"user alice"}` {
		t.Fatalf("first member = %+v", m)
	}
	if m := members[1]; m.Subject.ID != "bob" || m.Connections != 1 {
		t.Fatalf("second member = %+v", m)
	}
	if members, err := n.rt.Members(ctx, "editing:8"); err != nil || len(members) != 0 {
		t.Fatalf("members of another channel = %+v, %v", members, err)
	}

	// 一个标签页关闭时主体仍然在线，没有事件；最后一个连接断开后发出 leave。
	if kind, m := bob.membership(); kind != "join" || m.User != "user:bob" {
		t.Fatalf("bob's own join = %s %+v", kind, m)
	}
	tab.close()
	bob.noMembership(150 * time.Millisecond)
	if members, err := n.rt.Members(ctx, "editing:7"); err != nil || len(members) != 2 || members[0].Connections != 1 {
		t.Fatalf("members after closing one tab = %+v, %v", members, err)
	}
	alice.close()
	if kind, m := bob.membership(); kind != "leave" || m.User != "user:alice" {
		t.Fatalf("leave = %s %+v", kind, m)
	}
	if members, err := n.rt.Members(ctx, "editing:7"); err != nil || len(members) != 1 || members[0].Subject.ID != "bob" {
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

// 两个实例使用数据库的广播和在线成员存储：成员互相可见，重连到另一个实例时没有事件，
// 在另一个实例上取消订阅时立即发出 leave，而且只发一次。
// 取消订阅的控制消息在 Hub 的投递中到达，离开事件又要发布到 Hub，这里确认两者不会互相等待。
func TestPresenceAcrossInstancesOnDatabase(t *testing.T) {
	hubDB := dbtest.Open(t, broadcast.Migrations())
	presence := realtime.NewDatabasePresence(dbtest.Open(t, realtime.Migrations()))
	store := &tokens{}
	opts := realtime.Options{Authenticate: store.validate, Presence: presence, PresenceGrace: 300 * time.Millisecond}
	a := start(t, broadcast.NewDatabase(hubDB, nil), opts, nil)
	b := start(t, broadcast.NewDatabase(hubDB, nil), opts, nil)
	broadcasttest.Ready(t, a.hub, b.hub)
	for _, n := range []*node{a, b} {
		n.rt.Channel("editing:{article}", realtime.Presence(editors))
	}

	alice, bob := mustConnect(t, a.url, "alice", nil), mustConnect(t, b.url, "bob", nil)
	alice.mustSubscribe("editing:7")
	alice.membership() // 自己的 join
	// 等 b 读到 alice 的 join 之后 bob 再订阅：数据库后端按轮询投递其他实例的消息，
	// 否则这条稍早的 join 可能在 bob 订阅之后才到达 b。
	broadcasttest.Ready(t, a.hub, b.hub)
	bob.mustSubscribe("editing:7")
	if kind, m := alice.membership(); kind != "join" || m.User != "user:bob" {
		t.Fatalf("join from the other instance = %s %+v", kind, m)
	}
	if users, err := alice.presence("editing:7"); err != nil || len(users) != 2 {
		t.Fatalf("presence = %v, %+v", users, err)
	}
	if members, err := a.rt.Members(context.Background(), "editing:7"); err != nil || len(members) != 2 {
		t.Fatalf("members = %+v, %v", members, err)
	}

	// alice 换到另一个实例：先在 b 上订阅，再关闭 a 上的连接。bob 看不到加入（a 上还有她的记录），
	// 也看不到离开（a 的宽限期满时 b 上有她的记录），alice 也收不到自己的 join。
	// 先建新连接再关旧连接，结果不依赖重连有多快；如果先断开再重连，重连慢于宽限期时离开事件是正确的。
	if kind, m := bob.membership(); kind != "join" || m.User != "user:bob" {
		t.Fatalf("bob's own join = %s %+v", kind, m)
	}
	old := alice
	alice = mustConnect(t, b.url, "alice", nil)
	alice.mustSubscribe("editing:7") // 回复订阅之前在线记录已经写入
	old.close()
	bob.noMembership(600 * time.Millisecond)

	if err := a.rt.Unsubscribe(identity.Ref{Type: "user", ID: "bob"}, "editing:7"); err != nil {
		t.Fatal(err)
	}
	if ch := bob.unsubscribed(); ch != "editing:7" {
		t.Fatalf("unsubscribed from %q", ch)
	}
	if kind, m := alice.membership(); kind != "leave" || m.User != "user:bob" {
		t.Fatalf("leave from the other instance = %s %+v", kind, m)
	}
	// 连接所在的实例宽限期满时不再重复发出 leave。
	alice.noMembership(700 * time.Millisecond)
	if members, err := b.rt.Members(context.Background(), "editing:7"); err != nil || len(members) != 1 || members[0].Subject.ID != "alice" {
		t.Fatalf("members after unsubscribe = %+v, %v", members, err)
	}
}

// 连接离开后在线记录保留一个宽限期：期间主体重新加入时没有事件，期满仍然没有连接才发出 leave。
func TestPresenceGrace(t *testing.T) {
	const grace = 300 * time.Millisecond
	store := &tokens{}
	n := start(t, broadcasttest.NewBackend(), realtime.Options{Authenticate: store.validate, Presence: realtimetest.NewPresence(), PresenceGrace: grace}, nil)
	n.rt.Channel("editing:{article}", realtime.Presence(editors))
	ctx := context.Background()
	members := func() []realtime.Member {
		t.Helper()
		m, err := n.rt.Members(ctx, "editing:7")
		if err != nil {
			t.Fatal(err)
		}
		return m
	}

	bob := mustConnect(t, n.url, "bob", nil)
	bob.mustSubscribe("editing:7")
	bob.membership() // 自己的 join
	alice := mustConnect(t, n.url, "alice", nil)
	alice.mustSubscribe("editing:7")
	if kind, m := bob.membership(); kind != "join" || m.User != "user:alice" {
		t.Fatalf("join = %s %+v", kind, m)
	}

	// 断开后在宽限期内用新的连接重新加入：没有 leave，也没有 join，期间一直在成员列表中。
	alice.close()
	time.Sleep(grace / 3)
	if m := members(); len(m) != 2 {
		t.Fatalf("members during the grace period = %+v", m)
	}
	alice = mustConnect(t, n.url, "alice", nil)
	alice.mustSubscribe("editing:7")
	bob.noMembership(2 * grace)
	if m := members(); len(m) != 2 || m[0].Subject.ID != "alice" || m[0].Connections != 1 {
		t.Fatalf("members after rejoining = %+v", m)
	}

	// 同一个连接取消订阅后在宽限期内重新订阅：同样没有事件，在线记录也没有被删除。
	alice.unsubscribe("editing:7")
	alice.mustSubscribe("editing:7")
	bob.noMembership(2 * grace)
	if m := members(); len(m) != 2 {
		t.Fatalf("members after resubscribing = %+v", m)
	}

	// 离开后没有回来：宽限期内没有事件，期满后发出 leave，并从成员列表中消失。
	left := time.Now()
	alice.close()
	bob.noMembership(grace / 2)
	if kind, m := bob.membership(); kind != "leave" || m.User != "user:alice" {
		t.Fatalf("leave = %s %+v", kind, m)
	}
	if waited := time.Since(left); waited < grace {
		t.Fatalf("leave arrived after %v, before the grace period of %v", waited, grace)
	}
	if m := members(); len(m) != 1 || m[0].Subject.ID != "bob" {
		t.Fatalf("members after the grace period = %+v", m)
	}

	// 宽限期满之后再加入，是一次新的 join。
	alice = mustConnect(t, n.url, "alice", nil)
	alice.mustSubscribe("editing:7")
	if kind, m := bob.membership(); kind != "join" || m.User != "user:alice" {
		t.Fatalf("join after the grace period = %s %+v", kind, m)
	}
}

// 实例停止时不发离开事件：它上面的成员会重连到其他实例，在线记录保留到自行过期。
func TestShutdownDoesNotAnnounceLeaves(t *testing.T) {
	b := &bus{}
	presence := realtimetest.NewPresence()
	store := &tokens{}
	opts := realtime.Options{Authenticate: store.validate, Presence: presence, PresenceGrace: 100 * time.Millisecond}
	stopping := start(t, b.backend(), opts, nil)
	other := start(t, b.backend(), opts, nil)
	for _, n := range []*node{stopping, other} {
		n.rt.Channel("editing:{article}", realtime.Presence(editors))
	}
	bob := mustConnect(t, other.url, "bob", nil)
	bob.mustSubscribe("editing:7")
	bob.membership() // 自己的 join
	alice := mustConnect(t, stopping.url, "alice", nil)
	alice.mustSubscribe("editing:7")
	if kind, m := bob.membership(); kind != "join" || m.User != "user:alice" {
		t.Fatalf("join = %s %+v", kind, m)
	}

	if err := stopping.rt.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if code := alice.closed(); code != 3001 {
		t.Fatalf("closed with %d", code)
	}
	bob.noMembership(400 * time.Millisecond)
	if m, err := other.rt.Members(context.Background(), "editing:7"); err != nil || len(m) != 2 {
		t.Fatalf("members after the other instance stopped = %+v, %v", m, err)
	}
	// 重连到另一个实例，仍然没有事件。
	alice = mustConnect(t, other.url, "alice", nil)
	alice.mustSubscribe("editing:7")
	bob.noMembership(300 * time.Millisecond)
}

// Unsubscribe 把主体移出频道：离开事件立即发出，不等宽限期，之后也不重复。
func TestUnsubscribeAnnouncesLeaveImmediately(t *testing.T) {
	const grace = 1500 * time.Millisecond
	store := &tokens{}
	n := start(t, broadcasttest.NewBackend(), realtime.Options{Authenticate: store.validate, Presence: realtimetest.NewPresence(), PresenceGrace: grace}, nil)
	n.rt.Channel("editing:{article}", realtime.Presence(editors))
	bob := mustConnect(t, n.url, "bob", nil)
	bob.mustSubscribe("editing:7")
	bob.membership() // 自己的 join
	// alice 有两个连接，都被移出。
	alice, tab := mustConnect(t, n.url, "alice", nil), mustConnect(t, n.url, "alice", nil)
	alice.mustSubscribe("editing:7")
	tab.mustSubscribe("editing:7")
	if kind, m := bob.membership(); kind != "join" || m.User != "user:alice" {
		t.Fatalf("join = %s %+v", kind, m)
	}

	removed := time.Now()
	if err := n.rt.Unsubscribe(identity.Ref{Type: "user", ID: "alice"}, "editing:7"); err != nil {
		t.Fatal(err)
	}
	if kind, m := bob.membership(); kind != "leave" || m.User != "user:alice" || string(m.ChanInfo) != `{"article":"7","name":"user alice"}` {
		t.Fatalf("leave = %s %+v", kind, m)
	}
	if waited := time.Since(removed); waited > grace/2 {
		t.Fatalf("leave arrived after %v, the grace period is %v", waited, grace)
	}
	if m, err := n.rt.Members(context.Background(), "editing:7"); err != nil || len(m) != 1 || m[0].Subject.ID != "bob" {
		t.Fatalf("members right after unsubscribe = %+v, %v", m, err)
	}
	bob.noMembership(grace + 500*time.Millisecond) // 宽限期满时没有第二个 leave

	// 没有在线记录的主体和频道：什么也不发生。
	if err := n.rt.Unsubscribe(identity.Ref{Type: "user", ID: "nobody"}, "editing:7"); err != nil {
		t.Fatal(err)
	}
	bob.noMembership(100 * time.Millisecond)
}

func TestPresenceGraceIsCapped(t *testing.T) {
	hub := broadcast.New(broadcasttest.NewBackend(), nil)
	defer hub.Close()
	defer func() {
		if recover() == nil {
			t.Fatal("a grace period longer than MaxPresenceGrace did not panic")
		}
	}()
	_, _ = realtime.New(hub, realtime.Options{PresenceGrace: realtime.MaxPresenceGrace + time.Second})
}

func TestPresenceChannelNeedsStore(t *testing.T) {
	n := start(t, broadcasttest.NewBackend(), realtime.Options{}, nil)
	// 没有 presence 频道时不需要存储：没有在线成员，Unsubscribe 照常工作。
	if m, err := n.rt.Members(context.Background(), "editing:7"); err != nil || len(m) != 0 {
		t.Fatalf("members without a store = %+v, %v", m, err)
	}
	if err := n.rt.Unsubscribe(identity.Ref{Type: "user", ID: "alice"}, "editing:7"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("a presence channel without Options.Presence did not panic")
		}
	}()
	n.rt.Channel("editing:{article}", realtime.Presence(editors))
}
