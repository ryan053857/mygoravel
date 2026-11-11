// Package realtime 在进程内嵌入 Centrifuge（https://github.com/centrifugal/centrifuge），
// 为浏览器、SPA 和移动端提供 WebSocket 实时频道。客户端使用 Centrifuge 的官方 SDK。
//
//	rt, err := realtime.New(hub, realtime.Options{})
//	rt.Channel("articles", realtime.Public())
//	rt.Channel("chat:{conversation}", realtime.Private(canView))
//	router.Handle("/realtime", rt.Handler())
//
//	// 业务代码照常通过 Hub 发布，SSE 处理器和 WebSocket 客户端都会收到
//	hub.PublishJSON(ctx, "chat:"+id, MessageSent{...})
//
// 频道就是 broadcast.Hub 的主题，多个实例之间由 Hub 的后端传递消息。
// 消息最多投递一次，不保存历史：客户端断线重连后，从应用自己的接口取回期间的数据。
package realtime
