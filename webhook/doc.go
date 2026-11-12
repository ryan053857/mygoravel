// Package webhook 向租户配置的地址推送事件，并校验收到的 webhook。
//
// 签名遵循 Standard Webhooks（https://www.standardwebhooks.com/）：请求头 webhook-id 是事件 ID，
// 重试时不变，接收方用它去重；webhook-timestamp 是发送时间；webhook-signature 是
// v1,<base64(HMAC-SHA256(id.timestamp.body))>。请求体是 {"type", "timestamp", "data"}。
//
// 发送分两步。Send 在业务事务中找出租户里订阅了该事件的端点，为每个端点写入一条投递记录，
// 并在同一个事务中把投递任务发布到数据库队列：事务提交才会投递，回滚时一起取消。
// worker 用 Deliverer 处理投递任务，失败时按队列的退避重试，最后一次仍然失败时把投递记录标记为失败：
//
//	err := webhook.Send(ctx, tx, subject, webhook.Event{Type: "article.published", Data: ev})
//
//	webhook.NewDeliverer(db, webhook.Options{}).Handle(w)
//
// 端点和投递记录按租户管理：CreateEndpoint、UpdateEndpoint、DeleteEndpoint、GetEndpoint、ListEndpoints、
// RotateSecret；ListDeliveries、GetDelivery、Resend；Cleanup 删除超过保留期的投递记录。
//
// 接收其他服务按 Standard Webhooks 发来的 webhook 时，用 Verify 校验签名。
package webhook
