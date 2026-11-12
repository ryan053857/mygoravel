package sms

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/cockroachdb/errors"

	"github.com/ryan053857/mygoravel/logging"
)

// DefaultCooldown 是 FailoverOptions.Cooldown 不大于 0 时，一个服务商失败后被排到最后的时长。
const DefaultCooldown = time.Minute

// Named 是 Failover 中的一个服务商，Name 出现在日志和错误中，例如 aliyun、tencent。
type Named struct {
	Name   string
	Sender Sender
}

// FailoverOptions 是 Failover 的选项，零值可用。
type FailoverOptions struct {
	// Cooldown 是一个服务商失败后被排到最后的时长：这段时间内的短信直接从下一个开始，不必每条都先等它超时。
	// 过后重新按顺序尝试，它恢复了就继续使用。不大于 0 时为 DefaultCooldown。
	Cooldown time.Duration
	// Logger 记录换用后面的服务商和服务商恢复，nil 表示 slog.Default()。
	Logger *slog.Logger
}

// Failover 返回依次尝试 senders 的 Sender：前一个发送失败时换下一个，第一个成功的为准，全部失败时返回合并的错误。
// 用于验证码这类同步发送、用户在等的短信：一个服务商故障、余额不足或签名模板出问题时，短信仍然能经另一家发出。
// 每个服务商都要配置好同名的模板。
//
// 以下情况不再尝试后面的服务商：短信不合法（ErrInvalid）；服务商拒绝这个号码（ErrRejected），换哪家都发不到；
// 触发了服务商对这个号码的频率限制（ErrLimited），换一家发会绕过防刷保护；ctx 已经取消或超时。
// 失败的服务商在 Cooldown 内排到最后，其他的也都失败时仍然会尝试它。状态只在本进程中。
//
// 换用后面的服务商时，前一个可能其实已经发出（例如响应超时），用户会收到两条。
// senders 为空、名称为空或重复、Sender 为 nil 时 panic。
func Failover(senders []Named, opts FailoverOptions) Sender {
	if len(senders) == 0 {
		panic("sms: failover needs at least one sender")
	}
	seen := map[string]bool{}
	for _, s := range senders {
		switch {
		case s.Name == "":
			panic("sms: failover sender name is required")
		case s.Sender == nil:
			panic("sms: failover sender " + s.Name + " is nil")
		case seen[s.Name]:
			panic("sms: duplicate failover sender " + s.Name)
		}
		seen[s.Name] = true
	}
	f := &failover{
		senders:   append([]Named(nil), senders...),
		cooldown:  opts.Cooldown,
		logger:    logging.Or(opts.Logger).With(logging.AttrComponent, "sms"),
		downUntil: make([]time.Time, len(senders)),
	}
	if f.cooldown <= 0 {
		f.cooldown = DefaultCooldown
	}
	return f
}

type failover struct {
	senders  []Named
	cooldown time.Duration
	logger   *slog.Logger

	mu        sync.Mutex
	downUntil []time.Time // 每个服务商最近一次失败后的冷却截止时间；零值表示正常
}

func (f *failover) Send(ctx context.Context, msg Message) error {
	var errs []error
	var failed []string
	for _, i := range f.order() {
		s := f.senders[i]
		err := s.Sender.Send(ctx, msg)
		if err == nil {
			f.succeeded(ctx, i, failed, errs)
			return nil
		}
		errs = append(errs, errors.Wrapf(err, "sms: %s", s.Name))
		if final(ctx, err) {
			break
		}
		failed = append(failed, s.Name)
		f.mu.Lock()
		f.downUntil[i] = time.Now().Add(f.cooldown)
		f.mu.Unlock()
	}
	return errors.Join(errs...)
}

// final 报告 err 是否与服务商无关、换一家也不会成功（或不应该换）。
func final(ctx context.Context, err error) bool {
	return errors.Is(err, ErrInvalid) || errors.Is(err, ErrRejected) || errors.Is(err, ErrLimited) || ctx.Err() != nil
}

// order 返回本次尝试的顺序：按配置的顺序，冷却中的服务商排到最后。
func (f *failover) order() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	order := make([]int, 0, len(f.senders))
	var cooling []int
	for i := range f.senders {
		if now.Before(f.downUntil[i]) {
			cooling = append(cooling, i)
		} else {
			order = append(order, i)
		}
	}
	return append(order, cooling...)
}

// succeeded 记录第 i 个服务商发出了短信：它之前失败过时记录恢复；前面有服务商失败时记录换用。
func (f *failover) succeeded(ctx context.Context, i int, failed []string, errs []error) {
	f.mu.Lock()
	recovered := !f.downUntil[i].IsZero()
	f.downUntil[i] = time.Time{}
	f.mu.Unlock()
	name := f.senders[i].Name
	if len(failed) > 0 {
		f.logger.WarnContext(ctx, "sms sent by a fallback sender", "sent_by", name, "failed", failed, "error", errors.Join(errs...))
	}
	if recovered {
		f.logger.InfoContext(ctx, "sms sender recovered", "sender", name)
	}
}
