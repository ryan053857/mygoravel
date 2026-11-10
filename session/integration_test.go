//go:build integration

package session_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/alexedwards/scs/v2"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/database/dbtest"
	"github.com/ryan053857/mygoravel/session"
)

var (
	_ scs.CtxStore         = (*session.Store)(nil)
	_ scs.IterableCtxStore = (*session.Store)(nil)
)

func TestStore(t *testing.T) {
	ctx := context.Background()
	store := session.NewStore(dbtest.Open(t, database.Postgres, session.Migrations()))
	now := time.Now()

	if _, found, err := store.FindCtx(ctx, "missing"); found || err != nil {
		t.Fatalf("missing found=%v err=%v", found, err)
	}
	if err := store.CommitCtx(ctx, "a", []byte("one"), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// 再次提交覆盖数据和过期时间：缩短到已过期后读不到。
	if err := store.CommitCtx(ctx, "a", []byte("two"), now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if b, found, err := store.FindCtx(ctx, "a"); !found || err != nil || string(b) != "two" {
		t.Fatalf("a=%q found=%v err=%v", b, found, err)
	}
	if err := store.CommitCtx(ctx, "a", []byte("three"), now.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.FindCtx(ctx, "a"); found || err != nil {
		t.Fatalf("expired a found=%v err=%v", found, err)
	}

	// 非 UTC 时区的过期时间按同一时刻比较。
	east := time.FixedZone("UTC+8", 8*3600)
	if err := store.CommitCtx(ctx, "b", []byte("b"), now.Add(time.Minute).In(east)); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := store.FindCtx(ctx, "b"); !found {
		t.Fatal("b not found")
	}
	if err := store.CommitCtx(ctx, "c", []byte("c"), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	all, err := store.AllCtx(ctx)
	if err != nil || len(all) != 2 || string(all["b"]) != "b" || string(all["c"]) != "c" {
		t.Fatalf("all=%v err=%v", all, err)
	}

	if err := store.DeleteCtx(ctx, "c"); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteCtx(ctx, "c"); err != nil {
		t.Fatalf("delete missing: %v", err)
	}
	if _, found, _ := store.FindCtx(ctx, "c"); found {
		t.Fatal("c still found")
	}
}

func TestStoreCleanup(t *testing.T) {
	ctx := context.Background()
	store := session.NewStore(dbtest.Open(t, database.Postgres, session.Migrations()))
	past := time.Now().Add(-time.Minute)
	for _, token := range []string{"x1", "x2", "x3"} {
		if err := store.CommitCtx(ctx, token, []byte(token), past); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.CommitCtx(ctx, "live", []byte("live"), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Cleanup(ctx, 0); err == nil {
		t.Fatal("expected error for non-positive limit")
	}
	if n, err := store.Cleanup(ctx, 2); n != 2 || err != nil {
		t.Fatalf("first cleanup n=%d err=%v", n, err)
	}
	if n, err := store.Cleanup(ctx, 10); n != 1 || err != nil {
		t.Fatalf("second cleanup n=%d err=%v", n, err)
	}
	if _, found, _ := store.FindCtx(ctx, "live"); !found {
		t.Fatal("live session removed")
	}
}

func TestStoreWithSessionManager(t *testing.T) {
	db := dbtest.Open(t, database.Postgres, session.Migrations())
	srv, _ := newApp(t, session.NewStore(db))
	c := newClient(t)
	if resp := do(t, c, http.MethodPost, srv.URL+"/login", `{"user":"root"}`); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("login status=%d", resp.StatusCode)
	}
	if resp := do(t, c, http.MethodGet, srv.URL+"/admin", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("admin status=%d", resp.StatusCode)
	}
	var count int
	if err := db.NewSelect().Table("sessions").ColumnExpr("count(*)").Scan(context.Background(), &count); err != nil || count != 1 {
		t.Fatalf("sessions=%d err=%v", count, err)
	}
	if resp := do(t, c, http.MethodPost, srv.URL+"/logout", ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout status=%d", resp.StatusCode)
	}
	if err := db.NewSelect().Table("sessions").ColumnExpr("count(*)").Scan(context.Background(), &count); err != nil || count != 0 {
		t.Fatalf("sessions after logout=%d err=%v", count, err)
	}
}
