package apitoken

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/google/uuid"

	"github.com/ryan053857/mygoravel/crypt"
	"github.com/ryan053857/mygoravel/identity"
)

// Keys 是签名和校验 access token 的密钥，用 HMACKeys 或 Ed25519Keys 创建。
// 第一把签名，全部用于校验：轮换时新密钥在前，旧密钥留到用它签发的 access token 全部过期。
type Keys struct {
	alg    jose.SignatureAlgorithm
	signer jose.Signer
	verify map[string]any // kid → 校验密钥
	public []jose.JSONWebKey
}

// HMACKeys 用 HMAC-SHA256（HS256）签名，密钥由应用密钥派生，不需要另外配置。
// crypt.Config.PreviousKeys 派生的密钥仍可校验，轮换应用密钥后已签发的 access token 继续有效。
// 只有持有应用密钥的服务能校验 token；其他服务也要校验时用 Ed25519Keys。
func HMACKeys(c *crypt.Crypt) Keys {
	k := Keys{alg: jose.HS256, verify: map[string]any{}, public: []jose.JSONWebKey{}}
	for i, key := range c.Derive("mygoravel apitoken jwt v1") {
		sum := sha256.Sum256(key)
		kid := base64.RawURLEncoding.EncodeToString(sum[:12])
		k.verify[kid] = key
		if i == 0 {
			k.signer = newSigner(jose.HS256, key, kid)
		}
	}
	return k
}

// Ed25519Keys 用 key 签名（EdDSA），previous 是轮换前的公钥，仍可校验。
// 公钥由 JWT.JWKS 公开，其他服务取得后可以自行校验 access token，不需要访问数据库和应用密钥。
// 用 ed25519.GenerateKey 生成密钥，私钥按应用的方式保管（例如配置文件中的 base64）。key 无效时 panic。
func Ed25519Keys(key ed25519.PrivateKey, previous ...ed25519.PublicKey) Keys {
	if len(key) != ed25519.PrivateKeySize {
		panic("apitoken: invalid Ed25519 private key")
	}
	k := Keys{alg: jose.EdDSA, verify: map[string]any{}, public: []jose.JSONWebKey{}}
	for i, pub := range append([]ed25519.PublicKey{key.Public().(ed25519.PublicKey)}, previous...) {
		if len(pub) != ed25519.PublicKeySize {
			panic("apitoken: invalid Ed25519 public key")
		}
		jwk := jose.JSONWebKey{Key: pub, Algorithm: string(jose.EdDSA), Use: "sig"}
		thumb, err := jwk.Thumbprint(crypto.SHA256)
		if err != nil {
			panic(errors.Wrap(err, "apitoken: key thumbprint"))
		}
		jwk.KeyID = base64.RawURLEncoding.EncodeToString(thumb)
		k.verify[jwk.KeyID] = pub
		k.public = append(k.public, jwk)
		if i == 0 {
			k.signer = newSigner(jose.EdDSA, key, jwk.KeyID)
		}
	}
	return k
}

