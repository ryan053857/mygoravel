package session_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/internal/dbtesting"
	"github.com/ryan053857/mygoravel/session"
)

var (
	_ scs.CtxStore         = (*session.Store)(nil)
	_ scs.IterableCtxStore = (*session.Store)(nil)
)

// data 按 scs 默认编码生成会话数据；subjectID 非空时包含登录主体。
func data(t *testing.T, subjectID, note string) []byte {
	t.Helper()
	values := map[string]any{"note": note}
	if subjectID != "" {
		values["mygoravel.subject"] = identity.Subject{ID: subjectID}
	}
	b, err := scs.GobCodec{}.Encode(time.Now().Add(time.Hour), values)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// note 解码会话数据中的 note。
func note(t *testing.T, b []byte) string {
	t.Helper()
	_, values, err := scs.GobCodec{}.Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	return values["note"].(string)
}

func TestStore(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		ctx := context.Background()
		store := session.NewStore(dbtesting.Open(t, driver, session.Migrations()))
		now := time.Now()

		if _, found, err := store.FindCtx(ctx, "missing"); found || err != nil {
			t.Fatalf("missing found=%v err=%v", found, err)
		}
		if err := store.CommitCtx(ctx, "a", data(t, "", "one"), now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		// 再次提交覆盖数据和过期时间：缩短到已过期后读不到。
		if err := store.CommitCtx(ctx, "a", data(t, "", "two"), now.Add(2*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if b, found, err := store.FindCtx(ctx, "a"); !found || err != nil || note(t, b) != "two" {
			t.Fatalf("a found=%v err=%v", found, err)
		}
		if err := store.CommitCtx(ctx, "a", data(t, "", "three"), now.Add(-time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, found, err := store.FindCtx(ctx, "a"); found || err != nil {
			t.Fatalf("expired a found=%v err=%v", found, err)
		}

		// 非 UTC 时区的过期时间按同一时刻比较。
		east := time.FixedZone("UTC+8", 8*3600)
		if err := store.CommitCtx(ctx, "b", data(t, "", "b"), now.Add(time.Minute).In(east)); err != nil {
			t.Fatal(err)
		}
		if _, found, _ := store.FindCtx(ctx, "b"); !found {
			t.Fatal("b not found")
		}
		if err := store.CommitCtx(ctx, "c", data(t, "", "c"), now.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		all, err := store.AllCtx(ctx)
		if err != nil || len(all) != 2 || note(t, all["b"]) != "b" || note(t, all["c"]) != "c" {
			t.Fatalf("all=%d err=%v", len(all), err)
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
		// 删除前已在处理中的请求再次提交，会话也不会复活。
		if err := store.CommitCtx(ctx, "c", data(t, "", "c2"), now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if _, found, _ := store.FindCtx(ctx, "c"); found {
			t.Fatal("deleted c resurrected by commit")
		}
		if all, _ := store.AllCtx(ctx); len(all) != 1 {
			t.Fatalf("all after delete=%d", len(all))
		}

		if err := store.CommitCtx(ctx, "bad", []byte("not gob"), now.Add(time.Hour)); err == nil {
			t.Fatal("expected decode error")
		}
	})
}

func TestStoreRevokeSubject(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		ctx := context.Background()
		db := dbtesting.Open(t, driver, session.Migrations())
		store := session.NewStore(db)
		live := time.Now().Add(time.Hour)
		for token, subject := range map[string]string{"a1": "alice", "a2": "alice", "b1": "bob", "anon": ""} {
			if err := store.CommitCtx(ctx, token, data(t, subject, token), live); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.CommitCtx(ctx, "a-old", data(t, "alice", "old"), time.Now().Add(-time.Second)); err != nil {
			t.Fatal(err)
		}
		var rows []struct {
			Token     string  `bun:"token"`
			SubjectID *string `bun:"subject_id"`
		}
		if err := db.NewSelect().Table("sessions").Column("token", "subject_id").Scan(ctx, &rows); err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if (r.Token == "anon") != (r.SubjectID == nil) {
				t.Fatalf("token %s subject_id=%v", r.Token, r.SubjectID)
			}
		}

		if _, err := store.RevokeSubject(ctx, ""); err == nil {
			t.Fatal("expected error for empty subject")
		}
		// 已过期的 a-old 不计入。
		if n, err := store.RevokeSubject(ctx, "alice"); n != 2 || err != nil {
			t.Fatalf("revoke alice n=%d err=%v", n, err)
		}
		if n, err := store.RevokeSubject(ctx, "alice"); n != 0 || err != nil {
			t.Fatalf("revoke alice again n=%d err=%v", n, err)
		}
		// 撤销前已在处理中的请求再次提交，会话也不会复活。
		if err := store.CommitCtx(ctx, "a1", data(t, "alice", "late"), live); err != nil {
			t.Fatal(err)
		}
		all, err := store.AllCtx(ctx)
		if err != nil || len(all) != 2 || all["b1"] == nil || all["anon"] == nil {
			t.Fatalf("all=%d err=%v", len(all), err)
		}
	})
}

func TestStoreRevokeOthers(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		store := session.NewStore(dbtesting.Open(t, driver, session.Migrations()))
		srv := newApp(t, store)
		laptop, phone, bob := newClient(t), newClient(t), newClient(t)
		for c, user := range map[*http.Client]string{laptop: "alice", phone: "alice", bob: "bob"} {
			if resp := do(t, c, http.MethodPost, srv.URL+"/login", `{"user":"`+user+`"}`); resp.StatusCode != http.StatusNoContent {
				t.Fatalf("login %s status=%d", user, resp.StatusCode)
			}
		}

		resp := do(t, laptop, http.MethodPost, srv.URL+"/logout-others", "")
		var out revokedOut
		if err := json.NewDecoder(resp.Body).Decode(&out.Body); err != nil || resp.StatusCode != http.StatusOK || out.Body.Revoked != 1 {
			t.Fatalf("logout-others status=%d revoked=%d err=%v", resp.StatusCode, out.Body.Revoked, err)
		}
		for c, want := range map[*http.Client]int{laptop: http.StatusOK, phone: http.StatusUnauthorized, bob: http.StatusOK} {
			if resp := do(t, c, http.MethodGet, srv.URL+"/me", ""); resp.StatusCode != want {
				t.Fatalf("/me status=%d want %d", resp.StatusCode, want)
			}
		}

		// 没有主体时不能撤销。
		if _, err := store.RevokeOthers(context.Background(), identity.Subject{}); err == nil {
			t.Fatal("expected error without subject")
		}
		// 在 API 中调用时 Credential 是令牌 ID，撤销全部会话。
		n, err := store.RevokeOthers(context.Background(), identity.Subject{ID: "alice", Credential: "0193a8e0-0000-7000-8000-000000000000"})
		if err != nil || n != 1 {
			t.Fatalf("revoke from api: n=%d err=%v", n, err)
		}
		if resp := do(t, laptop, http.MethodGet, srv.URL+"/me", ""); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("laptop still signed in: %d", resp.StatusCode)
		}
	})
}

// 登录时记录设备；List 列出主体的会话并标出当前会话，Revoke 只能撤销自己的会话。
func TestStoreListAndRevoke(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		store := session.NewStore(dbtesting.Open(t, driver, session.Migrations()))
		srv := newApp(t, store)
		laptop, phone, bob := newClient(t), newClient(t), newClient(t)
		for c, user := range map[*http.Client]string{laptop: "alice", phone: "alice", bob: "bob"} {
			if resp := do(t, c, http.MethodPost, srv.URL+"/login", `{"user":"`+user+`"}`); resp.StatusCode != http.StatusNoContent {
				t.Fatalf("login %s status=%d", user, resp.StatusCode)
			}
		}
		devices := func(c *http.Client) []session.Session {
			t.Helper()
			resp := do(t, c, http.MethodGet, srv.URL+"/devices", "")
			var list []session.Session
			if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
				t.Fatal(err)
			}
			return list
		}
		list := devices(laptop)
		if len(list) != 2 {
			t.Fatalf("list=%+v", list)
		}
		var current, other session.Session
		for _, s := range list {
			if s.ID == "" || s.IP != "10.0.0.1" || s.UserAgent != "Go-http-client/1.1" || s.LoggedInAt.IsZero() || !s.ExpiresAt.After(time.Now()) {
				t.Fatalf("session=%+v", s)
			}
			if s.Current {
				current = s
			} else {
				other = s
			}
		}
		if current.ID == "" || other.ID == "" {
			t.Fatalf("current not marked: %+v", list)
		}
		// bob 不能撤销 alice 的会话。
		if resp := do(t, bob, http.MethodPost, srv.URL+"/devices/revoke?id="+other.ID, ""); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("bob revoked alice: %d", resp.StatusCode)
		}
		if resp := do(t, laptop, http.MethodPost, srv.URL+"/devices/revoke?id="+other.ID, ""); resp.StatusCode != http.StatusNoContent {
			t.Fatalf("revoke: %d", resp.StatusCode)
		}
		if resp := do(t, phone, http.MethodGet, srv.URL+"/me", ""); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("phone still signed in: %d", resp.StatusCode)
		}
		if list := devices(laptop); len(list) != 1 || !list[0].Current {
			t.Fatalf("after revoke: %+v", list)
		}
		if resp := do(t, laptop, http.MethodPost, srv.URL+"/devices/revoke?id="+other.ID, ""); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("revoke twice: %d", resp.StatusCode)
		}
	})
}

