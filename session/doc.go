// Package session 基于 alexedwards/scs 提供服务端渲染页面使用的 cookie 会话。
//
// New 按配置创建原生 *scs.SessionManager，读写会话数据直接使用 scs 的方法；
// Login、Logout、Subject 管理已登录的主体。Store 是基于 Bun 的存储，
// 配合 Migrations 发布的 sessions 表使用。加载会话、保护页面见 web 包。
//
//	sessions := session.New(cfg.Session, session.NewStore(db))
//	pages := web.New(web.Options{Sessions: sessions, Login: "/login", ...})
//
// JSON API 不使用会话，用 rest.RequireBearer 认证。
package session
