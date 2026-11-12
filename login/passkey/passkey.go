// Package passkey 用 passkey（WebAuthn）登录：手机、电脑的指纹、面容或设备密码，不需要密码，也不怕钓鱼。
//
// 一个 passkey 是 login.Realm 中的一个登录标识，Kind 为 "passkey"，Value 是 credential ID：
// 注册得到完成凭证，交给 Realm.Link（已登录的用户添加 passkey）或 Realm.Register（用 passkey 注册新账号）；
// 登录经过 Realm.External，所以租户隔离、Options.Resolve、“不能移除最后一种登录方式”都与其他标识相同，
// 也不再要求二次验证——passkey 本身就同时验证了设备和用户。本包只另外保存公钥，见 Migrations。
//
// 每个仪式分两步：Begin* 返回交给浏览器或 App 的选项和一个 token，Finish* 收到客户端的结果和这个 token。
// token 是用应用密钥加密的仪式状态，几分钟内有效，不需要服务器保存。
package passkey

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"slices"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/crypt"
	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/login"
)

// Kind 是 passkey 在 login.Realm 中的登录标识类型。应用的第三方登录提供方不要使用这个名称。
const Kind = "passkey"

var (
	// ErrInvalid 表示 passkey 仪式失败：token 无效或已过期、客户端的结果无法通过校验、passkey 不存在或没有绑定账号，
	// 或者疑似被克隆（签名计数倒退）。
	ErrInvalid = errorx.Define(errorx.Unauthenticated, "passkey.invalid")
	// ErrNotFound 表示要修改或移除的 passkey 不属于这个主体。
	ErrNotFound = errorx.Define(errorx.NotFound, "passkey.not_found")
)

//go:embed migrations
var migrationFiles embed.FS

// Migrations 返回 auth_passkeys 表的迁移，供应用发布；还需要 login.Migrations。
func Migrations() fs.FS {
	f, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		panic(err)
	}
	return f
}

// Config 是依赖方（Relying Party）的配置。
type Config struct {
	// RPID 是 passkey 绑定的域名，例如 example.com；passkey 可以在它和它的子域名上使用，之后不能更改。
	RPID string
	// RPName 是浏览器和系统在创建 passkey 时显示的名称，通常是应用的名称。
	RPName string
	// Origins 是允许发起仪式的来源，例如 https://example.com、https://admin.example.com；
	// Android App 是 android:apk-key-hash:<签名证书的哈希>。iOS App 使用关联域名，来源就是网站的地址。
	Origins []string
	// Timeout 是一个仪式的有效期，默认 5 分钟。
	Timeout time.Duration
}

// Store 为一套账号体系管理 passkey，可以并发使用。
type Store struct {
	db       bun.IDB
	realm    *login.Realm
	crypt    *crypt.Crypt
	wa       *webauthn.WebAuthn
	timeout  time.Duration
	userKeys [][]byte
	now      func() time.Time
}

type row struct {
	bun.BaseModel `bun:"table:auth_passkeys,alias:pk"`

	CredentialID string    `bun:"credential_id,pk"`
	SubjectType  string    `bun:"subject_type,notnull"`
	Credential   string    `bun:"credential,notnull"`
	Name         string    `bun:"name,notnull"`
	CreatedAt    time.Time `bun:"created_at,notnull"`
	LastUsedAt   time.Time `bun:"last_used_at,nullzero"`
}

// New 创建 realm 这套账号体系的 passkey 存储。db 必须已执行 login 和本包的迁移。
func New(db bun.IDB, realm *login.Realm, c *crypt.Crypt, cfg Config) (*Store, error) {
	if db == nil || realm == nil || c == nil {
		return nil, errors.New("passkey: db, realm and crypt are required")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Minute
	}
	wcfg := &webauthn.Config{
		RPID:          cfg.RPID,
		RPDisplayName: cfg.RPName,
		Timeouts: webauthn.TimeoutsConfig{
			Login:        webauthn.TimeoutConfig{Enforce: true, Timeout: cfg.Timeout, TimeoutUVD: cfg.Timeout},
			Registration: webauthn.TimeoutConfig{Enforce: true, Timeout: cfg.Timeout, TimeoutUVD: cfg.Timeout},
		},
	}
	for _, origin := range cfg.Origins {
		if protocol.IsOpaqueOrigin(origin) {
			wcfg.RPOpaqueOrigins = append(wcfg.RPOpaqueOrigins, origin)
		} else {
			wcfg.RPOrigins = append(wcfg.RPOrigins, origin)
		}
	}
	wa, err := webauthn.New(wcfg)
	if err != nil {
		return nil, errors.Wrap(err, "passkey: config")
	}
	return &Store{
		db: db, realm: realm, crypt: c, wa: wa, timeout: cfg.Timeout,
		userKeys: c.Derive("mygoravel passkey user v1"), now: time.Now,
	}, nil
}

