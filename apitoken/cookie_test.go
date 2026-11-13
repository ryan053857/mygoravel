package apitoken_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/go-tenon/tenon/apitoken"
	"github.com/go-tenon/tenon/httpx/rest"
	"github.com/go-tenon/tenon/identity"
)

var refreshCookie = apitoken.RefreshCookie{Name: "app_refresh", Path: "/auth"}

func TestRefreshCookie(t *testing.T) {
	t.Parallel()
	exp := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	c := refreshCookie.Set(apitoken.Pair{Refresh: "r1", RefreshExpiresAt: exp})
	if c.Name != "app_refresh" || c.Value != "r1" || c.Path != "/auth" || !c.Expires.Equal(exp) ||
		!c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie %+v", c)
	}
	if c := refreshCookie.Set(apitoken.Pair{Access: "a"}); c != nil {
		t.Fatalf("cookie without refresh token: %+v", c)
	}
	if c := refreshCookie.Clear(); c.MaxAge != -1 || c.Value != "" || c.Path != "/auth" {
		t.Fatalf("clear %+v", c)
	}
	defer func() {
		if recover() == nil {
			t.Error("RefreshCookie without Path did not panic")
		}
	}()
	apitoken.RefreshCookie{Name: "x"}.Clear()
}

type loginOut struct {
	SetCookie *http.Cookie `header:"Set-Cookie"`
	Body      struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token,omitempty"`
	}
}

// 同一个刷新接口接受请求体或 cookie 中的 refresh token：新的 refresh token 从哪里来就回到哪里去。
func TestRefreshCookieRoundTrip(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		j, _ := newJWT(t, apitoken.JWTConfig{})
		api := rest.New(rest.Options{Accounts: identity.Accounts{"user": nil}})
		respond := func(p apitoken.Pair, cookie bool) *loginOut {
			out := &loginOut{}
			out.Body.AccessToken = p.Access
			if cookie {
				out.SetCookie = refreshCookie.Set(p)
			} else {
				out.Body.RefreshToken = p.Refresh
			}
			return out
		}
		type loginIn struct {
			Body struct {
				Cookie bool `json:"cookie,omitempty"`
			}
		}
		op := huma.Operation{OperationID: "login", Method: http.MethodPost, Path: "/auth/login"}
		rest.Public()(&op)
		rest.Register(api, op, func(ctx context.Context, in *loginIn) (*loginOut, error) {
			p, err := j.Login(ctx, alice, apitoken.Device{})
			return respond(p, in.Body.Cookie), err
		})
		type refreshIn struct {
			Cookie string `cookie:"app_refresh"`
			Body   struct {
				RefreshToken string `json:"refresh_token,omitempty"`
			}
		}
		op = huma.Operation{OperationID: "refresh", Method: http.MethodPost, Path: "/auth/refresh"}
		rest.Public()(&op)
		rest.Register(api, op, func(ctx context.Context, in *refreshIn) (*loginOut, error) {
			token, cookie := in.Body.RefreshToken, false
			if token == "" {
				token, cookie = in.Cookie, true
			}
			p, err := j.Refresh(ctx, token)
			return respond(p, cookie), err
		})
		call := func(path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			for _, c := range cookies {
				req.AddCookie(c)
			}
			rr := httptest.NewRecorder()
			api.Handler().ServeHTTP(rr, req)
			return rr
		}

		// 浏览器：refresh token 只在 cookie 中。
		rr := call("/auth/login", `{"cookie":true}`)
		cookies := rr.Result().Cookies()
		if rr.Code != http.StatusOK || len(cookies) != 1 || strings.Contains(rr.Body.String(), "refresh_token") ||
			!cookies[0].HttpOnly || !cookies[0].Expires.Equal(time.Now().Add(testConfig.IdleTimeout).Truncate(time.Second)) {
			t.Fatalf("cookie login: %d %v %s", rr.Code, cookies, rr.Body)
		}
		time.Sleep(time.Minute)
		rr = call("/auth/refresh", `{}`, cookies[0])
		rotated := rr.Result().Cookies()
		if rr.Code != http.StatusOK || len(rotated) != 1 || rotated[0].Value == cookies[0].Value ||
			!rotated[0].Expires.After(cookies[0].Expires) {
			t.Fatalf("cookie refresh: %d %v %s", rr.Code, rotated, rr.Body)
		}
		// 宽限期内用上一个 token 刷新只得到 access token，不改动 cookie。
		if rr := call("/auth/refresh", `{}`, cookies[0]); rr.Code != http.StatusOK || len(rr.Result().Cookies()) != 0 {
			t.Fatalf("refresh within grace: %d %v", rr.Code, rr.Result().Cookies())
		}

		// App：refresh token 在请求体中，不写 cookie。
		rr = call("/auth/login", `{}`)
		if rr.Code != http.StatusOK || len(rr.Result().Cookies()) != 0 || !strings.Contains(rr.Body.String(), "refresh_token") {
			t.Fatalf("body login: %d %s", rr.Code, rr.Body)
		}
	})
}
