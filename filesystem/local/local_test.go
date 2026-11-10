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

	"github.com/ryan053857/mygoravel/filesystem/local"
)

func openDisk(t *testing.T, publicURL string) (*local.Disk, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "files")
	disk, err := local.Open(local.Config{Root: root, PublicURL: publicURL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = disk.Close() })
	return disk, root
}

func TestConfigValidate(t *testing.T) {
	for _, u := range []string{"/storage", "https://cdn.example.com/files", "http://localhost:8080/storage/"} {
		if err := (local.Config{Root: "x", PublicURL: u}).Validate(); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
	for _, u := range []string{"", "storage", "ftp://x/y", "https:///nohost", "/s?x=1"} {
		if err := (local.Config{Root: "x", PublicURL: u}).Validate(); err == nil {
			t.Errorf("%q accepted", u)
		}
	}
	if err := (local.Config{PublicURL: "/s"}).Validate(); err == nil {
		t.Error("empty root accepted")
	}
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
