package errorx

import (
	"fmt"
	"maps"
	"time"

	"github.com/cockroachdb/errors"
)

// Template 是不可变的业务错误模板，通常定义为包级变量。
type Template struct {
	kind Kind
	code string
}

// Define 创建模板，code 使用 article.not_found 形式的业务错误码。
func Define(kind Kind, code string) *Template {
	return &Template{kind: kind, code: code}
}

// Code 返回稳定错误码。
func (t *Template) Code() string { return t.code }

// Kind 返回错误分类。
func (t *Template) Kind() Kind { return t.kind }

// Error 实现 error，便于 errors.Is(err, ErrXxx)。
func (t *Template) Error() string { return t.code }

// Option 配置一次失败实例的公开信息。
type Option func(*Error)

// Public 附加允许公开的业务参数，具体呈现由边界决定。
func Public(key string, value any) Option {
	return func(e *Error) {
		if e.public == nil {
			e.public = make(map[string]any)
		}
		e.public[key] = value
	}
}

// RetryAfter 附加建议的重试等待时间。HTTP 边界写入 Retry-After 响应头，
// 其他边界（例如队列）可以据此延迟重试。
func RetryAfter(d time.Duration) Option {
	return func(e *Error) { e.retryAfter = d }
}

// Error 是一次具体失败，带 cause、公开参数和建议的重试等待时间。
type Error struct {
	code       string
	kind       Kind
	public     map[string]any
	retryAfter time.Duration
	cause      error
}

// New 创建失败实例。Unavailable 和 Internal 属于系统故障，在调用处附加堆栈；
// 其余分类是可预期的业务结果，由错误码定位，不附加堆栈。
func (t *Template) New(opts ...Option) error {
	e := t.instance(nil, opts)
	if t.kind.systemic() {
		return errors.WithStackDepth(e, 1)
	}
	return e
}

// Wrap 包装 cause 创建失败实例，堆栈规则与 New 相同。
func (t *Template) Wrap(cause error, opts ...Option) error {
	e := t.instance(cause, opts)
	if t.kind.systemic() {
		return errors.WithStackDepth(e, 1)
	}
	return e
}

func (t *Template) instance(cause error, opts []Option) *Error {
	e := &Error{code: t.code, kind: t.kind, cause: cause}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Error 返回 "code: cause"，与 cockroachdb/errors 包装器的约定一致；没有 cause 时只返回错误码。
func (e *Error) Error() string { return fmt.Sprint(e) }

// Unwrap 返回内部 cause。
func (e *Error) Unwrap() error { return e.cause }

// Is 按错误码匹配模板或其他实例。错误码全局唯一且稳定，
// 因此经 errors.EncodeError/DecodeError 跨进程传输后匹配依然成立。
func (e *Error) Is(target error) bool {
	switch t := target.(type) {
	case *Template:
		return e.code == t.code
	case *Error:
		return e.code == t.code
	default:
		return false
	}
}

// Code 返回稳定错误码。
func (e *Error) Code() string { return e.code }

// Kind 返回分类。
func (e *Error) Kind() Kind { return e.kind }

// Public 返回公开参数的浅拷贝。
func (e *Error) Public() map[string]any { return maps.Clone(e.public) }

// RetryAfter 返回建议的重试等待时间，未设置时为 0。
func (e *Error) RetryAfter() time.Duration { return e.retryAfter }

// Format 实现 fmt.Formatter，%+v 输出完整错误链与各层堆栈。
func (e *Error) Format(s fmt.State, verb rune) { errors.FormatError(e, s, verb) }

// SafeFormatError 实现 errors.SafeFormatter：错误码是稳定标识，脱敏时保留；
// 公开参数可能含用户数据，只在详细输出中出现并参与脱敏。
func (e *Error) SafeFormatError(p errors.Printer) error {
	p.Print(errors.Safe(e.code))
	if len(e.public) > 0 && p.Detail() {
		p.Printf("public=%v", e.public)
	}
	return e.cause
}

// CodeOf 从错误链提取业务错误码；没有则返回空。
func CodeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.code
	}
	var t *Template
	if errors.As(err, &t) {
		return t.code
	}
	return ""
}

// KindOf 从错误链提取 Kind；没有则返回 Internal。
func KindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.kind
	}
	var t *Template
	if errors.As(err, &t) {
		return t.kind
	}
	return Internal
}

// PublicOf 从错误链提取公开参数。
func PublicOf(err error) map[string]any {
	var e *Error
	if errors.As(err, &e) {
		return e.Public()
	}
	return nil
}

// RetryAfterOf 从错误链提取建议的重试等待时间；没有则返回 0。
func RetryAfterOf(err error) time.Duration {
	var e *Error
	if errors.As(err, &e) {
		return e.retryAfter
	}
	return 0
}
