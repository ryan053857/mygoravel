package session_test

import (
	"context"
	"testing"
	"time"

	"github.com/alexedwards/scs/v2/memstore"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/internal/dbtesting"
	"github.com/ryan053857/mygoravel/session"
)

// Lookup 直接读取存储：撤销后立即发现，不受请求中已加载的会话数据影响。
func TestLookup(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		ctx := context.Background()
		store := session.NewStore(dbtesting.Open(t, driver, session.Migrations()))
		sm := session.New(defaults(), store)
		if err := store.CommitCtx(ctx, "a1", data(t, "alice", "a1"), time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if err := store.CommitCtx(ctx, "anon", data(t, "", "anon"), time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if sub, ok, err := session.Lookup(ctx, sm, "a1"); !ok || err != nil || sub.ID != "alice" {
			t.Fatalf("live: sub=%+v ok=%v err=%v", sub, ok, err)
		}
		for _, token := range []string{"anon", "missing", ""} {
			if _, ok, err := session.Lookup(ctx, sm, token); ok || err != nil {
				t.Fatalf("%q: ok=%v err=%v", token, ok, err)
			}
		}
		if _, err := store.RevokeSubject(ctx, "alice"); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := session.Lookup(ctx, sm, "a1"); ok || err != nil {
			t.Fatalf("revoked: ok=%v err=%v", ok, err)
		}
	})

	// 不支持 context 的存储（scs 的 memstore）同样可用。
	mem := memstore.New()
	sm := session.New(defaults(), mem)
	if err := mem.Commit("m1", data(t, "bob", "m1"), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if sub, ok, err := session.Lookup(context.Background(), sm, "m1"); !ok || err != nil || sub.ID != "bob" {
		t.Fatalf("memstore: sub=%+v ok=%v err=%v", sub, ok, err)
	}
	_ = mem.Delete("m1")
	if _, ok, _ := session.Lookup(context.Background(), sm, "m1"); ok {
		t.Fatal("deleted session still found")
	}
}
