// Package archtest 检查框架包的依赖方向与 HTTP API 交付范围。
package archtest

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const module = "github.com/ryan053857/mygoravel"

// 每个包根可以引用的其他框架包根；同一包树中的引用直接放行。
var dependencies = map[string][]string{
	"cleanup":    {},
	"config":     {},
	"errorx":     {},
	"logging":    {"errorx"},
	"i18n":       {},
	"identity":   {},
	"page":       {"errorx"},
	"database":   {"config"},
	"filesystem": {"errorx", "identity"},
	"mail":       {"i18n"},
	"ratelimit":  {"errorx"},
	"httpx":      {"errorx", "identity", "i18n", "logging", "ratelimit"},
	"session":    {"identity"},
	"web":        {"errorx", "httpx", "i18n", "identity", "logging", "session"},
	"lock":       {},
	"hashing":    {},
	"queue":      {"logging"},
	"command":    {"cleanup", "config", "errorx", "logging", "i18n", "identity", "database", "httpx", "page"},
	"internal":   {"cleanup", "config", "errorx", "logging", "i18n", "identity", "database", "httpx", "page", "command", "filesystem", "mail", "ratelimit", "session", "lock", "hashing", "queue", "web"},
}

// 基础包不引用传输、数据库和命令组件。
var foundation = []string{"cleanup", "config", "errorx", "i18n", "identity"}

// 传输、数据库和命令组件的包前缀。
var infrastructure = []string{
	"net/http", "database/sql", "github.com/danielgtaylor/huma",
	"github.com/go-chi/chi", "github.com/uptrace/bun", "github.com/jackc/pgx",
	"github.com/pressly/goose", "github.com/spf13/cobra", "github.com/redis/go-redis",
}

// 后期应用形态使用的组件，当前 module 保持 HTTP API 依赖范围。
var laterFormModules = []string{
	"github.com/go-co-op/gocron",
	"github.com/gorilla/websocket", "google.golang.org/grpc",
	"github.com/xuri/excelize",
}

func under(pkg, prefix string) bool {
	return pkg == prefix || strings.HasPrefix(pkg, prefix+"/")
}

func rootOf(pkg string) (string, bool) {
	rel, ok := strings.CutPrefix(pkg, module+"/")
	root, _, _ := strings.Cut(rel, "/")
	return root, ok
}

func importViolation(from, to string) string {
	fromRoot, internal := rootOf(from)
	if !internal {
		return "source package is outside the framework module"
	}
	allowed, known := dependencies[fromRoot]
	if !known {
		return "source package has no dependency rule"
	}
	if toRoot, internal := rootOf(to); internal {
		if _, known := dependencies[toRoot]; !known {
			return "target package has no dependency rule"
		}
		if toRoot != fromRoot && !slices.Contains(allowed, toRoot) {
			return "import reverses the framework dependency direction"
		}
	}
	if slices.Contains(foundation, fromRoot) {
		for _, prefix := range infrastructure {
			if under(to, prefix) {
				return "foundation package imports an infrastructure component"
			}
		}
	}
	return ""
}

func TestPackageGraph(t *testing.T) {
	cmd := exec.Command("go", "list", "-f", `{{.ImportPath}}{{range .Imports}} {{.}}{{end}}`, module+"/...")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		pkg := fields[0]
		root, _ := rootOf(pkg)
		if _, known := dependencies[root]; !known {
			t.Errorf("%s: package has no dependency rule", pkg)
			continue
		}
		for _, imp := range fields[1:] {
			if reason := importViolation(pkg, imp); reason != "" {
				t.Errorf("%s imports %s: %s", pkg, imp, reason)
			}
		}
	}
}

func TestDependencyRules(t *testing.T) {
	for _, tc := range []struct {
		from, to string
		allowed  bool
	}{
		{module + "/httpx/rest", module + "/errorx", true},
		{module + "/database/dbtest", module + "/database/migrate", true},
		{module + "/database", module + "/config", true},
		{module + "/config", "go.yaml.in/yaml/v4", true},
		{module + "/errorx", module + "/httpx/rest", false},
		{module + "/httpx", module + "/command", false},
		{module + "/database", module + "/httpx", false},
		{module + "/filesystem", module + "/identity", true},
		{module + "/filesystem", module + "/httpx", false},
		{module + "/database", module + "/filesystem", false},
		{module + "/mail", module + "/i18n", true},
		{module + "/mail", module + "/database", false},
		{module + "/httpx", module + "/mail", false},
		{module + "/session", module + "/httpx/rest", false},
		{module + "/session", module + "/web", false},
		{module + "/web", module + "/session", true},
		{module + "/web", module + "/httpx", true},
		{module + "/httpx", module + "/web", false},
		{module + "/httpx", module + "/session", false},
		{module + "/session", module + "/database", false},
		{module + "/lock", "github.com/uptrace/bun", true},
		{module + "/lock", module + "/database", false},
		{module + "/cleanup", "github.com/jackc/pgx/v5", false},
		{module + "/config", "net/http", false},
		{module + "/i18n", "github.com/spf13/cobra", false},
		{module + "/logging", module + "/identity", false},
		{module + "/identity", module + "/internal/archtest", false},
	} {
		t.Run(tc.from+"->"+tc.to, func(t *testing.T) {
			reason := importViolation(tc.from, tc.to)
			if (reason == "") != tc.allowed {
				t.Fatalf("allowed=%v reason=%q", tc.allowed, reason)
			}
		})
	}
}

