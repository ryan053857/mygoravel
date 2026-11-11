package chat_test

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/uptrace/bun"
	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/session"
	tmysql "trpc.group/trpc-go/trpc-agent-go/session/mysql"
	tpg "trpc.group/trpc-go/trpc-agent-go/session/postgres"
	tsqlite "trpc.group/trpc-go/trpc-agent-go/session/sqlite"
	"trpc.group/trpc-go/trpc-agent-go/tool"

	"github.com/ryan053857/mygoravel/ai/chat"
	"github.com/ryan053857/mygoravel/ai/chat/chattest"
	aimysql "github.com/ryan053857/mygoravel/ai/chat/mysql"
	aipg "github.com/ryan053857/mygoravel/ai/chat/postgres"
	aisqlite "github.com/ryan053857/mygoravel/ai/chat/sqlite"
	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/internal/dbtesting"
)

// newSessions 按数据库类型创建会话存储；initSchema 为 true 时让 trpc-agent-go 自己建表。
func newSessions(t *testing.T, driver database.Driver, db *bun.DB, initSchema bool) session.Service {
	t.Helper()
	var s session.Service
	var err error
	switch driver.Name() {
	case "postgres":
		s, err = aipg.NewSessions(db, tpg.WithSkipDBInit(!initSchema))
	case "mysql":
		s, err = aimysql.NewSessions(db, tmysql.WithSkipDBInit(!initSchema))
	case "sqlite":
		s, err = aisqlite.NewSessions(db, tsqlite.WithSkipDBInit(!initSchema))
	default:
		t.Fatalf("unsupported driver %s", driver.Name())
	}
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestSessionMigrationsMatchTrpc 确保迁移建出的表与 trpc-agent-go 自己建的完全一致，升级 trpc-agent-go 时发现表结构的变化。
func TestSessionMigrationsMatchTrpc(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		auto := dbtesting.Open(t, driver, nil)
		s := newSessions(t, driver, auto, true)
		_ = s.Close()
		migrated := dbtesting.Open(t, driver, chat.Migrations())
		want, got := schema(t, driver, auto), schema(t, driver, migrated)
		if len(want) == 0 {
			t.Fatal("trpc-agent-go created no tables")
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("migrations differ from trpc-agent-go schema\nmigrations:\n%s\n\ntrpc-agent-go:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	})
}

func schema(t *testing.T, driver database.Driver, db *bun.DB) []string {
	t.Helper()
	ctx := context.Background()
	var out []string
	query := func(q string, args ...any) {
		rows, err := db.QueryContext(ctx, q, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]string, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			out = append(out, strings.Join(vals, " | "))
		}
	}
	switch driver.Name() {
	case "postgres":
		query(`SELECT table_name, column_name, data_type, is_nullable, coalesce(column_default, ''), coalesce(character_maximum_length::text, '')
			FROM information_schema.columns WHERE table_schema = 'public' AND table_name LIKE 'agent\_%' ORDER BY 1, 2`)
		query(`SELECT indexname, indexdef FROM pg_indexes WHERE schemaname = 'public' AND tablename LIKE 'agent\_%' ORDER BY 1`)
	case "mysql":
		var tables []string
		if err := db.NewRaw(`SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name LIKE 'agent\_%' ORDER BY 1`).Scan(ctx, &tables); err != nil {
			t.Fatal(err)
		}
		autoInc := regexp.MustCompile(` AUTO_INCREMENT=\d+`)
		for _, table := range tables {
			var name, ddl string
			if err := db.QueryRowContext(ctx, "SHOW CREATE TABLE "+table).Scan(&name, &ddl); err != nil {
				t.Fatal(err)
			}
			out = append(out, autoInc.ReplaceAllString(ddl, ""))
		}
	case "sqlite":
		query(`SELECT type, name, sql FROM sqlite_master WHERE name LIKE 'agent\_%' ESCAPE '\' ORDER BY name`)
		space := regexp.MustCompile(`\s+`)
		for i, s := range out {
			out[i] = space.ReplaceAllString(s, "")
		}
	}
	return out
}

// TestSessionsAcrossRestart 在迁移建出的表上运行到等待审批，再用新的 Runs（相当于新进程）批准并继续。
func TestSessionsAcrossRestart(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		db := dbtesting.Open(t, driver, chat.Migrations())
		ctx := context.Background()
		var refunded refunds
		refund := refunded.tool()
		m := chattest.NewModel(
			chattest.Calls(chattest.Call{ID: "r1", Name: "refund_order", Args: orderArgs{"A1"}}),
			chattest.Text("已退款"),
		)
		start := func() (*chat.Runs, func()) {
			sessions := newSessions(t, driver, db, false)
			ag := llmagent.New("support", llmagent.WithModel(m), llmagent.WithTools([]tool.Tool{refund}),
				llmagent.WithGenerationConfig(model.GenerationConfig{Stream: true}))
			rs := chat.NewRuns("test", ag, sessions, chat.RequireApproval(refund))
			return rs, func() { _ = rs.Close(); _ = sessions.Close() }
		}

		first, stop := start()
		r, err := first.Start(ctx, "u1", "c1", model.NewUserMessage("退款 A1"))
		if err != nil {
			t.Fatal(err)
		}
		wait(t, r)
		stop()
		// 会话存储关闭后，应用的连接池仍然可用。
		if err := db.PingContext(ctx); err != nil {
			t.Fatalf("app database closed with sessions: %v", err)
		}

		second, stop := start()
		defer stop()
		pending, err := second.Pending(ctx, "u1", "c1")
		if err != nil || len(pending) != 1 || pending[0].ID != "r1" {
			t.Fatalf("pending %+v, %v", pending, err)
		}
		r, err = second.Approve(ctx, "u1", "c1", "r1")
		if err != nil || r == nil {
			t.Fatalf("approve: %v %v", r, err)
		}
		wait(t, r)
		if len(refunded.done) != 1 {
			t.Fatalf("refunded %v", refunded.done)
		}
		if got := messagesOf(m.LastRequest()); !strings.HasSuffix(got, "user:退款 A1 | assistant:call refund_order | tool:refunded A1") {
			t.Fatalf("history sent to model: %s", got)
		}
		history, err := second.UIMessages(ctx, "u1", "c1")
		if err != nil || len(history) != 2 || history[1].Parts[len(history[1].Parts)-1].Text != "已退款" {
			t.Fatalf("history %+v, %v", history, err)
		}
	})
}

func wait(t *testing.T, r *chat.Run) {
	t.Helper()
	select {
	case <-r.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("run did not finish")
	}
}
