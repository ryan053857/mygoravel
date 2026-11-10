// Package apitoken 提供存储在数据库中的 API token，用作 JSON API 的 Bearer 凭据。
//
// token 分两类：Login 在 SPA、App 登录时签发，按 Config 滑动过期，代表一台设备；
// Create 创建个人访问令牌，供脚本和第三方集成使用，过期时间由创建者指定。
// Validate 是 rest.TokenValidator，校验 token 并顺带延长登录 token 的有效期；
// List、Revoke、RevokeOthers、RevokeSubject 管理已签发的 token。
// 表中只保存 token 的 SHA-256，明文只在签发时返回一次。
//
//	tokens := apitoken.NewStore(db, cfg.APIToken)
//	requireUser := rest.RequireBearer(api, tokens.Validate)
//	plain, err := tokens.Login(ctx, sub, apitoken.Device{IP: ip, UserAgent: ua})
package apitoken
