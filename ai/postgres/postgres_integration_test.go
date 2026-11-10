//go:build integration

package postgres_test

import (
	"context"
	"io/fs"
	"net/url"
	"strings"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/tool"
	"trpc.group/trpc-go/trpc-agent-go/tool/function"

	"github.com/ryan053857/mygoravel/ai"
	"github.com/ryan053857/mygoravel/ai/aitest"
	aipg "github.com/ryan053857/mygoravel/ai/postgres"
	"github.com/ryan053857/mygoravel/database/dbtest"
	"github.com/ryan053857/mygoravel/database/postgres"
)

type refundArgs struct {
	OrderID string `json:"order_id"`
}

// TestApprovalSurvivesRestart 在迁移创建的表上运行到审批，再用新的 Runs（相当于新进程）批准并继续。
func TestApprovalSurvivesRestart(t *testing.T) {
	migrations, err := fs.Sub(ai.Migrations(), "postgres")
	if err != nil {
		t.Fatal(err)
	}
	db := dbtest.Open(t, postgres.Driver, migrations)
	var name string
	if err := db.QueryRow("SELECT current_database()").Scan(&name); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dbtest.RequireURL(t, postgres.Driver))
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	ctx := context.Background()

	refunded := 0
	refund := function.NewFunctionTool(func(ctx context.Context, a refundArgs) (string, error) {
		refunded++
		return "refunded " + a.OrderID, nil
	}, function.WithName("refund_order"), function.WithDescription("退款"))
	m := aitest.NewModel(
		aitest.Calls(aitest.Call{ID: "r1", Name: "refund_order", Args: refundArgs{"A1"}}),
		aitest.Text("已退款"),
	)
	newRuns := func() (*ai.Runs, func()) {
		sessions, err := aipg.NewSessions(u.String())
		if err != nil {
			t.Fatal(err)
		}
		ag := llmagent.New("support", llmagent.WithModel(m), llmagent.WithTools([]tool.Tool{refund}),
			llmagent.WithGenerationConfig(model.GenerationConfig{Stream: true}))
		rs := ai.NewRuns("test", ag, sessions, ai.RequireApproval(refund))
		return rs, func() { _ = rs.Close(); _ = sessions.Close() }
	}

	first, closeFirst := newRuns()
	r, err := first.Start(ctx, "u1", "c1", model.NewUserMessage("退款 A1"))
	if err != nil {
		t.Fatal(err)
	}
	wait(t, r)
	closeFirst()

	second, closeSecond := newRuns()
	defer closeSecond()
	pending, err := second.Pending(ctx, "u1", "c1")
	if err != nil || len(pending) != 1 || pending[0].ID != "r1" {
		t.Fatalf("pending %+v, %v", pending, err)
	}
	r, err = second.Approve(ctx, "u1", "c1", "r1")
	if err != nil || r == nil {
		t.Fatalf("approve: %v %v", r, err)
	}
	wait(t, r)
	if refunded != 1 {
		t.Fatalf("refunded %d times", refunded)
	}
	var history []string
	for _, msg := range m.LastRequest() {
		history = append(history, string(msg.Role)+":"+msg.Content)
	}
	if got := strings.Join(history, " | "); !strings.HasSuffix(got, "user:退款 A1 | assistant: | tool:refunded A1") {
		t.Fatalf("history sent to model: %s", got)
	}
}

func wait(t *testing.T, r *ai.Run) {
	t.Helper()
	select {
	case <-r.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("run did not finish")
	}
}
