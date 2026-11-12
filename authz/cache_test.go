package authz_test

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bradfitz/gomemcache/memcache"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/authz"
	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/database/dbtest"
	"github.com/ryan053857/mygoravel/identity"
)

// openMemcached 连接 MEMCACHED_TEST_ADDR 指向的 Memcached。
func openMemcached(t *testing.T) *memcache.Client {
	t.Helper()
	addr := os.Getenv("MEMCACHED_TEST_ADDR")
	if addr == "" {
		t.Fatal("MEMCACHED_TEST_ADDR is required for this test")
	}
	mc := memcache.New(addr)
	t.Cleanup(func() { _ = mc.Close() })
	return mc
}

// cached 返回使用 mc 和随机命名空间缓存的选项。
func cached(t *testing.T, mc *memcache.Client) authz.Option {
	return authz.Cache(mc, "test:"+rand.Text()+":", time.Minute)
}

func newCachedStore(t *testing.T) *authz.Store {
	t.Helper()
	return authz.NewStore(dbtest.Open(t, authz.Migrations()), catalog, cached(t, openMemcached(t)))
}

func perms(t *testing.T, s *authz.Store, ref identity.Ref, tenant string) string {
	t.Helper()
	p, member, err := s.Permissions(context.Background(), ref, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if !member {
		return "-"
	}
	return strings.Join(p, ",")
}

// 权限读取一次后来自缓存；同一个命名空间的另一个 Store（另一个实例）修改之后，缓存失效。
func TestCacheSharedAcrossInstances(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, mc := dbtest.Open(t, authz.Migrations()), openMemcached(t)
	opt := cached(t, mc)
	a, b := authz.NewStore(db, catalog, opt), authz.NewStore(db, catalog, opt)
	reader := mustRole(t, a, authz.Role{Name: "reader", Permissions: []string{"articles:read"}})
	bob := user("bob")
	mustAssign(t, a, bob, "acme", reader.ID)
	if got := perms(t, a, bob, "acme"); got != "articles:read" {
		t.Fatalf("perms=%s", got)
	}

	// 绕过 Store 修改表：缓存仍然返回旧的权限，说明读取没有访问数据库。
	if _, err := db.NewDelete().TableExpr("authz_assignments").Where("subject_id = ?", "bob").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if got := perms(t, a, bob, "acme"); got != "articles:read" {
		t.Fatalf("not cached: perms=%s", got)
	}

	// 另一个 Store 修改分配，a 读到新的权限。
	mustAssign(t, b, bob, "acme", reader.ID)
	if _, err := b.UpdateRole(ctx, authz.Role{ID: reader.ID, Name: "reader", Permissions: []string{"articles:read", "articles:write"}}); err != nil {
		t.Fatal(err)
	}
	if got := perms(t, a, bob, "acme"); got != "articles:read,articles:write" {
		t.Fatalf("after update role: perms=%s", got)
	}

	// 不同命名空间的缓存互不影响。
	other := authz.NewStore(db, catalog, cached(t, mc))
	if got := perms(t, other, bob, "acme"); got != "articles:read,articles:write" {
		t.Fatalf("other namespace: perms=%s", got)
	}
}

// Store 建在 database.Tx 上时，缓存在事务提交之后失效；回滚时不变。
func TestCacheInTransaction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := dbtest.Open(t, authz.Migrations())
	opt := cached(t, openMemcached(t))
	s := authz.NewStore(db, catalog, opt)
	reader := mustRole(t, s, authz.Role{Name: "reader", Permissions: []string{"articles:read"}})
	bob := user("bob")
	if got := perms(t, s, bob, "acme"); got != "-" {
		t.Fatalf("perms=%s", got)
	}

	rollback := errors.New("rollback")
	err := database.RunInTx(ctx, db, nil, func(ctx context.Context, tx database.Tx) error {
		if err := authz.NewStore(tx, catalog, opt).Assign(ctx, bob, "acme", reader.ID); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if got := perms(t, s, bob, "acme"); got != "-" {
		t.Fatalf("after rollback: perms=%s", got)
	}

	err = database.RunInTx(ctx, db, nil, func(ctx context.Context, tx database.Tx) error {
		if err := authz.NewStore(tx, catalog, opt).Assign(ctx, bob, "acme", reader.ID); err != nil {
			return err
		}
		// 提交之前读取，缓存的是提交之前的权限。
		if got := perms(t, s, bob, "acme"); got != "-" {
			t.Errorf("before commit: perms=%s", got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := perms(t, s, bob, "acme"); got != "articles:read" {
		t.Fatalf("after commit: perms=%s", got)
	}
}

// 不能确定何时提交的事务不能与缓存一起使用。
func TestCacheRejectsPlainTx(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t, authz.Migrations())
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	opt := cached(t, openMemcached(t))
	for name, conn := range map[string]bun.IDB{"tx": tx, "*tx": &tx} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: expected panic", name)
				}
			}()
			authz.NewStore(conn, catalog, opt)
		}()
	}
	authz.NewStore(tx, catalog) // 不缓存时可以
}

// Memcached 不可用时直接查询数据库，修改照常成功。
func TestCacheDown(t *testing.T) {
	t.Parallel()
	mc := memcache.New("127.0.0.1:1")
	mc.Timeout = 50 * time.Millisecond
	s := authz.NewStore(dbtest.Open(t, authz.Migrations()), catalog, cached(t, mc))
	reader := mustRole(t, s, authz.Role{Name: "reader", Permissions: []string{"articles:read"}})
	mustAssign(t, s, user("bob"), "acme", reader.ID)
	if got := perms(t, s, user("bob"), "acme"); got != "articles:read" {
		t.Fatalf("perms=%s", got)
	}
	if err := s.Unassign(context.Background(), user("bob"), "acme", reader.ID); err != nil {
		t.Fatal(err)
	}
	if got := perms(t, s, user("bob"), "acme"); got != "-" {
		t.Fatalf("after unassign: perms=%s", got)
	}
}
