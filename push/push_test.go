package push_test

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/database/dbtest"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/notification"
	"github.com/ryan053857/mygoravel/push"
	"github.com/ryan053857/mygoravel/push/pushtest"
	"github.com/ryan053857/mygoravel/queue"
	"github.com/ryan053857/mygoravel/queue/queuetest"
)

var (
	alice = identity.Subject{Type: "customer", ID: "1", Tenant: "acme", Credential: "token-a1"}
	bob   = identity.Subject{Type: "customer", ID: "2", Tenant: "acme", Credential: "token-b1"}
	staff = identity.Subject{Type: "staff", ID: "1", Credential: "token-s1"}
)

// migrations 合并队列和推送的迁移，队列的版本号在前。
func migrations(t *testing.T) fs.FS {
	t.Helper()
	merged := fstest.MapFS{}
	for prefix, src := range map[string]fs.FS{"1": queue.Migrations(), "2": push.Migrations()} {
		err := fs.WalkDir(src, ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := fs.ReadFile(src, p)
			dir, name := path.Split(p)
			merged[dir+prefix+name] = &fstest.MapFile{Data: data}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return merged
}

func register(t *testing.T, db bun.IDB, sub identity.Subject, platform push.Platform, token string) {
	t.Helper()
	if err := push.Register(context.Background(), db, sub, platform, token); err != nil {
		t.Fatal(err)
	}
}

func devices(t *testing.T, db bun.IDB) map[string]push.Device {
	t.Helper()
	var list []push.Device
	if err := db.NewSelect().Model(&list).Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := map[string]push.Device{}
	for _, d := range list {
		m[d.Token] = d
	}
	return m
}

func TestRegister(t *testing.T) {
	db := dbtest.Open(t, migrations(t))
	ctx := context.Background()

	for name, tc := range map[string]struct {
		platform push.Platform
		token    string
	}{
		"unknown platform": {"windows", "d1"},
		"empty token":      {push.Android, ""},
		"long token":       {push.Android, strings.Repeat("x", 513)},
	} {
		if err := push.Register(ctx, db, alice, tc.platform, tc.token); !errors.Is(err, push.ErrInvalidDevice) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := push.Register(ctx, db, identity.Subject{Type: "customer"}, push.Android, "d1"); err == nil {
		t.Error("registered without subject ID")
	}

	register(t, db, alice, push.Android, "d1")
	first := devices(t, db)["d1"]
	if first.Tenant != "acme" || first.SubjectType != "customer" || first.SubjectID != "1" || first.Credential != "token-a1" {
		t.Fatalf("device %+v", first)
	}

	// 同一台设备换了账号：改为登记给新的主体，只有一行。
	register(t, db, bob, push.Android, "d1")
	got := devices(t, db)
	if len(got) != 1 || got["d1"].SubjectID != "2" || got["d1"].Credential != "token-b1" ||
		got["d1"].ID != first.ID || got["d1"].UpdatedAt.Before(first.UpdatedAt) {
		t.Fatalf("after re-register %+v", got)
	}

	// token 相同、平台不同是两台设备。
	register(t, db, alice, push.IOS, "d1")
	if n, err := db.NewSelect().Model((*push.Device)(nil)).Count(ctx); err != nil || n != 2 {
		t.Fatalf("%d devices, %v", n, err)
	}
}

func TestUnregister(t *testing.T) {
	db := dbtest.Open(t, migrations(t))
	ctx := context.Background()

	alicePhone, alicePad := alice, alice
	alicePad.Credential = "token-a2"
	aliceWeb := alice
	aliceWeb.Credential = ""
	register(t, db, alicePhone, push.Android, "phone")
	register(t, db, alicePad, push.IOS, "pad")
	register(t, db, aliceWeb, push.IOS, "nocred")
	register(t, db, bob, push.Android, "bob")

	// 没有凭据 ID 时不删除任何设备。
	if n, err := push.Unregister(ctx, db, alice, ""); err != nil || n != 0 {
		t.Fatalf("unregister empty credential: %d, %v", n, err)
	}
	// 别人的凭据 ID 删不掉自己的设备。
	if n, err := push.Unregister(ctx, db, alice, "token-b1"); err != nil || n != 0 {
		t.Fatalf("unregister other's credential: %d, %v", n, err)
	}
	if n, err := push.Unregister(ctx, db, alice, alicePad.Credential); err != nil || n != 1 {
		t.Fatalf("unregister: %d, %v", n, err)
	}
	if _, ok := devices(t, db)["pad"]; ok {
		t.Fatal("pad still registered")
	}

	register(t, db, alicePad, push.IOS, "pad")
	if n, err := push.UnregisterOthers(ctx, db, alicePhone); err != nil || n != 2 {
		t.Fatalf("unregister others: %d, %v", n, err)
	}
	got := devices(t, db)
	if len(got) != 2 || got["phone"].SubjectID != "1" || got["bob"].SubjectID != "2" {
		t.Fatalf("after unregister others %+v", got)
	}

	// 没有凭据 ID 的主体（例如来自会话）“退出其他设备”时删除它的全部设备。
	register(t, db, alicePad, push.IOS, "pad")
	if n, err := push.UnregisterOthers(ctx, db, aliceWeb); err != nil || n != 2 {
		t.Fatalf("unregister others without credential: %d, %v", n, err)
	}

	register(t, db, alicePhone, push.Android, "phone")
	if n, err := push.UnregisterSubject(ctx, db, bob.Ref()); err != nil || n != 1 {
		t.Fatalf("unregister subject: %d, %v", n, err)
	}
	if got := devices(t, db); len(got) != 1 || got["phone"].SubjectID != "1" {
		t.Fatalf("after unregister subject %+v", got)
	}
}

func TestCleanup(t *testing.T) {
	db := dbtest.Open(t, migrations(t))
	ctx := context.Background()
	register(t, db, alice, push.Android, "old1")
	register(t, db, alice, push.Android, "old2")
	register(t, db, alice, push.Android, "fresh")
	_, err := db.NewUpdate().Model((*push.Device)(nil)).Set("updated_at = ?", time.Now().Add(-100*24*time.Hour)).
		Where("token LIKE 'old%'").Exec(ctx)
	if err != nil {
		t.Fatal(err)
	}
	clean := push.Cleanup(db, 90*24*time.Hour)
	if n, err := clean(ctx, 1); err != nil || n != 1 {
		t.Fatalf("first cleanup: %d, %v", n, err)
	}
	if n, err := clean(ctx, 10); err != nil || n != 1 {
		t.Fatalf("second cleanup: %d, %v", n, err)
	}
	if got := devices(t, db); len(got) != 1 || got["fresh"].Token != "fresh" {
		t.Fatalf("after cleanup %+v", got)
	}
	if _, err := clean(ctx, 0); err == nil {
		t.Fatal("cleanup with zero limit")
	}
}

func msg() push.Message {
	return push.Message{Title: "文章已发布", Body: "Hello", Data: map[string]string{"article_id": "42"}}
}

func TestSend(t *testing.T) {
	db := dbtest.Open(t, migrations(t))
	ctx := context.Background()
	a, b := notification.RecipientOf(alice), notification.RecipientOf(bob)

	for name, m := range map[string]push.Message{
		"no title":     {Body: "b"},
		"no body":      {Title: "t"},
		"negative ttl": {Title: "t", Body: "b", TTL: -time.Second},
	} {
		if err := push.Send(ctx, db, m, a); err == nil {
			t.Errorf("%s: sent", name)
		}
	}
	if err := push.Send(ctx, db, msg(), notification.Recipient{Tenant: "acme"}); err == nil {
		t.Error("sent to an empty recipient")
	}
	if err := push.Send(ctx, db, msg()); err != nil {
		t.Fatal(err)
	}
	if jobs := queuetest.Pending(t, db, push.Deliver); len(jobs) != 0 {
		t.Fatalf("no recipients published %d jobs", len(jobs))
	}

	// 事务回滚时推送一起取消。
	rollback := errors.New("rollback")
	err := database.RunInTx(ctx, db, nil, func(ctx context.Context, tx database.Tx) error {
		if err := push.Send(ctx, tx, msg(), a); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if jobs := queuetest.Pending(t, db, push.Deliver); len(jobs) != 0 {
		t.Fatalf("rolled back send published %d jobs", len(jobs))
	}

	if err := push.Send(ctx, db, msg(), a, b, a); err != nil {
		t.Fatal(err)
	}
	jobs := queuetest.Pending(t, db, push.Deliver)
	if len(jobs) != 1 || !slices.Equal(jobs[0].To, []notification.Recipient{a, b}) ||
		jobs[0].Message.Data["article_id"] != "42" || jobs[0].SentAt.IsZero() {
		t.Fatalf("jobs %+v", jobs)
	}
}

func TestSendSplitsRecipients(t *testing.T) {
	db := dbtest.Open(t, migrations(t))
	to := make([]notification.Recipient, push.MaxBatch+1)
	for i := range to {
		to[i] = notification.Recipient{Tenant: "acme", Ref: identity.Ref{Type: "customer", ID: fmt.Sprint(i)}}
	}
	if err := push.Send(context.Background(), db, msg(), to...); err != nil {
		t.Fatal(err)
	}
	jobs := queuetest.Pending(t, db, push.Deliver)
	if len(jobs) != 2 || len(jobs[0].To) != push.MaxBatch || len(jobs[1].To) != 1 || jobs[0].ID == jobs[1].ID {
		t.Fatalf("%d jobs", len(jobs))
	}
}

func TestDeliver(t *testing.T) {
	db := dbtest.Open(t, migrations(t))
	ctx := context.Background()
	fake := &pushtest.Provider{}
	d := push.NewDeliverer(db, fake, push.Options{})

	register(t, db, alice, push.Android, "a-android")
	alicePad := alice
	alicePad.Credential = "token-a2"
	register(t, db, alicePad, push.IOS, "a-ios")
	register(t, db, bob, push.Android, "b-android")
	// 同一个主体在另一个租户中登记的设备，不属于 acme 中的收件人。
	aliceGlobex := alice
	aliceGlobex.Tenant = "globex"
	register(t, db, aliceGlobex, push.HarmonyOS, "a-globex")
	register(t, db, staff, push.IOS, "staff")

	if err := push.Send(ctx, db, msg(), notification.RecipientOf(alice), notification.RecipientOf(bob)); err != nil {
		t.Fatal(err)
	}
	job := queuetest.Pending(t, db, push.Deliver)[0]
	if err := d.DeliverNow(ctx, job); err != nil {
		t.Fatal(err)
	}
	batches := fake.Batches()
	if len(batches) != 2 {
		t.Fatalf("%d batches", len(batches))
	}
	android, ios := batches[0], batches[1]
	if android.Platform != push.Android || !slices.Equal(android.Tokens, []string{"a-android", "b-android"}) ||
		ios.Platform != push.IOS || !slices.Equal(ios.Tokens, []string{"a-ios"}) {
		t.Fatalf("batches %+v", batches)
	}
	if android.Job != job.ID || android.Key == ios.Key || android.Message.Title != "文章已发布" || !android.Expires.IsZero() {
		t.Fatalf("android batch %+v", android)
	}

	// 重试时每一批的 Key 不变，推送服务据此去重。
	if err := d.DeliverNow(ctx, job); err != nil {
		t.Fatal(err)
	}
	again := fake.Batches()[2:]
	if again[0].Key != android.Key || again[1].Key != ios.Key {
		t.Fatal("keys changed on retry")
	}

	// 没有登记设备的收件人什么也不推送。
	before := len(fake.Batches())
	nobody := push.Job{ID: job.ID, Message: msg(), To: []notification.Recipient{{Tenant: "acme", Ref: identity.Ref{Type: "customer", ID: "9"}}}}
	if err := d.DeliverNow(ctx, nobody); err != nil || len(fake.Batches()) != before {
		t.Fatalf("nobody: %v", err)
	}
}

func TestDeliverTTL(t *testing.T) {
	db := dbtest.Open(t, migrations(t))
	ctx := context.Background()
	fake := &pushtest.Provider{}
	d := push.NewDeliverer(db, fake, push.Options{})
	register(t, db, alice, push.Android, "a-android")

	m := msg()
	m.TTL = time.Hour
	if err := push.Send(ctx, db, m, notification.RecipientOf(alice)); err != nil {
		t.Fatal(err)
	}
	job := queuetest.Pending(t, db, push.Deliver)[0]
	if err := d.DeliverNow(ctx, job); err != nil {
		t.Fatal(err)
	}
	if b := fake.Batches(); len(b) != 1 || !b[0].Expires.Equal(job.SentAt.Add(time.Hour)) {
		t.Fatalf("batches %+v", b)
	}

	// 积压超过 TTL 的任务直接结束。
	job.SentAt = time.Now().Add(-2 * time.Hour)
	if err := d.DeliverNow(ctx, job); err != nil || len(fake.Batches()) != 1 {
		t.Fatalf("expired job: %v, %d batches", err, len(fake.Batches()))
	}
}

func TestDeliverErrors(t *testing.T) {
	db := dbtest.Open(t, migrations(t))
	ctx := context.Background()
	register(t, db, alice, push.Android, "a-android")
	register(t, db, bob, push.IOS, "b-ios")
	job := push.Job{Message: msg(), To: []notification.Recipient{notification.RecipientOf(alice), notification.RecipientOf(bob)}}

	failOn := func(platform push.Platform, err error) *pushtest.Provider {
		return &pushtest.Provider{Err: func(b push.Batch) error {
			if b.Platform == platform {
				return err
			}
			return nil
		}}
	}

	// 一个平台临时失败：整个任务重试，另一个平台照常推送。
	fake := failOn(push.Android, errors.New("timeout"))
	err := push.NewDeliverer(db, fake, push.Options{}).DeliverNow(ctx, job)
	if err == nil || queue.IsPermanent(err) || len(fake.Batches()) != 1 {
		t.Fatalf("temporary failure: %v, %d batches", err, len(fake.Batches()))
	}

	// 只有永久失败时不再重试。
	fake = failOn(push.Android, queue.Permanent(errors.New("bad app key")))
	err = push.NewDeliverer(db, fake, push.Options{}).DeliverNow(ctx, job)
	if !queue.IsPermanent(err) || len(fake.Batches()) != 1 {
		t.Fatalf("permanent failure: %v", err)
	}

	// 永久失败和临时失败同时出现时重试。
	fake = &pushtest.Provider{Err: func(b push.Batch) error {
		if b.Platform == push.Android {
			return queue.Permanent(errors.New("bad app key"))
		}
		return errors.New("timeout")
	}}
	err = push.NewDeliverer(db, fake, push.Options{}).DeliverNow(ctx, job)
	if err == nil || queue.IsPermanent(err) {
		t.Fatalf("mixed failure: %v", err)
	}
}

func TestDeliverThroughQueue(t *testing.T) {
	db := dbtest.Open(t, migrations(t))
	ctx := context.Background()
	register(t, db, alice, push.Android, "a-android")

	var calls atomic.Int32
	fake := &pushtest.Provider{Err: func(push.Batch) error {
		if calls.Add(1) == 1 {
			return queue.RetryAfter(errors.New("throttled"), 0)
		}
		return nil
	}}
	w := queue.NewWorker(db, queuetest.Config(t), nil)
	push.NewDeliverer(db, fake, push.Options{}).Handle(w)
	queuetest.Run(t, w)

	err := database.RunInTx(ctx, db, nil, func(ctx context.Context, tx database.Tx) error {
		return push.Send(ctx, tx, msg(), notification.RecipientOf(alice))
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for len(fake.Batches()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("not delivered")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := fake.Tokens(); !slices.Equal(got, []string{"a-android"}) {
		t.Fatalf("tokens %v", got)
	}
}
