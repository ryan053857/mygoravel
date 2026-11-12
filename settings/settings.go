package settings

import (
	"context"
	"database/sql"
	"embed"
	"encoding"
	"encoding/json"
	"io/fs"
	"reflect"
	"strings"
	"time"
	"unicode"

	"github.com/cockroachdb/errors"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/errorx"
)

// ErrInvalid 表示租户 ID 为空或包含空白。
var ErrInvalid = errorx.Define(errorx.InvalidArgument, "settings.invalid")

//go:embed migrations
var migrationFiles embed.FS

// Migrations 返回 settings 表的迁移，供应用发布。
func Migrations() fs.FS {
	f, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		panic(err)
	}
	return f
}

type row struct {
	bun.BaseModel `bun:"table:settings,alias:s"`

	Name      string          `bun:"name,pk"`
	Tenant    string          `bun:"tenant,pk"`
	Payload   json.RawMessage `bun:"payload,type:jsonb,notnull"`
	UpdatedAt time.Time       `bun:"updated_at,notnull"`
}

// Group 是一组设置，整组保存为 settings 表中的一行 JSON。T 是一个结构体，字段用 json 标签命名。
// Group 可以并发使用；Tenant 和 WithDB 返回副本，不修改原来的 Group。
type Group[T any] struct {
	db       bun.IDB
	name     string
	defaults json.RawMessage
	shape    shape
	tenant   string
	scoped   bool // 调用过 Tenant
}

// New 创建名为 name 的一组设置，defaults 是没有保存过时的值，也用于补上保存之后新增的字段。
// db 必须已执行 settings 迁移。
//
// 以下情况 panic，启动时就能发现：db 为 nil；name 为空或包含空白；T 不是结构体；
// T 或嵌套的结构体中有字段带 omitempty 或 omitzero（零值不会保存，读取时会被默认值覆盖）。
func New[T any](db bun.IDB, name string, defaults T) *Group[T] {
	if db == nil {
		panic("settings: nil database")
	}
	if !validKey(name) {
		panic(errors.Newf("settings: invalid group name %q", name))
	}
	t := reflect.TypeFor[T]()
	if t.Kind() != reflect.Struct {
		panic(errors.Newf("settings: group %q: %v is not a struct", name, t))
	}
	s := shapeOf(t, name)
	b, err := json.Marshal(defaults)
	if err != nil {
		panic(errors.Wrapf(err, "settings: group %q: encode defaults", name))
	}
	return &Group[T]{db: db, name: name, defaults: b, shape: s}
}

// Name 返回这组设置的名称。
func (g *Group[T]) Name() string { return g.name }

// WithDB 返回使用 db（通常是 database.RunInTx 的事务）的副本，让 Save、Update 与其他写入（例如操作日志）一起提交。
func (g *Group[T]) WithDB(db bun.IDB) *Group[T] {
	cp := *g
	cp.db = db
	return &cp
}

// Tenant 返回读写租户 id 的设置的副本。租户的设置与全局设置互不影响：租户没有保存过时使用默认值，
// 不回落到全局设置。id 为空或包含空白时，副本的每个方法都返回 ErrInvalid，不会当作全局设置。
func (g *Group[T]) Tenant(id string) *Group[T] {
	cp := *g
	cp.tenant, cp.scoped = id, true
	return &cp
}

// Get 返回保存的设置；没有保存过时返回默认值。保存之后新增的字段使用默认值，嵌套的结构体逐个字段补上，
// 其他类型（切片、map、指针等）整体使用保存的值。已删除的字段被忽略，下次保存时去掉。
// 保存的值与 T 的类型不符时（例如字段从字符串改成了数字）返回错误，此时应当写迁移转换保存的 JSON。
func (g *Group[T]) Get(ctx context.Context) (T, error) {
	var v T
	if err := g.check(); err != nil {
		return v, err
	}
	var r row
	err := g.db.NewSelect().Model(&r).Column("payload").
		Where("name = ? AND tenant = ?", g.name, g.tenant).Scan(ctx)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return v, g.decode(nil, &v)
	case err != nil:
		return v, errors.Wrapf(err, "settings: get %q", g.name)
	}
	return v, g.decode(r.Payload, &v)
}

// Save 用 v 覆盖保存的设置。两个人同时编辑同一组设置时，后保存的覆盖先保存的；只修改部分字段时用 Update。
func (g *Group[T]) Save(ctx context.Context, v T) error {
	if err := g.check(); err != nil {
		return err
	}
	return g.save(ctx, g.db, v)
}