func newSigner(alg jose.SignatureAlgorithm, key any, kid string) jose.Signer {
	opts := (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", kid)
	s, err := jose.NewSigner(jose.SigningKey{Algorithm: alg, Key: key}, opts)
	if err != nil {
		panic(errors.Wrap(err, "apitoken: signer"))
	}
	return s
}

// JWTConfig 是 JWT 模式的配置。
type JWTConfig struct {
	// Keys 签名和校验 access token，必填。
	Keys Keys
	// TTL 是 access token 的有效期，默认 10 分钟。撤销登录后，已签发的 access token 最多还能使用这么久。
	TTL time.Duration
	// Grace 是 refresh token 轮换后，上一个 token 仍可使用的时间，默认 30 秒，用于容忍客户端并发刷新：
	// 宽限期内用上一个 token 刷新只得到新的 access token（Pair.Refresh 为空）。
	// 超过宽限期再使用上一个 token 视为 token 被盗，整个登录被撤销。
	Grace time.Duration
	// Issuer 不为空时写入 iss，校验时要求一致。
	Issuer string
	// Audience 不为空时写入 aud，校验时要求包含它。
	Audience string
	// OnReuse 在识别出 refresh token 被重复使用、撤销这次登录之后调用，例如记录日志、提醒用户或撤销主体的全部凭据。可以为 nil。
	OnReuse func(ctx context.Context, t Token)
}

// JWT 以 access + refresh 的方式签发登录凭据：请求带短期的 access token（JWT），校验只验签名和有效期，不访问数据库；
// access token 过期后用 refresh token 换一对新的，refresh token 每次刷新都轮换。
//
// 每次登录仍是 api_tokens 中的一行（Kind 为 KindRefresh），Store 的 List、Revoke、RevokeOthers、RevokeSubject、Cleanup 照常使用。
// 撤销这一行后 refresh token 立即失效，已签发的 access token 在 TTL 内仍然可以发起请求；需要撤销立即生效的入口用 Store.Validate 的不透明 token。
// SSE、实时连接等长连接复查凭据时会查询这一行，撤销后连接照常结束，见 Validate。
// 可以并发使用。
type JWT struct {
	store *Store
	cfg   JWTConfig
}

// Pair 是一对登录凭据。
type Pair struct {
	// Access 是放在 Authorization: Bearer 中的 access token。
	Access string
	// Refresh 用于换取新的一对凭据；为空表示沿用客户端已有的 refresh token（宽限期内的重复刷新）。
	Refresh string
	// ExpiresAt 是 Access 的过期时间。
	ExpiresAt time.Time
}

// claims 是 access token 的内容，只包含签发时确定、之后不变的信息；权限由 Resolver 按请求加载。
type claims struct {
	jwt.Claims
	Session string `json:"sid"`
	Tenant  string `json:"tid,omitempty"`
	Actor   *actor `json:"act,omitempty"`
}

// actor 是代为登录时的实际操作者，见 RFC 8693 的 act。
type actor struct {
	Subject string `json:"sub"`
}

// NewJWT 用 store 保存登录，按 cfg 签发 access token。cfg.Keys 未设置，或 store 的空闲时长不大于 TTL 时 panic：
// 空闲时长按刷新计算，必须长于 access token 的有效期。
func NewJWT(store *Store, cfg JWTConfig) *JWT {
	if store == nil {
		panic("apitoken: nil store")
	}
	if cfg.Keys.signer == nil {
		panic("apitoken: JWTConfig.Keys is required")
	}
	if cfg.TTL == 0 {
		cfg.TTL = 10 * time.Minute
	}
	if cfg.Grace == 0 {
		cfg.Grace = 30 * time.Second
	}
	if cfg.TTL < 0 || cfg.Grace < 0 {
		panic("apitoken: JWTConfig.TTL and Grace must not be negative")
	}
	for _, idle := range []time.Duration{store.cfg.IdleTimeout, store.cfg.RememberIdleTimeout} {
		if idle > 0 && idle <= cfg.TTL {
			panic("apitoken: idle timeouts must be longer than JWTConfig.TTL")
		}
	}
	return &JWT{store: store, cfg: cfg}
}

// Login 为 sub 签发一对凭据，有效期规则与 Store.Login 相同：refresh token 按签发时的 Config 滑动过期，
// 每次刷新时延长。sub.Tenant、sub.Impersonator 写入 access token，含义与 Store.Login 相同。
func (j *JWT) Login(ctx context.Context, sub identity.Subject, d Device) (Pair, error) {
	refresh, r, err := j.store.login(ctx, sub, d, KindRefresh)
	if err != nil {
		return Pair{}, err
	}
	p, err := j.sign(r, r.CreatedAt)
	p.Refresh = refresh
	return p, err
}

// Refresh 用 refresh token 换一对新的凭据，并延长登录的有效期。
// refresh token 不存在、已撤销或已过期时返回 ErrInvalid；超过宽限期仍在使用的上一个 token 同样返回 ErrInvalid，
// 并撤销这次登录、调用 OnReuse。
func (j *JWT) Refresh(ctx context.Context, refresh string) (Pair, error) {
	if refresh == "" {
		return Pair{}, ErrInvalid.New()
	}
	s := j.store
	now := time.Now().UTC()
	h := hash(refresh)
	r, err := j.find(ctx, h, now)
	if err != nil {
		return Pair{}, err
	}
	if r.TokenHash != h {
		return j.reuse(ctx, r, now)
	}

	next := secret()
	q := s.db.NewUpdate().Model((*row)(nil)).
		Set("token_hash = ?", hash(next)).Set("previous_hash = ?", h).
		Set("rotated_at = ?", now).Set("last_used_at = ?", now).
		Where("id = ?", r.ID).Where("token_hash = ?", h)
	if r.IdleTimeout > 0 {
		r.ExpiresAt = deadline(r.EndsAt, time.Duration(r.IdleTimeout)*time.Second, now)
		q = q.Set("expires_at = ?", r.ExpiresAt)
	}
	res, err := q.Exec(ctx)
	if err != nil {
		return Pair{}, errors.Wrap(err, "apitoken: rotate")
	}
	if n, err := res.RowsAffected(); err != nil {
		return Pair{}, errors.Wrap(err, "apitoken: rotate")
	} else if n == 0 {
		// 并发的另一次刷新先完成了轮换，这个 token 已经成为上一个。
		if r, err = j.find(ctx, h, now); err != nil {
			return Pair{}, err
		}
		return j.reuse(ctx, r, now)
	}
	p, err := j.sign(r, now)
	p.Refresh = next
	return p, err
}

// find 返回当前或上一个 token 为 h 的未过期登录。
func (j *JWT) find(ctx context.Context, h string, now time.Time) (*row, error) {
	var r row
	err := j.store.live(j.store.db.NewSelect().Model(&r), now).Where("kind = ?", KindRefresh).
		Where("(token_hash = ? OR previous_hash = ?)", h, h).Limit(1).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalid.New()
	}
	if err != nil {
		return nil, errors.Wrap(err, "apitoken: refresh")
	}
	return &r, nil
}

