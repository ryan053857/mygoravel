package login

import (
	"context"
	"database/sql"
	"net/mail"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/cockroachdb/errors"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/identity"
)

// 内置的登录标识类型。第三方登录的类型是应用声明的提供方名称，例如 "google"、"wechat"。
const (
	KindEmail    = "email"
	KindPhone    = "phone"
	KindUsername = "username"
)

const (
	maxKind  = 32
	maxValue = 191
)

var (
	kindPattern  = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	phonePattern = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)
)

// Identifier 是一个登录标识：用什么找到一个主体。
type Identifier struct {
	Kind  string
	Value string
}

// Email 返回邮箱标识，保存前去掉首尾空白并转为小写。
func Email(address string) Identifier { return Identifier{Kind: KindEmail, Value: address} }

// Phone 返回手机号标识，必须是 E.164 格式，例如 +8613800138000；应用负责把用户的输入转换成这个格式。
func Phone(e164 string) Identifier { return Identifier{Kind: KindPhone, Value: e164} }

// Username 返回用户名标识，保存前去掉首尾空白并转为小写，不能包含空白。
func Username(name string) Identifier { return Identifier{Kind: KindUsername, Value: name} }

// String 返回 kind:value。
func (id Identifier) String() string { return id.Kind + ":" + id.Value }

// Normalize 返回规范化后的标识：邮箱和用户名去掉首尾空白并转为小写，其他类型去掉首尾空白。
// 格式不正确时返回 ErrInvalidIdentifier。
func (id Identifier) Normalize() (Identifier, error) {
	if len(id.Kind) > maxKind || !kindPattern.MatchString(id.Kind) {
		return Identifier{}, ErrInvalidIdentifier.New()
	}
	v := trimSpace(id.Value)
	switch id.Kind {
	case KindEmail:
		v = strings.ToLower(v)
		if a, err := mail.ParseAddress(v); err != nil || a.Address != v || a.Name != "" {
			return Identifier{}, ErrInvalidIdentifier.New()
		}
	case KindPhone:
		if !phonePattern.MatchString(v) {
			return Identifier{}, ErrInvalidIdentifier.New()
		}
	case KindUsername:
		v = strings.ToLower(v)
		if strings.ContainsFunc(v, unicode.IsSpace) {
			return Identifier{}, ErrInvalidIdentifier.New()
		}
	}
	if v == "" || len([]rune(v)) > maxValue || strings.ContainsFunc(v, unicode.IsControl) {
		return Identifier{}, ErrInvalidIdentifier.New()
	}
	return Identifier{Kind: id.Kind, Value: v}, nil
}

func trimSpace(s string) string { return strings.TrimFunc(s, unicode.IsSpace) }

// Attached 是主体的一个登录标识。
type Attached struct {
	Identifier
	Tenant    string
	Verified  bool // 是否证明过持有，例如通过验证码
	CreatedAt time.Time
}

type identifierRow struct {
	bun.BaseModel `bun:"table:auth_identifiers,alias:i"`

	SubjectType string    `bun:"subject_type,pk"`
	Tenant      string    `bun:"tenant,pk"`
	Kind        string    `bun:"kind,pk"`
	Value       string    `bun:"value,pk"`
	SubjectID   string    `bun:"subject_id,notnull"`
	VerifiedAt  time.Time `bun:"verified_at,nullzero"`
	CreatedAt   time.Time `bun:"created_at,notnull"`
}

