package chat_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/session/inmemory"
	"trpc.group/trpc-go/trpc-agent-go/tool"

	"github.com/ryan053857/mygoravel/ai/chat"
	"github.com/ryan053857/mygoravel/ai/chat/chattest"
	"github.com/ryan053857/mygoravel/broadcast"
	"github.com/ryan053857/mygoravel/broadcast/broadcasttest"
	"github.com/ryan053857/mygoravel/lock"
	"github.com/ryan053857/mygoravel/lock/locktest"
)

// deployment 是一个 web 实例（RunOnWorkers）和若干 worker 实例（Serve），共用对话存储、广播和锁。
type deployment struct {
	web     *chat.Runs
	workers []*chat.Runs
	stop    []context.CancelFunc
}

// newDeployment 创建部署。web 的模型没有任何回复，被调用就会出错：运行必须发生在 worker 上。
func newDeployment(t *testing.T, workerModel *chattest.Model, tools []tool.Tool, workers int, opts ...chat.Option) *deployment {
	t.Helper()
	sessions := inmemory.NewSessionService()
	hub := broadcast.New(broadcasttest.NewBackend(), nil)
	locker := lock.New(locktest.NewStore())
	newRuns := func(m *chattest.Model, extra ...chat.Option) *chat.Runs {
		ag := llmagent.New("support", llmagent.WithModel(m), llmagent.WithTools(tools),
			llmagent.WithGenerationConfig(model.GenerationConfig{Stream: true}))
		return chat.NewRuns("test", ag, sessions, append(append([]chat.Option{chat.Cluster(hub, locker)}, opts...), extra...)...)
	}
	d := &deployment{web: newRuns(chattest.NewModel(), chat.RunOnWorkers())}
	for range workers {
		w := newRuns(workerModel)
		ctx, cancel := context.WithCancel(context.Background())
		go func() { _ = w.Serve(ctx) }()
		d.workers = append(d.workers, w)
		d.stop = append(d.stop, cancel)
	}
	t.Cleanup(func() {
		for _, cancel := range d.stop {
			cancel()
		}
		_ = d.web.Close()
		for _, w := range d.workers {
			_ = w.Close()
		}
		_ = hub.Close()
		_ = sessions.Close()
	})
	time.Sleep(20 * time.Millisecond) // 等 Serve 订阅
	return d
}

func TestWorkersRunConversation(t *testing.T) {
	t.Parallel()
	m := chattest.NewModel(chattest.Text("你好 世界"))
	d := newDeployment(t, m, nil, 2)
	ctx := context.Background()
	r, err := d.web.Start(ctx, "u1", "c1", model.NewUserMessage("hi"))
	if err != nil {
		t.Fatal(err)
	}
	names, _ := collect(t, r, "")
	if got := strings.Join(names, " "); !strings.Contains(got, chat.EventText) || !strings.HasSuffix(got, chat.EventDone) {
		t.Fatalf("events %s", got)
	}
	// 两个 worker 中只有一个处理：模型只有一条回复，第二次调用会出错。
	if n := len(m.Requests()); n != 1 {
		t.Fatalf("model called %d times", n)
	}
	msgs, err := d.web.UIMessages(ctx, "u1", "c1")
	if err != nil || len(msgs) != 2 {
		t.Fatalf("history %+v err %v", msgs, err)
	}
}

func TestWorkersBusyAndOptions(t *testing.T) {
	t.Parallel()
	m := chattest.NewModel(chattest.Reply{Text: "慢", Delay: 300 * time.Millisecond})
	d := newDeployment(t, m, nil, 1)
	ctx := context.Background()
	r, err := d.web.Start(ctx, "u1", "c1", model.NewUserMessage("hi"))
	if err != nil {
		t.Fatal(err)
	}
	// worker 返回的错误按错误码还原。
	if _, err := d.web.Start(ctx, "u1", "c1", model.NewUserMessage("again")); !errors.Is(err, chat.ErrBusy) {
		t.Fatalf("second start: %v", err)
	}
	if _, err := d.web.Start(ctx, "u1", "c2", model.NewUserMessage("x"), agent.WithRuntimeState(map[string]any{"k": 1})); !errors.Is(err, chat.ErrRunOptions) {
		t.Fatalf("run options: %v", err)
	}
	<-r.Done()
}

func TestWorkersCancelFromWeb(t *testing.T) {
	t.Parallel()
	m := chattest.NewModel(chattest.Reply{Text: "很慢", Delay: 5 * time.Second})
	d := newDeployment(t, m, nil, 1)
	r, err := d.web.Start(context.Background(), "u1", "c1", model.NewUserMessage("hi"))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	r.Cancel()
	_, events := collect(t, r, "")
	if !events[len(events)-1].Data.(chat.Done).Canceled {
		t.Fatal("run was not canceled")
	}
}