// reuse 处理用上一个 refresh token 刷新：宽限期内只签发 access token，之后撤销这次登录。
func (j *JWT) reuse(ctx context.Context, r *row, now time.Time) (Pair, error) {
	if now.Sub(r.RotatedAt) <= j.cfg.Grace {
		return j.sign(r, now)
	}
	if _, err := j.store.db.NewDelete().Model((*row)(nil)).Where("id = ?", r.ID).Exec(ctx); err != nil {
		return Pair{}, errors.Wrap(err, "apitoken: revoke reused")
	}
	if j.cfg.OnReuse != nil {
		j.cfg.OnReuse(ctx, r.token())
	}
	return Pair{}, ErrInvalid.New()
}

// sign 为登录 r 签发 access token，过期时间不晚于登录本身。
func (j *JWT) sign(r *row, now time.Time) (Pair, error) {
	exp := now.Add(j.cfg.TTL)
	if !r.ExpiresAt.IsZero() && r.ExpiresAt.Before(exp) {
		exp = r.ExpiresAt
	}
	c := claims{
		Claims: jwt.Claims{
			Issuer:   j.cfg.Issuer,
			Subject:  identity.Ref{Type: identity.SubjectType(r.SubjectType), ID: r.SubjectID}.String(),
			IssuedAt: jwt.NewNumericDate(now),
			Expiry:   jwt.NewNumericDate(exp),
		},
		Session: r.ID.String(),
		Tenant:  r.Tenant,
	}
	if j.cfg.Audience != "" {
		c.Audience = jwt.Audience{j.cfg.Audience}
	}
	if imp := r.impersonator(); !imp.IsZero() {
		c.Actor = &actor{Subject: imp.String()}
	}
	access, err := jwt.Signed(j.cfg.Keys.signer).Claims(c).Serialize()
	if err != nil {
		return Pair{}, errors.Wrap(err, "apitoken: sign")
	}
	// NumericDate 精确到秒，返回与 token 中一致的时间。
	return Pair{Access: access, ExpiresAt: exp.Truncate(time.Second)}, nil
}

// leeway 是校验 access token 的 exp、iat 时允许的时钟偏差，容忍多个实例之间的时钟误差。
const leeway = 5 * time.Second

