package login

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"math/big"
	"slices"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/identity"
)

// Purpose 是验证码的用途。
type Purpose string

const (
	// SignIn 用验证码登录：标识已注册时验证后直接登录；未注册且 Options.Register 为 true 时，
	// 验证后得到注册用的完成凭证，交给 Register。
	SignIn Purpose = "sign_in"
	// Reset 找回或首次设置密码：只向已注册的标识发送，验证后得到完成凭证，交给 ResetPassword。
	Reset Purpose = "reset"
	// Verify 证明持有一个标识，验证后得到完成凭证，交给 Link 绑定到已登录的主体，或交给 Register。
	Verify Purpose = "verify"
	// Confirm 确认身份：由 SendConfirmCode 发给已登录主体自己的标识，交给 ConfirmCode。
	Confirm Purpose = "confirm"
)

// Code 是要发送给用户的验证码，由 Options.Send 发送。
type Code struct {
	Purpose Purpose
	// To 是接收验证码的标识，已规范化。
	To     Identifier
	Tenant string
	// Registered 表示发送时标识已注册，例如 SignIn 时据此决定内容写“登录”还是“注册”。
	Registered bool
	// RequestID 标识这次验证，与 Code 一起交给 VerifyCode；放进链接时，链接中同时带上两者。
	RequestID string
	Code      string
	ExpiresAt time.Time
}

type challengeRow struct {
	bun.BaseModel `bun:"table:auth_challenges,alias:c"`

	ID          uuid.UUID `bun:"id,pk,type:uuid"`
	Purpose     Purpose   `bun:"purpose,notnull"`
	SubjectType string    `bun:"subject_type,notnull"`
	Tenant      string    `bun:"tenant,notnull"`
	Kind        string    `bun:"kind,notnull"`
	Value       string    `bun:"value,notnull"`
	SubjectID   string    `bun:"subject_id,notnull"`
	CodeHash    string    `bun:"code_hash,notnull"`
	Attempts    int       `bun:"attempts,notnull"`
	ProofHash   string    `bun:"proof_hash,nullzero"`
	Data        string    `bun:"data,nullzero"`
	CompletedAt time.Time `bun:"completed_at,nullzero"`
	ExpiresAt   time.Time `bun:"expires_at,notnull"`
	CreatedAt   time.Time `bun:"created_at,notnull"`
}

func (c *challengeRow) identifier() Identifier { return Identifier{Kind: c.Kind, Value: c.Value} }

// SendCode 生成 purpose 用途的验证码，调用 Options.Send 发送给标识 id，返回请求 ID。
//
// 为了不暴露哪些标识已注册，不发送时也返回一个请求 ID，用它验证必然失败：
// Reset 不发送给未注册的标识；SignIn 在 Options.Register 为 false 时不发送给未注册的标识。
// 同一个标识的发送次数由 Options.Sends 限制，超出时返回 ratelimit.ErrExceeded。
func (r *Realm) SendCode(ctx context.Context, purpose Purpose, id Identifier) (string, error) {
	if r.opts.Send == nil {
		return "", errors.New("login: Options.Send is required to send codes")
	}
	if !slices.Contains([]Purpose{SignIn, Reset, Verify}, purpose) {
		return "", errors.Newf("login: SendCode does not send purpose %q", purpose)
	}
	id, tenant, owner, err := r.sending(ctx, id)
	if err != nil {
		return "", err
	}
	switch {
	case purpose == Reset && owner == "", purpose == SignIn && owner == "" && !r.opts.Register:
		requestID, err := uuid.NewV7()
		return requestID.String(), errors.Wrap(err, "login: new id")
	}
	return r.send(ctx, purpose, id, tenant, owner)
}