// owner 返回 tenant 中标识 id 所属主体的 ID；没有时返回空字符串。
func (r *Realm) owner(ctx context.Context, db bun.IDB, tenant string, id Identifier) (string, error) {
	var subjectID string
	err := db.NewSelect().Model((*identifierRow)(nil)).Column("subject_id").
		Where("subject_type = ? AND tenant = ? AND kind = ? AND value = ?", r.opts.Type, tenant, id.Kind, id.Value).
		Scan(ctx, &subjectID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return subjectID, errors.Wrap(err, "login: find identifier")
}

// attach 把标识 id 写给 subjectID；已属于其他主体时返回 ErrTaken，已属于 subjectID 时在 verified 时补上验证时间。
func (r *Realm) attach(ctx context.Context, db bun.IDB, tenant string, id Identifier, subjectID string, verified bool) error {
	now := r.now().UTC()
	row := &identifierRow{
		SubjectType: string(r.opts.Type), Tenant: tenant, Kind: id.Kind, Value: id.Value,
		SubjectID: subjectID, CreatedAt: now,
	}
	if verified {
		row.VerifiedAt = now
	}
	// 已存在时什么也不做，再读出标识的主体判断。
	if _, err := db.NewInsert().Model(row).On("CONFLICT (subject_type, tenant, kind, value) DO NOTHING").Exec(ctx); err != nil {
		return errors.Wrap(err, "login: attach identifier")
	}
	owner, err := r.owner(ctx, db, tenant, id)
	switch {
	case err != nil:
		return err
	case owner != subjectID:
		return ErrTaken.New()
	case verified:
		return r.markVerified(ctx, db, tenant, id)
	}
	return nil
}

// markVerified 为标识补上验证时间。
func (r *Realm) markVerified(ctx context.Context, db bun.IDB, tenant string, id Identifier) error {
	_, err := db.NewUpdate().Model((*identifierRow)(nil)).Set("verified_at = ?", r.now().UTC()).
		Where("subject_type = ? AND tenant = ? AND kind = ? AND value = ?", r.opts.Type, tenant, id.Kind, id.Value).
		Where("verified_at IS NULL").Exec(ctx)
	return errors.Wrap(err, "login: verify identifier")
}

// Attach 把登录标识 id 直接写给 ref，不经过验证码，用于管理员创建账号、用户名注册等可信的场景；
// 自助注册应使用 SendCode 和 Register。verified 表示标识是否已证明持有。
// PerTenant 时标识写入 identity.TenantFromContext(ctx)。标识已属于其他主体时返回 ErrTaken。
//
// 未验证的标识同样占用唯一性，所以只应在可信的场景下写入未验证的邮箱或手机号。
func (r *Realm) Attach(ctx context.Context, db bun.IDB, ref identity.Ref, id Identifier, verified bool) error {
	if err := r.check(ref); err != nil {
		return err
	}
	id, err := id.Normalize()
	if err != nil {
		return err
	}
	return r.attach(ctx, db, r.tenant(ctx), id, ref.ID, verified)
}

// Lookup 返回 identity.TenantFromContext(ctx)（PerTenant 时）中标识 id 所属的主体，不存在时返回 ErrNotFound。
// 用于管理后台按邮箱查找账号等；不要用它在登录前判断标识是否注册，那会暴露哪些标识已注册。
func (r *Realm) Lookup(ctx context.Context, id Identifier) (identity.Ref, error) {
	id, err := id.Normalize()
	if err != nil {
		return identity.Ref{}, err
	}
	owner, err := r.owner(ctx, r.db, r.tenant(ctx), id)
	if err != nil {
		return identity.Ref{}, err
	}
	if owner == "" {
		return identity.Ref{}, ErrNotFound.New()
	}
	return identity.Ref{Type: r.opts.Type, ID: owner}, nil
}

// Identifiers 返回 ref 的全部登录标识，按添加时间排列。
func (r *Realm) Identifiers(ctx context.Context, ref identity.Ref) ([]Attached, error) {
	if err := r.check(ref); err != nil {
		return nil, err
	}
	var rows []identifierRow
	err := r.db.NewSelect().Model(&rows).Where("subject_type = ? AND subject_id = ?", ref.Type, ref.ID).
		OrderExpr("created_at, kind, value").Scan(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "login: list identifiers")
	}
	out := make([]Attached, len(rows))
	for i, row := range rows {
		out[i] = Attached{
			Identifier: Identifier{Kind: row.Kind, Value: row.Value},
			Tenant:     row.Tenant,
			Verified:   !row.VerifiedAt.IsZero(),
			CreatedAt:  row.CreatedAt,
		}
	}
	return out, nil
}