// TestLocalFilesDoesNotLinkS3 确保仅使用本地文件服务不会链接 S3 SDK。
func TestLocalFilesDoesNotLinkS3(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", module+"/filesystem/local").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	for _, dep := range strings.Fields(string(out)) {
		if under(dep, "github.com/aws/aws-sdk-go-v2") {
			t.Errorf("local filesystem depends on %s", dep)
		}
	}
}

// TestMailCoreDoesNotLinkDrivers 确保只用 mail 核心包（例如在测试中用 mailtest）不会链接各驱动的 SDK。
func TestMailCoreDoesNotLinkDrivers(t *testing.T) {
	for _, pkg := range []string{module + "/mail", module + "/mail/mailtest", module + "/mail/logmail"} {
		out, err := exec.Command("go", "list", "-deps", pkg).CombinedOutput()
		if err != nil {
			t.Fatalf("go list: %v\n%s", err, out)
		}
		for _, dep := range strings.Fields(string(out)) {
			if under(dep, "github.com/wneessen/go-mail") || under(dep, "github.com/resend/resend-go") {
				t.Errorf("%s depends on %s", pkg, dep)
			}
		}
	}
}

func TestDependencyScope(t *testing.T) {
	cmd := exec.Command("go", "list", "-m", "-f", `{{if not .Indirect}}{{.Path}}{{end}}`, "all")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -m: %v\n%s", err, out)
	}
	for _, path := range strings.Fields(string(out)) {
		for _, prefix := range laterFormModules {
			if under(path, prefix) {
				t.Errorf("HTTP API module imports later-form component %s", path)
			}
		}
	}
}

// TestAPIDoesNotLinkTempl 确保 JSON API 与会话存储不会链接 templ，templ 只由 web 引入。
func TestAPIDoesNotLinkTempl(t *testing.T) {
	for _, pkg := range []string{module + "/httpx", module + "/httpx/rest", module + "/session"} {
		out, err := exec.Command("go", "list", "-deps", pkg).CombinedOutput()
		if err != nil {
			t.Fatalf("go list: %v\n%s", err, out)
		}
		for _, dep := range strings.Fields(string(out)) {
			if under(dep, "github.com/a-h/templ") {
				t.Errorf("%s depends on %s", pkg, dep)
			}
		}
	}
}

// TestCoreDoesNotLinkRedis 确保不用 Redis 的应用不会链接 go-redis，
// Redis 实现分别在 lock/redislock 和 ratelimit/redislimit 中。
func TestCoreDoesNotLinkRedis(t *testing.T) {
	for _, pkg := range []string{module + "/lock", module + "/ratelimit", module + "/httpx"} {
		out, err := exec.Command("go", "list", "-deps", pkg).CombinedOutput()
		if err != nil {
			t.Fatalf("go list: %v\n%s", err, out)
		}
		for _, dep := range strings.Fields(string(out)) {
			if under(dep, "github.com/redis/go-redis") {
				t.Errorf("%s depends on %s", pkg, dep)
			}
		}
	}
}

// TestCommandCoreIsLight 确保只用 command 核心包的应用（例如常驻 worker）
// 不会链接 HTTP API 和数据库组件；它们分别在 command/restcmd 和 command/migratecmd 中。
func TestCommandCoreIsLight(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", module+"/command").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	for _, dep := range strings.Fields(string(out)) {
		for _, heavy := range []string{module + "/httpx", module + "/database", "github.com/danielgtaylor/huma", "github.com/jackc/pgx", "github.com/pressly/goose"} {
			if under(dep, heavy) {
				t.Errorf("command depends on %s", dep)
			}
		}
	}
}

// TestDatabaseCoreDoesNotLinkDrivers 确保应用只链接自己 import 的数据库驱动：
// 核心包、迁移与迁移命令都不依赖 pgx 或 SQLite，两个驱动子包也互不依赖。
func TestDatabaseCoreDoesNotLinkDrivers(t *testing.T) {
	pgx, sqlite := []string{"github.com/jackc/pgx"}, []string{"modernc.org/sqlite"}
	for pkg, forbidden := range map[string][]string{
		module + "/database":           append(pgx, sqlite...),
		module + "/database/migrate":   append(pgx, sqlite...),
		module + "/command/migratecmd": append(pgx, sqlite...),
		module + "/database/postgres":  sqlite,
		module + "/database/sqlite":    pgx,
	} {
		out, err := exec.Command("go", "list", "-deps", pkg).CombinedOutput()
		if err != nil {
			t.Fatalf("go list: %v\n%s", err, out)
		}
		for _, dep := range strings.Fields(string(out)) {
			for _, driver := range forbidden {
				if under(dep, driver) {
					t.Errorf("%s depends on %s", pkg, dep)
				}
			}
		}
	}
}
