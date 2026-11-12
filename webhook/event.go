package webhook

import (
	"context"
	"reflect"

	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/identity"
)

// Event 声明一种事件：Name 是事件类型，端点按它订阅；T 是事件内容，编码为 JSON 后放在请求体的 data 中。
// 在包级变量中声明，用 Send 发送，用 Document 写入 OpenAPI 文档：
//
//	var Published = webhook.Event[PublishedHook]{Name: "article.published", Summary: "文章发布后推送"}
type Event[T any] struct {
	Name    string // 事件类型，例如 article.published
	Summary string // 一句话说明，写入 OpenAPI 文档
}

// Send 把事件推送给 subject 所在租户中订阅了它的端点：为每个端点写入一条投递记录，
// 并通过 queue.Database(db) 发布投递任务。db 通常是业务修改所在的事务，事务提交后才会投递，回滚时一起取消。
// 没有端点订阅时什么也不写。
//
// 投递任务总是发布到数据库队列，即使应用的其他任务使用 Redis 或 NATS，worker 也要用数据库驱动处理 Deliver。
func (e Event[T]) Send(ctx context.Context, db bun.IDB, subject identity.Subject, data T) error {
	return send(ctx, db, subject, e.Name, data)
}

func (e Event[T]) describe() description {
	return description{name: e.Name, summary: e.Summary, data: reflect.TypeFor[T]()}
}

// Documented 是可以写入 OpenAPI 文档的事件，由 Event 实现。
type Documented interface {
	describe() description
}

type description struct {
	name, summary string
	data          reflect.Type
}