// Update 在事务中读出设置，交给 fn 修改后保存。并发的 Update 依次执行，不会丢失修改。
// fn 返回错误时不保存，Update 返回这个错误。g 使用的数据库是 database.RunInTx 的事务时以保存点执行。
func (g *Group[T]) Update(ctx context.Context, fn func(*T) error) error {
	if err := g.check(); err != nil {
		return err
	}
	return database.RunInTx(ctx, g.db, nil, func(ctx context.Context, tx database.Tx) error {
		// 先写入默认值，保证有一行可以锁住；否则两个并发的 Update 都读到默认值，后写的覆盖先写的。
		_, err := tx.NewInsert().Model(&row{Name: g.name, Tenant: g.tenant, Payload: g.defaults, UpdatedAt: time.Now().UTC()}).
			On("CONFLICT (name, tenant) DO NOTHING").Exec(ctx)
		if err != nil {
			return errors.Wrapf(err, "settings: update %q", g.name)
		}
		var r row
		err = tx.NewSelect().Model(&r).Column("payload").
			Where("name = ? AND tenant = ?", g.name, g.tenant).For("UPDATE").Scan(ctx)
		if err != nil {
			return errors.Wrapf(err, "settings: update %q", g.name)
		}
		var v T
		if err := g.decode(r.Payload, &v); err != nil {
			return err
		}
		if err := fn(&v); err != nil {
			return err
		}
		return g.save(ctx, tx, v)
	})
}

// Reset 删除保存的设置，之后 Get 返回默认值。没有保存过时什么也不做。
func (g *Group[T]) Reset(ctx context.Context) error {
	if err := g.check(); err != nil {
		return err
	}
	_, err := g.db.NewDelete().Model((*row)(nil)).
		Where("name = ? AND tenant = ?", g.name, g.tenant).Exec(ctx)
	return errors.Wrapf(err, "settings: reset %q", g.name)
}

func (g *Group[T]) check() error {
	if g.scoped && !validKey(g.tenant) {
		return ErrInvalid.New(errorx.Public("field", "tenant"))
	}
	return nil
}

func (g *Group[T]) save(ctx context.Context, db bun.IDB, v T) error {
	b, err := json.Marshal(v)
	if err != nil {
		return errors.Wrapf(err, "settings: encode %q", g.name)
	}
	r := &row{Name: g.name, Tenant: g.tenant, Payload: b, UpdatedAt: time.Now().UTC()}
	_, err = db.NewInsert().Model(r).On("CONFLICT (name, tenant) DO UPDATE").
		Set("payload = EXCLUDED.payload").Set("updated_at = EXCLUDED.updated_at").Exec(ctx)
	return errors.Wrapf(err, "settings: save %q", g.name)
}

// decode 把保存的 JSON 合并到默认值上，解码到 v。payload 为 nil 时只解码默认值。
func (g *Group[T]) decode(payload json.RawMessage, v *T) error {
	data := g.defaults
	if payload != nil {
		var err error
		if data, err = merge(g.shape, g.defaults, payload); err != nil {
			return errors.Wrapf(err, "settings: decode %q", g.name)
		}
	}
	return errors.Wrapf(json.Unmarshal(data, v), "settings: decode %q", g.name)
}

// shape 是 T 中逐个字段合并的部分：键是 JSON 字段名，值是这个字段的 shape。
// 没有自定义解码的结构体逐个字段合并；nil 表示其他类型，整体使用保存的值。
type shape map[string]shape

var (
	jsonUnmarshaler = reflect.TypeFor[json.Unmarshaler]()
	textUnmarshaler = reflect.TypeFor[encoding.TextUnmarshaler]()
)

func shapeOf(t reflect.Type, path string) shape {
	if t.Kind() != reflect.Struct {
		return nil
	}
	if p := reflect.PointerTo(t); p.Implements(jsonUnmarshaler) || p.Implements(textUnmarshaler) {
		return nil // 例如 time.Time
	}
	s := shape{}
	addFields(s, t, path)
	return s
}

func addFields(s shape, t reflect.Type, path string) {
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" && f.Type.Kind() == reflect.Struct {
			addFields(s, f.Type, path) // 嵌入的结构体，字段提升到外层对象
			continue
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		for o := range strings.SplitSeq(opts, ",") {
			if o == "omitempty" || o == "omitzero" {
				panic(errors.Newf("settings: %s.%s: %s is not allowed, the zero value would read back as the default", path, name, o))
			}
		}
		if _, ok := s[name]; !ok {
			s[name] = shapeOf(f.Type, path+"."+name)
		}
	}
}

// merge 返回 stored 合并到 def 上的 JSON：s 不为 nil 且两边都是对象时逐个字段合并，否则使用 stored。
func merge(s shape, def, stored json.RawMessage) (json.RawMessage, error) {
	if s == nil {
		return stored, nil
	}
	var d, st map[string]json.RawMessage
	if json.Unmarshal(def, &d) != nil || json.Unmarshal(stored, &st) != nil || d == nil || st == nil {
		return stored, nil // 不是对象，交给解码报告类型错误
	}
	for k, v := range st {
		if dv, ok := d[k]; ok {
			m, err := merge(s[k], dv, v)
			if err != nil {
				return nil, err
			}
			v = m
		}
		d[k] = v
	}
	return json.Marshal(d)
}

// validKey 报告名称不为空，也不包含空白。
func validKey(s string) bool {
	return s != "" && !strings.ContainsFunc(s, unicode.IsSpace)
}
