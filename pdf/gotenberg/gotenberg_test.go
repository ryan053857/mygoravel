package gotenberg_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/a-h/templ"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/pdf"
	"github.com/ryan053857/mygoravel/pdf/gotenberg"
)

func html(s string) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, s)
		return err
	})
}

// request 是测试服务器收到的一次请求。
type request struct {
	path       string
	files      map[string]string
	fields     map[string]string
	user, pass string
	auth       bool
}

func server(t *testing.T, status int, body string) (*gotenberg.Renderer, *request) {
	t.Helper()
	got := &request{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path = r.URL.Path
		got.user, got.pass, got.auth = r.BasicAuth()
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		got.files = map[string]string{}
		for _, fh := range r.MultipartForm.File["files"] {
			f, err := fh.Open()
			require.NoError(t, err)
			data, err := io.ReadAll(f)
			require.NoError(t, err)
			got.files[fh.Filename] = string(data)
		}
		got.fields = map[string]string{}
		for k, v := range r.MultipartForm.Value {
			got.fields[k] = v[0]
		}
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	r, err := gotenberg.New(gotenberg.Config{URL: srv.URL + "/"})
	require.NoError(t, err)
	return r, got
}

func TestRender(t *testing.T) {
	t.Parallel()
	r, got := server(t, http.StatusOK, "%PDF-1.4 fake")
	data, err := r.Render(context.Background(), pdf.Document{
		HTML: html(`<link rel="stylesheet" href="invoice.css"><h1>发票</h1>`),
		Assets: fstest.MapFS{
			"invoice.css": {Data: []byte("@page { size: A5 }")},
			"logo.svg":    {Data: []byte("<svg/>")},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "%PDF-1.4 fake", string(data))
	assert.Equal(t, "/forms/chromium/convert/html", got.path)
	assert.Equal(t, map[string]string{
		"index.html":  `<link rel="stylesheet" href="invoice.css"><h1>发票</h1>`,
		"invoice.css": "@page { size: A5 }",
		"logo.svg":    "<svg/>",
	}, got.files)
	assert.Equal(t, map[string]string{
		"paperWidth":                  "210mm",
		"paperHeight":                 "297mm",
		"preferCssPageSize":           "true",
		"printBackground":             "true",
		"failOnResourceLoadingFailed": "true",
	}, got.fields)
	assert.False(t, got.auth)
}

func TestRenderWithoutAssets(t *testing.T) {
	t.Parallel()
	r, got := server(t, http.StatusOK, "%PDF")
	_, err := r.Render(context.Background(), pdf.Document{HTML: html("<p>hi</p>")})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"index.html": "<p>hi</p>"}, got.files)
}

func TestBasicAuth(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "pdf" || p != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		io.WriteString(w, "%PDF")
	}))
	t.Cleanup(srv.Close)
	r, err := gotenberg.New(gotenberg.Config{URL: srv.URL, Username: "pdf", Password: "secret"})
	require.NoError(t, err)
	_, err = r.Render(context.Background(), pdf.Document{HTML: html("x")})
	assert.NoError(t, err)
}

func TestRenderErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		status      int
		unavailable bool
	}{
		{http.StatusTooManyRequests, true},
		{http.StatusServiceUnavailable, true},
		{http.StatusConflict, false},
		{http.StatusBadRequest, false},
		{http.StatusInternalServerError, false},
	} {
		r, _ := server(t, tc.status, "Chromium failed to load resources: resource Stylesheet: net::ERR_FILE_NOT_FOUND\n")
		_, err := r.Render(context.Background(), pdf.Document{HTML: html("x")})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "net::ERR_FILE_NOT_FOUND", tc.status)
		assert.Equal(t, tc.unavailable, errorx.KindOf(err) == errorx.Unavailable, tc.status)
	}
}

func TestRenderUnreachable(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	r, err := gotenberg.New(gotenberg.Config{URL: srv.URL})
	require.NoError(t, err)
	_, err = r.Render(context.Background(), pdf.Document{HTML: html("x")})
	assert.ErrorIs(t, err, pdf.ErrUnavailable)
}

func TestRenderCanceled(t *testing.T) {
	t.Parallel()
	// 处理器一直不响应，直到测试结束。不能只等 r.Context()：请求体还没读完时，
	// net/http 发现不了客户端已经断开，处理器会一直阻塞，srv.Close 也就等不到它返回。
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) }) // 先于 srv.Close 执行
	r, err := gotenberg.New(gotenberg.Config{URL: srv.URL})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	go cancel()
	_, err = r.Render(ctx, pdf.Document{HTML: html("x")})
	assert.ErrorIs(t, err, context.Canceled)
	assert.NotErrorIs(t, err, pdf.ErrUnavailable)
}

func TestInvalidDocument(t *testing.T) {
	t.Parallel()
	r, got := server(t, http.StatusOK, "%PDF")
	for name, doc := range map[string]pdf.Document{
		"no html":  {},
		"subdir":   {HTML: html("x"), Assets: fstest.MapFS{"css/app.css": {Data: []byte("")}}},
		"reserved": {HTML: html("x"), Assets: fstest.MapFS{"index.html": {Data: []byte("")}}},
		"bad html": {HTML: templ.ComponentFunc(func(context.Context, io.Writer) error { return io.ErrUnexpectedEOF })},
	} {
		_, err := r.Render(context.Background(), doc)
		assert.Error(t, err, name)
	}
	assert.Empty(t, got.path, "invalid documents must not reach Gotenberg")
}

func TestConfig(t *testing.T) {
	t.Parallel()
	assert.NoError(t, gotenberg.Config{URL: "http://gotenberg:3000"}.Validate())
	assert.NoError(t, gotenberg.Config{URL: "https://pdf.internal"}.Validate())
	for _, u := range []string{"", "gotenberg:3000", "ftp://x", "http://"} {
		assert.Error(t, gotenberg.Config{URL: u}.Validate(), u)
	}
	_, err := gotenberg.New(gotenberg.Config{})
	assert.ErrorContains(t, err, "gotenberg: url is required")
}
