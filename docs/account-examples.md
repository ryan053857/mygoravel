# 完整示例：编辑后台与读者 App

- [简介](#introduction)
- [两张账号表](#account-tables)
- [装配](#assembling)
- [编辑后台](#editor-api)
- [读者 App](#reader-api)
- [接入入口](#wiring)

<a id="introduction"></a>
## 简介

本章把[登录与注册](login.md)、[第三方登录](social-login.md)和 [API 令牌](api-tokens.md)串起来，给出两套账号体系从建表到接口的完整代码。博客上线读者 App 之后，账号分成两套：

| | 编辑后台 | 读者 App |
| --- | --- | --- |
| 账号体系 | `editor.Type`，`editors` 表 | `reader.Type`，`readers` 表 |
| 入口 | 单页应用调用 `/admin` 下的 JSON API，不透明的登录 token | JSON API，JWT access token + refresh token |
| 账号怎么来 | 管理员邀请，编辑通过邮件中的链接设置密码 | 手机验证码自助注册，或 Sign in with Apple |
| 怎么登录 | 邮箱 + 密码，**必须**启用 TOTP 二次验证 | 手机验证码，或 Apple |
| 敏感操作 | 邀请编辑时再输入一次自己的密码 | 注销账号前用验证码或重新登录 Apple 确认身份 |

两套账号的登录凭据都保存在框架的 `auth_*` 表中，`editors`、`readers` 表只有业务字段。下面的五个文件都是完整的代码。

<a id="account-tables"></a>
## 两张账号表

下面是完整的 `internal/editor/editor.go`。`Resolve` 在每个请求中重新读取编辑，停用后立即失效；`Invite` 创建账号、写入邮箱，在事务提交后发送设置密码的邮件：

```go
package editor

import (
    "context"
    "database/sql"
    "errors"
    "time"

    "github.com/google/uuid"
    "github.com/uptrace/bun"

    "github.com/ryan053857/mygoravel/database"
    "github.com/ryan053857/mygoravel/errorx"
    "github.com/ryan053857/mygoravel/identity"
    "github.com/ryan053857/mygoravel/login"
)

// Type 是编辑这套账号体系。
const Type identity.SubjectType = "editor"

var ErrDisabled = errorx.Define(errorx.Unauthenticated, "editor.disabled")

// Editor 是编辑账号。邮箱、密码和二次验证都在 login 的表中，这里只有业务字段。
type Editor struct {
    bun.BaseModel `bun:"table:editors"`

    ID        string    `bun:"id,pk"`
    Name      string    `bun:"name,notnull"`
    Admin     bool      `bun:"admin,notnull"` // 可以邀请其他编辑
    Disabled  bool      `bun:"disabled,notnull"`
    CreatedAt time.Time `bun:"created_at,notnull"`
}

func (e *Editor) Ref() identity.Ref { return identity.Ref{Type: Type, ID: e.ID} }

// Resolve 每个请求重新读取编辑：停用后立即失效，权限随 Admin 变化。
func Resolve(db bun.IDB) identity.Resolver {
    return func(ctx context.Context, sub identity.Subject) (identity.Subject, error) {
        var e Editor
        err := db.NewSelect().Model(&e).Where("id = ?", sub.ID).Scan(ctx)
        if errors.Is(err, sql.ErrNoRows) || err == nil && e.Disabled {
            return identity.Subject{}, ErrDisabled.New()
        }
        if err != nil {
            return identity.Subject{}, err
        }
        sub.Permissions = []string{"articles:*"}
        if e.Admin {
            sub.Permissions = append(sub.Permissions, "editors:invite")
        }
        return sub, nil
    }
}

// Invite 由管理员创建编辑账号，并向邮箱发送设置密码的邀请。
type Invite struct {
    db     *bun.DB
    logins *login.Realm
}

func NewInvite(db *bun.DB, logins *login.Realm) *Invite { return &Invite{db: db, logins: logins} }

func (a *Invite) Handle(ctx context.Context, name, email string) (*Editor, error) {
    e := &Editor{ID: uuid.Must(uuid.NewV7()).String(), Name: name, CreatedAt: time.Now().UTC()}
    err := database.RunInTx(ctx, a.db, nil, func(ctx context.Context, tx database.Tx) error {
        if _, err := tx.NewInsert().Model(e).Exec(ctx); err != nil {
            return err
        }
        if err := a.logins.Attach(ctx, tx, e.Ref(), login.Email(email), false); err != nil {
            return err // 邮箱已被其他编辑使用：login.ErrTaken
        }
        // 提交之后才发送邀请，事务回滚时不会发出邮件。
        tx.AfterCommit(func(ctx context.Context) error {
            _, err := a.logins.SendCode(ctx, login.Reset, login.Email(email))
            return err
        })
        return nil
    })
    return e, err
}
```

邀请就是对一个还没有密码的账号发起[找回密码](login.md#reset)：编辑设置密码后，邮箱随之标为已验证。

下面是完整的 `internal/reader/reader.go`。读者自助注册，`SignUp` 在事务中创建账号行，再用完成凭证挂上已验证的手机号或 Apple 账号；`Delete` 注销账号：

```go
package reader

import (
    "context"
    "database/sql"
    "errors"
    "time"

    "github.com/google/uuid"
    "github.com/uptrace/bun"

    "github.com/ryan053857/mygoravel/apitoken"
    "github.com/ryan053857/mygoravel/database"
    "github.com/ryan053857/mygoravel/errorx"
    "github.com/ryan053857/mygoravel/identity"
    "github.com/ryan053857/mygoravel/login"
)

// Type 是读者这套账号体系。
const Type identity.SubjectType = "reader"

var ErrDisabled = errorx.Define(errorx.Unauthenticated, "reader.disabled")

// Reader 是读者账号。手机号和 Apple 账号在 login 的表中。
type Reader struct {
    bun.BaseModel `bun:"table:readers"`

    ID        string    `bun:"id,pk"`
    Name      string    `bun:"name,notnull"`
    Disabled  bool      `bun:"disabled,notnull"`
    CreatedAt time.Time `bun:"created_at,notnull"`
}

func (r *Reader) Ref() identity.Ref { return identity.Ref{Type: Type, ID: r.ID} }

// Resolve 拒绝已停用或已注销的读者。读者没有角色，数据按“归属本人”授权。
func Resolve(db bun.IDB) identity.Resolver {
    return func(ctx context.Context, sub identity.Subject) (identity.Subject, error) {
        var disabled bool
        err := db.NewSelect().Model((*Reader)(nil)).Column("disabled").Where("id = ?", sub.ID).Scan(ctx, &disabled)
        if errors.Is(err, sql.ErrNoRows) || err == nil && disabled {
            return identity.Subject{}, ErrDisabled.New()
        }
        return sub, err
    }
}

// SignUp 用完成凭证注册读者。
type SignUp struct {
    db     *bun.DB
    logins *login.Realm
}

func NewSignUp(db *bun.DB, logins *login.Realm) *SignUp { return &SignUp{db: db, logins: logins} }

func (a *SignUp) Handle(ctx context.Context, proof, name string) (identity.Subject, error) {
    r := &Reader{ID: uuid.Must(uuid.NewV7()).String(), Name: name, CreatedAt: time.Now().UTC()}
    var sub identity.Subject
    err := database.RunInTx(ctx, a.db, nil, func(ctx context.Context, tx database.Tx) error {
        if _, err := tx.NewInsert().Model(r).Exec(ctx); err != nil {
            return err
        }
        var err error
        sub, err = a.logins.Register(ctx, tx, proof, r.Ref())
        return err
    })
    return sub, err
}

// Delete 注销读者：删除账号行和全部登录凭据，再撤销全部 token。
type Delete struct {
    db     *bun.DB
    logins *login.Realm
    tokens *apitoken.Store
}

func NewDelete(db *bun.DB, logins *login.Realm, tokens *apitoken.Store) *Delete {
    return &Delete{db: db, logins: logins, tokens: tokens}
}

func (a *Delete) Handle(ctx context.Context, ref identity.Ref) error {
    err := database.RunInTx(ctx, a.db, nil, func(ctx context.Context, tx database.Tx) error {
        if _, err := tx.NewDelete().Model((*Reader)(nil)).Where("id = ?", ref.ID).Exec(ctx); err != nil {
            return err
        }
        return a.logins.Forget(ctx, tx, ref)
    })
    if err != nil {
        return err
    }
    _, err = a.tokens.RevokeSubject(ctx, ref)
    return err
}
```

注销之后，同一个手机号可以重新注册，得到一个新账号。

<a id="assembling"></a>
## 装配

下面是完整的 `internal/accounts/accounts.go`，它为两套账号各创建一个 [`login.Realm`](login.md#assembling)：编辑只能用邮箱和密码登录，验证码通过邮件发出邀请链接；读者开放注册，验证码通过[短信](sms.md)发送，模板 `login_code` 在短信服务商处申请。读者的 API 令牌使用 [JWT 模式](api-tokens.md#jwt)：

```go
package accounts

import (
    "context"
    "net/url"
    "time"

    "github.com/redis/rueidis"
    "github.com/uptrace/bun"

    "example.com/blog/internal/editor"
    "example.com/blog/internal/reader"
    "github.com/ryan053857/mygoravel/apitoken"
    "github.com/ryan053857/mygoravel/crypt"
    "github.com/ryan053857/mygoravel/hashing"
    "github.com/ryan053857/mygoravel/login"
    "github.com/ryan053857/mygoravel/login/oauth"
    "github.com/ryan053857/mygoravel/mail"
    "github.com/ryan053857/mygoravel/ratelimit"
    "github.com/ryan053857/mygoravel/ratelimit/redislimit"
    "github.com/ryan053857/mygoravel/sms"
)

type Config struct {
    // PasswordURL 是编辑后台单页应用中设置密码页面的地址，邀请邮件中的链接指向它。
    PasswordURL string          `yaml:"password_url"`
    EditorToken apitoken.Config `yaml:"editor_token"`
    ReaderToken apitoken.Config `yaml:"reader_token"`
    Apple       Apple           `yaml:"apple"`
}

// Apple 是在 Apple 开发者后台登记的 Sign in with Apple。
type Apple struct {
    ServiceID    string `yaml:"service_id"`
    ClientSecret string `yaml:"client_secret"`
    BundleID     string `yaml:"bundle_id"` // App 原生登录时 ID token 的受众
}

type Accounts struct {
    Editors      *login.Realm
    EditorTokens *apitoken.Store
    Readers      *login.Realm
    ReaderTokens *apitoken.Store
    ReaderJWT    *apitoken.JWT
    Apple        *oauth.Provider
}

func New(ctx context.Context, db *bun.DB, rdb rueidis.Client, cfg Config, hasher *hashing.Hasher, c *crypt.Crypt, mailer mail.Mailer, texts sms.Sender) (*Accounts, error) {
    every15 := ratelimit.Config{Rate: 5, Interval: 15 * time.Minute, Burst: 5}
    editors := login.New(db, login.Options{
        Type:          editor.Type,
        Hasher:        hasher,
        Crypt:         c,
        Issuer:        "Blog 编辑后台",
        PasswordKinds: []string{login.KindEmail},
        Resolve:       editor.Resolve(db),
        Attempts:      redislimit.New(rdb, "blog:login:editor:attempts", every15),
        Sends:         redislimit.New(rdb, "blog:login:editor:sends", every15),
        Send: func(ctx context.Context, code login.Code) error {
            link := cfg.PasswordURL + "?" + url.Values{"request": {code.RequestID}, "code": {code.Code}}.Encode()
            return mailer.Send(ctx, mail.Message{
                To:      []mail.Address{{Email: code.To.Value}},
                Subject: "设置 Blog 编辑后台的密码",
                Text:    "打开下面的链接设置密码，15 分钟内有效：\n" + link,
            })
        },
    })
    readers := login.New(db, login.Options{
        Type:     reader.Type,
        Register: true,
        Hasher:   hasher,
        Crypt:    c,
        Resolve:  reader.Resolve(db),
        Attempts: redislimit.New(rdb, "blog:login:reader:attempts", every15),
        Sends:    redislimit.New(rdb, "blog:login:reader:sends", every15),
        Send: func(ctx context.Context, code login.Code) error {
            return texts.Send(ctx, sms.Message{
                To:       code.To.Value,
                Template: "login_code",
                Params:   map[string]string{"code": code.Code},
            })
        },
    })
    apple, err := oauth.Discover(ctx, oauth.Config{
        Provider:     "apple",
        Issuer:       "https://appleid.apple.com",
        ClientID:     cfg.Apple.ServiceID,
        ClientSecret: cfg.Apple.ClientSecret,
        Audiences:    []string{cfg.Apple.ServiceID, cfg.Apple.BundleID},
        NoPKCE:       true,
        FormPost:     true,
    })
    if err != nil {
        return nil, err
    }
    tokens := apitoken.NewStore(db, cfg.ReaderToken)
    return &Accounts{
        Editors:      editors,
        EditorTokens: apitoken.NewStore(db, cfg.EditorToken),
        Readers:      readers,
        ReaderTokens: tokens,
        ReaderJWT:    apitoken.NewJWT(tokens, apitoken.JWTConfig{Keys: apitoken.HMACKeys(c)}),
        Apple:        apple,
    }, nil
}
```

两个 `Realm` 共用 `auth_*` 表：同一个邮箱可以既是编辑又是读者，彼此互不影响。两个 `apitoken.Store` 也共用 `api_tokens` 表，各自的有效期按配置签发。

<a id="editor-api"></a>
## 编辑后台

编辑后台是[单页应用](spa.md)，调用 `/admin` 下的接口：

1. 编辑收到邀请邮件，打开单页应用中的设置密码页。页面把链接中的 `request` 和 `code` 交给 `POST /auth/password/verify`，得到完成凭证，再带着它和新密码调用 `POST /auth/password`；
2. 用邮箱和密码登录（`POST /auth/login`）。启用了二次验证时得到 `pending`，带着它和验证器上的数字调用 `POST /auth/two-factor`；
3. 还没有启用二次验证时，先到启用页：`POST /account/two-factor/setup` 得到二维码，`POST /account/two-factor` 提交验证码启用；
4. 邀请其他编辑时（`POST /editors`），请求中再带上自己的密码。

“必须启用二次验证”由 Resolver 保证：没有启用的编辑没有任何权限，只能调用不要求权限的接口，也就是登录、启用二次验证和退出。下面是完整的 `internal/editor/editorapi/routes.go`：

```go
package editorapi

import (
    "context"
    "net/http"

    "github.com/danielgtaylor/huma/v2"
    "github.com/uptrace/bun"

    "example.com/blog/internal/accounts"
    "example.com/blog/internal/editor"
    "github.com/ryan053857/mygoravel/apitoken"
    "github.com/ryan053857/mygoravel/httpx/rest"
    "github.com/ryan053857/mygoravel/identity"
    "github.com/ryan053857/mygoravel/login"
)

// Resolve 是编辑后台 API 的 Resolver：在 editor.Resolve 的基础上，没有启用二次验证的编辑没有任何权限。
func Resolve(db *bun.DB, logins *login.Realm) identity.Resolver {
    base := editor.Resolve(db)
    return func(ctx context.Context, sub identity.Subject) (identity.Subject, error) {
        sub, err := base(ctx, sub)
        if err != nil {
            return sub, err
        }
        enabled, err := logins.TwoFactorEnabled(ctx, sub.Ref())
        if err != nil {
            return identity.Subject{}, err
        }
        if !enabled {
            sub.Permissions = nil
        }
        return sub, nil
    }
}

type LoginInput struct {
    UserAgent string `header:"User-Agent"`
    Body      struct {
        Email    string `json:"email" format:"email"`
        Password string `json:"password" minLength:"1"`
    }
}

type TwoFactorInput struct {
    UserAgent string `header:"User-Agent"`
    Body      struct {
        Pending string `json:"pending" minLength:"1"`
        Code    string `json:"code" minLength:"1"`
    }
}

// LoginOutput 是登录的结果：token；需要二次验证时只有 pending。
type LoginOutput struct {
    Body struct {
        Token     string `json:"token,omitempty"`
        Pending   string `json:"pending,omitempty" doc:"带着它和验证码调用 /auth/two-factor"`
        TwoFactor bool   `json:"two_factor" doc:"是否已经启用二次验证；为 false 时先去启用"`
    }
}

type VerifyLinkInput struct {
    Body struct {
        Request string `json:"request" minLength:"1"`
        Code    string `json:"code" minLength:"1"`
    }
}

type VerifyLinkOutput struct {
    Body struct {
        Proof string `json:"proof"`
    }
}

type PasswordInput struct {
    Body struct {
        Proof    string `json:"proof" minLength:"1"`
        Password string `json:"password" minLength:"1"`
    }
}

type SetupOutput struct {
    Body struct {
        Secret string `json:"secret"`
        URI    string `json:"uri" doc:"otpauth:// 地址，渲染成二维码"`
    }
}

type EnableInput struct {
    Body struct {
        Code string `json:"code" minLength:"1"`
    }
}

type EnableOutput struct {
    Body struct {
        RecoveryCodes []string `json:"recovery_codes"`
    }
}

type InviteInput struct {
    Body struct {
        Name     string `json:"name" minLength:"1" maxLength:"50"`
        Email    string `json:"email" format:"email"`
        Password string `json:"password" minLength:"1" doc:"当前编辑的密码，确认是本人操作"`
    }
}

type handlers struct {
    db     *bun.DB
    logins *login.Realm
    tokens *apitoken.Store
    invite *editor.Invite
}

func Register(api *rest.API, db *bun.DB, acc *accounts.Accounts) {
    h := &handlers{db: db, logins: acc.Editors, tokens: acc.EditorTokens, invite: editor.NewInvite(db, acc.Editors)}
    op := func(id, method, path string, auth func(*huma.Operation)) huma.Operation {
        o := huma.Operation{OperationID: id, Method: method, Path: path}
        auth(&o)
        return o
    }
    public := rest.Public()
    signedIn := rest.RequireBearer(api, acc.EditorTokens.Validate)
    rest.Register(api, op("editor-login", http.MethodPost, "/auth/login", public), h.login)
    rest.Register(api, op("editor-two-factor", http.MethodPost, "/auth/two-factor", public), h.twoFactor)
    rest.Register(api, op("editor-password-verify", http.MethodPost, "/auth/password/verify", public), h.verifyLink)
    rest.Register(api, op("editor-password", http.MethodPost, "/auth/password", public), h.setPassword)
    rest.Register(api, op("editor-logout", http.MethodDelete, "/auth/session", signedIn), h.logout)
    rest.Register(api, op("editor-two-factor-setup", http.MethodPost, "/account/two-factor/setup", signedIn), h.setup)
    rest.Register(api, op("editor-two-factor-enable", http.MethodPost, "/account/two-factor", signedIn), h.enable)
    rest.Register(api, op("editor-invite", http.MethodPost, "/editors", rest.RequireBearer(api, acc.EditorTokens.Validate, "editors:invite")), h.inviteEditor)
}

func (h *handlers) login(ctx context.Context, in *LoginInput) (*LoginOutput, error) {
    out, err := h.logins.Password(ctx, login.Email(in.Body.Email), in.Body.Password)
    if err != nil {
        return nil, err // 密码错误：login.ErrInvalidCredentials，401；连续失败太多次：ratelimit.ErrExceeded，429
    }
    if out.Pending != "" {
        resp := &LoginOutput{}
        resp.Body.Pending, resp.Body.TwoFactor = out.Pending, true
        return resp, nil
    }
    return h.issue(ctx, out.Subject, in.UserAgent)
}

func (h *handlers) twoFactor(ctx context.Context, in *TwoFactorInput) (*LoginOutput, error) {
    out, err := h.logins.TwoFactor(ctx, in.Body.Pending, in.Body.Code)
    if err != nil {
        return nil, err // 验证码不正确：login.ErrInvalidCode；输错太多次或超时：login.ErrInvalidProof，重新登录
    }
    return h.issue(ctx, out.Subject, in.UserAgent)
}

// issue 签发登录 token，并告诉单页应用这个编辑是否已经启用二次验证。
func (h *handlers) issue(ctx context.Context, sub identity.Subject, userAgent string) (*LoginOutput, error) {
    enabled, err := h.logins.TwoFactorEnabled(ctx, sub.Ref())
    if err != nil {
        return nil, err
    }
    token, err := h.tokens.Login(ctx, sub, apitoken.Device{UserAgent: userAgent})
    if err != nil {
        return nil, err
    }
    resp := &LoginOutput{}
    resp.Body.Token, resp.Body.TwoFactor = token, enabled
    return resp, nil
}

// verifyLink 校验邀请和找回密码邮件中的链接。设置密码页打开后才调用它：
// 邮件服务商的安全扫描会预先打开链接，只有页面上的脚本会提交验证码。
func (h *handlers) verifyLink(ctx context.Context, in *VerifyLinkInput) (*VerifyLinkOutput, error) {
    out, err := h.logins.VerifyCode(ctx, in.Body.Request, in.Body.Code)
    if err != nil {
        return nil, err // 链接已失效：login.ErrInvalidCode
    }
    resp := &VerifyLinkOutput{}
    resp.Body.Proof = out.Proof
    return resp, nil
}

// setPassword 用完成凭证设置密码。密码不符合要求时返回 login.ErrWeakPassword，凭证仍然有效，可以重新提交。
func (h *handlers) setPassword(ctx context.Context, in *PasswordInput) (*struct{}, error) {
    _, err := h.logins.ResetPassword(ctx, in.Body.Proof, in.Body.Password)
    return nil, err
}

func (h *handlers) logout(ctx context.Context, _ *struct{}) (*struct{}, error) {
    sub, _ := identity.FromContext(ctx)
    return nil, h.tokens.Revoke(ctx, sub, sub.Credential)
}

func (h *handlers) setup(ctx context.Context, _ *struct{}) (*SetupOutput, error) {
    sub, _ := identity.FromContext(ctx)
    setup, err := h.logins.SetupTOTP(ctx, h.db, sub.Ref(), sub.ID)
    if err != nil {
        return nil, err // 已经启用：login.ErrTwoFactorEnabled
    }
    resp := &SetupOutput{}
    resp.Body.Secret, resp.Body.URI = setup.Secret, setup.URI
    return resp, nil
}

func (h *handlers) enable(ctx context.Context, in *EnableInput) (*EnableOutput, error) {
    sub, _ := identity.FromContext(ctx)
    codes, err := h.logins.ConfirmTOTP(ctx, h.db, sub.Ref(), in.Body.Code)
    if err != nil {
        return nil, err // 验证码不正确：login.ErrInvalidCode，重新扫码
    }
    resp := &EnableOutput{}
    resp.Body.RecoveryCodes = codes
    return resp, nil
}

func (h *handlers) inviteEditor(ctx context.Context, in *InviteInput) (*struct{}, error) {
    sub, _ := identity.FromContext(ctx)
    if err := h.logins.Confirm(ctx, sub.Ref(), in.Body.Password); err != nil {
        return nil, err // 密码错误：login.ErrInvalidCredentials
    }
    _, err := h.invite.Handle(ctx, in.Body.Name, in.Body.Email)
    return nil, err // 邮箱已被使用：login.ErrTaken
}
```

几点说明：

- 待完成的登录（`pending`）由单页应用暂存，带到二次验证的请求中；它 15 分钟内有效，最多输错 5 次。
- 没有启用二次验证的编辑也会拿到 token，`two_factor` 为 `false`。Resolver 让这个 token 没有任何权限，单页应用据此直接进入启用页，启用后不需要重新登录。
- `SetupTOTP` 的第三个参数是验证器中显示的账号名，这里用编辑的 ID，也可以查出邮箱或姓名。`uri` 由单页应用渲染成二维码。
- 邀请编辑在请求中带上自己的密码，与[读者注销账号](#reader-api)带验证码的做法相同，见[确认身份](login.md#confirming)。

<a id="reader-api"></a>
## 读者 App

读者 App 的流程：

1. 输入手机号，`POST /auth/codes` 发送验证码；
2. `POST /auth/codes/verify` 提交验证码：已注册时得到一对 token，未注册时得到注册凭证；
3. 未注册时进入注册页，`POST /auth/sign-up` 带着注册凭证和昵称注册，得到一对 token；
4. 也可以用 Sign in with Apple：App 把系统返回的 ID token 交给 `POST /auth/apple`，结果与第 2 步相同；
5. access token 过期后 `POST /auth/refresh`；退出登录用 access token 调用 `DELETE /auth/session`；
6. 注销账号前先确认身份：有手机号的读者 `POST /account/confirm-code` 收到验证码，带着它调用 `DELETE /account`；只用 Apple 登录的读者重新登录一次 Apple，带上新的 ID token。

下面是完整的 `internal/reader/readerapi/routes.go`：

```go
package readerapi

import (
    "context"
    "net/http"
    "time"

    "github.com/danielgtaylor/huma/v2"
    "github.com/uptrace/bun"

    "example.com/blog/internal/accounts"
    "example.com/blog/internal/reader"
    "github.com/ryan053857/mygoravel/apitoken"
    "github.com/ryan053857/mygoravel/httpx/rest"
    "github.com/ryan053857/mygoravel/identity"
    "github.com/ryan053857/mygoravel/login"
)

type SendCodeInput struct {
    Body struct {
        Phone string `json:"phone" pattern:"^\\+[1-9][0-9]{6,14}$" doc:"E.164 格式，例如 +8613800138000"`
    }
}

type SendCodeOutput struct {
    Body struct {
        RequestID string `json:"request_id"`
    }
}

type VerifyCodeInput struct {
    UserAgent string `header:"User-Agent"`
    Body      struct {
        RequestID string `json:"request_id"`
        Code      string `json:"code"`
    }
}

type SignUpInput struct {
    UserAgent string `header:"User-Agent"`
    Body      struct {
        Proof string `json:"proof" minLength:"1"`
        Name  string `json:"name" minLength:"1" maxLength:"50"`
    }
}

type AppleInput struct {
    UserAgent string `header:"User-Agent"`
    Body      struct {
        IDToken string `json:"id_token" minLength:"1"`
        Nonce   string `json:"nonce,omitempty"`
    }
}

// ConfirmInput 确认操作的是读者本人：验证码，或重新登录 Apple 得到的 ID token。
type ConfirmInput struct {
    Body struct {
        RequestID    string `json:"request_id,omitempty" doc:"POST /account/confirm-code 返回的请求 ID"`
        Code         string `json:"code,omitempty"`
        AppleIDToken string `json:"apple_id_token,omitempty" doc:"没有手机号的读者重新登录 Apple 得到的 ID token"`
        Nonce        string `json:"nonce,omitempty"`
    }
}

type RefreshInput struct {
    Body struct {
        RefreshToken string `json:"refresh_token" minLength:"1"`
    }
}

// SessionOutput 是登录的结果：一对 token；还没有账号时只有注册凭证。
type SessionOutput struct {
    Body struct {
        AccessToken  string    `json:"access_token,omitempty"`
        RefreshToken string    `json:"refresh_token,omitempty" doc:"为空时继续使用已保存的 refresh token"`
        ExpiresAt    time.Time `json:"expires_at,omitzero" doc:"access token 的过期时间"`
        Proof        string    `json:"proof,omitempty" doc:"还没有账号：带着它调用 /auth/sign-up"`
    }
}

func session(pair apitoken.Pair) *SessionOutput {
    out := &SessionOutput{}
    out.Body.AccessToken, out.Body.RefreshToken, out.Body.ExpiresAt = pair.Access, pair.Refresh, pair.ExpiresAt
    return out
}

type handlers struct {
    acc    *accounts.Accounts
    signUp *reader.SignUp
    delete *reader.Delete
}

func Register(api *rest.API, db *bun.DB, acc *accounts.Accounts) {
    h := &handlers{
        acc:    acc,
        signUp: reader.NewSignUp(db, acc.Readers),
        delete: reader.NewDelete(db, acc.Readers, acc.ReaderTokens),
    }
    public := func(id, path string) huma.Operation {
        op := huma.Operation{OperationID: id, Method: http.MethodPost, Path: path}
        rest.Public()(&op)
        return op
    }
    rest.Register(api, public("reader-codes-send", "/auth/codes"), h.sendCode)
    rest.Register(api, public("reader-codes-verify", "/auth/codes/verify"), h.verifyCode)
    rest.Register(api, public("reader-sign-up", "/auth/sign-up"), h.register)
    rest.Register(api, public("reader-apple", "/auth/apple"), h.apple)
    rest.Register(api, public("reader-refresh", "/auth/refresh"), h.refresh)

    signedIn := func(id, method, path string) huma.Operation {
        op := huma.Operation{OperationID: id, Method: method, Path: path}
        rest.RequireBearer(api, acc.ReaderJWT.Validate)(&op)
        return op
    }
    rest.Register(api, signedIn("reader-logout", http.MethodDelete, "/auth/session"), h.logout)
    rest.Register(api, signedIn("reader-confirm-code", http.MethodPost, "/account/confirm-code"), h.sendConfirmCode)
    rest.Register(api, signedIn("reader-delete", http.MethodDelete, "/account"), h.deleteAccount)
}

func (h *handlers) sendCode(ctx context.Context, in *SendCodeInput) (*SendCodeOutput, error) {
    id, err := h.acc.Readers.SendCode(ctx, login.SignIn, login.Phone(in.Body.Phone))
    if err != nil {
        return nil, err
    }
    out := &SendCodeOutput{}
    out.Body.RequestID = id
    return out, nil
}

func (h *handlers) verifyCode(ctx context.Context, in *VerifyCodeInput) (*SessionOutput, error) {
    out, err := h.acc.Readers.VerifyCode(ctx, in.Body.RequestID, in.Body.Code)
    if err != nil {
        return nil, err
    }
    return h.issue(ctx, out, in.UserAgent)
}

func (h *handlers) register(ctx context.Context, in *SignUpInput) (*SessionOutput, error) {
    sub, err := h.signUp.Handle(ctx, in.Body.Proof, in.Body.Name)
    if err != nil {
        return nil, err
    }
    return h.issue(ctx, login.Outcome{Subject: sub}, in.UserAgent)
}

func (h *handlers) apple(ctx context.Context, in *AppleInput) (*SessionOutput, error) {
    ext, err := h.acc.Apple.IDToken(ctx, in.Body.IDToken, in.Body.Nonce)
    if err != nil {
        return nil, err
    }
    out, err := h.acc.Readers.External(ctx, ext)
    if err != nil {
        return nil, err
    }
    return h.issue(ctx, out, in.UserAgent)
}

// issue 登录成功时签发一对 token；还没有账号时返回注册凭证。
func (h *handlers) issue(ctx context.Context, out login.Outcome, userAgent string) (*SessionOutput, error) {
    if out.Proof != "" {
        resp := &SessionOutput{}
        resp.Body.Proof = out.Proof
        return resp, nil
    }
    pair, err := h.acc.ReaderJWT.Login(ctx, out.Subject, apitoken.Device{Remember: true, UserAgent: userAgent})
    if err != nil {
        return nil, err
    }
    return session(pair), nil
}

func (h *handlers) refresh(ctx context.Context, in *RefreshInput) (*SessionOutput, error) {
    pair, err := h.acc.ReaderJWT.Refresh(ctx, in.Body.RefreshToken)
    if err != nil {
        return nil, err
    }
    return session(pair), nil
}

func (h *handlers) logout(ctx context.Context, _ *struct{}) (*struct{}, error) {
    sub, _ := identity.FromContext(ctx)
    return nil, h.acc.ReaderTokens.Revoke(ctx, sub, sub.Credential)
}

// sendConfirmCode 向读者自己的手机号发送确认身份的验证码。
func (h *handlers) sendConfirmCode(ctx context.Context, _ *struct{}) (*SendCodeOutput, error) {
    sub, _ := identity.FromContext(ctx)
    ids, err := h.acc.Readers.Identifiers(ctx, sub.Ref())
    if err != nil {
        return nil, err
    }
    for _, id := range ids {
        if id.Kind == login.KindPhone {
            requestID, err := h.acc.Readers.SendConfirmCode(ctx, sub.Ref(), id.Identifier)
            if err != nil {
                return nil, err
            }
            out := &SendCodeOutput{}
            out.Body.RequestID = requestID
            return out, nil
        }
    }
    return nil, login.ErrNotFound.New()
}

func (h *handlers) deleteAccount(ctx context.Context, in *ConfirmInput) (*struct{}, error) {
    sub, _ := identity.FromContext(ctx)
    if err := h.confirm(ctx, sub.Ref(), in); err != nil {
        return nil, err
    }
    return nil, h.delete.Handle(ctx, sub.Ref())
}

// confirm 确认操作的是读者本人：校验发到手机的验证码，或者重新登录 Apple，登录的必须是同一个读者。
func (h *handlers) confirm(ctx context.Context, ref identity.Ref, in *ConfirmInput) error {
    if in.Body.AppleIDToken == "" {
        return h.acc.Readers.ConfirmCode(ctx, ref, in.Body.RequestID, in.Body.Code)
    }
    ext, err := h.acc.Apple.IDToken(ctx, in.Body.AppleIDToken, in.Body.Nonce)
    if err != nil {
        return err
    }
    out, err := h.acc.Readers.External(ctx, ext)
    if err != nil {
        return err
    }
    if out.Subject.Ref() != ref {
        return login.ErrInvalidCredentials.New()
    }
    return nil
}
```

几点说明：

- 读者没有启用二次验证，`VerifyCode` 和 `External` 不会返回 `Pending`。需要时与[编辑后台](#editor-api)一样处理。
- 发送验证码对未注册的手机号同样发送（`Register: true`），所以第 2 步才知道要注册；同一个手机号 15 分钟内最多发 5 次，见[限流与防探测](login.md#limits)。
- Apple 的 `Nonce` 应当由服务器下发，见 [App 原生登录](social-login.md#native-sign-in)。Apple 账号对方确认过的邮箱会一并写入，所以邮箱已被其他读者使用时注册返回 `login.ErrTaken`。
- 退出登录撤销的是这次登录的 refresh token，已签发的 access token 在 10 分钟内过期；注销账号还会让这个读者的全部登录立即无法刷新。
- 读者没有密码，注销前用验证码或重新登录 Apple [确认身份](login.md#confirming)。确认用的验证码只发给读者自己的手机号，不能用来登录。

<a id="wiring"></a>
## 接入入口

在[入口](dependencies.md#assembling-the-application)中创建 `accounts.Accounts`，交给两组 API。`texts` 是[短信发送器](sms.md#assembling)。编辑后台的 API 挂在 `/admin/api` 下，只接受编辑；读者的 API 只接受读者：

```go
acc, err := accounts.New(ctx, a.db, a.rdb, a.cfg.Accounts, hashing.New(a.cfg.Hashing), a.crypt, mailer, texts)
if err != nil {
    return err
}

router.Route("/admin/api", func(r chi.Router) {
    admin := rest.New(rest.Options{
        Title:    "Blog Admin API",
        Router:   r,
        BasePath: "/admin/api",
        Accounts: identity.Accounts{editor.Type: editorapi.Resolve(a.db, acc.Editors)},
    })
    editorapi.Register(admin, a.db, acc)
})
router.Handle("/admin/*", adminSPA) // 编辑后台的单页应用，见单页应用一章

router.Route("/api", func(r chi.Router) {
    api := rest.New(rest.Options{
        Title:    "Blog Reader API",
        Router:   r,
        BasePath: "/api",
        Accounts: identity.Accounts{reader.Type: reader.Resolve(a.db)},
    })
    readerapi.Register(api, a.db, acc)
})
```

编辑的 token 带到读者的 API 上、读者的 token 带到编辑后台的 API 上都会被当作未登录，见[多套账号体系](authentication.md#account-systems)。需要发布的迁移有 `login`、`ratelimit` 和 `apitoken`，以及两张账号表自己的迁移；`login.Realm` 和 `apitoken.Store` 的 `Cleanup` 注册为[定时任务](scheduling.md#framework-cleanups)。
