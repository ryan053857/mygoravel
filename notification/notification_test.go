package notification_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/database/dbtest"
	"github.com/ryan053857/mygoravel/notification"
)

var (
	alice = notification.Recipient{Tenant: "acme", Type: "user", ID: "u1"}
	bob   = notification.Recipient{Tenant: "acme", Type: "user", ID: "u2"}
	// 同一个用户在另一个租户中是另一个收件人。
	aliceGlobex = notification.Recipient{Tenant: "globex", Type: "user", ID: "u1"}
)

func send(t *testing.T, db bun.IDB, event string, to ...notification.Recipient) []notification.Notification {
	t.Helper()
	sent, err := notification.Send(context.Background(), db, notification.Notification{Event: event}, to...)
	if err != nil {
		t.Fatal(err)
	}
	return sent
}

func list(t *testing.T, db bun.IDB, q notification.Query) []notification.Notification {
	t.Helper()
	list, err := notification.List(context.Background(), db, q)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func events(list []notification.Notification) string {
	var out []string
	for _, n := range list {
		out = append(out, n.Event)
	}
	return strings.Join(out, ",")
}

func unread(t *testing.T, db bun.IDB, r notification.Recipient) int {
	t.Helper()
	n, err := notification.UnreadCount(context.Background(), db, r)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSendWritesOneRowPerRecipient(t *testing.T) {
	db := dbtest.Open(t, notification.Migrations())
	ctx := context.Background()
	before := time.Now().UTC().Add(-time.Second)
	sent, err := notification.Send(ctx, db, notification.Notification{
		Event: "article.published",
		Data:  map[string]any{"title": "Hello", "tags": []any{"go"}},
		// 由 Send 填写的字段会被覆盖。
		Tenant: "other", RecipientID: "forged", ReadAt: time.Now(),
	}, alice, bob, alice)
	if err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 || sent[0].ID == sent[1].ID || sent[0].Recipient() != alice || sent[1].Recipient() != bob {
		t.Fatalf("sent=%+v", sent)
	}

	got := list(t, db, notification.Query{Recipient: alice})
	if len(got) != 1 {
		t.Fatalf("list=%+v", got)
	}
	n := got[0]
	if n.ID != sent[0].ID || n.ID.Version() != 7 || n.Recipient() != alice || n.Event != "article.published" || n.Read() {
		t.Fatalf("notification=%+v", n)
	}
	if n.Data["title"] != "Hello" || len(n.Data["tags"].([]any)) != 1 {
		t.Fatalf("data=%#v", n.Data)
	}
	if n.CreatedAt.Before(before) || n.CreatedAt.After(time.Now().Add(time.Second)) {
		t.Fatalf("created_at=%v", n.CreatedAt)
	}
	if unread(t, db, bob) != 1 || unread(t, db, aliceGlobex) != 0 {
		t.Fatal("unexpected unread counts")
	}
}

func TestSendWithoutRecipientsOrData(t *testing.T) {
	db := dbtest.Open(t, notification.Migrations())
	if sent := send(t, db, "nobody"); sent != nil {
		t.Fatalf("sent=%+v", sent)
	}
	send(t, db, "plain", alice)
	got := list(t, db, notification.Query{Recipient: alice})
	if len(got) != 1 || got[0].Data == nil || len(got[0].Data) != 0 {
		t.Fatalf("list=%+v", got)
	}
}

func TestSendRollsBackWithTransaction(t *testing.T) {
	db := dbtest.Open(t, notification.Migrations())
	ctx := context.Background()
	err := db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		send(t, tx, "article.published", alice)
		return context.Canceled
	})
	if err != context.Canceled {
		t.Fatal(err)
	}
	if got := list(t, db, notification.Query{Recipient: alice}); len(got) != 0 {
		t.Fatalf("list=%+v", got)
	}
}

