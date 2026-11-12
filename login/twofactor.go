package login

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/identity"
)

// purposeTwoFactor 是等待二次验证的登录在 auth_challenges 中的用途，见 Outcome.Pending。
const purposeTwoFactor Purpose = "two_factor"

const (
	totpPeriod = 30
	totpDigits = 6
	// pendingAttempts 是一次待完成的登录最多可以输入二次验证码的次数。
	pendingAttempts = 5
	recoveryCount   = 8
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

type twoFactorRow struct {
	bun.BaseModel `bun:"table:auth_two_factor,alias:t"`

	SubjectType string    `bun:"subject_type,pk"`
	SubjectID   string    `bun:"subject_id,pk"`
	Secret      string    `bun:"secret,notnull"`
	LastStep    int64     `bun:"last_step,notnull"`
	ConfirmedAt time.Time `bun:"confirmed_at,nullzero"`
	CreatedAt   time.Time `bun:"created_at,notnull"`
}

type recoveryRow struct {
	bun.BaseModel `bun:"table:auth_recovery_codes,alias:rc"`

	SubjectType string `bun:"subject_type,pk"`
	SubjectID   string `bun:"subject_id,pk"`
	CodeHash    string `bun:"code_hash,pk"`
}

// TOTPSetup 是开始设置 TOTP 时交给用户的信息。
type TOTPSetup struct {
	// Secret 是 base32 编码的密钥，供无法扫码时手动输入。
	Secret string
	// URI 是 otpauth:// 地址，渲染成二维码，用身份验证器 App 扫描。
	URI string
}

// SetupTOTP 为 ref 生成新的 TOTP 密钥，返回给用户扫码；用户输入验证器显示的验证码后用 ConfirmTOTP 启用。
// account 是验证器中显示的账号名，例如邮箱；发行者是 Options.Issuer。
// 已启用二次验证时返回 ErrTwoFactorEnabled，需要先 DisableTwoFactor；正在设置但尚未确认的会被替换。
func (r *Realm) SetupTOTP(ctx context.Context, db bun.IDB, ref identity.Ref, account string) (TOTPSetup, error) {
	if err := r.check(ref); err != nil {
		return TOTPSetup{}, err
	}
	row, err := r.twoFactor(ctx, db, ref.ID)
	if err != nil {
		return TOTPSetup{}, err
	}
	if row != nil && !row.ConfirmedAt.IsZero() {
		return TOTPSetup{}, ErrTwoFactorEnabled.New()
	}
	key := make([]byte, 20)
	rand.Read(key)
	secret := b32.EncodeToString(key)
	row = &twoFactorRow{
		SubjectType: string(r.opts.Type), SubjectID: ref.ID, Secret: r.opts.Crypt.Encrypt(secret), CreatedAt: r.now().UTC(),
	}
	q := upsert(db.NewInsert().Model(row), "subject_type, subject_id", "secret", "last_step", "created_at")
	if _, err := q.Exec(ctx); err != nil {
		return TOTPSetup{}, errors.Wrap(err, "login: save totp")
	}
	return TOTPSetup{Secret: secret, URI: r.otpauthURI(secret, account)}, nil
}

// otpauthURI 返回 Google Authenticator 等验证器认可的 otpauth://totp/ 地址。
func (r *Realm) otpauthURI(secret, account string) string {
	label := account
	q := url.Values{"secret": {secret}, "algorithm": {"SHA1"}, "digits": {fmt.Sprint(totpDigits)}, "period": {fmt.Sprint(totpPeriod)}}
	if issuer := r.opts.Issuer; issuer != "" {
		label = issuer + ":" + account
		q.Set("issuer", issuer)
	}
	return (&url.URL{Scheme: "otpauth", Host: "totp", Path: "/" + label, RawQuery: q.Encode()}).String()
}

// ConfirmTOTP 用验证器显示的验证码确认 SetupTOTP 的密钥，启用二次验证，返回新生成的恢复码。
// 恢复码只在这里和 RegenerateRecoveryCodes 返回明文，请提示用户妥善保存。
// 没有正在设置的密钥时返回 ErrTwoFactorDisabled，验证码不正确时返回 ErrInvalidCode。
func (r *Realm) ConfirmTOTP(ctx context.Context, db bun.IDB, ref identity.Ref, code string) ([]string, error) {
	if err := r.check(ref); err != nil {
		return nil, err
	}
	row, err := r.twoFactor(ctx, db, ref.ID)
	switch {
	case err != nil:
		return nil, err
	case row == nil:
		return nil, ErrTwoFactorDisabled.New()
	case !row.ConfirmedAt.IsZero():
		return nil, ErrTwoFactorEnabled.New()
	}
	ok, err := r.useTOTP(ctx, db, row, code)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrInvalidCode.New()
	}
	_, err = db.NewUpdate().Model((*twoFactorRow)(nil)).Set("confirmed_at = ?", r.now().UTC()).
		Where("subject_type = ? AND subject_id = ?", ref.Type, ref.ID).Exec(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "login: confirm totp")
	}
	return r.newRecoveryCodes(ctx, db, ref.ID)
}

