package httpx_test

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"

	"github.com/ryan053857/mygoravel/httpx"
)

var bigJSON = `{"items":[` + strings.Repeat(`{"title":"hello world"},`, 200) + `{}]}`

func compressed(h http.Handler, method, accept string, header http.Header) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/", nil)
	if accept != "" {
		req.Header.Set("Accept-Encoding", accept)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	httpx.Compress()(h).ServeHTTP(rec, req)
	return rec
}

func respond(contentType, body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		io.WriteString(w, body)
	})
}

func TestCompressEncodings(t *testing.T) {
	h := respond("application/json", bigJSON)
	for accept, want := range map[string]string{
		"gzip":            "gzip",
		"gzip, deflate":   "gzip",
		"gzip, zstd":      "zstd",
		"zstd;q=0.5,gzip": "gzip",
	} {
		rec := compressed(h, "GET", accept, nil)
		if got := rec.Header().Get("Content-Encoding"); got != want {
			t.Fatalf("Accept-Encoding %q: Content-Encoding = %q, want %q", accept, got, want)
		}
		if !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") {
			t.Errorf("Vary = %q", rec.Header().Get("Vary"))
		}
		if rec.Body.Len() >= len(bigJSON) {
			t.Errorf("%s body not smaller: %d", want, rec.Body.Len())
		}
		var r io.Reader
		if want == "gzip" {
			zr, err := gzip.NewReader(rec.Body)
			if err != nil {
				t.Fatal(err)
			}
			r = zr
		} else {
			zr, err := zstd.NewReader(rec.Body)
			if err != nil {
				t.Fatal(err)
			}
			defer zr.Close()
			r = zr
		}
		if b, err := io.ReadAll(r); err != nil || string(b) != bigJSON {
			t.Fatalf("%s decoded = %d bytes, %v", want, len(b), err)
		}
	}
}

func TestCompressSkips(t *testing.T) {
	png := "\x89PNG\r\n\x1a\n" + strings.Repeat("x", 4096)
	brotli := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		w.Header().Set("Content-Encoding", "br")
		io.WriteString(w, bigJSON)
	})
	for name, c := range map[string]struct {
		h        http.Handler
		accept   string
		body, ce string
	}{
		"no accept-encoding": {respond("application/json", bigJSON), "", bigJSON, ""},
		"small":              {respond("application/json", `{"ok":true}`), "gzip", `{"ok":true}`, ""},
		"image":              {respond("image/png", png), "gzip", png, ""},
		"octet-stream":       {respond("application/octet-stream", bigJSON), "gzip", bigJSON, ""},
		"already encoded":    {brotli, "gzip, br", bigJSON, "br"},
	} {
		rec := compressed(c.h, "GET", c.accept, nil)
		if ce := rec.Header().Get("Content-Encoding"); ce != c.ce || rec.Body.String() != c.body {
			t.Errorf("%s: Content-Encoding = %q, body = %d bytes", name, ce, rec.Body.Len())
		}
	}
}

func TestCompressContentTypes(t *testing.T) {
	for ct, want := range map[string]bool{
		"text/html; charset=utf-8":  true,
		"text/css":                  true,
		"application/json":          true,
		"application/problem+json":  true,
		"application/javascript":    true,
		"image/svg+xml":             true,
		"application/wasm":          true,
		"text/event-stream":         false,
		"image/png":                 false,
		"font/woff2":                false,
		"application/pdf":           false,
		"application/zip":           false,
		"application/octet-stream":  false,
		"application/vnd.ms-excel":  false,
		"application/x-ndjson":      false,
		"video/mp4":                 false,
		"application/xml":           true,
		"application/atom+xml":      true,
		"text/plain; charset=utf-8": true,
	} {
		rec := compressed(respond(ct, bigJSON), "GET", "gzip", nil)
		if got := rec.Header().Get("Content-Encoding") == "gzip"; got != want {
			t.Errorf("%s: compressed = %v, want %v", ct, got, want)
		}
	}
}

