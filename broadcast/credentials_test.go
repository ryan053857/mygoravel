package broadcast_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/broadcast"
	"github.com/ryan053857/mygoravel/broadcast/broadcasttest"
	"github.com/ryan053857/mygoravel/identity"
)

// signaled 检查 ch 在 wait 内收到（want 为 true）或没有收到信号。
func signaled(t *testing.T, ch <-chan struct{}, want bool, what string) {
	t.Helper()
	wait := 50 * time.Millisecond
	if want {
		wait = 2 * time.Second
	}
	select {
	case <-ch:
		if !want {
			t.Fatalf("%s: unexpected signal", what)
		}
	case <-time.After(wait):
		if want {
			t.Fatalf("%s: no signal", what)
		}
	}
}

func TestCredentialsWatch(t *testing.T) {
	hub := broadcast.New(broadcasttest.NewBackend(), nil)
	defer hub.Close()
	creds := broadcast.NewCredentials(hub)
	ctx, cancel := context.WithCancel(identity.WithTenant(context.Background(), "acme"))
	defer cancel()
	ch := creds.Watch(ctx, identity.Subject{Type: "user", ID: "alice", Credential: "token:1"})
	// Watch 在后台订阅；等订阅生效再发提醒。
	time.Sleep(20 * time.Millisecond)

	bg := context.Background()
	for _, tc := range []struct {
		what    string
		recheck func() error
		want    bool
	}{
		{"same credential", func() error { return creds.RecheckCredential(bg, "token:1") }, true},
		{"other credential", func() error { return creds.RecheckCredential(bg, "token:2") }, false},
		{"same subject", func() error { return creds.RecheckSubject(bg, identity.Ref{Type: "user", ID: "alice"}) }, true},
		{"other subject", func() error { return creds.RecheckSubject(bg, identity.Ref{Type: "user", ID: "bob"}) }, false},
		{"same ID in another account system", func() error { return creds.RecheckSubject(bg, identity.Ref{Type: "staff", ID: "alice"}) }, false},
		{"request tenant", func() error { return creds.RecheckTenant(bg, "acme") }, true},
		{"other tenant", func() error { return creds.RecheckTenant(bg, "globex") }, false},
		{"platform", func() error { return creds.RecheckTenant(bg, "") }, false},
	} {
		if err := tc.recheck(); err != nil {
			t.Fatalf("%s: %v", tc.what, err)
		}
		signaled(t, ch, tc.want, tc.what)
	}

	// 读取前的多个提醒合并为一个信号。
	for range 3 {
		if err := creds.RecheckSubject(bg, identity.Ref{Type: "user", ID: "alice"}); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(50 * time.Millisecond) // 等后台把三条提醒都转成信号
	signaled(t, ch, true, "first of merged")
	signaled(t, ch, false, "merged")

	// ID 不能为空。
	if creds.RecheckCredential(bg, "") == nil || creds.RecheckSubject(bg, identity.Ref{}) == nil {
		t.Fatal("empty id accepted")
	}

	// ctx 结束后停止关注。
	cancel()
	time.Sleep(20 * time.Millisecond)
	if err := creds.RecheckSubject(bg, identity.Ref{Type: "user", ID: "alice"}); err != nil {
		t.Fatal(err)
	}
	signaled(t, ch, false, "after ctx ended")
}

// lossyBackend 在进程内投递消息，Lose 让 Hub 以为丢了消息。
type lossyBackend struct {
	mu      sync.Mutex
	deliver func(broadcast.Message)
	lost    func(error)
	ready   chan struct{}
}

func (b *lossyBackend) Publish(_ context.Context, msg broadcast.Message) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.deliver(msg)
	return nil
}

func (b *lossyBackend) Listen(ctx context.Context, deliver func(broadcast.Message), lost func(error)) {
	b.mu.Lock()
	b.deliver, b.lost = deliver, lost
	b.mu.Unlock()
	close(b.ready)
	<-ctx.Done()
}

func (b *lossyBackend) Lose() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lost(errors.New("connection reset"))
}

// Backend 可能丢了消息时，Watch 先发一个信号让连接复查，再重新订阅，之后的提醒照常送达。
func TestCredentialsWatchResubscribesAfterLoss(t *testing.T) {
	backend := &lossyBackend{ready: make(chan struct{})}
	hub := broadcast.New(backend, nil)
	defer hub.Close()
	<-backend.ready
	creds := broadcast.NewCredentials(hub)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := creds.Watch(ctx, identity.Subject{Type: "user", ID: "alice"})
	time.Sleep(20 * time.Millisecond)

	backend.Lose()
	signaled(t, ch, true, "after loss")
	time.Sleep(20 * time.Millisecond)
	if err := creds.RecheckSubject(context.Background(), identity.Ref{Type: "user", ID: "alice"}); err != nil {
		t.Fatal(err)
	}
	signaled(t, ch, true, "after resubscribe")
}

// Hub 关闭后 Watch 不再重新订阅。
func TestCredentialsWatchStopsWhenHubCloses(t *testing.T) {
	hub := broadcast.New(broadcasttest.NewBackend(), nil)
	creds := broadcast.NewCredentials(hub)
	ch := creds.Watch(context.Background(), identity.Subject{Type: "user", ID: "alice"})
	time.Sleep(20 * time.Millisecond)
	if err := hub.Close(); err != nil {
		t.Fatal(err)
	}
	signaled(t, ch, false, "hub closed")
}
