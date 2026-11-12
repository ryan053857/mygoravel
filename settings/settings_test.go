package settings_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/database/dbtest"
	"github.com/ryan053857/mygoravel/settings"
)

type Mail struct {
	From  string `json:"from"`
	Reply string `json:"reply"`
}

type Site struct {
	Name       string         `json:"name"`
	SignupOpen bool           `json:"signup_open"`
	Exts       []string       `json:"exts"`
	Limits     map[string]int `json:"limits"`
	Mail       Mail           `json:"mail"`
	Launched   time.Time      `json:"launched"`
}

var defaults = Site{
	Name:       "My Blog",
	SignupOpen: true,
	Exts:       []string{"jpg", "png"},
	Limits:     map[string]int{"posts": 10, "files": 5},
	Mail:       Mail{From: "noreply@example.com", Reply: "help@example.com"},
	Launched:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
}

func open(t *testing.T) *bun.DB {
	t.Helper()
	return dbtest.Open(t, settings.Migrations())
}

func get[T any](t *testing.T, g *settings.Group[T]) T {
	t.Helper()
	v, err := g.Get(context.Background())
	require.NoError(t, err)
	return v
}

func TestGetSaveReset(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	site := settings.New(open(t), "site", defaults)

	assert.Equal(t, defaults, get(t, site), "defaults before any save")

	s := get(t, site)
	s.Exts[0] = "gif" // 修改返回值不影响默认值
	s.Limits["posts"] = 99
	assert.Equal(t, defaults, get(t, site))

	saved := Site{Name: "Renamed", Exts: []string{}, Limits: map[string]int{"posts": 1}, Mail: Mail{From: "a@b.c"}}
	require.NoError(t, site.Save(ctx, saved))
	assert.Equal(t, saved, get(t, site), "zero values and removed map keys are kept, not replaced by defaults")

	require.NoError(t, site.Reset(ctx))
	assert.Equal(t, defaults, get(t, site))
	require.NoError(t, site.Reset(ctx), "reset without a row")
}

func TestNewFieldsUseDefaults(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := open(t)
	_, err := db.NewRaw(`INSERT INTO settings (name, tenant, payload, updated_at)
		VALUES ('site', '', '{"name": "Old", "exts": ["gif"], "mail": {"from": "old@example.com"}, "removed": 1}', ?)`,
		time.Now()).Exec(ctx)
	require.NoError(t, err)

	got := get(t, settings.New(db, "site", defaults))
	want := defaults
	want.Name = "Old"
	want.Exts = []string{"gif"}
	want.Mail.From = "old@example.com" // 嵌套结构体逐个字段合并，Reply 仍是默认值
	assert.Equal(t, want, got)
}

func TestTypeMismatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := open(t)
	_, err := db.NewRaw(`INSERT INTO settings (name, tenant, payload, updated_at)
		VALUES ('site', '', '{"name": 42}', ?)`, time.Now()).Exec(ctx)
	require.NoError(t, err)

	_, err = settings.New(db, "site", defaults).Get(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `settings: decode "site"`)
}

func TestTenants(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	site := settings.New(open(t), "site", defaults)

	require.NoError(t, site.Tenant("acme").Save(ctx, Site{Name: "Acme"}))
	assert.Equal(t, "Acme", get(t, site.Tenant("acme")).Name)
	assert.Equal(t, defaults, get(t, site), "global is unaffected")
	assert.Equal(t, defaults, get(t, site.Tenant("globex")), "no fallback to other rows")

	require.NoError(t, site.Save(ctx, Site{Name: "Global"}))
	assert.Equal(t, defaults, get(t, site.Tenant("globex")), "tenants do not fall back to global")

	for _, id := range []string{"", " acme"} {
		g := site.Tenant(id)
		_, err := g.Get(ctx)
		assert.ErrorIs(t, err, settings.ErrInvalid, "Get %q", id)
		assert.ErrorIs(t, g.Save(ctx, defaults), settings.ErrInvalid, "Save %q", id)
		assert.ErrorIs(t, g.Update(ctx, func(*Site) error { return nil }), settings.ErrInvalid, "Update %q", id)
		assert.ErrorIs(t, g.Reset(ctx), settings.ErrInvalid, "Reset %q", id)
	}
}

func TestUpdate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	site := settings.New(open(t), "site", defaults)

	require.NoError(t, site.Update(ctx, func(s *Site) error {
		assert.Equal(t, defaults, *s, "starts from defaults")
		s.SignupOpen = false
		return nil
	}))
	assert.False(t, get(t, site).SignupOpen)

	boom := errors.New("boom")
	err := site.Update(ctx, func(s *Site) error { s.Name = "Lost"; return boom })
	assert.ErrorIs(t, err, boom)
	assert.Equal(t, defaults.Name, get(t, site).Name, "not saved when fn fails")
}

func TestConcurrentUpdates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	counter := settings.New(open(t), "counter", struct {
		N int `json:"n"`
	}{})

	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			assert.NoError(t, counter.Update(ctx, func(c *struct {
				N int `json:"n"`
			}) error {
				c.N++
				return nil
			}))
		})
	}
	wg.Wait()
	assert.Equal(t, 10, get(t, counter).N)
}

func TestWithDB(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := open(t)
	site := settings.New(db, "site", defaults)
	boom := errors.New("boom")

	err := database.RunInTx(ctx, db, nil, func(ctx context.Context, tx database.Tx) error {
		require.NoError(t, site.WithDB(tx).Save(ctx, Site{Name: "In tx"}))
		require.NoError(t, site.WithDB(tx).Update(ctx, func(s *Site) error {
			assert.Equal(t, "In tx", s.Name, "Update sees the transaction's write")
			return nil
		}))
		return boom
	})
	require.ErrorIs(t, err, boom)
	assert.Equal(t, defaults, get(t, site), "rolled back with the transaction")
}

func TestNewPanics(t *testing.T) {
	t.Parallel()
	db := open(t)
	type omit struct {
		A int `json:"a,omitempty"`
	}
	type nested struct {
		M struct {
			B bool `json:"b,omitzero"`
		} `json:"m"`
	}
	type leaf struct {
		// 切片元素和指针整体替换，不合并，可以用 omitempty
		Items []omit `json:"items"`
		P     *omit  `json:"p"`
	}

	assert.PanicsWithError(t, `settings: site.a: omitempty is not allowed, the zero value would read back as the default`,
		func() { settings.New(db, "site", omit{}) })
	assert.Panics(t, func() { settings.New(db, "site", nested{}) })
	assert.Panics(t, func() { settings.New(db, "", defaults) })
	assert.Panics(t, func() { settings.New(db, "a b", defaults) })
	assert.Panics(t, func() { settings.New(db, "site", map[string]any{}) })
	assert.Panics(t, func() { settings.New[Site](nil, "site", defaults) })
	assert.NotPanics(t, func() { settings.New(db, "site", leaf{}) })
}