// Detach 移除 ref 的登录标识 id。标识不属于 ref 时返回 ErrNotFound；移除后 ref 将没有可用的登录方式时返回 ErrLastMethod，
// 例如只剩用户名而没有密码，见 RemovePassword。更换标识时与 Link 在同一个事务中调用，db 传入这个事务。
func (r *Realm) Detach(ctx context.Context, db bun.IDB, ref identity.Ref, id Identifier) error {
	if err := r.check(ref); err != nil {
		return err
	}
	id, err := id.Normalize()
	if err != nil {
		return err
	}
	ids, err := r.lockIdentifiers(ctx, db, ref.ID)
	if err != nil {
		return err
	}
	rest := slices.DeleteFunc(slices.Clone(ids), func(other Identifier) bool { return other == id })
	if len(rest) == len(ids) {
		return ErrNotFound.New()
	}
	hash, err := r.passwordHash(ctx, db, ref.ID)
	if err != nil {
		return err
	}
	if !r.canSignIn(rest, hash != "") {
		return ErrLastMethod.New()
	}
	_, err = db.NewDelete().Model((*identifierRow)(nil)).
		Where("subject_type = ? AND subject_id = ? AND kind = ? AND value = ?", ref.Type, ref.ID, id.Kind, id.Value).Exec(ctx)
	return errors.Wrap(err, "login: detach identifier")
}

// lockIdentifiers 返回主体的全部登录标识，并在事务中锁住这些行，
// 使并发的 Detach、RemovePassword 不会各自通过检查、合起来移除全部登录方式。
func (r *Realm) lockIdentifiers(ctx context.Context, db bun.IDB, subjectID string) ([]Identifier, error) {
	var rows []identifierRow
	err := db.NewSelect().Model(&rows).Column("kind", "value").
		Where("subject_type = ? AND subject_id = ?", r.opts.Type, subjectID).For("UPDATE").Scan(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "login: find identifiers")
	}
	ids := make([]Identifier, len(rows))
	for i, row := range rows {
		ids[i] = Identifier{Kind: row.Kind, Value: row.Value}
	}
	return ids, nil
}

// canSignIn 报告拥有登录标识 ids 的主体是否还能登录：第三方账号和 passkey 可以；
// 设置了 Options.Send 时，邮箱和手机号可以用验证码登录；有密码时，PasswordKinds 中的标识可以用密码登录。
// 只有用户名而没有密码的主体无法登录。
func (r *Realm) canSignIn(ids []Identifier, hasPassword bool) bool {
	for _, id := range ids {
		switch {
		case hasPassword && containsKind(r.opts.PasswordKinds, id.Kind):
			return true
		case id.Kind == KindEmail || id.Kind == KindPhone:
			if r.opts.Send != nil {
				return true
			}
		case id.Kind != KindUsername:
			return true
		}
	}
	return false
}

// Forget 删除 ref 的全部登录凭据：标识、密码、二次验证和未完成的验证码，用于注销账号。
// 应用在同一个事务中删除自己的账号行，并撤销 token。
func (r *Realm) Forget(ctx context.Context, db bun.IDB, ref identity.Ref) error {
	if err := r.check(ref); err != nil {
		return err
	}
	for _, model := range []any{(*identifierRow)(nil), (*passwordRow)(nil), (*challengeRow)(nil)} {
		_, err := db.NewDelete().Model(model).Where("subject_type = ? AND subject_id = ?", ref.Type, ref.ID).Exec(ctx)
		if err != nil {
			return errors.Wrap(err, "login: forget")
		}
	}
	return r.forgetTwoFactor(ctx, db, ref.ID)
}
