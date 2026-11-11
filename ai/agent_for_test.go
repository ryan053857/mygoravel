package ai_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/session/inmemory"
	"trpc.group/trpc-go/trpc-agent-go/tool"

	"github.com/ryan053857/mygoravel/ai"
	"github.com/ryan053857/mygoravel/ai/aitest"
)

// employees 模拟按对话选择 AI 员工：每个对话对应一个 agent，可以随时替换。
type employees struct {
	mu     sync.Mutex
	agents map[string]agent.Agent
}

func (e *employees) set(conversation string, ag agent.Agent) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.agents[conversation] = ag
}

func (e *employees) choose(_ context.Context, c ai.Conversation) (agent.Agent, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	ag, ok := e.agents[c.ID]
	if !ok {
		return nil, errors.New("no employee for " + c.ID)
	}
	return ag, nil
}

func employee(name string, m model.Model, tools ...tool.Tool) agent.Agent {
	return llmagent.New(name, llmagent.WithModel(m), llmagent.WithTools(tools),
		llmagent.WithGenerationConfig(model.GenerationConfig{Stream: true}))
}

func newAgentForRuns(t *testing.T, e *employees, opts ...ai.Option) *ai.Runs {
	t.Helper()
	sessions := inmemory.NewSessionService()
	rs := ai.NewRuns("test", nil, sessions, append(opts, ai.AgentFor(e.choose))...)
	t.Cleanup(func() {
		_ = rs.Close()
		_ = sessions.Close()
	})
	return rs
}

func answer(t *testing.T, r *ai.Run) string {
	t.Helper()
	_, events := collect(t, r, "")
	var b strings.Builder
	for _, ev := range events {
		if ev.Name == ai.EventText {
			b.WriteString(ev.Data.(ai.Text).Text)
		}
		if ev.Name == ai.EventError {
			b.WriteString("error:" + ev.Data.(ai.Error).Message)
		}
	}
	return b.String()
}

func TestAgentForChoosesPerConversation(t *testing.T) {
	a, b := aitest.NewModel(aitest.Text("我是小A")), aitest.NewModel(aitest.Text("我是小B"))
	e := &employees{agents: map[string]agent.Agent{"c1": employee("xiaoa", a), "c2": employee("xiaob", b)}}
	rs := newAgentForRuns(t, e)
	ctx := context.Background()
	for conv, want := range map[string]string{"c1": "我是小A", "c2": "我是小B"} {
		r, err := rs.Start(ctx, "u1", conv, model.NewUserMessage("你是谁"))
		if err != nil {
			t.Fatal(err)
		}
		if got := answer(t, r); got != want {
			t.Fatalf("%s answered %q, want %q", conv, got, want)
		}
	}
	if len(a.Requests()) != 1 || len(b.Requests()) != 1 {
		t.Fatalf("model calls a=%d b=%d", len(a.Requests()), len(b.Requests()))
	}
}

// 每一轮重新选择：修改 AI 员工的配置后，下一次运行就使用新的 agent。
func TestAgentForChangeTakesEffectNextRun(t *testing.T) {
	old, updated := aitest.NewModel(aitest.Text("旧配置")), aitest.NewModel(aitest.Text("新配置"))
	e := &employees{agents: map[string]agent.Agent{"c1": employee("xiaoa", old)}}
	rs := newAgentForRuns(t, e, ai.KeepFinished(0))
	ctx := context.Background()
	r, err := rs.Start(ctx, "u1", "c1", model.NewUserMessage("一"))
	if err != nil {
		t.Fatal(err)
	}
	if got := answer(t, r); got != "旧配置" {
		t.Fatalf("first answer %q", got)
	}
	e.set("c1", employee("xiaoa", updated))
	r, err = rs.Start(ctx, "u1", "c1", model.NewUserMessage("二"))
	if err != nil {
		t.Fatal(err)
	}
	if got := answer(t, r); got != "新配置" {
		t.Fatalf("second answer %q", got)
	}
	// 新的 agent 看到之前的对话。
	if got := describe(tail(updated.LastRequest())); !strings.Contains(got, "user:一") || !strings.Contains(got, "旧配置") {
		t.Fatalf("new agent saw %s", got)
	}
}

func TestAgentForChooseError(t *testing.T) {
	rs := newAgentForRuns(t, &employees{agents: map[string]agent.Agent{}})
	r, err := rs.Start(context.Background(), "u1", "nobody", model.NewUserMessage("hi"))
	if err != nil {
		t.Fatal(err)
	}
	if got := answer(t, r); !strings.Contains(got, "error:") || !strings.Contains(got, "no employee for nobody") {
		t.Fatalf("answer %q", got)
	}
}

// 工具由各个 AI 员工自己提供：按名称要求审批，批准后执行这个对话的 agent 上的工具。
func TestAgentForApprovalUsesEmployeeTool(t *testing.T) {
	m := aitest.NewModel(
		aitest.Reply{Calls: []aitest.Call{{ID: "r1", Name: "refund_order", Args: orderArgs{"A1"}}}},
		aitest.Text("已退款"),
	)
	var refunded refunds
	e := &employees{agents: map[string]agent.Agent{"c1": employee("xiaoa", m, refunded.tool())}}
	rs := newAgentForRuns(t, e, ai.RequireApprovalOf("refund_order"))
	ctx := context.Background()
	r, err := rs.Start(ctx, "u1", "c1", model.NewUserMessage("给 A1 退款"))
	if err != nil {
		t.Fatal(err)
	}
	<-r.Done()
	if len(refunded.done) != 0 {
		t.Fatal("tool ran before approval")
	}
	if calls, err := rs.Pending(ctx, "u1", "c1"); err != nil || len(calls) != 1 {
		t.Fatalf("pending %+v err %v", calls, err)
	}
	next, err := rs.Approve(ctx, "u1", "c1", "r1")
	if err != nil || next == nil {
		t.Fatalf("approve: %v %v", next, err)
	}
	if got := answer(t, next); got != "已退款" || len(refunded.done) != 1 {
		t.Fatalf("answer %q refunded %v", got, refunded.done)
	}
}

// 群聊：应用的消息表加按会话选择的 AI 员工。
func TestAgentForWithHistory(t *testing.T) {
	m := aitest.NewModel(aitest.Text("十点"))
	e := &employees{agents: map[string]agent.Agent{group.ID: employee("xiaoa", m)}}
	table := newMessageTable()
	rs := ai.NewRuns("test", nil, nil, ai.UseHistory(table), ai.AgentFor(e.choose))
	t.Cleanup(func() { _ = rs.Close() })
	table.write(group, model.NewUserMessage("[张三] @小A 几点开会"))
	r, err := rs.Notify(context.Background(), group.UserID, group.ID)
	if err != nil {
		t.Fatal(err)
	}
	<-r.Done()
	if got := table.dump(group); got != "user:[张三] @小A 几点开会 | assistant:十点" {
		t.Fatalf("table %s", got)
	}
}

func TestAgentForNewRunsChecks(t *testing.T) {
	e := &employees{agents: map[string]agent.Agent{}}
	sessions := inmemory.NewSessionService()
	defer sessions.Close()
	for name, build := range map[string]func(){
		"neither": func() { ai.NewRuns("test", nil, sessions) },
		"both":    func() { ai.NewRuns("test", employee("x", aitest.NewModel()), sessions, ai.AgentFor(e.choose)) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: NewRuns did not panic", name)
				}
			}()
			build()
		}()
	}
}
