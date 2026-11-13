// Package oauthserver 是 OAuth 2.1 授权服务器，让 claude.ai、ChatGPT 这类远程客户端在用户同意后访问 MCP 服务。
//
// 同意页是应用的单页应用中的一个页面：授权端点校验请求后跳到 Options.ConsentURL，同意页用 Bearer 调用 Register 注册的接口，
// 显示哪个应用申请哪些范围，用户同意后跳回客户端，客户端用授权码和 PKCE 换取令牌。
// 令牌是 apitoken 的 OAuth 令牌，只能用于授权时的资源，权限是用户本人的权限与同意的范围的交集，
// 在“已连接的应用”中可以撤销。
//
// 客户端不需要事先登记：client_id 是 https 地址时，按 Client ID Metadata Document 取回客户端的名称和回调地址；
// 自研的客户端在 Options.Clients 中预先登记。只支持公开客户端（token_endpoint_auth_method 为 none）和授权码流程，
// 不发刷新令牌：令牌按使用滑动过期，到期后客户端重新授权。
//
//	srv := oauthserver.New(db, tokens, oauthserver.Options{
//	    Issuer:     "https://blog.example.com",
//	    Resources:  []string{"https://blog.example.com/mcp"},
//	    Scopes:     []oauthserver.Scope{{Name: "articles:read", Description: "查看文章"}},
//	    ConsentURL: "/oauth/consent",
//	})
//	router.Handle(srv.MetadataPath(), srv.Metadata())
//	srv.Routes(router)
//	srv.Register(api, rest.RequireBearer(api, tokens.Validate))
//
//	res := srv.Resource("https://blog.example.com/mcp")
//	router.Handle(res.MetadataPath(), mcpx.ResourceMetadata(res))
//	router.Handle("/mcp", mcpx.Handler(tools, mcpx.HandlerOptions{Validate: tokens.ValidateFor(res.URL), Accounts: accounts, Resource: &res}))
package oauthserver
