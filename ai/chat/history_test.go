package chat_test

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
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
	"github.com/ryan053857/mygoravel/lock"
	"github.com/ryan053857/mygoravel/lock/locktest"
)

// messageTable 模拟应用自己的消息表：seq 是写入顺序，after 是 Save 时带回的游标。
// Load 按逻辑顺序返回：Save 保存的消息排在它的游标之后，而不是按写入时间。
type messageTable struct {
	mu      sync.Mutex
	rows    map[chat.Conversation][]row
	saveErr error
}

type row struct {
	msg   model.Message
	seq   int
	after int // 基于哪个 seq 之前的上下文产生；-1 表示排在写入的位置
	runID string
}

func newMessageTable() *messageTable {
	return &messageTable{rows: map[chat.Conversation][]row{}}
}

func (t *messageTable) ordered(c chat.Conversation) []row {
	rows := append([]row(nil), t.rows[c]...)
	key := func(r row) (int, int) {
		if r.after >= 0 {
			return r.after, 1
		}
		return r.seq, 0
	}
	sort.SliceStable(rows, func(i, j int) bool {
		ki, gi := key(rows[i])
		kj, gj := key(rows[j])
		if ki != kj {
			return ki < kj
		}
		if gi != gj {
			return gi < gj
		}
		return rows[i].seq < rows[j].seq
	})
	return rows
}

func (t *messageTable) Load(_ context.Context, c chat.Conversation) (chat.History, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var h chat.History
	for _, r := range t.ordered(c) {
		h.Messages = append(h.Messages, r.msg)
	}
	h.Cursor = strconv.Itoa(len(t.rows[c]))
	return h, nil
}

func (t *messageTable) Save(_ context.Context, c chat.Conversation, e chat.Entry) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.saveErr != nil {
		return t.saveErr
	}
	after := -1
	if e.After != "" {
		after, _ = strconv.Atoi(e.After)
	}
	t.rows[c] = append(t.rows[c], row{msg: e.Message, seq: len(t.rows[c]) + 1, after: after, runID: e.RunID})
	return nil
}

// write 是应用自己写入一条消息，例如群成员的发言。
func (t *messageTable) write(c chat.Conversation, msg model.Message) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rows[c] = append(t.rows[c], row{msg: msg, seq: len(t.rows[c]) + 1, after: -1})
}

// dump 按逻辑顺序描述对话。
func (t *messageTable) dump(c chat.Conversation) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var msgs []model.Message
	for _, r := range t.ordered(c) {
		msgs = append(msgs, r.msg)
	}
	return describe(msgs)
}

func (t *messageTable) runOf(c chat.Conversation, content string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, r := range t.rows[c] {
		if r.msg.Content == content {
			return r.runID
		}
	}
	return ""
}

func newHistoryRuns(t *testing.T, m *chattest.Model, tools []tool.Tool, store chat.HistoryStore, opts ...chat.Option) *chat.Runs {
	t.Helper()
	ag := llmagent.New("support", llmagent.WithModel(m), llmagent.WithTools(tools),
		llmagent.WithGenerationConfig(model.GenerationConfig{Stream: true}))
	rs := chat.NewRuns("test", ag, nil, append(opts, chat.UseHistory(store))...)
	t.Cleanup(func() { _ = rs.Close() })
	return rs
}