// Ceremony 是 Begin* 的结果。
type Ceremony struct {
	// Options 原样交给客户端：浏览器中是 navigator.credentials.create 或 get 的参数，
	// 先用 PublicKeyCredential.parseCreationOptionsFromJSON 或 parseRequestOptionsFromJSON 转换其中的 publicKey。
	Options json.RawMessage `json:"options"`
	// Token 由客户端连同结果一起交回 Finish*。
	Token string `json:"token"`
}

// state 是加密在 token 中的仪式状态。
type state struct {
	Purpose string               `json:"p"`
	Session webauthn.SessionData `json:"s"`
	Tenant  string               `json:"t"`
	Expires time.Time            `json:"e"`
}

// user 是交给 go-webauthn 的账号。
type user struct {
	id          []byte
	name        string
	credentials []webauthn.Credential
}

func (u *user) WebAuthnID() []byte                         { return u.id }
func (u *user) WebAuthnName() string                       { return u.name }
func (u *user) WebAuthnDisplayName() string                { return u.name }
func (u *user) WebAuthnCredentials() []webauthn.Credential { return u.credentials }

// BeginRegistration 开始创建 passkey。ref 是已登录的主体（添加 passkey），零值表示用 passkey 注册新账号；
// name 是创建时系统显示的账号名，例如邮箱或用户名。已登录主体已有的 passkey 不会被重复创建在同一个设备上。
func (s *Store) BeginRegistration(ctx context.Context, ref identity.Ref, name string) (Ceremony, error) {
	u := &user{name: name}
	if ref.IsZero() {
		u.id = make([]byte, 32)
		rand.Read(u.id)
	} else {
		// 同一个账号的 passkey 使用同一个用户句柄，同一个设备上再次创建时替换而不是新增一条。
		m := hmac.New(sha256.New, s.userKeys[0])
		m.Write([]byte(ref.String()))
		u.id = m.Sum(nil)
		rows, err := s.rows(ctx, ref)
		if err != nil {
			return Ceremony{}, err
		}
		for _, r := range rows {
			if cred, err := r.credential(); err == nil {
				u.credentials = append(u.credentials, cred)
			}
		}
	}
	creation, session, err := s.wa.BeginRegistration(u,
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
		webauthn.WithExclusions(webauthn.Credentials(u.credentials).CredentialDescriptors()),
	)
	if err != nil {
		return Ceremony{}, errors.Wrap(err, "passkey: begin registration")
	}
	return s.ceremony(ctx, "register", creation, session)
}

