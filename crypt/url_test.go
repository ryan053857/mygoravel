package crypt_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-tenon/tenon/crypt"
	"github.com/go-tenon/tenon/errorx"
)

func parse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u
}

func TestSignURL(t *testing.T) {
	c := newCrypt(t, crypt.GenerateKey())
	link := c.SignURL("https://blog.example.com/email/verify/42?email=a%40b.com", time.Hour)
	u := parse(t, link)
	assert.Equal(t, "https", u.Scheme)
	assert.Equal(t, "blog.example.com", u.Host)
	assert.Equal(t, "/email/verify/42", u.Path)
	assert.Equal(t, "a@b.com", u.Query().Get("email"))
	assert.NotEmpty(t, u.Query().Get("expires"))
	assert.NotEmpty(t, u.Query().Get("signature"))
	assert.NoError(t, c.VerifyURL(u))
}

func TestVerifyURLIgnoresSchemeAndHost(t *testing.T) {
	c := newCrypt(t, crypt.GenerateKey())
	u := parse(t, c.SignURL("https://blog.example.com/unsubscribe/7", 0))

	// 反向代理之后，处理器看到的 r.URL 只有路径和查询参数。
	r := httptest.NewRequest(http.MethodGet, u.RequestURI(), nil)
	assert.NoError(t, c.VerifyURL(r.URL))
	assert.NoError(t, c.VerifyURL(parse(t, "http://10.0.0.5:8080"+u.RequestURI())))
}

func TestVerifyURLRejectsTampering(t *testing.T) {
	c := newCrypt(t, crypt.GenerateKey())
	link := c.SignURL("https://blog.example.com/email/verify/42?email=a%40b.com", time.Hour)
	u := parse(t, link)
	sig := u.Query().Get("signature")

	mutate := func(f func(u *url.URL, q url.Values)) *url.URL {
		v := *u
		q := v.Query()
		f(&v, q)
		v.RawQuery = q.Encode()
		return &v
	}
	cases := map[string]*url.URL{
		"path":             mutate(func(u *url.URL, _ url.Values) { u.Path = "/email/verify/43" }),
		"param":            mutate(func(_ *url.URL, q url.Values) { q.Set("email", "evil@b.com") }),
		"added param":      mutate(func(_ *url.URL, q url.Values) { q.Set("utm_source", "mail") }),
		"removed param":    mutate(func(_ *url.URL, q url.Values) { q.Del("email") }),
		"expires extended": mutate(func(_ *url.URL, q url.Values) { q.Set("expires", "99999999999") }),
		"expires removed":  mutate(func(_ *url.URL, q url.Values) { q.Del("expires") }),
		"no signature":     mutate(func(_ *url.URL, q url.Values) { q.Del("signature") }),
		"bad signature":    mutate(func(_ *url.URL, q url.Values) { q.Set("signature", "%%%") }),
		"two signatures":   mutate(func(_ *url.URL, q url.Values) { q.Add("signature", sig) }),
		"other key":        parse(t, newCrypt(t, crypt.GenerateKey()).SignURL(link, time.Hour)),
	}
	for name, u := range cases {
		err := c.VerifyURL(u)
		assert.ErrorIs(t, err, crypt.ErrInvalidSignature, name)
		assert.Equal(t, errorx.PermissionDenied, errorx.KindOf(err), name)
	}
}

func TestSignURLReplacesReservedParams(t *testing.T) {
	c := newCrypt(t, crypt.GenerateKey())
	u := parse(t, c.SignURL("https://blog.example.com/a?expires=1&signature=x&b=2", 0))
	assert.Equal(t, url.Values{"b": {"2"}, "signature": u.Query()["signature"]}, u.Query())
	assert.NoError(t, c.VerifyURL(u))
}

func TestSignURLEscapedPath(t *testing.T) {
	c := newCrypt(t, crypt.GenerateKey())
	for _, raw := range []string{"https://blog.example.com/文章/你好", "https://blog.example.com/files/a%2Fb", "https://blog.example.com"} {
		u := parse(t, c.SignURL(raw, time.Hour))
		r := httptest.NewRequest(http.MethodGet, u.RequestURI(), nil)
		assert.NoError(t, c.VerifyURL(r.URL), raw)
	}

	// %2F 与 / 是不同的路径。
	u := parse(t, c.SignURL("https://blog.example.com/files/a%2Fb", time.Hour))
	other := parse(t, strings.Replace(u.String(), "a%2Fb", "a/b", 1))
	assert.ErrorIs(t, c.VerifyURL(other), crypt.ErrInvalidSignature)
}

func TestVerifyURLExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newCrypt(t, crypt.GenerateKey())
		u := parse(t, c.SignURL("https://blog.example.com/email/verify/42", time.Hour))
		forever := parse(t, c.SignURL("https://blog.example.com/unsubscribe/42", 0))
		assert.Empty(t, forever.Query().Get("expires"))

		time.Sleep(time.Hour - time.Second)
		assert.NoError(t, c.VerifyURL(u))

		time.Sleep(time.Second)
		err := c.VerifyURL(u)
		assert.ErrorIs(t, err, crypt.ErrExpired)
		assert.Equal(t, errorx.PermissionDenied, errorx.KindOf(err))

		time.Sleep(100 * 365 * 24 * time.Hour)
		assert.NoError(t, c.VerifyURL(forever))
	})
}

func TestVerifyURLRotation(t *testing.T) {
	oldKey, newKey := crypt.GenerateKey(), crypt.GenerateKey()
	link := newCrypt(t, oldKey).SignURL("https://blog.example.com/unsubscribe/7", 0)
	assert.NoError(t, newCrypt(t, newKey, oldKey).VerifyURL(parse(t, link)))
	assert.ErrorIs(t, newCrypt(t, newKey).VerifyURL(parse(t, link)), crypt.ErrInvalidSignature)
}

func TestSignURLPanics(t *testing.T) {
	c := newCrypt(t, crypt.GenerateKey())
	assert.Panics(t, func() { c.SignURL("https://blog.example.com/a", -time.Second) })
	assert.Panics(t, func() { c.SignURL("://bad", time.Hour) })
}
