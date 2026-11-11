package ai_test

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

	"github.com/ryan053857/mygoravel/ai"
	"github.com/ryan053857/mygoravel/ai/aitest"
	"github.com/ryan053857/mygoravel/broadcast"
	"github.com/ryan053857/mygoravel/broadcast/broadcasttest"
	"github.com/ryan053857/mygoravel/lock"
	"github.com/ryan053857/mygoravel/lock/locktest"
)

// deployment 是一个 web 实例（RunOnWorkers）和若干 worker 实例（Serve），共用对话存储、广播和锁。
type deployment struct {
	web     *ai.Runs
	workers []*ai.Runs
	stop    []context.CancelFunc
}

// newDeployment 创建部署。web 的模型没有任何回复，被调用就会出错：运行必须发生在 worker 上。
func newDeployment(t *testing.T, workerModel *aitest.Model, tools []tool.Tool, workers int, opts ...ai.Option) *deployment {
	t.Helper()
	sessions := inmemory.NewSessionService()
	hub := broadcast.New(broadcasttest.NewBackend(), nil)
	locker := lock.New(locktest.NewStore())
	newRuns := func(m *aitest.Model, extra ...ai.Option) *ai.Runs {
		ag := llmagent.New("support", llmagent.WithModel(m), llmagent.WithTools(tools),
			llmagent.WithGenerationConfig(model.GenerationConfig{Stream: true}))
		return ai.NewRuns("test", ag, sessions, append(append([]ai.Option{ai.Cluster(hub, locker)}, opts...), extra...)...)
	}
	d := &deployment{web: newRuns(aitest.NewModel(), ai.RunOnWorkers())}
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
	m := aitest.NewModel(aitest.Text("你好 世界"))
	d := newDeployment(t, m, nil, 2)
	ctx := context.Background()
	r, err := d.web.Start(ctx, "u1", "c1", model.NewUserMessage("hi"))
	if err != nil {
		t.Fatal(err)
	}
	names, _ := collect(t, r, "")
	if got := strings.Join(names, " "); !strings.Contains(got, ai.EventText) || !strings.HasSuffix(got, ai.EventDone) {
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
	m := aitest.NewModel(aitest.Reply{Text: "慢", Delay: 300 * time.Millisecond})
	d := newDeployment(t, m, nil, 1)
	ctx := context.Background()
	r, err := d.web.Start(ctx, "u1", "c1", model.NewUserMessage("hi"))
	if err != nil {
		t.Fatal(err)
	}
	// worker 返回的错误按错误码还原。
	if _, err := d.web.Start(ctx, "u1", "c1", model.NewUserMessage("again")); !errors.Is(err, ai.ErrBusy) {
		t.Fatalf("second start: %v", err)
	}
	if _, err := d.web.Start(ctx, "u1", "c2", model.NewUserMessage("x"), agent.WithRuntimeState(map[string]any{"k": 1})); !errors.Is(err, ai.ErrRunOptions) {
		t.Fatalf("run options: %v", err)
	}
	<-r.Done()
}

func TestWorkersCancelFromWeb(t *testing.T) {
	m := aitest.NewModel(aitest.Reply{Text: "很慢", Delay: 5 * time.Second})
	d := newDeployment(t, m, nil, 1)
	r, err := d.web.Start(context.Background(), "u1", "c1", model.NewUserMessage("hi"))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	r.Cancel()
	_, events := collect(t, r, "")
	if !events[len(events)-1].Data.(ai.Done).Canceled {
		t.Fatal("run was not canceled")
	}
}

func TestWorkersApproval(t *testing.T) {
	m := aitest.NewModel(
		aitest.Reply{Calls: []aitest.Call{{ID: "r1", Name: "refund_order", Args: orderArgs{"A1"}}}},
		aitest.Text("已退款"),
	)
	var refunded refunds
	refund := refunded.tool()
	d := newDeployment(t, m, []tool.Tool{refund}, 1, ai.RequireApproval(refund))
	ctx := context.Background()
	r, err := d.web.Start(ctx, "u1", "c1", model.NewUserMessage("给 A1 退款"))
	if err != nil {
		t.Fatal(err)
	}
	<-r.Done()
	next, err := d.web.Approve(ctx, "u1", "c1", "r1")
	if err != nil || next == nil {
		t.Fatalf("approve: %v %v", next, err)
	}
	names, _ := collect(t, next, "")
	if len(refunded.done) != 1 || !strings.Contains(strings.Join(names, " "), ai.EventText) {
		t.Fatalf("refunded %v events %q", refunded.done, names)
	}
}

func TestWorkersUnavailable(t *testing.T) {
	defer ai.SetReplyTimeout(200 * time.Millisecond)()
	ctx := context.Background()
	t.Run("no worker", func(t *testing.T) {
		d := newDeployment(t, aitest.NewModel(), nil, 0)
		if _, err := d.web.Start(ctx, "u1", "c1", model.NewUserMessage("hi")); !errors.Is(err, ai.ErrNoWorker) {
			t.Fatalf("start without workers: %v", err)
		}
	})
	t.Run("stopping worker is skipped", func(t *testing.T) {
		m := aitest.NewModel(aitest.Text("好"))
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
		d := newDeployment(t, aitest.NewModel(), nil, 1)
		if err := d.workers[0].Shutdown(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := d.web.Start(ctx, "u1", "c1", model.NewUserMessage("hi")); !errors.Is(err, ai.ErrNoWorker) {
			t.Fatalf("start while workers stop: %v", err)
		}
	})
}

func TestWorkersNotifyWithHistory(t *testing.T) {
	m := aitest.NewModel(aitest.Text("十点"))
	table := newMessageTable()
	hub := broadcast.New(broadcasttest.NewBackend(), nil)
	locker := lock.New(locktest.NewStore())
	newRuns := func(m *aitest.Model, extra ...ai.Option) *ai.Runs {
		ag := llmagent.New("support", llmagent.WithModel(m), llmagent.WithGenerationConfig(model.GenerationConfig{Stream: true}))
		return ai.NewRuns("test", ag, nil, append([]ai.Option{ai.UseHistory(table), ai.Cluster(hub, locker)}, extra...)...)
	}
	web, worker := newRuns(aitest.NewModel(), ai.RunOnWorkers()), newRuns(m)
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