func TestStoreCleanup(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		ctx := context.Background()
		db := dbtesting.Open(t, driver, session.Migrations())
		store := session.NewStore(db)
		past := time.Now().Add(-time.Minute)
		for _, token := range []string{"x1", "x2", "x3"} {
			if err := store.CommitCtx(ctx, token, data(t, "", token), past); err != nil {
				t.Fatal(err)
			}
		}
		for _, token := range []string{"live", "revoked"} {
			if err := store.CommitCtx(ctx, token, data(t, "", token), time.Now().Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
		}
		// 撤销的行保留到过期，过期前不清理。
		if err := store.DeleteCtx(ctx, "revoked"); err != nil {
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
		if n := countSessions(t, db, false); n != 2 {
			t.Fatalf("rows after cleanup=%d", n)
		}
	})
}

func TestStoreWithSessionManager(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		db := dbtesting.Open(t, driver, session.Migrations())
		srv := newApp(t, session.NewStore(db))
		c := newClient(t)
		if resp := do(t, c, http.MethodPost, srv.URL+"/login", `{"user":"root"}`); resp.StatusCode != http.StatusNoContent {
			t.Fatalf("login status=%d", resp.StatusCode)
		}
		if resp := do(t, c, http.MethodGet, srv.URL+"/admin", ""); resp.StatusCode != http.StatusOK {
			t.Fatalf("admin status=%d", resp.StatusCode)
		}
		if n := countSessions(t, db, true); n != 1 {
			t.Fatalf("live sessions=%d", n)
		}
		if resp := do(t, c, http.MethodPost, srv.URL+"/logout", ""); resp.StatusCode != http.StatusNoContent {
			t.Fatalf("logout status=%d", resp.StatusCode)
		}
		if n := countSessions(t, db, true); n != 0 {
			t.Fatalf("live sessions after logout=%d", n)
		}
	})
}

// countSessions 返回 sessions 表的行数；liveOnly 时只计未撤销的行。
func countSessions(t *testing.T, db *bun.DB, liveOnly bool) int {
	t.Helper()
	q := db.NewSelect().Table("sessions").ColumnExpr("count(*)")
	if liveOnly {
		q = q.Where("revoked_at IS NULL")
	}
	var n int
	if err := q.Scan(context.Background(), &n); err != nil {
		t.Fatal(err)
	}
	return n
}
