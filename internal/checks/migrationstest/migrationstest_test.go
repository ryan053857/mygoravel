// Package migrationstest 检查框架各个包内嵌的迁移：每个包都登记在这里，三种数据库的迁移文件一一对应，
// 并且能通过公开的 publish 命令原样发布。
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
	"github.com/ryan053857/mygoravel/broadcast"
	"github.com/ryan053857/mygoravel/command"
	"github.com/ryan053857/mygoravel/command/migratecmd"
	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/database/migrate"
	"github.com/ryan053857/mygoravel/database/mysql"
	"github.com/ryan053857/mygoravel/database/postgres"
	"github.com/ryan053857/mygoravel/database/sqlite"
	"github.com/ryan053857/mygoravel/feature"
	"github.com/ryan053857/mygoravel/filesystem"
	"github.com/ryan053857/mygoravel/lock"
	"github.com/ryan053857/mygoravel/login"
	"github.com/ryan053857/mygoravel/notification"
	"github.com/ryan053857/mygoravel/operation"
	"github.com/ryan053857/mygoravel/queue"
	"github.com/ryan053857/mygoravel/ratelimit"
	"github.com/ryan053857/mygoravel/realtime"
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
	"broadcast":     {Name: "broadcast", FS: broadcast.Migrations()},
	"sheet/exports": {Name: "exports", FS: exports.Migrations()},
	"feature":       {Name: "feature", FS: feature.Migrations()},
	"filesystem":    {Name: "filesystem", FS: filesystem.Migrations()},
	"sheet/imports": {Name: "imports", FS: imports.Migrations()},
	"lock":          {Name: "lock", FS: lock.Migrations()},
	"login":         {Name: "login", FS: login.Migrations()},
	"notification":  {Name: "notification", FS: notification.Migrations()},
	"operation":     {Name: "operation", FS: operation.Migrations()},
	"queue":         {Name: "queue", FS: queue.Migrations()},
	"ratelimit":     {Name: "ratelimit", FS: ratelimit.Migrations()},
	"realtime":      {Name: "realtime", FS: realtime.Migrations()},
	"search":        {Name: "search", FS: search.Migrations()},
	"session":       {Name: "session", FS: session.Migrations()},
	"webhook":       {Name: "webhook", FS: webhook.Migrations()},
}

var drivers = []database.Driver{postgres.Driver, mysql.Driver, sqlite.Driver}

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

// TestDialectsHaveTheSameFiles 确保每个包在三种数据库下的迁移文件一一对应：
// 同名文件描述同一次变更，发布时共用一个来源标记。
func TestDialectsHaveTheSameFiles(t *testing.T) {
	for dir, src := range sources {
		t.Run(dir, func(t *testing.T) {
			var want []string
			for _, driver := range drivers {
				entries, err := fs.ReadDir(src.FS, driver.Name())
				if err != nil || len(entries) == 0 {
					t.Fatalf("%s: entries=%v err=%v", driver.Name(), entries, err)
				}
				var names []string
				for _, e := range entries {
					names = append(names, e.Name())
				}
				if want == nil {
					want = names
				} else if !slices.Equal(names, want) {
					t.Errorf("%s has %v, %s has %v", driver.Name(), names, drivers[0].Name(), want)
				}
			}
		})
	}
}

// TestPublishThroughPublicCommand 确保内嵌的 SQL 能通过公开的 publish 命令原样发布，重复发布不再写入。
func TestPublishThroughPublicCommand(t *testing.T) {
	for dir, src := range sources {
		for _, driver := range drivers {
			t.Run(dir+"/"+driver.Name(), func(t *testing.T) {
				embedded, err := fs.Sub(src.FS, driver.Name())
				if err != nil {
					t.Fatal(err)
				}
				entries, err := fs.ReadDir(embedded, ".")
				if err != nil || len(entries) == 0 {
					t.Fatalf("embedded migrations=%v err=%v", entries, err)
				}
				target := filepath.Join(t.TempDir(), "migrations")
				run := func() []string {
					t.Helper()
					root := command.Root("api", "test")
					root.AddCommand(migratecmd.Publish(driver, src))
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
}