// RegenerateRecoveryCodes 作废 ref 现有的恢复码并生成新的一组。未启用二次验证时返回 ErrTwoFactorDisabled。
func (r *Realm) RegenerateRecoveryCodes(ctx context.Context, db bun.IDB, ref identity.Ref) ([]string, error) {
	if err := r.check(ref); err != nil {
		return nil, err
	}
	if enabled, err := r.enabled(ctx, db, ref.ID); err != nil {
		return nil, err
	} else if !enabled {
		return nil, ErrTwoFactorDisabled.New()
	}
	return r.newRecoveryCodes(ctx, db, ref.ID)
}

// newRecoveryCodes 替换主体的恢复码，返回明文。
func (r *Realm) newRecoveryCodes(ctx context.Context, db bun.IDB, subjectID string) ([]string, error) {
	if _, err := db.NewDelete().Model((*recoveryRow)(nil)).
		Where("subject_type = ? AND subject_id = ?", r.opts.Type, subjectID).Exec(ctx); err != nil {
		return nil, errors.Wrap(err, "login: delete recovery codes")
	}
	codes := make([]string, recoveryCount)
	rows := make([]recoveryRow, recoveryCount)
	for i := range codes {
		b := make([]byte, 7)
		rand.Read(b)
		raw := strings.ToLower(b32.EncodeToString(b))[:10]
		codes[i] = raw[:5] + "-" + raw[5:]
		rows[i] = recoveryRow{SubjectType: string(r.opts.Type), SubjectID: subjectID, CodeHash: r.recoveryHash(r.recoveryKeys[0], subjectID, raw)}
	}
	if _, err := db.NewInsert().Model(&rows).Exec(ctx); err != nil {
		return nil, errors.Wrap(err, "login: save recovery codes")
	}
	return codes, nil
}

// RecoveryCodesLeft 返回 ref 还未使用的恢复码个数，用于提醒用户重新生成。
func (r *Realm) RecoveryCodesLeft(ctx context.Context, ref identity.Ref) (int, error) {
	if err := r.check(ref); err != nil {
		return 0, err
	}
	n, err := r.db.NewSelect().Model((*recoveryRow)(nil)).
		Where("subject_type = ? AND subject_id = ?", ref.Type, ref.ID).Count(ctx)
	return int(n), errors.Wrap(err, "login: count recovery codes")
}

// TwoFactorEnabled 报告 ref 是否启用了二次验证。
func (r *Realm) TwoFactorEnabled(ctx context.Context, ref identity.Ref) (bool, error) {
	if err := r.check(ref); err != nil {
		return false, err
	}
	return r.enabled(ctx, r.db, ref.ID)
}

