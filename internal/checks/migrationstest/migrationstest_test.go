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

	"github.com/go-tenon/tenon/activity"
	"github.com/go-tenon/tenon/ai/chat"
	"github.com/go-tenon/tenon/apitoken"
	"github.com/go-tenon/tenon/authz"
	"github.com/go-tenon/tenon/command"
	"github.com/go-tenon/tenon/command/migratecmd"
	"github.com/go-tenon/tenon/database/migrate"
	"github.com/go-tenon/tenon/feature"
	"github.com/go-tenon/tenon/filesystem"
	"github.com/go-tenon/tenon/login"
	"github.com/go-tenon/tenon/login/passkey"
	"github.com/go-tenon/tenon/notification"
	"github.com/go-tenon/tenon/oauthserver"
	"github.com/go-tenon/tenon/operation"
	"github.com/go-tenon/tenon/push"
	"github.com/go-tenon/tenon/queue"
	"github.com/go-tenon/tenon/search"
	"github.com/go-tenon/tenon/settings"
	"github.com/go-tenon/tenon/sheet/exports"
	"github.com/go-tenon/tenon/sheet/imports"
	"github.com/go-tenon/tenon/vector"
	"github.com/go-tenon/tenon/webhook"
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
	"oauthserver":   {Name: "oauthserver", FS: oauthserver.Migrations()},
	"operation":     {Name: "operation", FS: operation.Migrations()},
	"push":          {Name: "push", FS: push.Migrations()},
	"queue":         {Name: "queue", FS: queue.Migrations()},
	"search":        {Name: "search", FS: search.Migrations()},
	"vector":        {Name: "vector", FS: vector.Migrations()},
	"settings":      {Name: "settings", FS: settings.Migrations()},
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
