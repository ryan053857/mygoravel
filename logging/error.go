package logging

import (
	"fmt"
	"log/slog"

	slogformatter "github.com/samber/slog-formatter"

	"github.com/ryan053857/mygoravel/errorx"
)

// ErrorFormatter 把 error 类型的字段展开为分组：
//
//	"error": {"message": "...", "code": "article.slug_taken", "stack": "..."}
//
// code 取自 errorx.CodeOf，为空时省略；stack 是 %+v 输出的完整错误链（含创建处的堆栈），
// 与 message 相同时省略。New 默认启用它。
func ErrorFormatter() slogformatter.Formatter {
	return slogformatter.FormatByType(func(err error) slog.Value {
		msg := err.Error()
		attrs := []slog.Attr{slog.String("message", msg)}
		if code := errorx.CodeOf(err); code != "" {
			attrs = append(attrs, slog.String("code", code))
		}
		if stack := fmt.Sprintf("%+v", err); stack != msg {
			attrs = append(attrs, slog.String("stack", stack))
		}
		return slog.GroupValue(attrs...)
	})
}