// SendConfirmCode 向 ref 自己的标识 id 发送 Confirm 用途的验证码，返回请求 ID，交给 ConfirmCode。
// 没有密码的账号（只用验证码、第三方账号或 passkey 登录）用它在敏感操作之前确认身份，见 Confirm。
// id 不属于 ref 时返回 ErrNotFound；发送次数与 SendCode 一样由 Options.Sends 限制。
func (r *Realm) SendConfirmCode(ctx context.Context, ref identity.Ref, id Identifier) (string, error) {
	if err := r.check(ref); err != nil {
		return "", err
	}
	if r.opts.Send == nil {
		return "", errors.New("login: Options.Send is required to send codes")
	}
	id, tenant, owner, err := r.sending(ctx, id)
	if err != nil {
		return "", err
	}
	if owner != ref.ID {
		return "", ErrNotFound.New()
	}
	return r.send(ctx, Confirm, id, tenant, owner)
}

// ConfirmCode 校验 SendConfirmCode 发给 ref 的验证码，确认身份，用法与 Confirm 相同，接收验证码的标识同时标为已验证。
// 验证码不正确、已过期、已用过、尝试次数已用完、不是发给 ref 的，或者标识已经不属于 ref 时返回 ErrInvalidCode。
func (r *Realm) ConfirmCode(ctx context.Context, ref identity.Ref, requestID, code string) error {
	if err := r.check(ref); err != nil {
		return err
	}
	c, err := r.attempt(ctx, requestID, code, func(c *challengeRow) bool {
		return c.Purpose == Confirm && c.SubjectID == ref.ID
	})
	if err != nil {
		return err
	}
	return r.use(ctx, c)
}

// sending 规范化标识、检查发送次数，返回标识、当前租户和标识的主人（未注册时为空）。
func (r *Realm) sending(ctx context.Context, id Identifier) (Identifier, string, string, error) {
	id, err := id.Normalize()
	if err != nil {
		return id, "", "", err
	}
	tenant := r.tenant(ctx)
	if err := r.opts.Sends.Allow(ctx, r.limitKey(tenant, id)); err != nil {
		return id, "", "", err
	}
	owner, err := r.owner(ctx, r.db, tenant, id)
	return id, tenant, owner, err
}

// send 保存并发送一个验证码，返回请求 ID。
func (r *Realm) send(ctx context.Context, purpose Purpose, id Identifier, tenant, owner string) (string, error) {
	requestID, err := uuid.NewV7()
	if err != nil {
		return "", errors.Wrap(err, "login: new id")
	}
	now := r.now().UTC()
	code := digits(r.opts.CodeLength)
	row := &challengeRow{
		ID: requestID, Purpose: purpose, SubjectType: string(r.opts.Type), Tenant: tenant,
		Kind: id.Kind, Value: id.Value, SubjectID: owner,
		CodeHash: r.codeHash(r.codeKeys[0], requestID, code), ExpiresAt: now.Add(r.opts.CodeTTL), CreatedAt: now,
	}
	if _, err := r.db.NewInsert().Model(row).Exec(ctx); err != nil {
		return "", errors.Wrap(err, "login: save code")
	}
	err = r.opts.Send(ctx, Code{
		Purpose: purpose, To: id, Tenant: tenant, Registered: owner != "",
		RequestID: requestID.String(), Code: code, ExpiresAt: row.ExpiresAt,
	})
	if err != nil {
		_, _ = r.db.NewDelete().Model(row).WherePK().Exec(context.WithoutCancel(ctx))
		return "", err
	}
	return requestID.String(), nil
}

