// Package login 证明“这个人是谁”：登录标识、密码、验证码与完成凭证。
//
// 框架不拥有账号表。每套账号体系（运营人员、商家成员、客户……）用一个 Realm 管理登录凭据，
// 数据保存在 auth_* 表中，按 identity.Ref 引用应用自己的账号行。Realm 只回答“这是谁”，
// 得到 identity.Subject 后由 API 签发凭据：tokens.Login 或 JWT.Login。
//
//	customers := login.New(db, login.Options{Type: customer.Type, PerTenant: true, Policy: login.Policy{Register: true},
//		Hasher: hasher, Crypt: crypt, Send: sendCode})
//	out, err := customers.Password(ctx, login.Email(email), password)
//
// 验证码分两步：SendCode 发出短验证码并返回请求 ID；VerifyCode 校验后，已注册的标识直接登录，
// 其余情况返回一次性的完成凭证（Outcome.Proof），交给 Register、ResetPassword 或 Link。
// 框架不调用应用来创建账号：应用的注册 action 在自己的事务中建账号行，再用 Register 把已验证的标识挂上去。
package login
