// Package session 基于 alexedwards/scs 提供 cookie 会话。
//
// New 按配置创建原生 *scs.SessionManager，读写会话数据直接使用 scs 的方法；
// Login、Logout 管理已认证主体，Require 保护 Huma 路由，Resolver 决定每次请求使用的主体；
// Store 是基于 Bun 的存储，配合 Migrations 发布的 sessions 表使用。
//
//	store := session.NewStore(db)
//	sessions := session.New(cfg.Session, store, nil)
//	router.Use(sessions.LoadAndSave)
//	requireUser := session.Require(api, sessions, session.Stored)
package session