// VerifyCode 校验 SendCode 发出的验证码。不正确、已过期、已用过、尝试次数已用完，
// 或者不是在发送时的租户中验证时返回 ErrInvalidCode。
//
// SignIn 且标识已注册时直接登录，返回 Outcome.Subject（启用了二次验证时为 Outcome.Pending），并把标识标为已验证；
// 其余情况返回 Outcome.Proof，交给 Register、ResetPassword 或 Link 完成。
func (r *Realm) VerifyCode(ctx context.Context, requestID, code string) (Outcome, error) {
	c, err := r.attempt(ctx, requestID, code, func(c *challengeRow) bool { return c.Purpose != Confirm })
	if err != nil {
		return Outcome{}, err
	}
	if c.Purpose == SignIn && c.SubjectID != "" {
		if err := r.use(ctx, c); err != nil {
			return Outcome{}, err
		}
		return r.authenticated(ctx, c.SubjectID, c.Tenant)
	}
	secret := token()
	n, err := affected(r.db.NewUpdate().Model((*challengeRow)(nil)).
		Set("proof_hash = ?", sha(secret)).Set("expires_at = ?", r.now().UTC().Add(r.opts.ProofTTL)).
		Where("id = ?", c.ID).Where("proof_hash IS NULL AND completed_at IS NULL").Exec(ctx))
	if err != nil {
		return Outcome{}, errors.Wrap(err, "login: issue proof")
	}
	if n == 0 {
		return Outcome{}, ErrInvalidCode.New()
	}
	return Outcome{Proof: c.ID.String() + "." + secret}, nil
}

// attempt 校验一次验证码：记录不存在、已过期、已用过、不是在当前租户中验证、accept 不接受、
// 尝试次数已用完或验证码不正确时返回 ErrInvalidCode。
func (r *Realm) attempt(ctx context.Context, requestID, code string, accept func(*challengeRow) bool) (*challengeRow, error) {
	rid, err := uuid.Parse(requestID)
	if err != nil {
		return nil, ErrInvalidCode.New()
	}
	var c challengeRow
	err = r.db.NewSelect().Model(&c).Where("id = ?", rid).Where("subject_type = ?", r.opts.Type).
		Where("proof_hash IS NULL AND completed_at IS NULL").Where("expires_at > ?", r.now().UTC()).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (c.Tenant != r.tenant(ctx) || !accept(&c)) {
		return nil, ErrInvalidCode.New()
	}
	if err != nil {
		return nil, errors.Wrap(err, "login: find code")
	}
	// 先记一次尝试再比较，并发的猜测也不会超过次数限制。
	n, err := affected(r.db.NewUpdate().Model((*challengeRow)(nil)).Set("attempts = attempts + 1").
		Where("id = ?", rid).Where("attempts < ?", r.opts.CodeAttempts).
		Where("proof_hash IS NULL AND completed_at IS NULL").Exec(ctx))
	if err != nil {
		return nil, errors.Wrap(err, "login: count attempt")
	}
	if n == 0 || !r.codeMatches(rid, code, c.CodeHash) {
		return nil, ErrInvalidCode.New()
	}
	return &c, nil
}

// use 用掉发给已注册标识的验证码（登录或确认身份），并把标识标为已验证。
// 并发使用、或者发送之后标识已被移除或转给了别人时返回 ErrInvalidCode。
func (r *Realm) use(ctx context.Context, c *challengeRow) error {
	return r.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		n, err := affected(tx.NewUpdate().Model((*challengeRow)(nil)).Set("completed_at = ?", r.now().UTC()).
			Where("id = ?", c.ID).Where("proof_hash IS NULL AND completed_at IS NULL").Exec(ctx))
		if err != nil {
			return errors.Wrap(err, "login: complete code")
		}
		owner, err := r.owner(ctx, tx, c.Tenant, c.identifier())
		switch {
		case err != nil:
			return err
		case n == 0 || owner != c.SubjectID:
			return ErrInvalidCode.New()
		}
		return r.markVerified(ctx, tx, c.Tenant, c.identifier())
	})
}

// proof 返回完成凭证对应的验证记录。凭证无效、已过期、已用过、用途不在 purposes 中，
// 或者不是在验证码发送时的租户中使用时返回 ErrInvalidProof。
func (r *Realm) proof(ctx context.Context, db bun.IDB, proof string, purposes ...Purpose) (*challengeRow, error) {
	idPart, secret, _ := strings.Cut(proof, ".")
	rid, err := uuid.Parse(idPart)
	if err != nil || secret == "" {
		return nil, ErrInvalidProof.New()
	}
	var c challengeRow
	err = db.NewSelect().Model(&c).Where("id = ?", rid).Where("subject_type = ?", r.opts.Type).
		Where("proof_hash IS NOT NULL AND completed_at IS NULL").Where("expires_at > ?", r.now().UTC()).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalidProof.New()
	}
	if err != nil {
		return nil, errors.Wrap(err, "login: find proof")
	}
	if subtle.ConstantTimeCompare([]byte(sha(secret)), []byte(c.ProofHash)) != 1 ||
		!slices.Contains(purposes, c.Purpose) || c.Tenant != r.tenant(ctx) {
		return nil, ErrInvalidProof.New()
	}
	return &c, nil
}

