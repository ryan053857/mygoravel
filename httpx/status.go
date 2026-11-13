package httpx

import (
	"net/http"

	"github.com/go-tenon/tenon/errorx"
)

// StatusOf 把错误的 errorx.Kind 映射为 HTTP 状态码；不是业务错误时返回 500。
func StatusOf(err error) int {
	switch errorx.KindOf(err) {
	case errorx.InvalidArgument:
		return http.StatusUnprocessableEntity
	case errorx.Unauthenticated:
		return http.StatusUnauthorized
	case errorx.PermissionDenied:
		return http.StatusForbidden
	case errorx.NotFound:
		return http.StatusNotFound
	case errorx.Conflict:
		return http.StatusConflict
	case errorx.ResourceExhausted:
		return http.StatusTooManyRequests
	case errorx.Unavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}