func TestSendRejectsInvalid(t *testing.T) {
	db := dbtest.Open(t, notification.Migrations())
	long := strings.Repeat("x", 192)
	for name, c := range map[string]struct {
		n  notification.Notification
		to notification.Recipient
	}{
		"empty event":        {notification.Notification{}, alice},
		"no recipient id":    {notification.Notification{Event: "e"}, notification.Recipient{Type: "user"}},
		"no recipient type":  {notification.Notification{Event: "e"}, notification.Recipient{ID: "u1"}},
		"long recipient id":  {notification.Notification{Event: "e"}, notification.Recipient{Type: "user", ID: long}},
		"unencodable values": {notification.Notification{Event: "e", Data: map[string]any{"f": func() {}}}, alice},
	} {
		if _, err := notification.Send(context.Background(), db, c.n, c.to); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestListFiltersAndPages(t *testing.T) {
	db := dbtest.Open(t, notification.Migrations())
	ctx := context.Background()
	first := send(t, db, "e1", alice, bob)
	send(t, db, "e2", alice)
	send(t, db, "e3", alice, aliceGlobex)
	send(t, db, "e4", alice)
	if err := notification.MarkRead(ctx, db, alice, first[0].ID); err != nil {
		t.Fatal(err)
	}

	for name, c := range map[string]struct {
		q    notification.Query
		want string
	}{
		"all":      {notification.Query{Recipient: alice}, "e4,e3,e2,e1"},
		"unread":   {notification.Query{Recipient: alice, Unread: true}, "e4,e3,e2"},
		"limit":    {notification.Query{Recipient: alice, Limit: 2}, "e4,e3"},
		"other":    {notification.Query{Recipient: bob}, "e1"},
		"tenant":   {notification.Query{Recipient: aliceGlobex}, "e3"},
		"platform": {notification.Query{Recipient: notification.Recipient{Type: "user", ID: "u1"}}, ""},
	} {
		if got := events(list(t, db, c.q)); got != c.want {
			t.Errorf("%s: got %s want %s", name, got, c.want)
		}
	}

	var pages []string
	var before uuid.UUID
	for {
		page := list(t, db, notification.Query{Recipient: alice, Limit: 3, Before: before})
		if len(page) == 0 {
			break
		}
		pages = append(pages, events(page))
		before = page[len(page)-1].ID
	}
	if got := strings.Join(pages, "|"); got != "e4,e3,e2|e1" {
		t.Fatalf("pages=%s", got)
	}
}

func TestGetOnlyReturnsOwnNotification(t *testing.T) {
	db := dbtest.Open(t, notification.Migrations())
	ctx := context.Background()
	sent := send(t, db, "e1", alice)
	n, err := notification.Get(ctx, db, alice, sent[0].ID)
	if err != nil || n.ID != sent[0].ID || n.Event != "e1" {
		t.Fatalf("n=%+v err=%v", n, err)
	}
	for _, c := range []struct {
		r  notification.Recipient
		id uuid.UUID
	}{{bob, sent[0].ID}, {aliceGlobex, sent[0].ID}, {alice, uuid.New()}} {
		if _, err := notification.Get(ctx, db, c.r, c.id); !errors.Is(err, notification.ErrNotFound) {
			t.Fatalf("recipient=%+v err=%v", c.r, err)
		}
	}
}

func TestMarkReadOnlyAffectsOwnUnreadNotifications(t *testing.T) {
	db := dbtest.Open(t, notification.Migrations())
	ctx := context.Background()
	forAlice := send(t, db, "e1", alice)
	forBob := send(t, db, "e1", bob)
	send(t, db, "e2", alice)
	if unread(t, db, alice) != 2 {
		t.Fatal("expected 2 unread")
	}

	// bob 的 ID 被忽略；没有 ID 时什么也不做。
	if err := notification.MarkRead(ctx, db, alice, forAlice[0].ID, forBob[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := notification.MarkRead(ctx, db, alice); err != nil {
		t.Fatal(err)
	}
	if unread(t, db, alice) != 1 || unread(t, db, bob) != 1 {
		t.Fatal("unexpected unread counts")
	}
	n, err := notification.Get(ctx, db, alice, forAlice[0].ID)
	if err != nil || !n.Read() {
		t.Fatalf("n=%+v err=%v", n, err)
	}

	// 再次标记已读不改变原来的已读时间。
	readAt := n.ReadAt
	time.Sleep(10 * time.Millisecond)
	if err := notification.MarkAllRead(ctx, db, alice); err != nil {
		t.Fatal(err)
	}
	if unread(t, db, alice) != 0 || unread(t, db, bob) != 1 {
		t.Fatal("unexpected unread counts after mark all")
	}
	if n, err := notification.Get(ctx, db, alice, forAlice[0].ID); err != nil || !n.ReadAt.Equal(readAt) {
		t.Fatalf("read_at=%v want %v err=%v", n.ReadAt, readAt, err)
	}
}

func TestDeleteOnlyAffectsOwnNotifications(t *testing.T) {
	db := dbtest.Open(t, notification.Migrations())
	ctx := context.Background()
	forAlice := send(t, db, "e1", alice)
	forBob := send(t, db, "e1", bob)
	send(t, db, "e2", alice)
	if err := notification.Delete(ctx, db, alice, forAlice[0].ID, forBob[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := notification.Delete(ctx, db, alice); err != nil {
		t.Fatal(err)
	}
	if got := events(list(t, db, notification.Query{Recipient: alice})); got != "e2" {
		t.Fatalf("alice=%s", got)
	}
	if got := events(list(t, db, notification.Query{Recipient: bob})); got != "e1" {
		t.Fatalf("bob=%s", got)
	}
}

func TestCleanupDeletesOldNotifications(t *testing.T) {
	db := dbtest.Open(t, notification.Migrations())
	ctx := context.Background()
	for range 3 {
		send(t, db, "old", alice)
	}
	send(t, db, "new", alice)
	// 已读和未读的旧通知都会被删除。
	if err := notification.MarkAllRead(ctx, db, alice); err != nil {
		t.Fatal(err)
	}
	send(t, db, "old", bob)
	_, err := db.NewUpdate().Model((*notification.Notification)(nil)).
		Set("created_at = ?", time.Now().Add(-48*time.Hour).UTC()).Where("event = ?", "old").Exec(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cleanup := notification.Cleanup(db, 24*time.Hour)
	for _, want := range []int{3, 1, 0} {
		n, err := cleanup(ctx, 3)
		if err != nil || n != want {
			t.Fatalf("n=%d want=%d err=%v", n, want, err)
		}
	}
	if got := events(list(t, db, notification.Query{Recipient: alice})); got != "new" {
		t.Fatalf("list=%s", got)
	}
	if _, err := cleanup(ctx, 0); err == nil {
		t.Fatal("expected error for non-positive limit")
	}
}