// ServeContent 的条件请求、HEAD 和 Range 在压缩下照常工作。
func TestCompressServeContent(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		http.ServeContent(w, r, "app.css", time.Time{}, strings.NewReader(bigJSON))
	})
	rec := compressed(h, "GET", "gzip", nil)
	if rec.Header().Get("Content-Encoding") != "gzip" || rec.Header().Get("ETag") != `"v1"` {
		t.Fatalf("headers = %v", rec.Header())
	}
	if rec.Header().Get("Accept-Ranges") != "" || rec.Header().Get("Content-Length") != "" {
		t.Errorf("Accept-Ranges/Content-Length kept: %v", rec.Header())
	}

	rec = compressed(h, "GET", "gzip", http.Header{"If-None-Match": {`"v1"`}})
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 || rec.Header().Get("Content-Encoding") != "" {
		t.Errorf("If-None-Match: %d, %d bytes, %v", rec.Code, rec.Body.Len(), rec.Header())
	}

	rec = compressed(h, "GET", "gzip", http.Header{"Range": {"bytes=0-9"}})
	if rec.Code != http.StatusPartialContent || rec.Body.String() != bigJSON[:10] || rec.Header().Get("Content-Encoding") != "" {
		t.Errorf("Range: %d, %q, %v", rec.Code, rec.Body.String(), rec.Header())
	}

	rec = compressed(h, "HEAD", "gzip", nil)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("HEAD: %d, %d bytes", rec.Code, rec.Body.Len())
	}
}

// SSE 的响应头立即送达，每条事件随即送达，不经过压缩缓冲。
func TestCompressSSE(t *testing.T) {
	send := make(chan string)
	h := httpx.Compress()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.SSE(w, r, func(s *httpx.Stream) error {
			for {
				select {
				case <-s.Done():
					return nil
				case data := <-send:
					if err := s.Send(httpx.Event{}, data); err != nil {
						return err
					}
				}
			}
		})
	}))
	base, _ := startServer(t, httpx.DefaultConfig(), h)

	req, _ := http.NewRequest("GET", base, nil)
	req.Header.Set("Accept-Encoding", "gzip, zstd")
	got := make(chan *http.Response, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Error(err)
			close(got)
			return
		}
		got <- resp
	}()
	var resp *http.Response
	select {
	case resp = <-got:
		if resp == nil {
			t.FailNow()
		}
	case <-time.After(2 * time.Second):
		t.Fatal("response headers not flushed")
	}
	defer resp.Body.Close()
	if ce := resp.Header.Get("Content-Encoding"); ce != "" {
		t.Fatalf("Content-Encoding = %q", ce)
	}
	lines := make(chan string, 8)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	for _, data := range []string{"one", "two"} {
		send <- data
		select {
		case line := <-lines:
			if line != "data: "+data {
				t.Fatalf("line = %q", line)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("event %q not flushed", data)
		}
		<-lines // 事件之间的空行
	}
}

// WebSocket 握手原样交给下一个处理器，可以接管连接。
func TestCompressUpgrade(t *testing.T) {
	h := httpx.Compress()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if w.Header().Get("Vary") != "" {
			t.Errorf("Vary = %q", w.Header().Get("Vary"))
		}
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		rw.Flush()
	}))
	base, _ := startServer(t, httpx.DefaultConfig(), h)

	conn, err := net.Dial("tcp", strings.TrimPrefix(base, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, "GET / HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nAccept-Encoding: gzip\r\n\r\n")
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

// 处理器只写响应头时，状态码照常送出。
func TestCompressStatusOnly(t *testing.T) {
	rec := compressed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), "DELETE", "gzip", nil)
	if rec.Code != http.StatusNoContent || rec.Header().Get("Content-Encoding") != "" || !bytes.Equal(rec.Body.Bytes(), nil) {
		t.Errorf("%d %v %q", rec.Code, rec.Header(), rec.Body.String())
	}
}
