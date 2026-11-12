package push

import "context"

// DeliverNow 直接执行一个推送任务，不经过队列。
func (d *Deliverer) DeliverNow(ctx context.Context, job Job) error { return d.deliver(ctx, job) }
