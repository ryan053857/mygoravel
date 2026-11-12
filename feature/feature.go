package feature

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/binary"
	"io/fs"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/cockroachdb/errors"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/identity"
)

// ErrInvalid 表示设置不合法，例如开关不在目录中、给租户或主体设置比例、比例不在 0 到 100 之间。
var ErrInvalid = errorx.Define(errorx.InvalidArgument, "feature.invalid")

// maxKey 是开关名和租户的最大长度，与 MySQL 迁移中 VARCHAR(191) 的列宽一致。
const maxKey = 191

//go:embed migrations
var migrationFiles embed.FS

// Migrations 返回 Store 使用的 feature_settings 表的迁移，供应用发布。
// 根目录下按数据库类型分为 postgres、mysql 和 sqlite 三个目录，由 migrate.Publish 按应用的数据库选择。
func Migrations() fs.FS {
	f, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		panic(err)
	}
	return f
}

// Flag 是在代码中声明的一个开关。
type Flag struct {
	// Name 是开关名，例如 articles.new-editor，保存在设置中。
	Name string
	// Description 在后台页面中显示。
	Description string
	// ByTenant 为 true 时按租户放量，同一租户的人结果一致；否则按主体放量。
	ByTenant bool
	// Default 在没有任何设置命中时决定结果，nil 表示关闭。它可能在每次检查时调用，应当足够快。
	Default func(ctx context.Context, sub identity.Subject) bool
}

// Catalog 是应用声明的全部开关。Set 只接受目录中的开关，Prune 删除不在目录中的开关的设置。
type Catalog []Flag

// Scope 是一条设置的范围：全员（Everyone）、某个租户（Tenant）或某个主体（Subject）。
type Scope struct {
	kind   scopeKind
	tenant string
	ref    identity.Ref
}

type scopeKind uint8

const (
	everyone scopeKind = iota
	tenantScope
	subjectScope
)

// Everyone 是全员的设置。
var Everyone = Scope{}

// Tenant 是租户 id 的设置。id 为空时 Set 和 Clear 返回 ErrInvalid，不会当作全员。
func Tenant(id string) Scope { return Scope{kind: tenantScope, tenant: id} }

// Subject 是主体 ref 的设置，在主体所属的每个租户中都生效。ref 不合法（见 identity.Ref.Validate）时
// Set 和 Clear 返回 ErrInvalid。identity.System 主体不适用主体的设置。
func Subject(ref identity.Ref) Scope { return Scope{kind: subjectScope, ref: ref} }

// Tenant 返回租户的 ID；不是租户的设置时返回 ""。
func (s Scope) Tenant() string { return s.tenant }

// Subject 返回主体；不是主体的设置时返回零值。
func (s Scope) Subject() identity.Ref { return s.ref }

// Mode 是设置的类型。
type Mode string

const (
	ModeOn      Mode = "on"
	ModeOff     Mode = "off"
	ModePercent Mode = "percent" // 只用于全员
)

// State 是一条设置的值。
type State struct {
	Mode    Mode
	Percent int // Mode 为 ModePercent 时有效，0 到 100
}

var (
	// On 开启。设给全员时忽略租户和主体的单独设置。
	On = State{Mode: ModeOn}
	// Off 关闭。设给全员时忽略租户和主体的单独设置，用作紧急关闭。
	Off = State{Mode: ModeOff}
)

// Percent 让 n% 的主体或租户开启，只能设给全员。租户和主体的单独设置优先于它。
// Percent(100) 包括没有主体或租户的请求，例如未登录的访客，以及 identity.System；小于 100 时它们不在比例内。
func Percent(n int) State { return State{Mode: ModePercent, Percent: n} }

// Setting 是一条设置。
type Setting struct {
	Scope     Scope
	State     State
	UpdatedAt time.Time
}

type row struct {
	bun.BaseModel `bun:"table:feature_settings,alias:fs"`

	Name        string    `bun:"name,pk"`
	Tenant      string    `bun:"tenant,pk"`
	SubjectType string    `bun:"subject_type,pk"`
	SubjectID   string    `bun:"subject_id,pk"`
	Mode        Mode      `bun:"mode,notnull"`
	Percent     int       `bun:"percent,notnull"`
	UpdatedAt   time.Time `bun:"updated_at,notnull"`
}

// Store 是基于 Bun 的开关设置存储，查询兼容 PostgreSQL、MySQL 与 SQLite，可以并发使用。
type Store struct {
	db      bun.IDB
	catalog Catalog
	names   map[string]bool
}

