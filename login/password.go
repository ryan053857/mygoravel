package login

import (
	"context"
	"database/sql"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/hashing"
	"github.com/ryan053857/mygoravel/identity"
)

type passwordRow struct {
	bun.BaseModel `bun:"table:auth_passwords,alias:p"`

	SubjectType string    `bun:"subject_type,pk"`
	SubjectID   string    `bun:"subject_id,pk"`
	Hash        string    `bun:"hash,notnull"`
	ChangedAt   time.Time `bun:"changed_at,notnull"`
}

// Password 用登录标识和密码登录。标识的类型必须在 Options.PasswordKinds 中。
//
// 同一个标识每次尝试都消耗 Options.Attempts 的一次配额，成功后清零，所以只有连续的失败受到限制；
// 超出时返回 ratelimit.ErrExceeded。标识不存在、没有密码或密码不正确都返回 ErrInvalidCredentials，耗时相同。
// 成功后如果哈希参数已过时，按当前配置重新哈希。
func (r *Realm) Password(ctx context.Context, id Identifier, password string) (Outcome, error) {
	id, err := id.Normalize()
	if err != nil || !containsKind(r.opts.PasswordKinds, id.Kind) {
		return Outcome{}, ErrInvalidCredentials.New()
	}
	tenant := r.tenant(ctx)
	key := r.limitKey(tenant, id)
	if err := r.opts.Attempts.Allow(ctx, key); err != nil {
		return Outcome{}, err
	}
	owner, err := r.owner(ctx, r.db, tenant, id)
	if err != nil {
		return Outcome{}, err
	}
	hash := ""
	if owner != "" {
		if hash, err = r.passwordHash(ctx, r.db, owner); err != nil {
			return Outcome{}, err
		}
	}
	if hash == "" {
		r.opts.Hasher.Check(password, r.dummy())
		return Outcome{}, ErrInvalidCredentials.New()
	}
	if !r.opts.Hasher.Check(password, hash) {
		return Outcome{}, ErrInvalidCredentials.New()
	}
	if err := r.opts.Attempts.Reset(ctx, key); err != nil {
		return Outcome{}, err
	}
	if r.opts.Hasher.NeedsRehash(hash) {
		if err := r.storePassword(ctx, r.db, owner, password, false); err != nil {
			return Outcome{}, err
		}
	}
	return r.signedIn(ctx, owner, tenant)
}

// limitKey 是标识在限流器中的 key。
func (r *Realm) limitKey(tenant string, id Identifier) string {
	return string(r.opts.Type) + "|" + tenant + "|" + id.String()
}

// passwordHash 返回主体的密码哈希；没有设置密码时返回空字符串。
func (r *Realm) passwordHash(ctx context.Context, db bun.IDB, subjectID string) (string, error) {
	var hash string
	err := db.NewSelect().Model((*passwordRow)(nil)).Column("hash").
		Where("subject_type = ? AND subject_id = ?", r.opts.Type, subjectID).Scan(ctx, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return hash, errors.Wrap(err, "login: find password")
}

// storePassword 哈希并保存主体的密码。policy 为 true 时先按 Options.PasswordPolicy 检查。
func (r *Realm) storePassword(ctx context.Context, db bun.IDB, subjectID, password string, policy bool) error {
	if policy {
		if err := r.opts.PasswordPolicy(password); err != nil {
			return err
		}
	}
	hash, err := r.opts.Hasher.Make(password)
	if errors.Is(err, hashing.ErrPasswordTooLong) {
		return ErrWeakPassword.Wrap(err)
	}
	if err != nil {
		return errors.Wrap(err, "login: hash password")
	}
	row := &passwordRow{SubjectType: string(r.opts.Type), SubjectID: subjectID, Hash: hash, ChangedAt: r.now().UTC()}
	_, err = upsert(db, db.NewInsert().Model(row), "subject_type, subject_id", "hash", "changed_at").Exec(ctx)
	return errors.Wrap(err, "login: save password")
}

// SetPassword 设置或修改 ref 的密码，按 Options.PasswordPolicy 检查，不符合时返回它的错误。
// 修改密码后，应用通常还要撤销 ref 的其他会话和 token。
func (r *Realm) SetPassword(ctx context.Context, db bun.IDB, ref identity.Ref, password string) error {
	if err := r.check(ref); err != nil {
		return err
	}
	return r.storePassword(ctx, db, ref.ID, password, true)
}

// RemovePassword 删除 ref 的密码，之后只能用验证码等其他方式登录。没有密码时什么也不做。
func (r *Realm) RemovePassword(ctx context.Context, db bun.IDB, ref identity.Ref) error {
	if err := r.check(ref); err != nil {
		return err
	}
	_, err := db.NewDelete().Model((*passwordRow)(nil)).Where("subject_type = ? AND subject_id = ?", ref.Type, ref.ID).Exec(ctx)
	return errors.Wrap(err, "login: remove password")
}

// HasPassword 报告 ref 是否设置了密码，例如决定页面显示“设置密码”还是“修改密码”。
func (r *Realm) HasPassword(ctx context.Context, ref identity.Ref) (bool, error) {
	if err := r.check(ref); err != nil {
		return false, err
	}
	hash, err := r.passwordHash(ctx, r.db, ref.ID)
	return hash != "", err
}

// Confirm 确认 ref 的密码，用于修改邮箱、删除账号等敏感操作之前再次验证身份。
// 密码不正确或没有设置密码时返回 ErrInvalidCredentials；失败次数与登录一样由 Options.Attempts 限制，成功后清零。
func (r *Realm) Confirm(ctx context.Context, ref identity.Ref, password string) error {
	if err := r.check(ref); err != nil {
		return err
	}
	key := string(r.opts.Type) + "|confirm|" + ref.ID
	if err := r.opts.Attempts.Allow(ctx, key); err != nil {
		return err
	}
	hash, err := r.passwordHash(ctx, r.db, ref.ID)
	if err != nil {
		return err
	}
	if hash == "" || !r.opts.Hasher.Check(password, hash) {
		return ErrInvalidCredentials.New()
	}
	return r.opts.Attempts.Reset(ctx, key)
}
