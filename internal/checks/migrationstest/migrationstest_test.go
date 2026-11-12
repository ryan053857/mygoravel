// Package migrationstest 检查框架各个包内嵌的迁移：每个包都登记在这里，并且能通过公开的 publish 命令原样发布。
package migrationstest

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ryan053857/mygoravel/activity"
	"github.com/ryan053857/mygoravel/ai/chat"
	"github.com/ryan053857/mygoravel/apitoken"
	"github.com/ryan053857/mygoravel/authz"
	"github.com/ryan053857/mygoravel/command"
	"github.com/ryan053857/mygoravel/command/migratecmd"
	"github.com/ryan053857/mygoravel/database/migrate"
	"github.com/ryan053857/mygoravel/feature"
	"github.com/ryan053857/mygoravel/filesystem"
	"github.com/ryan053857/mygoravel/login"
	"github.com/ryan053857/mygoravel/login/passkey"
	"github.com/ryan053857/mygoravel/notification"
	"github.com/ryan053857/mygoravel/operation"
	"github.com/ryan053857/mygoravel/push"
	"github.com/ryan053857/mygoravel/queue"
	"github.com/ryan053857/mygoravel/search"
	"github.com/ryan053857/mygoravel/session"
	"github.com/ryan053857/mygoravel/sheet/exports"
	"github.com/ryan053857/mygoravel/sheet/imports"
	"github.com/ryan053857/mygoravel/webhook"
)

// sources 是框架中带迁移的包：键是包相对模块根的目录，Name 是文档中发布它时使用的来源名。
// 新增带迁移的包时在这里登记，TestEveryPackageIsListed 检查没有遗漏。
var sources = map[string]migrate.Source{
	"activity":      {Name: "activity", FS: activity.Migrations()},
	"ai/chat":       {Name: "ai", FS: chat.Migrations()},
	"apitoken":      {Name: "apitoken", FS: apitoken.Migrations()},
	"authz":         {Name: "authz", FS: authz.Migrations()},
	"sheet/exports": {Name: "exports", FS: exports.Migrations()},
	"feature":       {Name: "feature", FS: feature.Migrations()},
	"filesystem":    {Name: "filesystem", FS: filesystem.Migrations()},
	"sheet/imports": {Name: "imports", FS: imports.Migrations()},
	"login":         {Name: "login", FS: login.Migrations()},
	"login/passkey": {Name: "passkey", FS: passkey.Migrations()},
	"notification":  {Name: "notification", FS: notification.Migrations()},
	"operation":     {Name: "operation", FS: operation.Migrations()},
	"push":          {Name: "push", FS: push.Migrations()},
	"queue":         {Name: "queue", FS: queue.Migrations()},
	"search":        {Name: "search", FS: search.Migrations()},
	"session":       {Name: "session", FS: session.Migrations()},
	"webhook":       {Name: "webhook", FS: webhook.Migrations()},
}

// TestEveryPackageIsListed 确保仓库中每个 migrations 目录都登记在 sources 中。
func TestEveryPackageIsListed(t *testing.T) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	root := strings.TrimSpace(string(out))
	var found []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}
		switch d.Name() {
		case ".git", "testdata":
			return filepath.SkipDir
		case "migrations":
			rel, err := filepath.Rel(root, filepath.Dir(path))
			if err != nil {
				return err
			}
			found = append(found, filepath.ToSlash(rel))
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range found {
		if _, ok := sources[dir]; !ok {
			t.Errorf("%s has migrations but is not listed in sources", dir)
		}
	}
	for dir := range sources {
		if !slices.Contains(found, dir) {
			t.Errorf("%s is listed in sources but has no migrations directory", dir)
		}
	}
}

// TestPublishThroughPublicCommand 确保内嵌的 SQL 能通过公开的 publish 命令原样发布，重复发布不再写入。
func TestPublishThroughPublicCommand(t *testing.T) {
	for dir, src := range sources {
		t.Run(dir, func(t *testing.T) {
			embedded := src.FS
			entries, err := fs.ReadDir(embedded, ".")
			if err != nil || len(entries) == 0 {
				t.Fatalf("embedded migrations=%v err=%v", entries, err)
			}
			target := filepath.Join(t.TempDir(), "migrations")
			run := func() []string {
				t.Helper()
				root := command.Root("api", "test")
				root.AddCommand(migratecmd.Publish(src))
				root.SetArgs([]string{"publish", "--dir", target, "--config", filepath.Join(target, "absent.yaml")})
				var output bytes.Buffer
				root.SetOut(&output)
				if err := root.Execute(); err != nil {
					t.Fatal(err)
				}
				return strings.Fields(output.String())
			}
			names := run()
			if len(names) != len(entries) {
				t.Fatalf("published=%v embedded=%v", names, entries)
			}
			for _, entry := range entries {
				original, err := fs.ReadFile(embedded, entry.Name())
				if err != nil {
					t.Fatal(err)
				}
				i := slices.IndexFunc(names, func(name string) bool {
					return strings.HasSuffix(name, "_"+src.Name+"_"+entry.Name())
				})
				if i < 0 {
					t.Fatalf("missing published %s in %v", entry.Name(), names)
				}
				published, err := os.ReadFile(filepath.Join(target, names[i]))
				if err != nil {
					t.Fatal(err)
				}
				want := append([]byte("-- mygoravel: "+src.Name+"/"+strings.TrimSuffix(entry.Name(), ".sql")+"\n"), original...)
				if !bytes.Equal(published, want) {
					t.Fatalf("published content differs from embedded SQL: %s", names[i])
				}
			}
			if again := run(); len(again) != 0 {
				t.Fatalf("repeat published=%v", again)
			}
		})
	}
}