// NewStore 创建存储。db 必须已执行 feature 迁移。
// 目录中的开关名为空、重复、包含空白或超过 191 字节时 panic。
func NewStore(db bun.IDB, catalog Catalog) *Store {
	if db == nil {
		panic("feature: nil database")
	}
	names := make(map[string]bool, len(catalog))
	for _, f := range catalog {
		if !validKey(f.Name) || names[f.Name] {
			panic(errors.Newf("feature: invalid or duplicate flag name %q", f.Name))
		}
		names[f.Name] = true
	}
	return &Store{db: db, catalog: slices.Clone(catalog), names: names}
}

// WithDB 返回使用 db（通常是业务事务）的副本，用于让 Set、Clear 与其他写入（例如操作日志）一起提交。
func (s *Store) WithDB(db bun.IDB) *Store {
	cp := *s
	cp.db = db
	return &cp
}

// Catalog 返回开关目录，供后台页面列出全部开关。
func (s *Store) Catalog() Catalog { return slices.Clone(s.catalog) }

// Active 报告 f 对 sub 是否开启，判断顺序见包文档。读取设置出错时记录 Error 日志，返回 f.Default 的结果。
func (s *Store) Active(ctx context.Context, sub identity.Subject, f Flag) bool {
	rows, err := s.load(ctx, sub, f.Name)
	if err != nil {
		slog.ErrorContext(ctx, "feature: load settings, using default", "flag", f.Name, "error", err)
	}
	return evaluate(ctx, sub, f, rows)
}

// For 一次读出 sub 相关的全部设置，用于在一个请求中检查多个开关。读取出错时记录 Error 日志，
// 返回的 Flags 只使用各开关的 Default。Flags 在调用 Default 时使用 ctx，只在本次请求中使用。
func (s *Store) For(ctx context.Context, sub identity.Subject) Flags {
	rows, err := s.load(ctx, sub, "")
	if err != nil {
		slog.ErrorContext(ctx, "feature: load settings, using defaults", "error", err)
	}
	byName := make(map[string][]row)
	for _, r := range rows {
		byName[r.Name] = append(byName[r.Name], r)
	}
	return Flags{ctx: ctx, sub: sub, rows: byName}
}

// Flags 是 For 读出的一个主体的设置。
type Flags struct {
	ctx  context.Context
	sub  identity.Subject
	rows map[string][]row
}

// Active 报告 f 是否开启，结果与 Store.Active 相同。
func (f Flags) Active(flag Flag) bool { return evaluate(f.ctx, f.sub, flag, f.rows[flag.Name]) }

// load 读取 sub 能命中的设置：全员、sub 的租户和 sub 本人；name 不为空时只读这个开关。
func (s *Store) load(ctx context.Context, sub identity.Subject, name string) ([]row, error) {
	var rows []row
	q := s.db.NewSelect().Model(&rows)
	if name != "" {
		q = q.Where("name = ?", name)
	}
	q = q.WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
		q = q.WhereOr("tenant = '' AND subject_id = ''")
		if sub.Tenant != "" {
			q = q.WhereOr("tenant = ? AND subject_id = ''", sub.Tenant)
		}
		if ref := subjectOf(sub); !ref.IsZero() {
			q = q.WhereOr("tenant = '' AND subject_type = ? AND subject_id = ?", ref.Type, ref.ID)
		}
		return q
	})
	return rows, errors.Wrap(q.Scan(ctx), "feature: load settings")
}

// subjectOf 返回用于主体设置和按主体放量的主体：identity.System（例如后台任务）不适用，返回零值。
func subjectOf(sub identity.Subject) identity.Ref {
	if sub.Type == identity.System {
		return identity.Ref{}
	}
	return sub.Ref()
}

func evaluate(ctx context.Context, sub identity.Subject, f Flag, rows []row) bool {
	var everyone, tenant, subject *row
	for i := range rows {
		r := &rows[i]
		switch {
		case r.Tenant == "" && r.SubjectID == "":
			everyone = r
		case r.SubjectID == "":
			tenant = r
		default:
			subject = r
		}
	}
	switch {
	case everyone != nil && everyone.Mode != ModePercent:
		return everyone.Mode == ModeOn
	case subject != nil:
		return subject.Mode == ModeOn
	case tenant != nil:
		return tenant.Mode == ModeOn
	case everyone != nil:
		unit := subjectOf(sub).String()
		if f.ByTenant {
			unit = sub.Tenant
		}
		return inRollout(f.Name, unit, everyone.Percent)
	case f.Default != nil:
		return f.Default(ctx, sub)
	}
	return false
}

