// Package pushtest 提供记录推送的 push.Provider，用于测试。
//
//	fake := &pushtest.Provider{}
//	push.NewDeliverer(db, fake, push.Options{}).Handle(w)
//	// ... 触发推送，等待任务处理
//	batches := fake.Batches()
//
// 只检查发布了哪些推送、不运行 worker 时，用 queuetest.Pending(t, db, push.Deliver) 读取还没处理的推送任务。
package pushtest

import (
	"context"
	"slices"
	"sync"

	"github.com/go-tenon/tenon/push"
)

// Provider 记录收到的每一批推送，可并发使用，零值可用。
type Provider struct {
	// Err 不为 nil 时决定每一批的结果，用于测试失败和重试；返回 nil 的批次照常记录。
	Err func(push.Batch) error

	mu      sync.Mutex
	batches []push.Batch
}

var _ push.Provider = (*Provider)(nil)

// Push 记录 b，Err 返回错误时不记录。
func (p *Provider) Push(_ context.Context, b push.Batch) error {
	if p.Err != nil {
		if err := p.Err(b); err != nil {
			return err
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.batches = append(p.batches, b)
	return nil
}

// Batches 返回已经记录的批次，按收到的顺序。
func (p *Provider) Batches() []push.Batch {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.batches)
}

// Tokens 返回已经推送到的全部设备 token，按收到的顺序。
func (p *Provider) Tokens() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var tokens []string
	for _, b := range p.batches {
		tokens = append(tokens, b.Tokens...)
	}
	return tokens
}