// FinishRegistration 校验客户端创建 passkey 的结果（navigator.credentials.create 返回的 PublicKeyCredential 的 JSON），
// 保存公钥，返回完成凭证：已登录的用户交给 Realm.Link，新账号交给 Realm.Register。name 是给这个 passkey 起的名称，例如“iPhone”。
func (s *Store) FinishRegistration(ctx context.Context, token string, response []byte, name string) (string, error) {
	st, err := s.open(ctx, token, "register")
	if err != nil {
		return "", err
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(response)
	if err != nil {
		return "", ErrInvalid.Wrap(err)
	}
	cred, err := s.wa.CreateCredential(&user{id: st.Session.UserID}, st.Session, parsed)
	if err != nil {
		return "", ErrInvalid.Wrap(err)
	}
	id := base64.RawURLEncoding.EncodeToString(cred.ID)
	if len(id) > 191 {
		return "", ErrInvalid.New()
	}
	encoded, err := json.Marshal(cred)
	if err != nil {
		return "", errors.Wrap(err, "passkey: encode credential")
	}
	r := &row{
		CredentialID: id, SubjectType: string(s.realm.Type()), Credential: string(encoded),
		Name: truncate(strings.TrimSpace(name), 255), CreatedAt: s.now().UTC(),
	}
	if _, err := s.db.NewInsert().Model(r).Exec(ctx); err != nil {
		return "", errors.Wrap(err, "passkey: save")
	}
	out, err := s.realm.External(ctx, login.External{Provider: Kind, Subject: id})
	if err != nil {
		return "", err
	}
	if out.Proof == "" {
		return "", ErrInvalid.New()
	}
	return out.Proof, nil
}

// BeginLogin 开始用 passkey 登录。不需要先输入账号：浏览器列出这个网站的 passkey 供用户选择，
// 也可以在登录框中自动填充（conditional mediation）。
func (s *Store) BeginLogin(ctx context.Context) (Ceremony, error) {
	assertion, session, err := s.wa.BeginDiscoverableLogin()
	if err != nil {
		return Ceremony{}, errors.Wrap(err, "passkey: begin login")
	}
	return s.ceremony(ctx, "login", assertion, session)
}

// FinishLogin 校验客户端用 passkey 签名的结果（navigator.credentials.get 返回的 PublicKeyCredential 的 JSON），
// 返回登录成功的主体，交给 pages.Login 或 tokens.Login。passkey 不存在、没有绑定账号、属于其他租户，
// 或者签名计数倒退（疑似被克隆）时返回 ErrInvalid。
func (s *Store) FinishLogin(ctx context.Context, token string, response []byte) (login.Outcome, error) {
	st, err := s.open(ctx, token, "login")
	if err != nil {
		return login.Outcome{}, err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(response)
	if err != nil {
		return login.Outcome{}, ErrInvalid.Wrap(err)
	}
	var found *row
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		r, err := s.row(ctx, base64.RawURLEncoding.EncodeToString(rawID))
		if err != nil {
			return nil, err
		}
		cred, err := r.credential()
		if err != nil {
			return nil, err
		}
		found = r
		// 账号由 credential ID 对应的登录标识决定，用户句柄只需与客户端返回的一致。
		return &user{id: userHandle, credentials: []webauthn.Credential{cred}}, nil
	}
	cred, err := s.wa.ValidateDiscoverableLogin(handler, st.Session, parsed)
	if err != nil {
		return login.Outcome{}, ErrInvalid.Wrap(err)
	}
	if cred.Authenticator.CloneWarning {
		return login.Outcome{}, ErrInvalid.New()
	}
	encoded, err := json.Marshal(cred)
	if err != nil {
		return login.Outcome{}, errors.Wrap(err, "passkey: encode credential")
	}
	_, err = s.db.NewUpdate().Model((*row)(nil)).Set("credential = ?", string(encoded)).Set("last_used_at = ?", s.now().UTC()).
		Where("credential_id = ?", found.CredentialID).Exec(ctx)
	if err != nil {
		return login.Outcome{}, errors.Wrap(err, "passkey: update")
	}
	out, err := s.realm.External(ctx, login.External{Provider: Kind, Subject: found.CredentialID})
	if err != nil {
		return login.Outcome{}, err
	}
	if out.Proof != "" {
		return login.Outcome{}, ErrInvalid.New()
	}
	return out, nil
}

// Passkey 是主体的一个 passkey。
type Passkey struct {
	ID         string
	Name       string
	Synced     bool // 是否在设备之间同步（例如 iCloud 钥匙串、Google 密码管理器）
	CreatedAt  time.Time
	LastUsedAt time.Time // 零值表示还没有用来登录过
}

// List 返回 ref 的全部 passkey，按创建时间排列。
func (s *Store) List(ctx context.Context, ref identity.Ref) ([]Passkey, error) {
	rows, err := s.rows(ctx, ref)
	if err != nil {
		return nil, err
	}
	out := make([]Passkey, 0, len(rows))
	for _, r := range rows {
		p := Passkey{ID: r.CredentialID, Name: r.Name, CreatedAt: r.CreatedAt, LastUsedAt: r.LastUsedAt}
		if cred, err := r.credential(); err == nil {
			p.Synced = cred.Flags.BackupState
		}
		out = append(out, p)
	}
	return out, nil
}

// Rename 修改 ref 的 passkey id 的名称。不属于 ref 时返回 ErrNotFound。
func (s *Store) Rename(ctx context.Context, ref identity.Ref, id, name string) error {
	if err := s.owns(ctx, ref, id); err != nil {
		return err
	}
	_, err := s.db.NewUpdate().Model((*row)(nil)).Set("name = ?", truncate(strings.TrimSpace(name), 255)).
		Where("credential_id = ?", id).Exec(ctx)
	return errors.Wrap(err, "passkey: rename")
}

// Remove 移除 ref 的 passkey id，在 db 的事务中同时移除对应的登录标识。不属于 ref 时返回 ErrNotFound；
// 移除后 ref 将没有可用的登录方式时返回 login.ErrLastMethod。设备上保存的 passkey 由用户自己删除。
func (s *Store) Remove(ctx context.Context, db bun.IDB, ref identity.Ref, id string) error {
	if err := s.owns(ctx, ref, id); err != nil {
		return err
	}
	if err := s.realm.Detach(ctx, db, ref, login.Identifier{Kind: Kind, Value: id}); err != nil {
		return err
	}
	_, err := db.NewDelete().Model((*row)(nil)).Where("credential_id = ?", id).Exec(ctx)
	return errors.Wrap(err, "passkey: remove")
}

// Cleanup 删除最多 limit 个没有绑定到任何账号、创建超过一天的 passkey，返回删除的个数：
// 注册后没有完成 Link 或 Register 的，以及账号注销（Realm.Forget）之后留下的。由应用定时调用。
func (s *Store) Cleanup(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		return 0, errors.New("passkey: cleanup limit must be positive")
	}
	bound := s.db.NewSelect().Table("auth_identifiers").Column("value").
		Where("subject_type = ? AND kind = ?", s.realm.Type(), Kind)
	orphans := s.db.NewSelect().Model((*row)(nil)).Column("credential_id").
		Where("subject_type = ?", s.realm.Type()).Where("created_at < ?", s.now().UTC().Add(-24*time.Hour)).
		Where("credential_id NOT IN (?)", bound).OrderExpr("created_at").Limit(int64(limit))
	res, err := s.db.NewDelete().Model((*row)(nil)).
		Where("credential_id IN (?)", orphans).Exec(ctx)
	if err != nil {
		return 0, errors.Wrap(err, "passkey: cleanup")
	}
	n, err := res.RowsAffected()
	return int(n), errors.Wrap(err, "passkey: cleanup")
}

