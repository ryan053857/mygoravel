package command

import (
	"github.com/cockroachdb/errors"

	"github.com/ryan053857/mygoravel/errorx"
)

// 进程退出码。
const (
	ExitFailure     = 1 // 操作被业务规则拒绝
	ExitUsage       = 2 // 未知命令、未知选项或多余参数
	ExitConfig      = 3 // 配置文件缺失或无效
	ExitUnavailable = 4 // 外部依赖不可用
	ExitInternal    = 5 // 其他未分类的错误
)

// AsExitError 为 err 标记退出码；err 为 nil 时返回 nil。
func AsExitError(code int, err error) error {
	if err == nil {
		return nil
	}
	return &ExitError{Code: code, Err: err}
}

// ExitError 携带进程退出码。
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }

// ExitCode 返回 err 对应的退出码，nil 为 0。显式标记的退出码优先；其次按 errorx.Kind 映射：
// Unavailable 为 ExitUnavailable，Internal 和未分类错误为 ExitInternal，
// 其余业务错误为 ExitFailure。
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	switch errorx.KindOf(err) {
	case errorx.Unavailable:
		return ExitUnavailable
	case errorx.Internal:
		return ExitInternal
	default:
		return ExitFailure
	}
}
