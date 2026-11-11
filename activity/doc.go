// Package activity 记录操作日志：谁（actor）在哪个租户中对什么（target）做了什么（event），
// 保存在数据库的 activity_log 表中，供产品界面按对象或操作人查看历史。
//
// Record 在业务事务中写入记录，与业务修改一起提交或回滚；List 按租户分页查询；
// Cleanup 删除超过保留期的记录，交给定时任务执行。
//
//	err := db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
//	    // ... 修改文章
//	    return activity.Record(ctx, tx, subject, activity.Activity{
//	        Event: "article.published", TargetType: "article", TargetID: item.ID.String(),
//	    })
//	})
package activity