// complete 把完成凭证标为已用。并发使用同一个凭证时只有一个成功，其余返回 ErrInvalidProof。
func (r *Realm) complete(ctx context.Context, db bun.IDB, c *challengeRow) error {
	n, err := affected(db.NewUpdate().Model((*challengeRow)(nil)).Set("completed_at = ?", r.now().UTC()).
		Where("id = ?", c.ID).Where("completed_at IS NULL").Exec(ctx))
	if err != nil {
		return errors.Wrap(err, "login: complete proof")
	}
	if n == 0 {
		return ErrInvalidProof.New()
	}
	return nil
}

// RegisterOption 是 Register 的选项。
type RegisterOption func(*registration)

type registration struct {
	password string
}

// WithPassword 在注册时同时设置密码，按 Options.PasswordPolicy 检查。
func WithPassword(password string) RegisterOption {
	return func(reg *registration) { reg.password = password }
}

// Register 用 SignIn、Verify 或 External 的完成凭证，把已验证的标识写给应用刚创建的主体 ref，返回可以签发凭据的主体。
// External 的凭证写入第三方账号，以及对方确认过的邮箱。
//
// 应用的注册 action 在自己的事务中先创建账号行，再调用 Register 并传入同一个事务 db；
// 任何一步失败都一起回滚，凭证仍可再次使用。标识（包括第三方确认过的邮箱）已属于其他主体时返回 ErrTaken，凭证不被消耗。
func (r *Realm) Register(ctx context.Context, db bun.IDB, proof string, ref identity.Ref, opts ...RegisterOption) (identity.Subject, error) {
	if err := r.check(ref); err != nil {
		return identity.Subject{}, err
	}
	var reg registration
	for _, opt := range opts {
		opt(&reg)
	}
	if reg.password != "" {
		if err := r.opts.PasswordPolicy(reg.password); err != nil {
			return identity.Subject{}, err
		}
	}
	c, err := r.proof(ctx, db, proof, SignIn, Verify, purposeExternal)
	if err != nil {
		return identity.Subject{}, err
	}
	ids, err := c.proven()
	if err != nil {
		return identity.Subject{}, err
	}
	for _, id := range ids {
		if owner, err := r.owner(ctx, db, c.Tenant, id); err != nil {
			return identity.Subject{}, err
		} else if owner != "" && owner != ref.ID {
			return identity.Subject{}, ErrTaken.New()
		}
	}
	if err := r.complete(ctx, db, c); err != nil {
		return identity.Subject{}, err
	}
	for _, id := range ids {
		if err := r.attach(ctx, db, c.Tenant, id, ref.ID, true); err != nil {
			return identity.Subject{}, err
		}
	}
	if reg.password != "" {
		if err := r.storePassword(ctx, db, ref.ID, reg.password, false); err != nil {
			return identity.Subject{}, err
		}
	}
	return identity.Subject{Type: ref.Type, ID: ref.ID, Tenant: c.Tenant}, nil
}

