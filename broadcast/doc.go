// Package broadcast 在应用的多个实例之间广播消息，供 SSE、WebSocket 等长连接推送使用。
//
// Hub 是每个进程一个的广播中心：Publish 把消息经由 Backend 发给所有实例，
// Subscribe 订阅一个或多个主题。每个实例只从 Backend 接收一次，再分发给本实例的全部订阅。
//
// 消息只投递给当前在线的订阅，最多一次。订阅跟不上（缓冲区满），或 Backend 可能丢了消息时，
// Hub 结束受影响的订阅，而不是悄悄丢弃消息：长连接随之结束，客户端重连后由应用按
// Last-Event-ID 从自己的数据（例如消息表）补发。
//
// Backend：子包 redisbroadcast 使用 Redis Pub/Sub；broadcasttest 提供测试用的内存 Backend。
//
//	hub := broadcast.New(redisbroadcast.New(rdb, "blog:broadcast"), nil)
//	defer hub.Close()
//	err := hub.PublishJSON(ctx, "room:42", event)
//
//	sub := hub.Subscribe("room:42")
//	defer sub.Close()
//	for msg := range sub.C() { ... }
//
// Credentials 基于 Hub 提醒所有实例上的 SSE 连接立即重新校验凭据，用于撤销登录后立即断开。
package broadcast
