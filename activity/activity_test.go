package activity_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/activity"
	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/internal/dbtesting"
	"github.com/ryan053857/mygoravel/logging"
)

var alice = identity.Subject{ID: "u1", Type: identity.SubjectUser, Tenant: "acme", Permissions: []string{"*"}}

func record(t *testing.T, db bun.IDB, actor identity.Subject, a activity.Activity) {
	t.Helper()
	if err := activity.Record(context.Background(), db, actor, a); err != nil {
		t.Fatal(err)
	}
}

func events(list []activity.Activity) string {
	var out []string
	for _, a := range list {
		out = append(out, a.Event)
	}
	return strings.Join(out, ",")
}

func TestRecordFillsActorTenantAndRequestID(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		db := dbtesting.Open(t, driver, activity.Migrations())
		ctx := logging.WithAttrs(context.Background(), slog.String(logging.AttrRequestID, "req-1"))
		before := time.Now().UTC().Add(-time.Second)
		err := activity.Record(ctx, db, alice, activity.Activity{
			Event: "article.published", TargetType: "article", TargetID: "a1",
			Properties: map[string]any{"title": "Hello", "tags": []any{"go"}},
			// 由 Record 填写的字段会被覆盖。
			Tenant: "other", ActorID: "forged", ImpersonatorID: "forged", RequestID: "forged",
		})
		if err != nil {
			t.Fatal(err)
		}
		list, err := activity.List(context.Background(), db, activity.Query{Tenant: "acme"})
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 1 {
			t.Fatalf("list=%+v", list)
		}
		a := list[0]
		if a.ID.Version() != 7 || a.Tenant != "acme" || a.ActorType != identity.SubjectUser || a.ActorID != "u1" || a.ImpersonatorID != "" ||
			a.Event != "article.published" || a.TargetType != "article" || a.TargetID != "a1" || a.RequestID != "req-1" {
			t.Fatalf("activity=%+v", a)
		}
		if a.Properties["title"] != "Hello" || len(a.Properties["tags"].([]any)) != 1 {
			t.Fatalf("properties=%#v", a.Properties)
		}
		if a.CreatedAt.Before(before) || a.CreatedAt.After(time.Now().Add(time.Second)) {
			t.Fatalf("created_at=%v", a.CreatedAt)
		}
	})
}

// 代为登录时，操作人是被代为登录的用户，ImpersonatorID 记下实际操作的人。
func TestRecordImpersonation(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		db := dbtesting.Open(t, driver, activity.Migrations())
		asAlice := alice
		asAlice.Impersonator = "staff"
		record(t, db, asAlice, activity.Activity{Event: "article.published"})
		list, err := activity.List(context.Background(), db, activity.Query{Tenant: "acme"})
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 1 || list[0].ActorID != "u1" || list[0].ImpersonatorID != "staff" {
			t.Fatalf("list=%+v", list)
		}
	})
}

func TestRecordWithoutActorAndProperties(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		db := dbtesting.Open(t, driver, activity.Migrations())
		record(t, db, identity.Subject{Tenant: "acme"}, activity.Activity{Event: "articles.scheduled_published"})
		list, err := activity.List(context.Background(), db, activity.Query{Tenant: "acme"})
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 1 || list[0].ActorID != "" || list[0].Properties == nil || len(list[0].Properties) != 0 {
			t.Fatalf("list=%+v", list)
		}
	})
}

func TestRecordRollsBackWithTransaction(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		db := dbtesting.Open(t, driver, activity.Migrations())
		ctx := context.Background()
		err := db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
			if err := activity.Record(ctx, tx, alice, activity.Activity{Event: "article.deleted"}); err != nil {
				return err
			}
			return context.Canceled
		})
		if err != context.Canceled {
			t.Fatal(err)
		}
		list, err := activity.List(ctx, db, activity.Query{Tenant: "acme"})
		if err != nil || len(list) != 0 {
			t.Fatalf("list=%+v err=%v", list, err)
		}
	})
}

