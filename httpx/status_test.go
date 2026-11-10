package httpx_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/httpx"
)

func TestStatusOf(t *testing.T) {
	for kind, want := range map[errorx.Kind]int{
		errorx.InvalidArgument:   http.StatusUnprocessableEntity,
		errorx.Unauthenticated:   http.StatusUnauthorized,
		errorx.PermissionDenied:  http.StatusForbidden,
		errorx.NotFound:          http.StatusNotFound,
		errorx.Conflict:          http.StatusConflict,
		errorx.ResourceExhausted: http.StatusTooManyRequests,
		errorx.Unavailable:       http.StatusServiceUnavailable,
		errorx.Internal:          http.StatusInternalServerError,
	} {
		if got := httpx.StatusOf(errorx.Define(kind, "test.code").New()); got != want {
			t.Errorf("%v: got %d, want %d", kind, got, want)
		}
	}
	if got := httpx.StatusOf(errors.New("plain")); got != http.StatusInternalServerError {
		t.Errorf("plain error: %d", got)
	}
}
