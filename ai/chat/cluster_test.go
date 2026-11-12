package chat_test

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/session/inmemory"
	"trpc.group/trpc-go/trpc-agent-go/tool"

	"github.com/ryan053857/mygoravel/ai/chat"
	"github.com/ryan053857/mygoravel/ai/chat/chattest"
	"github.com/ryan053857/mygoravel/broadcast"
	"github.com/ryan053857/mygoravel/broadcast/broadcasttest"
	"github.com/ryan053857/mygoravel/broadcast/redisbroadcast"
	"github.com/ryan053857/mygoravel/internal/redistest"
	"github.com/ryan053857/mygoravel/lock"
	"github.com/ryan053857/mygoravel/lock/locktest"
	"github.com/ryan053857/mygoravel/lock/redislock"
)

// twoInstances 创建共用会话存储、广播和锁的两个 Runs，相当于同一个应用的两个实例。
func twoInstances(t *testing.T, m *chattest.Model, tools []tool.Tool) (*chat.Runs, *chat.Runs) {
	t.Helper()
	return twoInstancesOn(t, m, tools, broadcasttest.NewBackend(), locktest.NewStore())
}

func twoInstancesOn(t *testing.T, m *chattest.Model, tools []tool.Tool, backend broadcast.Backend, store lock.Store) (*chat.Runs, *chat.Runs) {
	t.Helper()
	sessions := inmemory.NewSessionService()
	hub := broadcast.New(backend, nil)
	locker := lock.New(store)
	newInstance := func() *chat.Runs {
		ag := llmagent.New("support", llmagent.WithModel(m), llmagent.WithTools(tools),
			llmagent.WithGenerationConfig(model.GenerationConfig{Stream: true}))
		return chat.NewRuns("test", ag, sessions, chat.Cluster(hub, locker))
	}
	a, b := newInstance(), newInstance()
	t.Cleanup(func() {
		_ = a.Close()
		_ = b.Close()
		_ = hub.Close()
		_ = sessions.Close()
	})
	return a, b
}

func TestClusterMirrorsRunOnOtherInstance(t *testing.T) {
	m := chattest.NewModel(
		chattest.Reply{Reasoning: "先查", Text: "我查 一下", Calls: []chattest.Call{{ID: "g1", Name: "get_order", Args: orderArgs{"A1"}}}},
		chattest.Text("都 查好了"),
	)
	a, b := twoInstances(t, m, []tool.Tool{getOrder(400 * time.Millisecond)})
	mirrorScenario(t, m, a, b)
}

// TestClusterOnRedis 用 Redis 广播和 Redis 锁运行同一个场景。
func TestClusterOnRedis(t *testing.T) {
	rdb := redistest.Open(t)
	m := chattest.NewModel(
		chattest.Reply{Reasoning: "先查", Text: "我查 一下", Calls: []chattest.Call{{ID: "g1", Name: "get_order", Args: orderArgs{"A1"}}}},
		chattest.Text("都 查好了"),
	)
	a, b := twoInstancesOn(t, m, []tool.Tool{getOrder(400 * time.Millisecond)}, redisbroadcast.New(rdb, "test:chat:"+rand.Text()), redislock.New(rdb))
	mirrorScenario(t, m, a, b)
}

func mirrorScenario(t *testing.T, m *chattest.Model, a, b *chat.Runs) {
	t.Helper()
	ctx := context.Background()
	local, err := a.Start(ctx, "u1", "c1", model.NewUserMessage("查 A1"))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)

	// 另一个实例上：对话正忙，Get 返回镜像，插话转发给运行所在的实例。
	if _, err := b.Start(ctx, "u1", "c1", model.NewUserMessage("又一个问题")); !errors.Is(err, chat.ErrBusy) {
		t.Fatalf("start on other instance: %v", err)
	}
	remote, err := b.Get(ctx, "u1", "c1")
	if err != nil || remote == nil || remote.ID != local.ID {
		t.Fatalf("mirror %v, %v", remote, err)
	}
	if err := b.Steer(ctx, "u1", "c1", model.NewUserMessage("还有 B2")); err != nil {
		t.Fatal(err)
	}

	want, _ := collect(t, local, "")
	got, _ := collect(t, remote, "")
	// 镜像的快照合并了连续的文本增量，按文本内容比较。
	if joined(got) != joined(want) || !strings.Contains(strings.Join(got, ","), "user:还有 B2") {
		t.Fatalf("mirror events %q\nlocal events  %q", got, want)
	}
	if !strings.HasSuffix(messagesOf(m.LastRequest()), "user:还有 B2") {
		t.Fatalf("steer did not reach the model: %s", messagesOf(m.LastRequest()))
	}

	// 运行结束、锁释放后，另一个实例上没有运行，可以开始新的。
	time.Sleep(300 * time.Millisecond)
	if r, err := b.Get(ctx, "u1", "c1"); err != nil || r != nil {
		t.Fatalf("after finish: %v %v", r, err)
	}
}

// joined 把事件摘要中的文本拼接起来，忽略增量的切分方式。
func joined(events []string) string {
	var b strings.Builder
	last := ""
	for _, e := range events {
		name, text, _ := strings.Cut(e, ":")
		if name != last || (name != "text" && name != "reasoning") {
			b.WriteString("|" + name + ":")
		}
		b.WriteString(text)
		last = name
	}
	return b.String()
}

