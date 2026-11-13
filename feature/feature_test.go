package feature_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/go-tenon/tenon/database/dbtest"
	"github.com/go-tenon/tenon/errorx"
	"github.com/go-tenon/tenon/feature"
	"github.com/go-tenon/tenon/identity"
)

var (
	editor = feature.Flag{
		Name:     "articles.new-editor",
		ByTenant: true,
		Default: func(_ context.Context, sub identity.Subject) bool {
			return sub.Can("platform:staff")
		},
	}
	search  = feature.Flag{Name: "search.v2"}
	catalog = feature.Catalog{editor, search}
)

func user(id, tenant string) identity.Subject {
	return identity.Subject{ID: id, Type: "user", Tenant: tenant}
}

func userRef(id string) identity.Ref { return identity.Ref{Type: "user", ID: id} }

func newStore(t *testing.T) *feature.Store {
	t.Helper()
	return feature.NewStore(dbtest.Open(t, feature.Migrations()), catalog)
}

func set(t *testing.T, s *feature.Store, f feature.Flag, scope feature.Scope, state feature.State) {
	t.Helper()
	require.NoError(t, s.Set(context.Background(), f, scope, state))
}

// check 断言 Active 与 For 的结果一致并等于 want。
func check(t *testing.T, s *feature.Store, sub identity.Subject, f feature.Flag, want bool, msg string) {
	t.Helper()
	ctx := context.Background()
	assert.Equal(t, want, s.Active(ctx, sub, f), "Active: "+msg)
	assert.Equal(t, want, s.For(ctx, sub).Active(f), "For: "+msg)
}

func TestPrecedence(t *testing.T) {
	s := newStore(t)
	alice := user("alice", "acme")
	staff := user("bob", "acme")
	staff.Permissions = []string{"platform:*"}

	check(t, s, alice, editor, false, "default")
	check(t, s, staff, editor, true, "default for staff")
	check(t, s, alice, search, false, "nil default")

	set(t, s, editor, feature.Tenant("acme"), feature.On)
	check(t, s, alice, editor, true, "tenant on")
	check(t, s, user("carol", "globex"), editor, false, "other tenant")

	set(t, s, editor, feature.Subject(userRef("alice")), feature.Off)
	check(t, s, alice, editor, false, "user beats tenant")
	check(t, s, user("dave", "acme"), editor, true, "tenant still on for others")

	set(t, s, editor, feature.Everyone, feature.Percent(0))
	check(t, s, alice, editor, false, "targets beat percent")
	check(t, s, user("dave", "acme"), editor, true, "tenant beats percent")
	check(t, s, staff, editor, true, "tenant beats percent for staff")
	check(t, s, user("carol", "globex"), editor, false, "percent 0 ignores default")

	set(t, s, editor, feature.Everyone, feature.Off)
	check(t, s, user("dave", "acme"), editor, false, "everyone off is a kill switch")
	check(t, s, staff, editor, false, "kill switch beats default")

	set(t, s, editor, feature.Everyone, feature.On)
	check(t, s, alice, editor, true, "everyone on beats user off")
	check(t, s, identity.Subject{}, editor, true, "everyone on includes anonymous")

	require.NoError(t, s.Clear(context.Background(), editor, feature.Everyone))
	check(t, s, alice, editor, false, "cleared everyone, user off again")
	check(t, s, alice, search, false, "other flag unaffected")
}

func TestServiceSubject(t *testing.T) {
	s := newStore(t)
	system := identity.Subject{ID: "system", Type: identity.System, Tenant: "acme"}
	set(t, s, search, feature.Subject(identity.Ref{Type: identity.System, ID: "system"}), feature.On)
	check(t, s, system, search, false, "subject settings do not apply to identity.System")
	set(t, s, search, feature.Tenant("acme"), feature.On)
	check(t, s, system, search, true, "tenant settings apply")

	set(t, s, search, feature.Everyone, feature.Percent(99))
	require.NoError(t, s.Clear(context.Background(), search, feature.Tenant("acme")))
	check(t, s, system, search, false, "no user ID, not in a user rollout below 100")
	check(t, s, system, editor, false, "default without staff permission")
}

