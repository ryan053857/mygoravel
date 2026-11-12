package authz_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ryan053857/mygoravel/authz"
	"github.com/ryan053857/mygoravel/database/dbtest"
	"github.com/ryan053857/mygoravel/httpx/rest"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/web"
)

var catalog = authz.Catalog{
	{Name: "articles:read", Description: "查看文章"},
	{Name: "articles:write", Description: "编辑文章"},
	{Name: "comments:delete", Description: "删除评论"},
	{Name: "members:manage", Description: "管理成员"},
	{Name: "platform:tenants", Description: "管理租户"},
}

// Resolve 是一个 identity.Resolver，可以直接作为 rest、web 的 Options.Accounts 中一套账号的 Resolver。
var (
	_ = rest.Options{Accounts: identity.Accounts{"user": (*authz.Store)(nil).Resolve}}
	_ = web.Options{Accounts: identity.Accounts{"user": (*authz.Store)(nil).Resolve}}
)

func newStore(t *testing.T) *authz.Store {
	t.Helper()
	return authz.NewStore(dbtest.Open(t, authz.Migrations()), catalog)
}

func mustRole(t *testing.T, s *authz.Store, r authz.Role) authz.Role {
	t.Helper()
	role, err := s.CreateRole(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	return role
}

// user 返回测试中 user 账号体系的主体。
func user(id string) identity.Ref { return identity.Ref{Type: "user", ID: id} }

func mustAssign(t *testing.T, s *authz.Store, subject identity.Ref, tenant, roleID string) {
	t.Helper()
	if err := s.Assign(context.Background(), subject, tenant, roleID); err != nil {
		t.Fatal(err)
	}
}

func names(roles []authz.Role) string {
	var out []string
	for _, r := range roles {
		out = append(out, r.Tenant+"/"+r.Name)
	}
	return strings.Join(out, ",")
}

func TestNewStorePanics(t *testing.T) {
	db := dbtest.Open(t, authz.Migrations())
	for name, build := range map[string]func(){
		"nil db":            func() { authz.NewStore(nil, catalog) },
		"empty catalog":     func() { authz.NewStore(db, nil) },
		"wildcard catalog":  func() { authz.NewStore(db, authz.Catalog{{Name: "articles:*"}}) },
		"spaced permission": func() { authz.NewStore(db, authz.Catalog{{Name: "a b"}}) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: expected panic", name)
				}
			}()
			build()
		}()
	}
}

func TestRoles(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	editor := mustRole(t, s, authz.Role{Name: "editor", Description: "编辑", Permissions: []string{"articles:*", "articles:read", "comments:delete"}})
	if editor.ID == "" || strings.Join(editor.Permissions, ",") != "articles:*,articles:read,comments:delete" {
		t.Fatalf("editor=%+v", editor)
	}
	reviewer := mustRole(t, s, authz.Role{Tenant: "acme", Name: "reviewer", Permissions: []string{"articles:read"}})
	mustRole(t, s, authz.Role{Tenant: "globex", Name: "editor"}) // 不同租户可以同名
	mustRole(t, s, authz.Role{Name: "owner", Permissions: []string{"*"}})

	for name, r := range map[string]authz.Role{
		"unknown permission":  {Name: "x", Permissions: []string{"articles:delete"}},
		"wildcard no match":   {Name: "x", Permissions: []string{"billing:*"}},
		"all tenants role":    {Tenant: authz.AllTenants, Name: "x"},
		"empty name":          {Name: ""},
		"spaced name":         {Name: "two words"},
		"spaced permission":   {Name: "x", Permissions: []string{"articles: read"}},
		"suffix-only pattern": {Name: "x", Permissions: []string{"articles*"}},
	} {
		if _, err := s.CreateRole(ctx, r); !errors.Is(err, authz.ErrInvalid) {
			t.Errorf("%s: err=%v", name, err)
		}
	}
	if _, err := s.CreateRole(ctx, authz.Role{Name: "editor"}); !errors.Is(err, authz.ErrRoleExists) {
		t.Fatalf("duplicate: %v", err)
	}

	if roles, err := s.Roles(ctx, "acme"); err != nil || names(roles) != "/editor,/owner,acme/reviewer" {
		t.Fatalf("acme roles=%s err=%v", names(roles), err)
	}
	if roles, err := s.Roles(ctx, ""); err != nil || names(roles) != "/editor,/owner" {
		t.Fatalf("global roles=%s err=%v", names(roles), err)
	}
	if _, err := s.Role(ctx, reviewer.ID, "globex"); !errors.Is(err, authz.ErrRoleNotFound) {
		t.Fatalf("other tenant's role visible: %v", err)
	}
	if got, err := s.Role(ctx, reviewer.ID, "acme"); err != nil || got.Name != "reviewer" || got.Permissions[0] != "articles:read" {
		t.Fatalf("role=%+v err=%v", got, err)
	}
	if _, err := s.Role(ctx, "not-a-uuid", ""); !errors.Is(err, authz.ErrRoleNotFound) {
		t.Fatalf("malformed id: %v", err)
	}

	// 修改名称和权限；租户不能修改。
	updated, err := s.UpdateRole(ctx, authz.Role{ID: reviewer.ID, Tenant: "", Name: "auditor", Permissions: []string{"comments:delete"}})
	if err != nil || updated.Tenant != "acme" {
		t.Fatalf("update=%+v err=%v", updated, err)
	}
	if got, _ := s.Role(ctx, reviewer.ID, ""); got.Name != "auditor" || got.Tenant != "acme" || strings.Join(got.Permissions, ",") != "comments:delete" {
		t.Fatalf("after update=%+v", got)
	}
	if _, err := s.UpdateRole(ctx, authz.Role{ID: editor.ID, Name: "owner"}); !errors.Is(err, authz.ErrRoleExists) {
		t.Fatalf("rename onto existing: %v", err)
	}
	if _, err := s.UpdateRole(ctx, authz.Role{ID: editor.ID, Name: "editor", Permissions: []string{"nope"}}); !errors.Is(err, authz.ErrInvalid) {
		t.Fatalf("update with unknown permission: %v", err)
	}
	if _, err := s.UpdateRole(ctx, authz.Role{ID: "00000000-0000-0000-0000-000000000000", Name: "x"}); !errors.Is(err, authz.ErrRoleNotFound) {
		t.Fatalf("update missing: %v", err)
	}
}

