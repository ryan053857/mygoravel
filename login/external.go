package login

import (
	"context"
	"encoding/json"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
)

// purposeExternal 是尚未绑定的第三方账号在 auth_challenges 中的用途，见 External。
const purposeExternal Purpose = "external"

// External 是第三方提供方确认过的账号，例如 login/oidc 校验 ID token 得到的结果，
// 或者应用用 OAuth 2.0 读取的 GitHub、微信账号。
type External struct {
	// Provider 是提供方的名称，例如 google、apple、github、wechat，作为登录标识的 Kind：
	// 小写字母开头，只含小写字母、数字和下划线，不能是 email、phone、username。
	Provider string
	// Subject 是对方的用户 ID，例如 OpenID Connect 的 sub、GitHub 的数字 id、微信的 unionid。
	// 不要用邮箱或用户名，它们可能变化。
	Subject string
	// Email 是对方提供的邮箱，EmailVerified 表示对方确认过它属于这个用户。
	Email         string
	EmailVerified bool
	// Name 是对方提供的显示名称，供注册页预填，不参与登录。
	Name string
}

// externalData 是保存在注册凭证中的、对方确认过的邮箱。
type externalData struct {
	Email string `json:"email,omitempty"`
}

// Identifier 返回第三方账号对应的登录标识（Kind 为 Provider，Value 为 Subject），提供方名称不合法时返回错误。
func (ext External) Identifier() (Identifier, error) {
	switch ext.Provider {
	case KindEmail, KindPhone, KindUsername:
		return Identifier{}, errors.Newf("login: %q cannot be an external provider", ext.Provider)
	}
	id, err := Identifier{Kind: ext.Provider, Value: ext.Subject}.Normalize()
	if err != nil {
		return Identifier{}, errors.Wrapf(err, "login: external account %s/%s", ext.Provider, ext.Subject)
	}
	return id, nil
}

// External 用第三方账号登录。调用前应用必须已经确认 ext 来自提供方，例如 login/oidc 校验过 ID token。
//
// 账号已经绑定时登录成功，返回 Outcome.Subject；第三方账号由对方负责身份验证，不再要求二次验证。
// 还没有绑定时返回 Outcome.Proof：交给 Register 注册新账号（对方确认过的邮箱同时作为登录标识），
// 或者交给 Link 绑定到已登录的账号。
//
// 框架不按邮箱把第三方账号自动关联到已有账号：别人可以用一个声称同一邮箱的第三方账号借此登录。
// 邮箱已属于其他账号时 Register 返回 ErrTaken，应用提示用户先用原来的方式登录，再绑定第三方账号。
func (r *Realm) External(ctx context.Context, ext External) (Outcome, error) {
	id, err := ext.Identifier()
	if err != nil {
		return Outcome{}, err
	}
	tenant := r.tenant(ctx)
	owner, err := r.owner(ctx, r.db, tenant, id)
	if err != nil {
		return Outcome{}, err
	}
	if owner != "" {
		return r.authenticated(ctx, r.db, owner, tenant, false, id.Kind)
	}

	var data externalData
	if ext.EmailVerified {
		if email, err := Email(ext.Email).Normalize(); err == nil {
			data.Email = email.Value
		}
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return Outcome{}, errors.Wrap(err, "login: encode external account")
	}
	rid, err := uuid.NewV7()
	if err != nil {
		return Outcome{}, errors.Wrap(err, "login: new id")
	}
	secret := token()
	now := r.now().UTC()
	row := &challengeRow{
		ID: rid, Purpose: purposeExternal, SubjectType: string(r.opts.Type), Tenant: tenant,
		Kind: id.Kind, Value: id.Value, ProofHash: sha(secret), Data: string(encoded),
		ExpiresAt: now.Add(r.opts.ProofTTL), CreatedAt: now,
	}
	if _, err := r.db.NewInsert().Model(row).Exec(ctx); err != nil {
		return Outcome{}, errors.Wrap(err, "login: save external account")
	}
	return Outcome{Proof: rid.String() + "." + secret}, nil
}

// proven 返回完成凭证证明的全部登录标识：验证码证明的标识；第三方账号，以及对方确认过的邮箱。
func (c *challengeRow) proven() ([]Identifier, error) {
	ids := []Identifier{c.identifier()}
	if c.Purpose != purposeExternal || c.Data == "" {
		return ids, nil
	}
	var data externalData
	if err := json.Unmarshal([]byte(c.Data), &data); err != nil {
		return nil, errors.Wrap(err, "login: decode external account")
	}
	if data.Email != "" {
		ids = append(ids, Identifier{Kind: KindEmail, Value: data.Email})
	}
	return ids, nil
}