// Validate 实现 rest.TokenValidator：access token 只校验签名和有效期（允许 5 秒时钟偏差），不访问数据库。
//
// 长连接复查时（identity.Rechecking 报告为 true）改为确认登录仍然存在：按 sid 查一次 api_tokens，
// 登录被撤销或过期时返回 ErrInvalid，所以撤销后 SSE、实时连接在下一次复查时结束；
// access token 本身过期不影响复查，连接不会每个 TTL 断开一次。
//
// 返回的主体 Method 为 bearer，Credential 为登录的 ID（与 Store.List 的 Token.ID 相同，可以交给 Store.Revoke），
// Tenant、Impersonator 与签发时相同，Scopes、Permissions 为空。无效或过期时返回 ErrInvalid；
// 只是过期时返回的错误同时匹配 identity.ErrExpired，实时连接据此让客户端 SDK 换新的 access token 重连。
//
// 不是 JWT 的 token 交给 Store.Validate，所以同一个 API 也接受个人访问令牌和不透明的登录 token；refresh token 不能用作凭据。
func (j *JWT) Validate(ctx context.Context, token string) (identity.Subject, error) {
	if strings.Count(token, ".") != 2 {
		return j.store.Validate(ctx, token)
	}
	tok, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{j.cfg.Keys.alg})
	if err != nil || len(tok.Headers) != 1 {
		return identity.Subject{}, ErrInvalid.New()
	}
	key, ok := j.cfg.Keys.verify[tok.Headers[0].KeyID]
	if !ok {
		return identity.Subject{}, ErrInvalid.New()
	}
	var c claims
	if err := tok.Claims(key, &c); err != nil || c.Expiry == nil {
		return identity.Subject{}, ErrInvalid.New()
	}
	want := jwt.Expected{Issuer: j.cfg.Issuer}
	if j.cfg.Audience != "" {
		want.AnyAudience = jwt.Audience{j.cfg.Audience}
	}
	// 先校验有效期以外的各项，再单独检查有效期：只是过期的 token 由客户端刷新后重试。
	recheck := identity.Rechecking(ctx)
	checked := c.Claims
	checked.Expiry = nil
	if err := checked.ValidateWithLeeway(want, leeway); err != nil {
		return identity.Subject{}, ErrInvalid.New()
	}
	// 复查时由数据库中的登录决定是否有效，不看 access token 的有效期。
	if !recheck && c.ValidateWithLeeway(want, leeway) != nil {
		return identity.Subject{}, ErrInvalid.Wrap(identity.ErrExpired.New())
	}
	ref, err := identity.ParseRef(c.Subject)
	if err != nil || c.Session == "" {
		return identity.Subject{}, ErrInvalid.New()
	}
	sub := identity.Subject{
		Type:       ref.Type,
		ID:         ref.ID,
		Method:     "bearer",
		Credential: c.Session,
		Tenant:     c.Tenant,
	}
	if c.Actor != nil {
		if sub.Impersonator, err = identity.ParseRef(c.Actor.Subject); err != nil {
			return identity.Subject{}, ErrInvalid.New()
		}
	}
	if recheck {
		if err := j.alive(ctx, sub); err != nil {
			return identity.Subject{}, err
		}
	}
	return sub, nil
}

// alive 确认 access token 所属的登录没有被撤销或过期。
func (j *JWT) alive(ctx context.Context, sub identity.Subject) error {
	id, err := uuid.Parse(sub.Credential)
	if err != nil {
		return ErrInvalid.New()
	}
	ok, err := j.store.live(j.store.db.NewSelect().Model((*row)(nil)), time.Now().UTC()).
		Where("id = ?", id).Where("kind = ?", KindRefresh).
		Where("subject_type = ? AND subject_id = ?", sub.Type, sub.ID).Exists(ctx)
	if err != nil {
		return errors.Wrap(err, "apitoken: recheck")
	}
	if !ok {
		return ErrInvalid.New()
	}
	return nil
}

// JWKS 返回公开公钥（JSON Web Key Set）的 handler，通常挂在 /.well-known/jwks.json，
// 供其他服务校验 access token。HMACKeys 没有可以公开的密钥，返回空集合。
func (j *JWT) JWKS() http.Handler {
	body, err := json.Marshal(jose.JSONWebKeySet{Keys: j.cfg.Keys.public})
	if err != nil {
		panic(errors.Wrap(err, "apitoken: jwks"))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/jwk-set+json")
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = w.Write(body)
	})
}