func TestClusterResumeAndCancelFromOtherInstance(t *testing.T) {
	m := chattest.NewModel(chattest.Reply{Text: "很慢", Delay: 5 * time.Second})
	a, b := twoInstances(t, m, nil)
	url := chatServer(t, b)
	local, err := a.Start(context.Background(), "u1", "c1", model.NewUserMessage("hi"))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)

	// 浏览器连到另一个实例续传，在那里取消。
	resp, err := http.Get(url + "/chat/c1/stream")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		r, _ := b.Get(context.Background(), "u1", "c1")
		r.Cancel()
	}()
	if got := strings.Join(chunks(t, resp), "|"); got != "start|abort|[DONE]" {
		t.Fatalf("resumed on other instance: %s", got)
	}
	select {
	case <-local.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not reach the running instance")
	}
}

func TestClusterMirrorDetectsSilentInstance(t *testing.T) {
	defer chat.SetClusterTiming(time.Hour, 200*time.Millisecond)()
	m := chattest.NewModel(chattest.Reply{Text: "迟到的回答", Delay: 700 * time.Millisecond})
	a, b := twoInstances(t, m, nil)
	ctx := context.Background()
	if _, err := a.Start(ctx, "u1", "c1", model.NewUserMessage("hi")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	remote, err := b.Get(ctx, "u1", "c1")
	if err != nil || remote == nil {
		t.Fatalf("mirror %v, %v", remote, err)
	}
	// 运行所在的实例长时间没有消息（这里关掉了心跳）：镜像以错误结束，而不是一直等待。
	got, _ := collect(t, remote, "")
	if len(got) != 1 || !strings.HasPrefix(got[0], "error") {
		t.Fatalf("events %q", got)
	}
	// 再次 Get 会重新建立镜像。
	again, err := b.Get(ctx, "u1", "c1")
	if err != nil || again == nil || again == remote {
		t.Fatalf("second mirror %v, %v", again, err)
	}
}

// 一个实例停止时，进行中的运行继续直到结束，其他实例照常镜像它。
func TestClusterShutdownFinishesRunsMirroredElsewhere(t *testing.T) {
	m := chattest.NewModel(chattest.Reply{Text: "说 完 了", Delay: 400 * time.Millisecond})
	a, b := twoInstances(t, m, nil)
	ctx := context.Background()
	if _, err := a.Start(ctx, "u1", "c1", model.NewUserMessage("hi")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	shutdown := make(chan error, 1)
	go func() { shutdown <- a.Shutdown(ctx) }()

	mirrored, err := b.Get(ctx, "u1", "c1")
	if err != nil {
		t.Fatal(err)
	}
	_, events := collect(t, mirrored, "")
	if done := events[len(events)-1].Data.(chat.Done); done.Canceled {
		t.Fatal("run was canceled by shutdown")
	}
	if err := <-shutdown; err != nil {
		t.Fatal(err)
	}
	// 运行结束后，对话可以在其他实例上继续。等它结束，不留下运行中的 goroutine 影响后面的测试。
	next, err := b.Start(ctx, "u1", "c1", model.NewUserMessage("还有"))
	if err != nil {
		t.Fatalf("continue on other instance: %v", err)
	}
	collect(t, next, "")
}

// 对话在其他实例上回答时，Append 返回 ErrBusy。
func TestClusterAppendWhileRunningElsewhere(t *testing.T) {
	m := chattest.NewModel(chattest.Reply{Text: "慢", Delay: 300 * time.Millisecond})
	a, b := twoInstances(t, m, nil)
	ctx := context.Background()
	r, err := a.Start(ctx, "u1", "c1", model.NewUserMessage("hi"))
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Append(ctx, "u1", "c1", "system", model.NewUserMessage("x")); !errors.Is(err, chat.ErrBusy) {
		t.Fatalf("append on other instance: %v", err)
	}
	<-r.Done()
	// 运行所在的实例随后释放对话锁。
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := b.Append(ctx, "u1", "c1", "system", model.NewUserMessage("x"))
		if err == nil {
			break
		}
		if !errors.Is(err, chat.ErrBusy) || time.Now().After(deadline) {
			t.Fatalf("append after the run: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// 同一个对话在保留期内再次运行：上一次运行的 owner 关闭，其他实例镜像到的是这一次运行。
func TestClusterRerunWithinKeepMirrorsLatest(t *testing.T) {
	m := chattest.NewModel(chattest.Text("第一次"), chattest.Reply{Text: "第二次", Delay: 500 * time.Millisecond})
	a, b := twoInstances(t, m, nil)
	ctx := context.Background()
	first, err := a.Start(ctx, "u1", "c1", model.NewUserMessage("一"))
	if err != nil {
		t.Fatal(err)
	}
	<-first.Done()
	var second *chat.Run
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if second, err = a.Start(ctx, "u1", "c1", model.NewUserMessage("二")); err == nil {
			break
		}
		if !errors.Is(err, chat.ErrBusy) || time.Now().After(deadline) {
			t.Fatal(err)
		}
	}
	for range 5 {
		mirrored, err := b.Get(ctx, "u1", "c1")
		if err != nil || mirrored == nil {
			t.Fatalf("get: %v", err)
		}
		if mirrored.ID != second.ID {
			t.Fatalf("mirrored run %s, want %s", mirrored.ID, second.ID)
		}
	}
	<-second.Done()
}