// ResetPassword 用 Reset 的完成凭证设置新密码，按 Options.PasswordPolicy 检查；接收验证码的标识同时标为已验证，
// 它的密码登录失败计数清零。返回密码所属的主体，应用通常接着撤销它的全部会话和 token。
// 它不登录：用户接着用新密码登录，启用了二次验证的账号仍然需要二次验证。
func (r *Realm) ResetPassword(ctx context.Context, proof, password string) (identity.Ref, error) {
	if err := r.opts.PasswordPolicy(password); err != nil {
		return identity.Ref{}, err
	}
	var c *challengeRow
	err := r.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var err error
		if c, err = r.proof(ctx, tx, proof, Reset); err != nil {
			return err
		}
		if err := r.complete(ctx, tx, c); err != nil {
			return err
		}
		// 发送之后标识可能已被移除或转给了别人。
		owner, err := r.owner(ctx, tx, c.Tenant, c.identifier())
		if err != nil {
			return err
		}
		if owner != c.SubjectID {
			return ErrInvalidProof.New()
		}
		if err := r.markVerified(ctx, tx, c.Tenant, c.identifier()); err != nil {
			return err
		}
		return r.storePassword(ctx, tx, c.SubjectID, password, false)
	})
	if err != nil {
		return identity.Ref{}, err
	}
	if err := r.opts.Attempts.Reset(ctx, r.limitKey(c.Tenant, c.identifier())); err != nil {
		return identity.Ref{}, err
	}
	return identity.Ref{Type: r.opts.Type, ID: c.SubjectID}, nil
}

// Link 用 Verify、SignIn 或 External 的完成凭证，把已验证的标识绑定到已登录的主体 ref，例如添加邮箱、更换手机号、
// 绑定第三方账号（只绑定第三方账号本身，不绑定它的邮箱）；
// 标识已属于 ref 时只把它标为已验证。标识属于其他主体时返回 ErrTaken，凭证不被消耗。
// 更换标识时，应用在同一个事务中再用 Detach 移除旧的。
func (r *Realm) Link(ctx context.Context, db bun.IDB, ref identity.Ref, proof string) error {
	if err := r.check(ref); err != nil {
		return err
	}
	c, err := r.proof(ctx, db, proof, Verify, SignIn, purposeExternal)
	if err != nil {
		return err
	}
	if owner, err := r.owner(ctx, db, c.Tenant, c.identifier()); err != nil {
		return err
	} else if owner != "" && owner != ref.ID {
		return ErrTaken.New()
	}
	if err := r.complete(ctx, db, c); err != nil {
		return err
	}
	return r.attach(ctx, db, c.Tenant, c.identifier(), ref.ID, true)
}

// Cleanup 删除最多 limit 条已过期或已完成的验证记录，返回删除的条数。由应用定时调用。
func (r *Realm) Cleanup(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		return 0, errors.New("login: cleanup limit must be positive")
	}
	// 外层派生表让带 LIMIT 的子查询在 MySQL 中也能用于 IN。
	done := r.db.NewSelect().Model((*challengeRow)(nil)).Column("id").
		Where("expires_at <= ? OR completed_at IS NOT NULL", r.now().UTC()).OrderExpr("expires_at").Limit(int64(limit))
	n, err := affected(r.db.NewDelete().Model((*challengeRow)(nil)).
		Where("id IN (SELECT id FROM (?) AS done)", done).Exec(ctx))
	return int(n), errors.Wrap(err, "login: cleanup")
}

// codeHash 是验证码的 HMAC-SHA256，绑定请求 ID，不同请求的相同验证码得到不同的值。
func (r *Realm) codeHash(key []byte, requestID uuid.UUID, code string) string {
	m := hmac.New(sha256.New, key)
	m.Write(requestID[:])
	m.Write([]byte(code))
	return hex.EncodeToString(m.Sum(nil))
}

// codeMatches 用当前和轮换前的应用密钥依次比较验证码。
func (r *Realm) codeMatches(requestID uuid.UUID, code, want string) bool {
	for _, key := range r.codeKeys {
		if subtle.ConstantTimeCompare([]byte(r.codeHash(key, requestID, code)), []byte(want)) == 1 {
			return true
		}
	}
	return false
}

// digits 返回 n 位随机数字。
func digits(n int) string {
	limit := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
	v, err := rand.Int(rand.Reader, limit)
	if err != nil {
		panic(errors.Wrap(err, "login: random"))
	}
	s := v.String()
	return strings.Repeat("0", n-len(s)) + s
}

// token 返回 32 字节随机数的 URL 安全 base64。
func token() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
