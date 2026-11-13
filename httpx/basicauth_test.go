package httpx_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ryan053857/mygoravel/httpx"
)

func TestBasicAuth(t *testing.T) {
	t.Parallel()
	verify := func(_ context.Context, user, password string) (bool, error) {
		if user == "broken" {
			return false, errors.New("store down")
		}
		return user == "ops" && password == "s3cret", nil
	}
	h := httpx.BasicAuth("Queue", verify)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello " + httpx.BasicUser(r.Context())))
	}))
	for name, tc := range map[string]struct {
		user, password string
		set            bool
		code           int
	}{
		"no credentials": {code: http.StatusUnauthorized},
		"wrong password": {user: "ops", password: "nope", set: true, code: http.StatusUnauthorized},
		"verify fails":   {user: "broken", password: "x", set: true, code: http.StatusServiceUnavailable},
		"valid":          {user: "ops", password: "s3cret", set: true, code: http.StatusOK},
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if tc.set {
			req.SetBasicAuth(tc.user, tc.password)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != tc.code {
			t.Errorf("%s: %d", name, w.Code)
		}
		challenge := w.Header().Get("WWW-Authenticate")
		if (tc.code == http.StatusUnauthorized) != (challenge == `Basic realm="Queue", charset="UTF-8"`) {
			t.Errorf("%s: challenge %q", name, challenge)
		}
		if tc.code == http.StatusOK && w.Body.String() != "hello ops" {
			t.Errorf("%s: body %q", name, w.Body)
		}
	}
	if httpx.BasicUser(context.Background()) != "" {
		t.Fatal("user outside BasicAuth")
	}
}