// DisableTwoFactor 关闭 ref 的二次验证，删除密钥和恢复码。应用通常先用 Confirm 或 CheckTwoFactor 再次确认身份。
func (r *Realm) DisableTwoFactor(ctx context.Context, db bun.IDB, ref identity.Ref) error {
	if err := r.check(ref); err != nil {
		return err
	}
	return r.forgetTwoFactor(ctx, db, ref.ID)
}

func (r *Realm) forgetTwoFactor(ctx context.Context, db bun.IDB, subjectID string) error {
	for _, model := range []any{(*twoFactorRow)(nil), (*recoveryRow)(nil)} {
		_, err := db.NewDelete().Model(model).Where("subject_type = ? AND subject_id = ?", r.opts.Type, subjectID).Exec(ctx)
		if err != nil {
			return errors.Wrap(err, "login: disable two-factor")
		}
	}
	return nil
}

// CheckTwoFactor 校验 ref 的 TOTP 验证码或恢复码，恢复码用过即作废，同一个 TOTP 验证码也不能用两次。
// 用于敏感操作前再次确认身份。不正确时返回 ErrInvalidCode，未启用时返回 ErrTwoFactorDisabled；
// 失败次数由 Options.Attempts 限制，成功后清零。
func (r *Realm) CheckTwoFactor(ctx context.Context, ref identity.Ref, code string) error {
	if err := r.check(ref); err != nil {
		return err
	}
	return r.checkTwoFactor(ctx, ref.ID, code)
}

func (r *Realm) checkTwoFactor(ctx context.Context, subjectID, code string) error {
	key := string(r.opts.Type) + "|two_factor|" + subjectID
	if err := r.opts.Attempts.Allow(ctx, key); err != nil {
		return err
	}
	row, err := r.twoFactor(ctx, r.db, subjectID)
	if err != nil {
		return err
	}
	if row == nil || row.ConfirmedAt.IsZero() {
		return ErrTwoFactorDisabled.New()
	}
	ok, err := r.useTOTP(ctx, r.db, row, code)
	if err == nil && !ok {
		ok, err = r.useRecoveryCode(ctx, subjectID, code)
	}
	if err != nil {
		return err
	}
	if !ok {
		return ErrInvalidCode.New()
	}
	return r.opts.Attempts.Reset(ctx, key)
}

// TwoFactor 用 TOTP 验证码或恢复码完成等待二次验证的登录（Outcome.Pending），返回登录成功的主体。
// 一次待完成的登录最多可以输错 5 次，之后需要重新登录；pending 无效、已过期或已用过时返回 ErrInvalidProof。
func (r *Realm) TwoFactor(ctx context.Context, pending, code string) (Outcome, error) {
	c, err := r.proof(ctx, r.db, pending, purposeTwoFactor)
	if err != nil {
		return Outcome{}, err
	}
	n, err := affected(r.db.NewUpdate().Model((*challengeRow)(nil)).Set("attempts = attempts + 1").
		Where("id = ?", c.ID).Where("attempts < ?", pendingAttempts).Where("completed_at IS NULL").Exec(ctx))
	if err != nil {
		return Outcome{}, errors.Wrap(err, "login: count attempt")
	}
	if n == 0 {
		return Outcome{}, ErrInvalidProof.New()
	}
	if err := r.checkTwoFactor(ctx, c.SubjectID, code); err != nil {
		return Outcome{}, err
	}
	if err := r.complete(ctx, r.db, c); err != nil {
		return Outcome{}, err
	}
	return r.signedIn(ctx, c.SubjectID, c.Tenant)
}

