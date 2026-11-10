//go:build integration

package consumetest_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const framework = "github.com/ryan053857/mygoravel"

// 通过 GOPROXY 下载候选模块并运行公开 API 测试。
func TestExternalModuleConsumesFramework(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	proxy := t.TempDir()
	dir := t.TempDir()
	version := fmt.Sprintf("v0.0.0-review.%d", time.Now().UnixNano())
	moduleDir := filepath.Join(proxy, filepath.FromSlash(framework), "@v")
	must(t, os.MkdirAll(moduleDir, 0755))
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	must(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			if rel != "." && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		w, err := zw.Create(framework + "@" + version + "/" + filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	}))
	must(t, zw.Close())
	must(t, os.WriteFile(filepath.Join(moduleDir, version+".zip"), buf.Bytes(), 0644))
	mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	must(t, err)
	must(t, os.WriteFile(filepath.Join(moduleDir, version+".mod"), mod, 0644))
	info, _ := json.Marshal(map[string]any{"Version": version, "Time": time.Now().UTC()})
	must(t, os.WriteFile(filepath.Join(moduleDir, version+".info"), info, 0644))
	must(t, os.WriteFile(filepath.Join(moduleDir, "list"), []byte(version+"\n"), 0644))
	// 在独立 module 中运行框架的公开契约测试。
	for _, pkg := range []string{"httpx/rest", "config", "errorx", "command", "command/restcmd", "command/migratecmd", "filesystem", "filesystem/local", "filesystem/s3", "mail", "mail/mailtest", "mail/logmail", "mail/smtp", "mail/resend", "session", "lock"} {
		destination := filepath.Join(dir, filepath.Base(pkg))
		must(t, os.MkdirAll(destination, 0755))
		files, err := filepath.Glob(filepath.Join(root, pkg, "*_test.go"))
		must(t, err)
		for _, file := range files {
			source, err := os.ReadFile(file)
			must(t, err)
			must(t, os.WriteFile(filepath.Join(destination, filepath.Base(file)), source, 0644))
		}
	}
	must(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module consumer.example\n\ngo 1.27.3\n\nrequire "+framework+" "+version+"\n"), 0644))
	proxyURL := (&url.URL{Scheme: "file", Path: filepath.ToSlash(proxy)}).String()
	upstream, err := exec.Command("go", "env", "GOPROXY").Output()
	must(t, err)
	env := append(os.Environ(), "GOWORK=off", "GOPROXY="+proxyURL+","+strings.TrimSpace(string(upstream)), "GONOSUMDB="+framework, "GONOPROXY=none", "GOFLAGS=-mod=mod")
	run := func(args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "go", args...)
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %v: %v\n%s", args, err, out)
		}
		return out
	}
	run("mod", "tidy")
	resolved := run("list", "-m", "-json", framework)
	var module struct {
		Dir     string
		Replace any
	}
	must(t, json.Unmarshal(resolved, &module))
	if module.Replace != nil || module.Dir == root || module.Dir == "" {
		t.Fatalf("not consuming published candidate: %s", resolved)
	}
	run("test", "-race", "-count=1", "./...")
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
