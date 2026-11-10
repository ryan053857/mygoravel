package local_test

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ryan053857/mygoravel/filesystem"
	"github.com/ryan053857/mygoravel/filesystem/local"
)

func openDisk(t *testing.T, publicURL string) (*local.Disk, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "files")
	disk, err := local.Open(filesystem.LocalConfig{Root: root, PublicURL: publicURL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = disk.Close() })
	return disk, root
}

func TestPutOpenStatDelete(t *testing.T) {
	ctx := context.Background()
	disk, _ := openDisk(t, "https://cdn.example.com/storage/")
	key := "2026/11/10/a b.txt"
	if err := disk.Put(ctx, key, "text/plain", strings.NewReader("hello"), 5); err != nil {
		t.Fatal(err)
	}
	if info, err := disk.Stat(ctx, key); err != nil || info.Size != 5 {
		t.Fatalf("stat = %+v %v", info, err)
	}
	body, err := disk.Open(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(body)
	_ = body.Close()
	if string(data) != "hello" {
		t.Fatalf("data = %q", data)
	}
	if got := disk.URL(key); got != "https://cdn.example.com/storage/2026/11/10/a%20b.txt" {
		t.Fatalf("URL = %s", got)
	}
	for range 2 {
		if err := disk.Delete(ctx, key); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := disk.Stat(ctx, key); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stat after delete = %v", err)
	}
	if err := disk.Put(ctx, "../escape", "", strings.NewReader(""), 0); err == nil {
		t.Fatal("escaping key accepted")
	}
}

type failingReader struct{ n int }

func (f *failingReader) Read(p []byte) (int, error) {
	if f.n == 0 {
		return 0, errors.New("network broken")
	}
	f.n--
	p[0] = 'x'
	return 1, nil
}

func TestPutFailureLeavesNothing(t *testing.T) {
	ctx := context.Background()
	disk, root := openDisk(t, "/storage")
	if err := disk.Put(ctx, "a/b.bin", "", &failingReader{n: 3}, 10); err == nil {
		t.Fatal("expected error")
	}
	entries, err := os.ReadDir(filepath.Join(root, "a"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("leftovers = %v %v", entries, err)
	}
}

func TestServeHTTP(t *testing.T) {
	ctx := context.Background()
	disk, root := openDisk(t, "/storage")
	if err := disk.Put(ctx, "2026/x.txt", "text/plain", strings.NewReader("hi"), 2); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "2026", ".tmp-pending"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/storage/", disk)
	get := func(path string) (int, string) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code, rec.Body.String()
	}
	if code, body := get("/storage/2026/x.txt"); code != http.StatusOK || body != "hi" {
		t.Fatalf("file = %d %q", code, body)
	}
	for _, p := range []string{"/storage/", "/storage/2026/", "/storage/2026/.tmp-pending", "/storage/missing"} {
		if code, _ := get(p); code != http.StatusNotFound {
			t.Errorf("%s = %d", p, code)
		}
	}
}

func TestOpenRequiresConfig(t *testing.T) {
	if _, err := local.Open(filesystem.LocalConfig{}); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("zero config = %v", err)
	}
	if _, err := local.Open(filesystem.LocalConfig{Root: t.TempDir()}); err == nil {
		t.Fatal("missing public_url accepted")
	}
}

// 上传的 HTML、SVG 在应用的源下打开时不能执行脚本：按 Content-Type 加上 sandbox 并作为附件下载；
// 内容嗅探为 HTML 的无扩展名文件同样处理。所有响应都禁止浏览器再次嗅探。
func TestServeHTTPSecurityHeaders(t *testing.T) {
	ctx := context.Background()
	disk, _ := openDisk(t, "/storage")
	html := "<!DOCTYPE html><script>alert(1)</script>"
	for key, body := range map[string]string{
		"a.html": html, "b.svg": `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`,
		"noext": html, "c.png": "\x89PNG\r\n\x1a\n0000", "d.txt": "hi",
	} {
		if err := disk.Put(ctx, key, "", strings.NewReader(body), int64(len(body))); err != nil {
			t.Fatal(err)
		}
	}
	for key, active := range map[string]bool{"a.html": true, "b.svg": true, "noext": true, "c.png": false, "d.txt": false} {
		for _, rangeHeader := range []string{"", "bytes=0-3"} {
			req := httptest.NewRequest(http.MethodGet, "/storage/"+key, nil)
			if rangeHeader != "" {
				req.Header.Set("Range", rangeHeader)
			}
			rec := httptest.NewRecorder()
			http.StripPrefix("", disk).ServeHTTP(rec, req)
			h := rec.Header()
			if rec.Code >= 300 || h.Get("X-Content-Type-Options") != "nosniff" {
				t.Errorf("%s %q: code=%d headers=%v", key, rangeHeader, rec.Code, h)
			}
			sandboxed := h.Get("Content-Security-Policy") == "sandbox" && h.Get("Content-Disposition") == "attachment"
			if sandboxed != active {
				t.Errorf("%s %q: content-type=%q sandboxed=%v", key, rangeHeader, h.Get("Content-Type"), sandboxed)
			}
		}
	}
}
