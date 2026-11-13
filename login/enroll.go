package login

import (
	"context"

	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/identity"
)

// purposeEnroll 是必须先启用二次验证的登录在 auth_challenges 中的用途，见 Outcome.Enroll。
const purposeEnroll Purpose = "enroll"

// EnrollTOTP 为 Outcome.Enroll 对应的账号生成 TOTP 密钥，返回给用户扫码，与 SetupTOTP 相同；account 是验证器中显示的账号名。
// 可以重复调用，每次生成新的密钥。enroll 无效、已过期或已用过时返回 ErrInvalidProof。
func (r *Realm) EnrollTOTP(ctx context.Context, enroll, account string) (TOTPSetup, error) {
	c, err := r.proof(ctx, r.db, enroll, purposeEnroll)
	if err != nil {
		return TOTPSetup{}, err
	}
	return r.SetupTOTP(ctx, r.db, identity.Ref{Type: r.opts.Type, ID: c.SubjectID}, account)
}

// CompleteEnroll 用验证器上的数字确认 EnrollTOTP 的密钥，启用二次验证并完成登录：返回登录成功的 Outcome.Subject
// （Auth 记下登录时的方式加上 totp），以及新生成的恢复码，与 ConfirmTOTP 相同，只在这里返回明文。
// 验证码不正确时返回 ErrInvalidCode；一次待完成的登录最多可以输错 5 次，enroll 无效、已过期或已用过时返回 ErrInvalidProof。
func (r *Realm) CompleteEnroll(ctx context.Context, enroll, code string) (Outcome, []string, error) {
	c, err := r.proof(ctx, r.db, enroll, purposeEnroll)
	if err != nil {
		return Outcome{}, nil, err
	}
	if err := r.attemptPending(ctx, c); err != nil {
		return Outcome{}, nil, err
	}
	methods, err := c.methods()
	if err != nil {
		return Outcome{}, nil, err
	}
	var codes []string
	err = r.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var err error
		if codes, err = r.ConfirmTOTP(ctx, tx, identity.Ref{Type: r.opts.Type, ID: c.SubjectID}, code); err != nil {
			return err
		}
		return r.complete(ctx, tx, c)
	})
	if err != nil {
		return Outcome{}, nil, err
	}
	out, err := r.signedIn(ctx, c.SubjectID, c.Tenant, append(methods, MethodTOTP)...)
	return out, codes, err
}