// 主体的设置按类型和 ID 一起区分：另一套账号体系中的 alice 是另一个人。
func TestAccountSystems(t *testing.T) {
	s := newStore(t)
	staffAlice := identity.Subject{ID: "alice", Type: "staff"}
	set(t, s, search, feature.Subject(staffAlice.Ref()), feature.On)
	check(t, s, staffAlice, search, true, "staff alice on")
	check(t, s, user("alice", ""), search, false, "user alice unaffected")
}

// TestRollout 经过数据库检查比例放量的接线：设置的比例生效、结果对同一个人稳定。
// 哈希的统计性质由 TestInRolloutDistribution 直接检查纯函数，这里每次判断都查一次数据库，只用少量主体。
func TestRollout(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	active := func(f feature.Flag, sub identity.Subject) bool { return s.Active(ctx, sub, f) }

	const n = 20
	count := func(percent int) map[string]bool {
		set(t, s, search, feature.Everyone, feature.Percent(percent))
		on := map[string]bool{}
		for i := range n {
			if id := fmt.Sprint("u", i); active(search, user(id, "")) {
				on[id] = true
			}
		}
		return on
	}
	assert.Empty(t, count(0))
	half := count(50)
	assert.NotEmpty(t, half, "50% enables someone")
	assert.Less(t, len(half), n, "50% leaves someone out")
	assert.Len(t, count(100), n)

	// 结果对同一个人稳定。
	set(t, s, search, feature.Everyone, feature.Percent(50))
	for i := range n {
		sub := user(fmt.Sprint("u", i), "")
		assert.Equal(t, half[sub.ID], active(search, sub))
	}

	// 未登录的访客不在 100 以下的比例内。
	assert.False(t, active(search, identity.Subject{}))
}

func TestRolloutByTenant(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	set(t, s, editor, feature.Everyone, feature.Percent(50))
	on := 0
	const tenants = 10
	for i := range tenants {
		tenant := fmt.Sprint("t", i)
		first := s.Active(ctx, user("a", tenant), editor)
		for j := range 3 {
			assert.Equal(t, first, s.Active(ctx, user(fmt.Sprint("u", j), tenant), editor), "same result within %s", tenant)
		}
		if first {
			on++
		}
	}
	assert.NotZero(t, on, "50% enables some tenants")
	assert.Less(t, on, tenants, "50% leaves some tenants out")
	assert.False(t, s.Active(ctx, user("a", ""), editor), "no tenant, not in a tenant rollout")
}

func TestWithDB(t *testing.T) {
	db := dbtest.Open(t, feature.Migrations())
	s := feature.NewStore(db, catalog)
	ctx := context.Background()
	err := db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		require.NoError(t, s.WithDB(tx).Set(ctx, search, feature.Everyone, feature.On))
		return errors.New("rollback")
	})
	require.Error(t, err)
	assert.False(t, s.Active(ctx, user("alice", ""), search), "rolled back with the transaction")
}

func TestForReadsOnce(t *testing.T) {
	s := newStore(t)
	set(t, s, editor, feature.Tenant("acme"), feature.On)
	set(t, s, search, feature.Subject(userRef("alice")), feature.On)
	set(t, s, search, feature.Tenant("globex"), feature.On)
	flags := s.For(context.Background(), user("alice", "acme"))
	assert.True(t, flags.Active(editor))
	assert.True(t, flags.Active(search))
	unknown := feature.Flag{Name: "not-in-catalog", Default: func(context.Context, identity.Subject) bool { return true }}
	assert.True(t, flags.Active(unknown), "flags outside the catalog use their default")
}

func TestLoadErrorUsesDefault(t *testing.T) {
	db := dbtest.Open(t, nil) // 没有迁移，表不存在
	s := feature.NewStore(db, catalog)
	staff := user("bob", "acme")
	staff.Permissions = []string{"platform:staff"}
	check(t, s, staff, editor, true, "default on error")
	check(t, s, user("alice", "acme"), editor, false, "default on error")
}