// rows 返回 ref 的 passkey：ref 的 passkey 登录标识对应的行。
func (s *Store) rows(ctx context.Context, ref identity.Ref) ([]row, error) {
	ids, err := s.ids(ctx, ref)
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	var rows []row
	err = s.db.NewSelect().Model(&rows).Where("credential_id IN (?)", bun.List(ids)).OrderExpr("created_at").Scan(ctx)
	return rows, errors.Wrap(err, "passkey: list")
}

func (s *Store) ids(ctx context.Context, ref identity.Ref) ([]string, error) {
	attached, err := s.realm.Identifiers(ctx, ref)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, a := range attached {
		if a.Kind == Kind {
			ids = append(ids, a.Value)
		}
	}
	return ids, nil
}

func (s *Store) owns(ctx context.Context, ref identity.Ref, id string) error {
	ids, err := s.ids(ctx, ref)
	if err != nil {
		return err
	}
	if !slices.Contains(ids, id) {
		return ErrNotFound.New()
	}
	return nil
}

func (s *Store) row(ctx context.Context, id string) (*row, error) {
	var r row
	err := s.db.NewSelect().Model(&r).Where("credential_id = ?", id).Where("subject_type = ?", s.realm.Type()).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalid.New()
	}
	return &r, errors.Wrap(err, "passkey: find")
}

func (r *row) credential() (webauthn.Credential, error) {
	var cred webauthn.Credential
	err := json.Unmarshal([]byte(r.Credential), &cred)
	return cred, errors.Wrap(err, "passkey: decode credential")
}

// ceremony 把仪式状态加密成 token，与给客户端的选项一起返回。
func (s *Store) ceremony(ctx context.Context, purpose string, options any, session *webauthn.SessionData) (Ceremony, error) {
	opts, err := json.Marshal(options)
	if err != nil {
		return Ceremony{}, errors.Wrap(err, "passkey: encode options")
	}
	st, err := json.Marshal(state{
		Purpose: purpose, Session: *session, Tenant: identity.TenantFromContext(ctx), Expires: s.now().Add(s.timeout),
	})
	if err != nil {
		return Ceremony{}, errors.Wrap(err, "passkey: encode state")
	}
	return Ceremony{Options: opts, Token: s.crypt.Encrypt(string(st))}, nil
}

// open 解密 token，确认它是 purpose 的仪式、没有过期，并且在发起时的租户中使用。
func (s *Store) open(ctx context.Context, token, purpose string) (state, error) {
	plain, err := s.crypt.Decrypt(token)
	if err != nil {
		return state{}, ErrInvalid.Wrap(err)
	}
	var st state
	if err := json.Unmarshal([]byte(plain), &st); err != nil {
		return state{}, ErrInvalid.Wrap(err)
	}
	if st.Purpose != purpose || !s.now().Before(st.Expires) || st.Tenant != identity.TenantFromContext(ctx) {
		return state{}, ErrInvalid.New()
	}
	return st, nil
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