func TestRecordRejectsInvalid(t *testing.T) {
	db := dbtesting.Open(t, dbtesting.Drivers()[0], activity.Migrations())
	long := strings.Repeat("x", 192)
	for name, c := range map[string]struct {
		actor identity.Subject
		a     activity.Activity
	}{
		"empty event":        {alice, activity.Activity{}},
		"id without type":    {alice, activity.Activity{Event: "e", TargetID: "1"}},
		"long event":         {alice, activity.Activity{Event: long}},
		"long target id":     {alice, activity.Activity{Event: "e", TargetType: "t", TargetID: long}},
		"long actor id":      {identity.Subject{ID: long}, activity.Activity{Event: "e"}},
		"unencodable values": {alice, activity.Activity{Event: "e", Properties: map[string]any{"f": func() {}}}},
	} {
		if err := activity.Record(context.Background(), db, c.actor, c.a); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestListFiltersAndPages(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		db := dbtesting.Open(t, driver, activity.Migrations())
		bob := identity.Subject{ID: "u2", Type: identity.SubjectUser, Tenant: "acme"}
		record(t, db, alice, activity.Activity{Event: "article.created", TargetType: "article", TargetID: "a1"})
		record(t, db, bob, activity.Activity{Event: "article.updated", TargetType: "article", TargetID: "a1"})
		record(t, db, alice, activity.Activity{Event: "article.created", TargetType: "article", TargetID: "a2"})
		record(t, db, alice, activity.Activity{Event: "member.invited", TargetType: "user", TargetID: "a1"})
		record(t, db, identity.Subject{ID: "u1", Tenant: "globex"}, activity.Activity{Event: "article.created", TargetType: "article", TargetID: "a1"})
		record(t, db, identity.Subject{ID: "ops"}, activity.Activity{Event: "tenant.suspended", TargetType: "tenant", TargetID: "acme"})

		ctx := context.Background()
		for name, c := range map[string]struct {
			q    activity.Query
			want string
		}{
			"tenant":   {activity.Query{Tenant: "acme"}, "member.invited,article.created,article.updated,article.created"},
			"platform": {activity.Query{}, "tenant.suspended"},
			"target":   {activity.Query{Tenant: "acme", TargetType: "article", TargetID: "a1"}, "article.updated,article.created"},
			"type":     {activity.Query{Tenant: "acme", TargetType: "article"}, "article.created,article.updated,article.created"},
			"actor":    {activity.Query{Tenant: "acme", ActorID: "u1"}, "member.invited,article.created,article.created"},
			"event":    {activity.Query{Tenant: "acme", Event: "article.created"}, "article.created,article.created"},
			"limit":    {activity.Query{Tenant: "acme", Limit: 2}, "member.invited,article.created"},
		} {
			list, err := activity.List(ctx, db, c.q)
			if err != nil {
				t.Fatal(err)
			}
			if got := events(list); got != c.want {
				t.Errorf("%s: got %s want %s", name, got, c.want)
			}
		}

		var pages []string
		var before uuid.UUID
		for {
			page, err := activity.List(ctx, db, activity.Query{Tenant: "acme", Limit: 3, Before: before})
			if err != nil {
				t.Fatal(err)
			}
			if len(page) == 0 {
				break
			}
			pages = append(pages, events(page))
			before = page[len(page)-1].ID
		}
		if got := strings.Join(pages, "|"); got != "member.invited,article.created,article.updated|article.created" {
			t.Fatalf("pages=%s", got)
		}

		if _, err := activity.List(ctx, db, activity.Query{TargetID: "a1"}); err == nil {
			t.Fatal("expected error for target id without type")
		}
	})
}

func TestCleanupDeletesOldRecords(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		db := dbtesting.Open(t, driver, activity.Migrations())
		ctx := context.Background()
		for range 3 {
			record(t, db, alice, activity.Activity{Event: "old"})
		}
		record(t, db, alice, activity.Activity{Event: "new"})
		_, err := db.NewUpdate().Model((*activity.Activity)(nil)).
			Set("created_at = ?", time.Now().Add(-48*time.Hour).UTC()).Where("event = ?", "old").Exec(ctx)
		if err != nil {
			t.Fatal(err)
		}
		cleanup := activity.Cleanup(db, 24*time.Hour)
		for _, want := range []int{2, 1, 0} {
			n, err := cleanup(ctx, 2)
			if err != nil || n != want {
				t.Fatalf("n=%d want=%d err=%v", n, want, err)
			}
		}
		list, err := activity.List(ctx, db, activity.Query{Tenant: "acme"})
		if err != nil || events(list) != "new" {
			t.Fatalf("list=%s err=%v", events(list), err)
		}
		if _, err := cleanup(ctx, 0); err == nil {
			t.Fatal("expected error for non-positive limit")
		}
	})
}
