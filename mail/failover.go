package mail

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/cockroachdb/errors"

	"github.com/ryan053857/mygoravel/logging"
)

// DefaultCooldown 是 FailoverOptions.Cooldown 不大于 0 时，一个发送器失败后被排到最后的时长。
const DefaultCooldown = time.Minute

// Named 是 Failover 中的一个发送器，Name 出现在日志和错误中，例如 resend、ses。
type Named struct {
	Name   string
	Mailer Mailer
}

// FailoverOptions 是 Failover 的选项，零值可用。
type FailoverOptions struct {
	// Cooldown 是一个发送器失败后被排到最后的时长：这段时间内的邮件直接从下一个发送器开始，
	// 不必每封都先等它超时。过后重新按顺序尝试，它恢复了就继续使用。不大于 0 时为 DefaultCooldown。
	Cooldown time.Duration
	// Logger 记录换用后面的发送器和发送器恢复，nil 表示 slog.Default()。
	Logger *slog.Logger
}

// Failover 返回依次尝试 mailers 的 Mailer：前一个发送失败时换下一个，第一个成功的为准，全部失败时返回合并的错误。
// 用于验证码这类同步发送、有人在等的邮件：主要的服务故障、限流或额度用完时，邮件仍然能经备用的服务发出。
//
// 邮件本身不合法（ErrInvalid）或 ctx 已经取消、超时时不再尝试后面的发送器，直接返回错误。
// 失败的发送器在 Cooldown 内排到最后，这期间先用其他发送器；其他的也都失败时仍然会尝试它。
// 状态只在本进程中，用于省掉对已知故障的服务的等待。
//
// 换用后面的发送器时，前面的服务可能其实已经收下了邮件（例如响应超时），收件人会收到两封。
// 每个发送器使用自己配置的默认发件人，备用服务的发件域名同样需要配置 SPF 和 DKIM。
//
// mailers 为空、名称为空或重复、Mailer 为 nil 时 panic。
func Failover(mailers []Named, opts FailoverOptions) Mailer {
	if len(mailers) == 0 {
		panic("mail: failover needs at least one mailer")
	}
	seen := map[string]bool{}
	for _, m := range mailers {
		switch {
		case m.Name == "":
			panic("mail: failover mailer name is required")
		case m.Mailer == nil:
			panic("mail: failover mailer " + m.Name + " is nil")
		case seen[m.Name]:
			panic("mail: duplicate failover mailer " + m.Name)
		}
		seen[m.Name] = true
	}
	f := &failover{
		mailers:   append([]Named(nil), mailers...),
		cooldown:  opts.Cooldown,
		logger:    logging.Or(opts.Logger).With(logging.AttrComponent, "mail"),
		downUntil: make([]time.Time, len(mailers)),
	}
	if f.cooldown <= 0 {
		f.cooldown = DefaultCooldown
	}
	return f
}

type failover struct {
	mailers  []Named
	cooldown time.Duration
	logger   *slog.Logger

	mu        sync.Mutex
	downUntil []time.Time // 每个发送器最近一次失败后的冷却截止时间；零值表示正常
}

func (f *failover) Send(ctx context.Context, msg Message) error {
	var errs []error
	var failed []string
	for _, i := range f.order() {
		m := f.mailers[i]
		err := m.Mailer.Send(ctx, msg)
		if err == nil {
			f.succeeded(ctx, i, failed, errs)
			return nil
		}
		errs = append(errs, errors.Wrapf(err, "mail: %s", m.Name))
		// 邮件不合法时换哪个发送器都一样；调用方已经不再等待时也不必再试。
		if errors.Is(err, ErrInvalid) || ctx.Err() != nil {
			break
		}
		failed = append(failed, m.Name)
		f.mu.Lock()
		f.downUntil[i] = time.Now().Add(f.cooldown)
		f.mu.Unlock()
	}
	return errors.Join(errs...)
}

// order 返回本次尝试的顺序：按配置的顺序，冷却中的发送器排到最后。
func (f *failover) order() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	order := make([]int, 0, len(f.mailers))
	var cooling []int
	for i := range f.mailers {
		if now.Before(f.downUntil[i]) {
			cooling = append(cooling, i)
		} else {
			order = append(order, i)
		}
	}
	return append(order, cooling...)
}

// succeeded 记录第 i 个发送器发出了邮件：它之前失败过时记录恢复；前面有发送器失败时记录换用。
func (f *failover) succeeded(ctx context.Context, i int, failed []string, errs []error) {
	f.mu.Lock()
	recovered := !f.downUntil[i].IsZero()
	f.downUntil[i] = time.Time{}
	f.mu.Unlock()
	name := f.mailers[i].Name
	if len(failed) > 0 {
		f.logger.WarnContext(ctx, "mail sent by a fallback mailer", "sent_by", name, "failed", failed, "error", errors.Join(errs...))
	}
	if recovered {
		f.logger.InfoContext(ctx, "mailer recovered", "mailer", name)
	}
}