// 不缓存和缓存的 Store 结果相同：每次修改之后的读取都得到新的权限。
func TestAssignAndResolve(t *testing.T) {
	for name, open := range map[string]func(t *testing.T) *authz.Store{"database": newStore, "cached": newCachedStore} {
		t.Run(name, func(t *testing.T) { testAssignAndResolve(t, open(t)) })
	}
}

func testAssignAndResolve(t *testing.T, s *authz.Store) {
	ctx := context.Background()
	owner := mustRole(t, s, authz.Role{Name: "owner", Permissions: []string{"*"}})
	member := mustRole(t, s, authz.Role{Name: "member", Permissions: []string{"articles:read"}})
	moderator := mustRole(t, s, authz.Role{Tenant: "acme", Name: "moderator", Permissions: []string{"articles:read", "comments:delete"}})
	empty := mustRole(t, s, authz.Role{Tenant: "acme", Name: "guest"})
	support := mustRole(t, s, authz.Role{Name: "support", Permissions: []string{"articles:read"}})
	platform := mustRole(t, s, authz.Role{Name: "platform-admin", Permissions: []string{"platform:tenants"}})

	// staff 与 alice 的 ID 相同，属于另一套账号体系，角色互不相通。
	alice, bob, guest, staff := user("alice"), user("bob"), user("guest"), identity.Ref{Type: "staff", ID: "alice"}
	mustAssign(t, s, alice, "acme", owner.ID)
	mustAssign(t, s, bob, "acme", member.ID)
	mustAssign(t, s, bob, "acme", moderator.ID)
	mustAssign(t, s, bob, "acme", moderator.ID) // 重复分配什么也不做
	mustAssign(t, s, bob, "globex", member.ID)
	mustAssign(t, s, guest, "acme", empty.ID)
	mustAssign(t, s, staff, authz.AllTenants, support.ID)
	mustAssign(t, s, staff, "", platform.ID)

	for name, call := range map[string]func() error{
		"custom role in other tenant": func() error { return s.Assign(ctx, bob, "globex", moderator.ID) },
		"custom role in all tenants":  func() error { return s.Assign(ctx, bob, authz.AllTenants, moderator.ID) },
		"custom role on platform":     func() error { return s.Assign(ctx, bob, "", moderator.ID) },
		"empty subject":               func() error { return s.Assign(ctx, identity.Ref{}, "acme", member.ID) },
		"subject without type":        func() error { return s.Assign(ctx, identity.Ref{ID: "bob"}, "acme", member.ID) },
	} {
		if err := call(); !errors.Is(err, authz.ErrInvalid) {
			t.Errorf("%s: err=%v", name, err)
		}
	}
	if err := s.Assign(ctx, bob, "acme", "00000000-0000-0000-0000-000000000000"); !errors.Is(err, authz.ErrRoleNotFound) {
		t.Fatalf("missing role: %v", err)
	}

	resolve := func(ref identity.Ref, tenant string) (identity.Subject, error) {
		return s.Resolve(identity.WithTenant(ctx, tenant), identity.Subject{Type: ref.Type, ID: ref.ID, Method: "bearer"})
	}
	for _, tc := range []struct {
		subject       identity.Ref
		tenant, perms string
	}{
		{alice, "acme", "*"},
		{bob, "acme", "articles:read,comments:delete"}, // 两个角色合并去重
		{bob, "globex", "articles:read"},
		{guest, "acme", ""},              // 有角色但没有权限，仍然是成员
		{staff, "acme", "articles:read"}, // 来自所有租户的分配，没有同 ID 的 alice 的 owner
		{staff, "", "platform:tenants"},  // 平台权限只来自平台的分配
		{bob, "", ""},                    // 平台中没有角色也可以访问，权限为空
		{alice, "", ""},                  // 没有同 ID 的 staff 的平台权限
	} {
		sub, err := resolve(tc.subject, tc.tenant)
		if err != nil || sub.Tenant != tc.tenant || strings.Join(sub.Permissions, ",") != tc.perms || sub.Method != "bearer" {
			t.Errorf("%v@%q: sub=%+v err=%v", tc.subject, tc.tenant, sub, err)
		}
	}
	if sub, _ := resolve(alice, "acme"); !sub.Can("members:manage") {
		t.Error("owner cannot manage members")
	}
	for _, tc := range []struct {
		subject identity.Ref
		tenant  string
	}{{user("carol"), "acme"}, {alice, "globex"}} {
		if _, err := resolve(tc.subject, tc.tenant); !errors.Is(err, authz.ErrNotMember) {
			t.Errorf("%v@%s: err=%v", tc.subject, tc.tenant, err)
		}
	}

	if tenants, err := s.Tenants(ctx, bob); err != nil || strings.Join(tenants, ",") != "acme,globex" {
		t.Fatalf("tenants=%v err=%v", tenants, err)
	}
	if tenants, _ := s.Tenants(ctx, staff); len(tenants) != 0 {
		t.Fatalf("staff tenants=%v", tenants)
	}
	members, err := s.Members(ctx, "acme")
	if err != nil || len(members) != 3 || members[0].Subject != alice || members[1].Subject != bob ||
		names(members[1].Roles) != "/member,acme/moderator" {
		t.Fatalf("members=%+v err=%v", members, err)
	}
	if roles, err := s.RolesOf(ctx, bob, "acme"); err != nil || names(roles) != "/member,acme/moderator" {
		t.Fatalf("roles of bob=%s err=%v", names(roles), err)
	}
	if roles, err := s.RolesOf(ctx, staff, ""); err != nil || names(roles) != "/platform-admin" {
		t.Fatalf("roles of staff=%s err=%v", names(roles), err)
	}

	if err := s.Unassign(ctx, bob, "acme", moderator.ID); err != nil {
		t.Fatal(err)
	}
	if sub, _ := resolve(bob, "acme"); slices.Contains(sub.Permissions, "comments:delete") {
		t.Fatalf("unassigned role still grants: %v", sub.Permissions)
	}
	if n, err := s.RemoveMember(ctx, bob, "acme"); n != 1 || err != nil {
		t.Fatalf("remove member n=%d err=%v", n, err)
	}
	if _, err := resolve(bob, "acme"); !errors.Is(err, authz.ErrNotMember) {
		t.Fatalf("removed member: %v", err)
	}

	// 删除角色同时删除它的分配。
	if err := s.DeleteRole(ctx, owner.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := resolve(alice, "acme"); !errors.Is(err, authz.ErrNotMember) {
		t.Fatalf("deleted role still assigned: %v", err)
	}
	if err := s.DeleteRole(ctx, owner.ID); !errors.Is(err, authz.ErrRoleNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
}

func TestCatalog(t *testing.T) {
	s := newStore(t)
	got := s.Catalog()
	got[0].Name = "changed"
	if s.Catalog()[0].Name != "articles:read" {
		t.Fatal("Catalog returned shared slice")
	}
}