// describe 把消息列表写成一行，便于比较。
func describe(msgs []model.Message) string {
	var parts []string
	for _, m := range msgs {
		s := string(m.Role) + ":" + strings.ReplaceAll(m.Content, "\n", " ")
		for _, tc := range m.ToolCalls {
			s += "[call " + tc.ID + "]"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " | ")
}

// tail 返回请求中的对话部分，去掉 trpc-agent-go 加在前面的系统提示。
func tail(msgs []model.Message) []model.Message {
	for i, m := range msgs {
		if m.Role != model.RoleSystem {
			return msgs[i:]
		}
	}
	return nil
}

var group = chat.Conversation{UserID: "", ID: "g1"}

func TestHistoryNotify(t *testing.T) {
	t.Parallel()
	m := chattest.NewModel(chattest.Text("明天 上午 十点"))
	table := newMessageTable()
	rs := newHistoryRuns(t, m, nil, table)
	ctx := context.Background()

	table.write(group, model.NewUserMessage("[张三] @助手 明天几点开会"))
	r, err := rs.Notify(ctx, group.UserID, group.ID)
	if err != nil {
		t.Fatal(err)
	}
	names, _ := collect(t, r, "")
	if got := describe(tail(m.LastRequest())); got != "user:[张三] @助手 明天几点开会" {
		t.Fatalf("model saw %s", got)
	}
	if got := table.dump(group); got != "user:[张三] @助手 明天几点开会 | assistant:明天 上午 十点" {
		t.Fatalf("table %s", got)
	}
	if got := table.runOf(group, "明天 上午 十点"); got != r.ID {
		t.Fatalf("answer saved by run %q, want %q", got, r.ID)
	}
	if !strings.Contains(strings.Join(names, " "), chat.EventText) {
		t.Fatalf("events %q", names)
	}
}

// 回答期间到达的消息不被拒绝：运行结束后再运行一轮，多次通知合并为一轮。
func TestHistoryNotifyWhileRunningCoalesces(t *testing.T) {
	t.Parallel()
	m := chattest.NewModel(chattest.Reply{Text: "第一轮", Delay: 300 * time.Millisecond}, chattest.Text("第二轮"))
	table := newMessageTable()
	rs := newHistoryRuns(t, m, nil, table)
	ctx := context.Background()

	table.write(group, model.NewUserMessage("[张三] 第一个问题"))
	r, err := rs.Notify(ctx, group.UserID, group.ID)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	for _, text := range []string{"[李四] 补充一", "[王五] 补充二"} {
		table.write(group, model.NewUserMessage(text))
		again, err := rs.Notify(ctx, group.UserID, group.ID)
		if err != nil || again == nil || again.ID != r.ID {
			t.Fatalf("notify while running returned another run or %v", err)
		}
	}
	<-r.Done()
	if n := len(m.Requests()); n != 2 {
		t.Fatalf("model called %d times, want 2", n)
	}
	want := "user:[张三] 第一个问题 | assistant:第一轮 | user:[李四] 补充一 | user:[王五] 补充二"
	if got := describe(tail(m.LastRequest())); got != want {
		t.Fatalf("second round saw %s\nwant %s", got, want)
	}
	if got := table.dump(group); got != want+" | assistant:第二轮" {
		t.Fatalf("table %s", got)
	}
}

func TestHistoryStartSavesUserMessage(t *testing.T) {
	t.Parallel()
	m := chattest.NewModel(chattest.Text("你好"))
	table := newMessageTable()
	rs := newHistoryRuns(t, m, nil, table)
	c := chat.Conversation{UserID: "u1", ID: "c1"}
	r, err := rs.Start(context.Background(), c.UserID, c.ID, model.NewUserMessage("在吗"))
	if err != nil {
		t.Fatal(err)
	}
	<-r.Done()
	if got := table.dump(c); got != "user:在吗 | assistant:你好" {
		t.Fatalf("table %s", got)
	}
}

func TestHistoryApproval(t *testing.T) {
	t.Parallel()
	m := chattest.NewModel(
		chattest.Reply{Calls: []chattest.Call{{ID: "r1", Name: "refund_order", Args: orderArgs{"A1"}}}},
		chattest.Text("已退款"),
	)
	var refunded refunds
	refund := refunded.tool()
	table := newMessageTable()
	rs := newHistoryRuns(t, m, []tool.Tool{refund}, table, chat.RequireApproval(refund))
	ctx := context.Background()
	c := chat.Conversation{UserID: "u1", ID: "c1"}

	r, err := rs.Start(ctx, c.UserID, c.ID, model.NewUserMessage("给 A1 退款"))
	if err != nil {
		t.Fatal(err)
	}
	<-r.Done()
	// 等待审批的调用由应用保存的消息算出，与运行所在的进程无关。
	calls, err := rs.Pending(ctx, c.UserID, c.ID)
	if err != nil || len(calls) != 1 || calls[0].ID != "r1" {
		t.Fatalf("pending %+v err %v", calls, err)
	}
	if _, err := rs.Notify(ctx, c.UserID, c.ID); !errors.Is(err, chat.ErrAwaitingApproval) {
		t.Fatalf("notify while awaiting approval: %v", err)
	}
	next, err := rs.Approve(ctx, c.UserID, c.ID, "r1")
	if err != nil || next == nil {
		t.Fatalf("approve: %v %v", next, err)
	}
	<-next.Done()
	if len(refunded.done) != 1 {
		t.Fatalf("refunded %v", refunded.done)
	}
	if got := table.dump(c); got != "user:给 A1 退款 | assistant:[call r1] | tool:refunded A1 | assistant:已退款" {
		t.Fatalf("table %s", got)
	}
}

func TestHistorySteerIsSaved(t *testing.T) {
	t.Parallel()
	m := chattest.NewModel(
		chattest.Calls(chattest.Call{ID: "g1", Name: "get_order", Args: orderArgs{"A1"}}),
		chattest.Text("都查好了"),
	)
	table := newMessageTable()
	rs := newHistoryRuns(t, m, []tool.Tool{getOrder(300 * time.Millisecond)}, table)
	ctx := context.Background()
	c := chat.Conversation{UserID: "u1", ID: "c1"}
	r, err := rs.Start(ctx, c.UserID, c.ID, model.NewUserMessage("查 A1"))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := r.Steer(ctx, model.NewUserMessage("还有 B2")); err != nil {
		t.Fatal(err)
	}
	<-r.Done()
	got := table.dump(c)
	if !strings.Contains(got, "user:还有 B2") || !strings.HasSuffix(got, "assistant:都查好了") {
		t.Fatalf("table %s", got)
	}
	if !strings.Contains(describe(m.LastRequest()), "user:还有 B2") {
		t.Fatalf("model did not see the steered message")
	}
}

func TestHistorySaveFailureStopsRun(t *testing.T) {
	t.Parallel()
	m := chattest.NewModel(chattest.Text("回答"))
	table := newMessageTable()
	rs := newHistoryRuns(t, m, nil, table)
	table.write(group, model.NewUserMessage("问题"))
	table.saveErr = errors.New("disk full")
	r, err := rs.Notify(context.Background(), group.UserID, group.ID)
	if err != nil {
		t.Fatal(err)
	}
	names, _ := collect(t, r, "")
	if !strings.Contains(strings.Join(names, " "), chat.EventError) {
		t.Fatalf("events %q", names)
	}
}

func TestHistoryNothingToAnswer(t *testing.T) {
	t.Parallel()
	m := chattest.NewModel()
	table := newMessageTable()
	rs := newHistoryRuns(t, m, nil, table)
	table.write(group, model.NewUserMessage("问题"))
	table.write(group, model.NewAssistantMessage("已经回答过了"))
	r, err := rs.Notify(context.Background(), group.UserID, group.ID)
	if err != nil {
		t.Fatal(err)
	}
	<-r.Done()
	if n := len(m.Requests()); n != 0 {
		t.Fatalf("model called %d times", n)
	}
}

func TestHistoryModeAPIs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rs := newHistoryRuns(t, chattest.NewModel(), nil, newMessageTable())
	if _, err := rs.UIMessages(ctx, "u1", "c1"); !errors.Is(err, chat.ErrNeedsSessions) {
		t.Fatalf("UIMessages: %v", err)
	}
	if err := rs.Append(ctx, "u1", "c1", "system", model.NewUserMessage("x")); !errors.Is(err, chat.ErrNeedsSessions) {
		t.Fatalf("Append: %v", err)
	}

	plain := newRuns(t, chattest.NewModel(), nil)
	if _, err := plain.Notify(ctx, "u1", "c1"); !errors.Is(err, chat.ErrNeedsHistory) {
		t.Fatalf("Notify without History: %v", err)
	}

	ag := llmagent.New("support", llmagent.WithModel(chattest.NewModel()))
	for name, build := range map[string]func(){
		"neither": func() { chat.NewRuns("test", ag, nil) },
		"both":    func() { chat.NewRuns("test", ag, inmemory.NewSessionService(), chat.UseHistory(newMessageTable())) },
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

// 消息在回答保存之前写入、通知在运行结束之后才到达：新的运行仍然回应它。
func TestHistoryLateNotify(t *testing.T) {
	t.Parallel()
	m := chattest.NewModel(chattest.Reply{Text: "第一轮", Delay: 200 * time.Millisecond}, chattest.Text("第二轮"))
	table := newMessageTable()
	rs := newHistoryRuns(t, m, nil, table)
	ctx := context.Background()
	table.write(group, model.NewUserMessage("[张三] 问题"))
	r, err := rs.Notify(ctx, group.UserID, group.ID)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	table.write(group, model.NewUserMessage("[李四] 补充")) // 写入时回答还没保存
	<-r.Done()
	next, err := rs.Notify(ctx, group.UserID, group.ID) // 通知晚到
	if err != nil {
		t.Fatal(err)
	}
	<-next.Done()
	if n := len(m.Requests()); n != 2 {
		t.Fatalf("model called %d times, want 2", n)
	}
	if got := describe(tail(m.LastRequest())); got != "user:[张三] 问题 | assistant:第一轮 | user:[李四] 补充" {
		t.Fatalf("second run saw %s", got)
	}
}

// 多实例：通知落在另一个实例上时转发给运行所在的实例，运行结束后再运行一轮。
func TestClusterHistoryNotifyForwarded(t *testing.T) {
	t.Parallel()
	m := chattest.NewModel(chattest.Reply{Text: "第一轮", Delay: 300 * time.Millisecond}, chattest.Text("第二轮"))
	table := newMessageTable()
	hub := broadcast.New(broadcasttest.NewBackend(), nil)
	locker := lock.New(locktest.NewStore())
	newInstance := func() *chat.Runs {
		ag := llmagent.New("support", llmagent.WithModel(m), llmagent.WithGenerationConfig(model.GenerationConfig{Stream: true}))
		return chat.NewRuns("test", ag, nil, chat.UseHistory(table), chat.Cluster(hub, locker))
	}
	a, b := newInstance(), newInstance()
	t.Cleanup(func() {
		_ = a.Close()
		_ = b.Close()
		_ = hub.Close()
	})
	ctx := context.Background()
	table.write(group, model.NewUserMessage("[张三] 问题"))
	r, err := a.Notify(ctx, group.UserID, group.ID)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	table.write(group, model.NewUserMessage("[李四] 补充"))
	forwarded, err := b.Notify(ctx, group.UserID, group.ID)
	if err != nil || forwarded == nil || forwarded.ID != r.ID {
		t.Fatalf("notify on the other instance did not reach the run: %v", err)
	}
	<-r.Done()
	if n := len(m.Requests()); n != 2 {
		t.Fatalf("model called %d times, want 2", n)
	}
	if got := table.dump(group); got != "user:[张三] 问题 | assistant:第一轮 | user:[李四] 补充 | assistant:第二轮" {
		t.Fatalf("table %s", got)
	}
}