func TestWorkersApproval(t *testing.T) {
	t.Parallel()
	m := chattest.NewModel(
		chattest.Reply{Calls: []chattest.Call{{ID: "r1", Name: "refund_order", Args: orderArgs{"A1"}}}},
		chattest.Text("已退款"),
	)
	var refunded refunds
	refund := refunded.tool()
	d := newDeployment(t, m, []tool.Tool{refund}, 1, chat.RequireApproval(refund))
	ctx := context.Background()
	r, err := d.web.Start(ctx, "u1", "c1", model.NewUserMessage("给 A1 退款"))
	if err != nil {
		t.Fatal(err)
	}
	<-r.Done()
	// 镜像收到结束事件时，执行它的 worker 可能还没释放对话的锁，这期间 Approve 返回 ErrBusy。
	var next *chat.Run
	deadline := time.Now().Add(5 * time.Second)
	for {
		next, err = d.web.Approve(ctx, "u1", "c1", "r1")
		if !errors.Is(err, chat.ErrBusy) || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil || next == nil {
		t.Fatalf("approve: %v %v", next, err)
	}
	names, _ := collect(t, next, "")
	if len(refunded.done) != 1 || !strings.Contains(strings.Join(names, " "), chat.EventText) {
		t.Fatalf("refunded %v events %q", refunded.done, names)
	}
}

// 运行在 worker 回复之前就结束、对话锁已经释放时，web 仍然拿到这次运行的镜像（#173）。
func TestWorkersRunFinishedBeforeReply(t *testing.T) {
	defer chat.SetReplyTimeout(500 * time.Millisecond)()
	defer chat.SetBeforeDone(func(r *chat.Run) {
		<-r.Done()
		time.Sleep(50 * time.Millisecond) // 等 worker 释放对话锁
	})()
	d := newDeployment(t, chattest.NewModel(chattest.Text("好")), nil, 1)
	r, err := d.web.Start(context.Background(), "u1", "c1", model.NewUserMessage("hi"))
	if err != nil || r == nil {
		t.Fatalf("start: %v %v", r, err)
	}
	names, _ := collect(t, r, "")
	if !strings.Contains(strings.Join(names, " "), chat.EventText) {
		t.Fatalf("events %q", names)
	}
}

func TestWorkersUnavailable(t *testing.T) {
	defer chat.SetReplyTimeout(200 * time.Millisecond)()
	ctx := context.Background()
	t.Run("no worker", func(t *testing.T) {
		d := newDeployment(t, chattest.NewModel(), nil, 0)
		if _, err := d.web.Start(ctx, "u1", "c1", model.NewUserMessage("hi")); !errors.Is(err, chat.ErrNoWorker) {
			t.Fatalf("start without workers: %v", err)
		}
	})
	t.Run("stopping worker is skipped", func(t *testing.T) {
		m := chattest.NewModel(chattest.Text("好"))
		d := newDeployment(t, m, nil, 2)
		if err := d.workers[0].Shutdown(ctx); err != nil {
			t.Fatal(err)
		}
		// 两个 worker 都收到请求，停止中的那个不接手。
		r, err := d.web.Start(ctx, "u1", "c1", model.NewUserMessage("hi"))
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		<-r.Done()
		if n := len(m.Requests()); n != 1 {
			t.Fatalf("model called %d times", n)
		}
	})
	t.Run("all workers stopping", func(t *testing.T) {
		d := newDeployment(t, chattest.NewModel(), nil, 1)
		if err := d.workers[0].Shutdown(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := d.web.Start(ctx, "u1", "c1", model.NewUserMessage("hi")); !errors.Is(err, chat.ErrNoWorker) {
			t.Fatalf("start while workers stop: %v", err)
		}
	})
}

func TestWorkersNotifyWithHistory(t *testing.T) {
	t.Parallel()
	m := chattest.NewModel(chattest.Text("十点"))
	table := newMessageTable()
	hub := broadcast.New(broadcasttest.NewBackend(), nil)
	locker := lock.New(locktest.NewStore())
	newRuns := func(m *chattest.Model, extra ...chat.Option) *chat.Runs {
		ag := llmagent.New("support", llmagent.WithModel(m), llmagent.WithGenerationConfig(model.GenerationConfig{Stream: true}))
		return chat.NewRuns("test", ag, nil, append([]chat.Option{chat.UseHistory(table), chat.Cluster(hub, locker)}, extra...)...)
	}
	web, worker := newRuns(chattest.NewModel(), chat.RunOnWorkers()), newRuns(m)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = worker.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = web.Close()
		_ = worker.Close()
		_ = hub.Close()
	})
	time.Sleep(20 * time.Millisecond)

	table.write(group, model.NewUserMessage("[张三] 几点开会"))
	r, err := web.Notify(context.Background(), group.UserID, group.ID)
	if err != nil {
		t.Fatal(err)
	}
	<-r.Done()
	if got := table.dump(group); got != "user:[张三] 几点开会 | assistant:十点" {
		t.Fatalf("table %s", got)
	}
}
