// Package mcpx 把 action 作为 MCP（Model Context Protocol）工具提供给 Claude、Cursor 等 AI 客户端。
//
// 协议和传输由官方 Go SDK（github.com/modelcontextprotocol/go-sdk）实现，本包补上应用需要的部分：
// 与 JSON API 相同的 Bearer 认证、按权限筛选工具、按 Huma 规则校验参数、把业务错误转换为给模型看的结果。
//
// 工具入口是 action 的一个方法，签名是 func(ctx, In) (Out, error)，与 HTTP 入口一样只做转换：
//
//	func (a *Publish) Tool(ctx context.Context, in PublishArgs) (*ArticleDTO, error) {
//	    subject, _ := identity.FromContext(ctx)
//	    item, err := a.Handle(ctx, subject, in.ID)
//	    ...
//	}
//
//	srv := mcpx.New(mcpx.Options{Name: "blog"})
//	mcpx.Register(srv, mcpx.Tool{Name: "article_publish", Description: "发布一篇草稿", Permissions: []string{"articles:publish"}},
//	    article.NewPublish(db).Tool)
//	router.Handle("/mcp", mcpx.Handler(srv, mcpx.HandlerOptions{Validate: tokens.Validate, Accounts: accounts}))
//
// 服务不保存会话，每个请求独立处理，多个实例之间不需要共享任何状态。
// mcpx/mcpxtest 在测试中以指定的主体调用工具。
package mcpx
