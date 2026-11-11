// Package notification 是站内通知的收件箱：发给某个租户中某个主体的通知，
// 保存在数据库的 notifications 表中，带有已读状态。
//
// Send 在业务事务中为每个收件人写入一条通知，与业务修改一起提交或回滚；
// List、UnreadCount、Get 读取收件人自己的通知，MarkRead、MarkAllRead、Delete 修改它们；
// Cleanup 删除超过保留期的通知，交给定时任务执行。
//
// 邮件、实时推送等其他渠道由 action 显式组合：在同一个事务中把邮件交给队列，
// 提交之后用 broadcast 推送 Send 返回的通知。
//
//	sent, err := notification.Send(ctx, tx, notification.Notification{
//	    Event: "article.published",
//	    Data:  map[string]any{"article_id": item.ID, "title": item.Title},
//	}, editors...)
package notification