// authenticated 在第一步验证（密码、验证码）通过后调用：启用了二次验证时返回 Outcome.Pending，否则登录成功。
func (r *Realm) authenticated(ctx context.Context, subjectID, tenant string) (Outcome, error) {
	enabled, err := r.enabled(ctx, r.db, subjectID)
	if err != nil || !enabled {
		if err != nil {
			return Outcome{}, err
		}
		return r.signedIn(ctx, subjectID, tenant)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Outcome{}, errors.Wrap(err, "login: new id")
	}
	secret := token()
	now := r.now().UTC()
	row := &challengeRow{
		ID: id, Purpose: purposeTwoFactor, SubjectType: string(r.opts.Type), Tenant: tenant, SubjectID: subjectID,
		ProofHash: sha(secret), ExpiresAt: now.Add(r.opts.ProofTTL), CreatedAt: now,
	}
	if _, err := r.db.NewInsert().Model(row).Exec(ctx); err != nil {
		return Outcome{}, errors.Wrap(err, "login: save pending login")
	}
	return Outcome{Pending: id.String() + "." + secret}, nil
}

func (r *Realm) twoFactor(ctx context.Context, db bun.IDB, subjectID string) (*twoFactorRow, error) {
	var row twoFactorRow
	err := db.NewSelect().Model(&row).Where("subject_type = ? AND subject_id = ?", r.opts.Type, subjectID).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.Wrap(err, "login: find two-factor")
	}
	return &row, nil
}

func (r *Realm) enabled(ctx context.Context, db bun.IDB, subjectID string) (bool, error) {
	row, err := r.twoFactor(ctx, db, subjectID)
	return row != nil && !row.ConfirmedAt.IsZero(), err
}

// useTOTP 校验 TOTP 验证码，允许前后各一个时间步的时钟误差；通过的时间步必须晚于上次使用的，
// 并用条件更新记下，并发提交同一个验证码只有一次通过。
func (r *Realm) useTOTP(ctx context.Context, db bun.IDB, row *twoFactorRow, code string) (bool, error) {
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return false, nil
	}
	secret, err := r.opts.Crypt.Decrypt(row.Secret)
	if err != nil {
		return false, errors.Wrap(err, "login: decrypt totp secret")
	}
	key, err := b32.DecodeString(secret)
	if err != nil {
		return false, errors.Wrap(err, "login: decode totp secret")
	}
	now := r.now().Unix() / totpPeriod
	for step := now - 1; step <= now+1; step++ {
		if step <= row.LastStep || subtle.ConstantTimeCompare([]byte(totp(key, step)), []byte(code)) != 1 {
			continue
		}
		n, err := affected(db.NewUpdate().Model((*twoFactorRow)(nil)).Set("last_step = ?", step).
			Where("subject_type = ? AND subject_id = ?", row.SubjectType, row.SubjectID).
			Where("last_step < ?", step).Exec(ctx))
		if err != nil {
			return false, errors.Wrap(err, "login: use totp")
		}
		return n > 0, nil
	}
	return false, nil
}

// totp 返回 RFC 6238 的验证码：HMAC-SHA1，6 位数字。
func totp(key []byte, step int64) string {
	m := hmac.New(sha1.New, key)
	_ = binary.Write(m, binary.BigEndian, step)
	sum := m.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, v%1_000_000)
}

// useRecoveryCode 校验并作废一个恢复码，输入中的空白和连字符被忽略，不区分大小写。
func (r *Realm) useRecoveryCode(ctx context.Context, subjectID, code string) (bool, error) {
	raw := strings.ToLower(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(code)))
	if len(raw) != 10 {
		return false, nil
	}
	for _, key := range r.recoveryKeys {
		n, err := affected(r.db.NewDelete().Model((*recoveryRow)(nil)).
			Where("subject_type = ? AND subject_id = ?", r.opts.Type, subjectID).
			Where("code_hash = ?", r.recoveryHash(key, subjectID, raw)).Exec(ctx))
		if err != nil {
			return false, errors.Wrap(err, "login: use recovery code")
		}
		if n > 0 {
			return true, nil
		}
	}
	return false, nil
}

// recoveryHash 是恢复码的 HMAC-SHA256，绑定主体。
func (r *Realm) recoveryHash(key []byte, subjectID, raw string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(subjectID))
	m.Write([]byte{0})
	m.Write([]byte(raw))
	return hex.EncodeToString(m.Sum(nil))
}
