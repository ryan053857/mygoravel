// Package errorx 定义业务错误的稳定错误码、分类和公开参数。
//
// Define 创建模板，New 和 Wrap 创建实例；只有 Unavailable 和 Internal 这类系统故障
// 在调用处附加 cockroachdb/errors 堆栈，可预期的业务结果由错误码定位。
// errors.Is 按错误码匹配，errors.As 提取错误实例，传输边界负责状态映射。
// 已注册 cockroachdb/errors 编解码器，错误跨进程传输后仍保留错误码、分类、公开参数和重试等待时间。
//
//	var ErrNotFound = errorx.Define(errorx.NotFound, "article.not_found")
//
//	return ErrNotFound.New(errorx.Public("article_id", id))
package errorx