// inRollout 报告 unit 是否落在 name 的前 percent% 中。哈希包含开关名，不同开关选中的人不同。
func inRollout(name, unit string, percent int) bool {
	if percent >= 100 {
		return true
	}
	if unit == "" || percent <= 0 {
		return false
	}
	h := sha256.Sum256([]byte(name + "\x00" + unit))
	return int(binary.BigEndian.Uint32(h[:4])%100) < percent
}

// Set 保存 f 在 scope 上的设置，已有设置时覆盖。f 不在目录中、租户 ID 为空、主体不合法、
// 给租户或主体设置比例、比例不在 0 到 100 之间时返回 ErrInvalid。
func (s *Store) Set(ctx context.Context, f Flag, scope Scope, state State) error {
	if !s.names[f.Name] {
		return ErrInvalid.New(errorx.Public("field", "flag"))
	}
	if err := checkScope(scope); err != nil {
		return err
	}
	switch {
	case state.Mode == ModeOn || state.Mode == ModeOff:
		state.Percent = 0
	case state.Mode != ModePercent || state.Percent < 0 || state.Percent > 100 || scope != Everyone:
		return ErrInvalid.New(errorx.Public("field", "state"))
	}
	r := &row{
		Name: f.Name, Tenant: scope.tenant, SubjectType: string(scope.ref.Type), SubjectID: scope.ref.ID,
		Mode: state.Mode, Percent: state.Percent, UpdatedAt: time.Now().UTC(),
	}
	q := s.db.NewInsert().Model(r)
	if s.db.Dialect().Name() == dialect.MySQL {
		q = q.On("DUPLICATE KEY UPDATE")
	} else {
		q = q.On("CONFLICT (name, tenant, subject_type, subject_id) DO UPDATE")
	}
	_, err := q.Set("mode = ?", r.Mode).Set("percent = ?", r.Percent).Set("updated_at = ?", r.UpdatedAt).Exec(ctx)
	return errors.Wrap(err, "feature: set")
}

// Clear 删除 f 在 scope 上的设置，恢复按后面的规则判断。没有这条设置时什么也不做。
func (s *Store) Clear(ctx context.Context, f Flag, scope Scope) error {
	if err := checkScope(scope); err != nil {
		return err
	}
	_, err := s.db.NewDelete().Model((*row)(nil)).
		Where("name = ?", f.Name).Where("tenant = ?", scope.tenant).
		Where("subject_type = ? AND subject_id = ?", scope.ref.Type, scope.ref.ID).Exec(ctx)
	return errors.Wrap(err, "feature: clear")
}

// Settings 返回 f 的全部设置：全员在前，然后是各租户、各主体；租户按 ID 排序，主体按类型和 ID 排序。
func (s *Store) Settings(ctx context.Context, f Flag) ([]Setting, error) {
	var rows []row
	err := s.db.NewSelect().Model(&rows).Where("name = ?", f.Name).
		OrderExpr("subject_type ASC, subject_id ASC, tenant ASC").Scan(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "feature: list settings")
	}
	out := make([]Setting, len(rows))
	for i, r := range rows {
		out[i] = Setting{
			Scope:     scopeOf(r),
			State:     State{Mode: r.Mode, Percent: r.Percent},
			UpdatedAt: r.UpdatedAt,
		}
	}
	return out, nil
}

// Prune 删除不在目录中的开关的全部设置，返回删除的条数。开关从代码中删除之后调用。
func (s *Store) Prune(ctx context.Context) (int, error) {
	q := s.db.NewDelete().Model((*row)(nil))
	if len(s.names) == 0 {
		q = q.Where("1 = 1")
	} else {
		names := make([]string, 0, len(s.names))
		for n := range s.names {
			names = append(names, n)
		}
		q = q.Where("name NOT IN (?)", bun.List(names))
	}
	res, err := q.Exec(ctx)
	if err != nil {
		return 0, errors.Wrap(err, "feature: prune")
	}
	n, err := res.RowsAffected()
	return int(n), errors.Wrap(err, "feature: prune")
}

func checkScope(scope Scope) error {
	switch {
	case scope.kind == tenantScope && !validKey(scope.tenant),
		scope.kind == subjectScope && scope.ref.Validate() != nil:
		return ErrInvalid.New(errorx.Public("field", "scope"))
	}
	return nil
}

func scopeOf(r row) Scope {
	switch {
	case r.SubjectID != "":
		return Subject(identity.Ref{Type: identity.SubjectType(r.SubjectType), ID: r.SubjectID})
	case r.Tenant != "":
		return Tenant(r.Tenant)
	}
	return Everyone
}

// validKey 报告名称不为空、不超过 maxKey 个字节，也不包含空白。
func validKey(s string) bool {
	return s != "" && len(s) <= maxKey && !strings.ContainsFunc(s, unicode.IsSpace)
}