func TestSetValidation(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for name, tc := range map[string]struct {
		f     feature.Flag
		scope feature.Scope
		state feature.State
	}{
		"unknown flag":      {feature.Flag{Name: "nope"}, feature.Everyone, feature.On},
		"empty tenant":      {search, feature.Tenant(""), feature.On},
		"empty subject":     {search, feature.Subject(identity.Ref{}), feature.On},
		"subject no type":   {search, feature.Subject(identity.Ref{ID: "alice"}), feature.On},
		"tenant percent":    {search, feature.Tenant("acme"), feature.Percent(10)},
		"user percent":      {search, feature.Subject(userRef("alice")), feature.Percent(10)},
		"negative percent":  {search, feature.Everyone, feature.Percent(-1)},
		"percent above 100": {search, feature.Everyone, feature.Percent(101)},
		"zero state":        {search, feature.Everyone, feature.State{}},
	} {
		err := s.Set(ctx, tc.f, tc.scope, tc.state)
		assert.ErrorIs(t, err, feature.ErrInvalid, name)
		assert.Equal(t, errorx.InvalidArgument, errorx.KindOf(err), name)
	}
	assert.ErrorIs(t, s.Clear(ctx, search, feature.Tenant("")), feature.ErrInvalid)
	settings, err := s.Settings(ctx, search)
	require.NoError(t, err)
	assert.Empty(t, settings)
}

func TestSettings(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	set(t, s, search, feature.Subject(userRef("bob")), feature.On)
	set(t, s, search, feature.Tenant("globex"), feature.Off)
	set(t, s, search, feature.Everyone, feature.Percent(20))
	set(t, s, search, feature.Subject(userRef("alice")), feature.Off)
	set(t, s, search, feature.Tenant("acme"), feature.On)
	set(t, s, search, feature.Everyone, feature.Percent(30))         // 覆盖
	set(t, s, search, feature.Subject(userRef("alice")), feature.On) // 覆盖
	set(t, s, editor, feature.Tenant("acme"), feature.On)

	settings, err := s.Settings(ctx, search)
	require.NoError(t, err)
	var got []string
	for _, st := range settings {
		assert.False(t, st.UpdatedAt.IsZero())
		got = append(got, fmt.Sprintf("%s/%s=%s:%d", st.Scope.Tenant(), st.Scope.Subject().ID, st.State.Mode, st.State.Percent))
	}
	assert.Equal(t, []string{
		"/=percent:30",
		"acme/=on:0",
		"globex/=off:0",
		"/alice=on:0",
		"/bob=on:0",
	}, got)
	assert.Equal(t, feature.Everyone, settings[0].Scope)
	assert.Equal(t, feature.Tenant("acme"), settings[1].Scope)
	assert.Equal(t, feature.Subject(userRef("alice")), settings[3].Scope)

	require.NoError(t, s.Clear(ctx, search, feature.Subject(userRef("bob"))))
	require.NoError(t, s.Clear(ctx, search, feature.Subject(userRef("nobody"))))
	settings, err = s.Settings(ctx, search)
	require.NoError(t, err)
	assert.Len(t, settings, 4)
}

func TestPrune(t *testing.T) {
	db := dbtest.Open(t, feature.Migrations())
	ctx := context.Background()
	old := feature.Flag{Name: "old"}
	before := feature.NewStore(db, feature.Catalog{editor, search, old})
	set(t, before, old, feature.Everyone, feature.On)
	set(t, before, old, feature.Tenant("acme"), feature.Off)
	set(t, before, search, feature.Everyone, feature.On)

	n, err := feature.NewStore(db, catalog).Prune(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	settings, err := before.Settings(ctx, search)
	require.NoError(t, err)
	assert.Len(t, settings, 1)

	n, err = feature.NewStore(db, nil).Prune(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
}

func TestNewStorePanics(t *testing.T) {
	db := dbtest.Open(t, feature.Migrations())
	assert.NotPanics(t, func() { feature.NewStore(db, nil) })
	for name, c := range map[string]feature.Catalog{
		"empty name": {{Name: ""}},
		"space":      {{Name: "new editor"}},
		"duplicate":  {search, search},
	} {
		assert.Panics(t, func() { feature.NewStore(db, c) }, name)
	}
	assert.Len(t, feature.NewStore(db, catalog).Catalog(), 2)
}
