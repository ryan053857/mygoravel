package redisbroadcast_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/redis/rueidis"

	"github.com/ryan053857/mygoravel/broadcast"
	"github.com/ryan053857/mygoravel/broadcast/broadcasttest"
	"github.com/ryan053857/mygoravel/broadcast/redisbroadcast"
	"github.com/ryan053857/mygoravel/internal/redistest"
)

func TestBackend(t *testing.T) {
	for _, srv := range redistest.Servers {
		t.Run(srv.Name, func(t *testing.T) {
			broadcasttest.TestBackend(t, func(t *testing.T) (broadcast.Backend, broadcast.Backend) {
				channel := "test:broadcast:" + rand.Text()
				return redisbroadcast.New(srv.Open(t), channel), redisbroadcast.New(srv.Open(t), channel)
			})
		})
	}
}

// 订阅连接被断开后，订阅以 ErrLost 结束；Backend 重新订阅，新的订阅照常收到消息。
func TestConnectionLossEndsSubscriptions(t *testing.T) {
	defer redisbroadcast.SetPingInterval(200 * time.Millisecond)()
	for _, srv := range redistest.Servers {
		t.Run(srv.Name, func(t *testing.T) {
			name := "test-" + rand.Text()
			h := broadcast.New(redisbroadcast.New(srv.OpenNamed(t, name), "test:broadcast:"+rand.Text()), nil)
			defer h.Close()
			sub := h.Subscribe("room:1")
			broadcasttest.Ready(t, h)

			if err := killPubSub(srv.Open(t), name); err != nil {
				t.Fatal(err)
			}
			expectLost(t, sub)
			expectRecovered(t, h)
		})
	}
}

// 频道所在的槽迁移到别的节点后，原节点结束分片订阅：订阅以 ErrLost 结束，Backend 到新节点重新订阅。
func TestSlotMigrationEndsSubscriptions(t *testing.T) {
	ctx := context.Background()
	rdb := redistest.Cluster.Open(t)
	channel := "test:broadcast:" + rand.Text()
	h := broadcast.New(redisbroadcast.New(rdb, channel), nil)
	defer h.Close()
	sub := h.Subscribe("room:1")
	broadcasttest.Ready(t, h)

	slot, err := rdb.Do(ctx, rdb.B().ClusterKeyslot().Key(channel).Build()).AsInt64()
	if err != nil {
		t.Fatal(err)
	}
	owner, err := slotOwner(ctx, rdb, slot)
	if err != nil {
		t.Fatal(err)
	}
	// 原子槽迁移（Redis 8.4）在目标节点上发起，槽里的键随之迁移，并行运行的其他测试不受影响。
	moved := false
	for addr, node := range rdb.Nodes() {
		if addr == owner {
			continue
		}
		s := strconv.FormatInt(slot, 10)
		if err := node.Do(ctx, node.B().Arbitrary("CLUSTER", "MIGRATION", "IMPORT").Args(s, s).Build()).Error(); err != nil {
			t.Fatal(err)
		}
		moved = true
		break
	}
	if !moved {
		t.Fatal("no other node to migrate the slot to")
	}
	expectLost(t, sub)
	expectRecovered(t, h)
}

func expectLost(t *testing.T, sub *broadcast.Subscription) {
	t.Helper()
	select {
	case _, ok := <-sub.C():
		if ok || !errors.Is(sub.Err(), broadcast.ErrLost) {
			t.Fatalf("ok=%v err=%v", ok, sub.Err())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("subscription not ended")
	}
}

// expectRecovered 等 Backend 重新订阅，然后检查新的订阅照常收到消息。
// 重新订阅成功之前每次失败都会再报告一次丢失（例如槽迁移后客户端还没更新拓扑，订阅收到 MOVED），
// 所以先等就绪再订阅。
func expectRecovered(t *testing.T, h *broadcast.Hub) {
	t.Helper()
	broadcasttest.Ready(t, h)
	again := h.Subscribe("room:1")
	if err := h.Publish(context.Background(), "room:1", []byte("back")); err != nil {
		t.Fatal(err)
	}
	if got := broadcasttest.Receive(t, again); string(got.Data) != "back" {
		t.Fatalf("got %q", got.Data)
	}
}

// slotOwner 从 CLUSTER SLOTS 中找出负责 slot 的主节点地址。
func slotOwner(ctx context.Context, rdb rueidis.Client, slot int64) (string, error) {
	ranges, err := rdb.Do(ctx, rdb.B().ClusterSlots().Build()).ToArray()
	if err != nil {
		return "", err
	}
	for _, r := range ranges {
		v, err := r.ToArray()
		if err != nil || len(v) < 3 {
			return "", fmt.Errorf("unexpected CLUSTER SLOTS entry: %v", r)
		}
		start, _ := v[0].AsInt64()
		end, _ := v[1].AsInt64()
		if slot < start || slot > end {
			continue
		}
		master, err := v[2].ToArray()
		if err != nil || len(master) < 2 {
			return "", fmt.Errorf("unexpected CLUSTER SLOTS node: %v", v[2])
		}
		host, _ := master[0].ToString()
		port, _ := master[1].AsInt64()
		return net.JoinHostPort(host, strconv.FormatInt(port, 10)), nil
	}
	return "", fmt.Errorf("slot %d is not served", slot)
}

func TestNewPanics(t *testing.T) {
	for name, f := range map[string]func(){
		"nil client":    func() { redisbroadcast.New(nil, "c") },
		"empty channel": func() { redisbroadcast.New(redistest.Standalone.Open(t), "") },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected panic")
				}
			}()
			f()
		})
	}
}

// killPubSub 断开名为 name 的客户端的订阅连接，不影响并行运行的其他测试。集群中逐个节点查找。
func killPubSub(rdb rueidis.Client, name string) error {
	ctx := context.Background()
	killed := 0
	for _, node := range rdb.Nodes() {
		n, err := killNamed(ctx, node, name)
		if err != nil {
			return err
		}
		killed += n
	}
	if killed == 0 {
		return errors.New("no subscription connection to kill")
	}
	return nil
}

func killNamed(ctx context.Context, rdb rueidis.Client, name string) (int, error) {
	list, err := rdb.Do(ctx, rdb.B().ClientList().TypePubsub().Build()).ToString()
	if err != nil {
		return 0, err
	}
	killed := 0
	list = strings.TrimPrefix(list, "txt:") // RESP3 的 verbatim string 带格式前缀
	for line := range strings.Lines(list) {
		fields := map[string]string{}
		for f := range strings.FieldsSeq(line) {
			if k, v, ok := strings.Cut(f, "="); ok {
				fields[k] = v
			}
		}
		if fields["name"] != name {
			continue
		}
		id, err := strconv.ParseInt(fields["id"], 10, 64)
		if err != nil {
			return 0, err
		}
		if err := rdb.Do(ctx, rdb.B().ClientKill().Id(id).Build()).Error(); err != nil {
			return 0, err
		}
		killed++
	}
	return killed, nil
}
