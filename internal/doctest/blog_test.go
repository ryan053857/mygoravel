package doctest_test

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// docFile 是文档中给出完整代码的一个博客示例文件。
type docFile struct {
	path string // 在博客应用中的路径，正文在代码块之前写出它
	doc  string // docs 目录下的文档
}

// blogFiles 是要编译的完整文件。其余的包在 testdata/blog 中只声明用到的部分。
var blogFiles = []docFile{
	{"cmd/api/main.go", "commands.md"},
	{"internal/cli/cli.go", "commands.md"},
	{"internal/cli/seed.go", "migrations.md"},
	{"internal/auth/auth.go", "authentication.md"},
	{"internal/auth/permissions.go", "authorization.md"},
	{"internal/bootstrap/app.go", "dependencies.md"},
	{"internal/bootstrap/http.go", "dependencies.md"},
	{"internal/bootstrap/worker.go", "queue.md"},
	{"internal/bootstrap/scheduler.go", "scheduling.md"},
	{"internal/auth/social/social.go", "social-login.md"},
	{"internal/auth/authweb/social.go", "social-login.md"},
	{"internal/auth/authhttp/social.go", "social-login.md"},
	{"internal/article/publish.go", "actions.md"},
	{"internal/article/get.go", "routing.md"},
	{"internal/article/routes.go", "routing.md"},
	{"internal/article/dto.go", "routing.md"},
}

// blogFragments 是文档中没有 package 和 import 的片段，补上它们后作为文件编译。
var blogFragments = []struct {
	path, doc, prefix, header string
}{
	{"internal/cli/queue.go", "queue.md", "func queueCmd(", `package cli

import (
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"example.com/blog/internal/settings"
	"github.com/ryan053857/mygoravel/command"
	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/database/postgres"
	"github.com/ryan053857/mygoravel/queue"
)

var _ = queueCmd
`},
	{"internal/bootstrap/mailer.go", "mail.md", "func newMailer(", `package bootstrap

import (
	"example.com/blog/internal/settings"
	"github.com/ryan053857/mygoravel/mail"
	"github.com/ryan053857/mygoravel/mail/logmail"
	"github.com/ryan053857/mygoravel/mail/resend"
	"github.com/ryan053857/mygoravel/mail/smtp"
)
`},
}

var goBlock = regexp.MustCompile("(?s)```go\n(.*?)```")

// TestBlogExampleCompiles 把文档中博客示例的入口代码提取到一个临时 module 中编译，
// 用 go vet 检查（包含编译），防止文档中的完整文件与框架 API 或彼此之间不一致。
func TestBlogExampleCompiles(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("testdata/blog")); err != nil {
		t.Fatal(err)
	}

	for _, f := range blogFiles {
		writeFile(t, dir, f.path, completeFile(t, root, f))
	}
	for _, f := range blogFragments {
		writeFile(t, dir, f.path, f.header+"\n"+fragment(t, root, f.doc, f.prefix))
	}

	gomod := "module example.com/blog\n\ngo 1.27.3\n\nrequire github.com/ryan053857/mygoravel v0.0.0\n\nreplace github.com/ryan053857/mygoravel => " + root + "\n"
	writeFile(t, dir, "go.mod", gomod)
	sum, err := os.ReadFile(filepath.Join(root, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "go.sum", string(sum))

	cmd := exec.Command("go", "vet", "./...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go vet: %v\n%s", err, out)
	}
}

// completeFile 返回 f.doc 中路径为 f.path 的完整文件：以 package 开头、
// 并且在它与上一个代码块之间的正文中写出了 f.path 的代码块。每个路径只能出现一次。
func completeFile(t *testing.T, root string, f docFile) string {
	t.Helper()
	text := readDoc(t, root, f.doc)
	var found []string
	prev := 0
	for _, m := range goBlock.FindAllStringSubmatchIndex(text, -1) {
		prose, body := text[prev:m[0]], text[m[2]:m[3]]
		prev = m[1]
		if strings.HasPrefix(body, "package ") && strings.Contains(prose, "`"+f.path+"`") {
			found = append(found, body)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%s: found %d complete files for %s, want 1", f.doc, len(found), f.path)
	}
	return found[0]
}

// fragment 返回 doc 中以 prefix 开头的唯一代码块。
func fragment(t *testing.T, root, doc, prefix string) string {
	t.Helper()
	var found []string
	for _, m := range goBlock.FindAllStringSubmatch(readDoc(t, root, doc), -1) {
		if strings.HasPrefix(m[1], prefix) {
			found = append(found, m[1])
		}
	}
	if len(found) != 1 {
		t.Fatalf("%s: found %d blocks starting with %q, want 1", doc, len(found), prefix)
	}
	return found[0]
}

func readDoc(t *testing.T, root, name string) string {
	t.Helper()
	b, err := fs.ReadFile(os.DirFS(filepath.Join(root, "docs")), name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func writeFile(t *testing.T, dir, path, content string) {
	t.Helper()
	full := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
