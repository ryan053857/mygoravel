// Package archtest 检查框架包的分层、依赖方向与 HTTP API 交付范围。
package archtest

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const module = "github.com/ryan053857/mygoravel"

// layer 是框架包所在的层。包只能引用同层或更低层的包，并且引用必须列在 rules 中。
type layer int

const (
	// 基础：错误、身份、配置、日志等，不依赖数据库、传输和命令组件。
	layerFoundation layer = iota + 1
	// 基础设施：数据库、队列、锁、文件、邮件等，与入口无关。
	layerInfrastructure
	// 入口：HTTP API、页面、实时频道和命令行。
	layerEntry
	// 功能：建立在以上各层之上的应用功能。入口不引用它们。
	layerFeature
)

// rule 是一棵包树的依赖规则。
type rule struct {
	layer layer
	// imports 是它可以引用的其他包树（rules 的键）。只列出实际用到的引用，
	// TestDependencyRulesAreUsed 检查没有多余的条目。
	imports []string
}

// rules 的键是相对模块根的包路径，规则对这个包和它的子包生效；子包另有规则时用子包的（最长的键优先）。
// 同一条规则内的包可以互相引用。internal 中是测试辅助包，可以引用任何框架包。
var rules = map[string]rule{
	"cleanup":   {layerFoundation, nil},
	"config":    {layerFoundation, nil},
	"errorx":    {layerFoundation, nil},
	"i18n":      {layerFoundation, nil},
	"hashing":   {layerFoundation, nil},
	"identity":  {layerFoundation, []string{"errorx"}},
	"logging":   {layerFoundation, []string{"errorx"}},
	"page":      {layerFoundation, []string{"errorx"}},
	"crypt":     {layerFoundation, []string{"errorx"}},
	"form":      {layerFoundation, []string{"i18n"}},
	"telemetry": {layerFoundation, nil},

	"lock":       {layerInfrastructure, nil},
	"outbound":   {layerInfrastructure, nil},
	"database":   {layerInfrastructure, []string{"errorx"}},
	"ratelimit":  {layerInfrastructure, []string{"errorx"}},
	"document":   {layerInfrastructure, []string{"errorx"}},
	"pdf":        {layerInfrastructure, []string{"errorx"}},
	"mail":       {layerInfrastructure, []string{"i18n"}},
	"queue":      {layerInfrastructure, []string{"logging"}},
	"schedule":   {layerInfrastructure, []string{"lock", "logging"}},
	"broadcast":  {layerInfrastructure, []string{"identity", "logging"}},
	"filesystem": {layerInfrastructure, []string{"errorx", "identity"}},
	"sheet":      {layerInfrastructure, []string{"errorx", "form"}},

	"httpx":              {layerEntry, []string{"errorx", "i18n", "identity", "logging", "ratelimit"}},
	"session":            {layerEntry, []string{"errorx", "identity"}},
	"web":                {layerEntry, []string{"errorx", "form", "httpx", "i18n", "identity", "logging", "session"}},
	"web/upload":         {layerEntry, []string{"errorx", "filesystem", "httpx", "identity", "web"}},
	"realtime":           {layerEntry, []string{"broadcast", "httpx", "identity", "logging"}},
	"command":            {layerEntry, []string{"cleanup", "config", "errorx", "logging"}},
	"command/httpcmd":    {layerEntry, []string{"command", "httpx"}},
	"command/migratecmd": {layerEntry, []string{"command", "database"}},

	"apitoken":      {layerFeature, []string{"errorx", "identity"}},
	"authz":         {layerFeature, []string{"errorx", "identity"}},
	"notification":  {layerFeature, []string{"errorx", "identity"}},
	"feature":       {layerFeature, []string{"errorx", "identity"}},
	"activity":      {layerFeature, []string{"identity", "logging"}},
	"operation":     {layerFeature, []string{"database", "errorx", "i18n", "identity", "queue"}},
	"webhook":       {layerFeature, []string{"errorx", "identity", "outbound", "queue"}},
	"sheet/imports": {layerFeature, []string{"database", "errorx", "filesystem", "i18n", "identity", "operation", "sheet"}},
	"sheet/exports": {layerFeature, []string{"database", "filesystem", "identity", "operation", "sheet"}},
	"search":        {layerFeature, []string{"page", "queue"}},
	"search/gse":    {layerFeature, []string{"search"}},
	"ai":            {layerFeature, nil},
	"ai/chat":       {layerFeature, []string{"ai", "broadcast", "database", "errorx", "httpx", "lock", "logging"}},

	"internal": {},
}

// 传输、数据库和命令组件的包前缀，基础层不引用它们。
var components = []string{
	"net/http", "database/sql", "github.com/danielgtaylor/huma",
	"github.com/go-chi/chi", "github.com/uptrace/bun", "github.com/jackc/pgx",
	"github.com/pressly/goose", "github.com/spf13/cobra", "github.com/redis/go-redis",
}

// 基础层中的例外：form 按 Huma 的标签和校验规则转换字段值。
var componentExceptions = map[string]string{"form": "github.com/danielgtaylor/huma"}

// 后期应用形态使用的组件，当前 module 保持 HTTP API 依赖范围。
var laterFormModules = []string{
	"github.com/gorilla/websocket", "google.golang.org/grpc",
}

func under(pkg, prefix string) bool {
	return pkg == prefix || strings.HasPrefix(pkg, prefix+"/")
}

// ruleOf 返回 pkg 适用的规则的键：rules 中是 pkg 或其上级目录的最长的键。pkg 不在本模块中，或没有规则时 ok 为 false。
func ruleOf(pkg string) (key string, ok bool) {
	rel, internal := strings.CutPrefix(pkg, module+"/")
	if !internal {
		return "", false
	}
	for k := range rules {
		if under(rel, k) && len(k) > len(key) {
			key, ok = k, true
		}
	}
	return key, ok
}

func importViolation(from, to string) string {
	if !under(from, module) {
		return "source package is outside the framework module"
	}
	fromKey, known := ruleOf(from)
	if !known {
		return "source package has no dependency rule"
	}
	fromRule := rules[fromKey]
	if under(to, module) {
		toKey, known := ruleOf(to)
		if !known {
			return "target package has no dependency rule"
		}
		if toKey != fromKey && fromKey != "internal" {
			if rules[toKey].layer > fromRule.layer {
				return "import goes up a layer"
			}
			if !slices.Contains(fromRule.imports, toKey) {
				return "import reverses the framework dependency direction"
			}
		}
	}
	if fromRule.layer == layerFoundation {
		for _, prefix := range components {
			if under(to, prefix) && componentExceptions[fromKey] != prefix {
				return "foundation package imports an infrastructure component"
			}
		}
	}
	return ""
}

// imports 返回模块中每个包直接引用的包。
func imports(t *testing.T) map[string][]string {
	t.Helper()
	cmd := exec.Command("go", "list", "-f", `{{.ImportPath}}{{range .Imports}} {{.}}{{end}}`, module+"/...")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	graph := map[string][]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		graph[fields[0]] = fields[1:]
	}
	return graph
}

func TestPackageGraph(t *testing.T) {
	for pkg, imps := range imports(t) {
		if _, known := ruleOf(pkg); !known {
			t.Errorf("%s: package has no dependency rule", pkg)
			continue
		}
		for _, imp := range imps {
			if reason := importViolation(pkg, imp); reason != "" {
				t.Errorf("%s imports %s: %s", pkg, imp, reason)
			}
		}
	}
}

// TestRulesRespectLayers 确保 rules 本身符合分层：每条规则放行的引用都在同层或更低层，并且指向存在的规则。
func TestRulesRespectLayers(t *testing.T) {
	for key, r := range rules {
		if key != "internal" && (r.layer < layerFoundation || r.layer > layerFeature) {
			t.Errorf("%s: no layer", key)
		}
		for _, to := range r.imports {
			target, known := rules[to]
			switch {
			case !known:
				t.Errorf("%s may import %s, which has no rule", key, to)
			case target.layer > r.layer:
				t.Errorf("%s (layer %d) may import %s (layer %d): imports only go to the same or a lower layer", key, r.layer, to, target.layer)
			}
		}
	}
}

// TestDependencyRulesAreUsed 确保 rules 中没有用不到的条目：放行了却没有用到的引用，
// 以后被悄悄用上时不会有人注意到。
func TestDependencyRulesAreUsed(t *testing.T) {
	used := map[string]map[string]bool{}
	for pkg, imps := range imports(t) {
		from, _ := ruleOf(pkg)
		if used[from] == nil {
			used[from] = map[string]bool{}
		}
		for _, imp := range imps {
			if to, internal := ruleOf(imp); internal {
				used[from][to] = true
			}
		}
	}
	for from, r := range rules {
		if used[from] == nil {
			t.Errorf("%s: dependency rule for a package that does not exist", from)
		}
		for _, to := range r.imports {
			if !used[from][to] {
				t.Errorf("%s may import %s but does not: remove it from the rule", from, to)
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
		{module + "/database", module + "/config", false},
		{module + "/internal/dbtesting", module + "/database/sqlite", true},
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
		{module + "/apitoken", module + "/httpx/rest", false},
		{module + "/apitoken", module + "/database", false},
		{module + "/authz", module + "/httpx/rest", false},
		{module + "/authz", module + "/web", false},
		{module + "/lock", "github.com/uptrace/bun", true},
		{module + "/lock", module + "/database", false},
		{module + "/cleanup", "github.com/jackc/pgx/v5", false},
		{module + "/config", "net/http", false},
		{module + "/i18n", "github.com/spf13/cobra", false},
		{module + "/logging", module + "/identity", false},
		{module + "/identity", module + "/internal/checks/archtest", false},
		// 子包有自己的规则，不放宽所在的包树。
		{module + "/sheet", module + "/operation", false},
		{module + "/sheet/imports", module + "/operation", true},
		{module + "/sheet/imports", module + "/sheet", true},
		{module + "/sheet", module + "/sheet/imports", false},
		{module + "/command", module + "/httpx", false},
		{module + "/command/httpcmd", module + "/httpx/rest", true},
		{module + "/command/devcmd", module + "/command", true},
		{module + "/ai/openai", module + "/ai", true},
		{module + "/ai", module + "/httpx", false},
		{module + "/ai/chat/postgres", module + "/database", true},
		{module + "/web", module + "/filesystem", false},
		{module + "/web/upload", module + "/filesystem", true},
		{module + "/web/ui", module + "/web", true},
		// 引用只能指向同层或更低层。
		{module + "/errorx", module + "/database", false},
		{module + "/queue", module + "/httpx", false},
		{module + "/httpx", module + "/apitoken", false},
		{module + "/web", module + "/authz", false},
		{module + "/form", "github.com/danielgtaylor/huma/v2", true},
		{module + "/page", "net/http", false},
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

// TestOnlyRealtimeLinksCentrifuge 确保不使用 WebSocket 实时频道的应用不会链接 Centrifuge：
// 广播、SSE 和 AI 的集群协作都只依赖 broadcast。
func TestOnlyRealtimeLinksCentrifuge(t *testing.T) {
	for _, pkg := range []string{"/broadcast", "/broadcast/redisbroadcast", "/broadcast/natsbroadcast", "/httpx/rest", "/web", "/ai", "/ai/chat"} {
		out, err := exec.Command("go", "list", "-deps", module+pkg).CombinedOutput()
		if err != nil {
			t.Fatalf("go list: %v\n%s", err, out)
		}
		for _, dep := range strings.Fields(string(out)) {
			if under(dep, "github.com/centrifugal") {
				t.Errorf("%s depends on %s", pkg, dep)
			}
		}
	}
}

// TestDocumentCoreIsLight 确保只切分 Markdown 或只用文本转换器的应用不会链接 PDF、Excel 和 HTML 的依赖，
// 它们分别在 document/convert/pdf、xlsx 和 html 中；只用 PDF 转换器时也不会链接 Excel 和 HTML 的依赖。
func TestDocumentCoreIsLight(t *testing.T) {
	heavy := []string{"github.com/klippa-app/go-pdfium", "github.com/tetratelabs/wazero", "github.com/xuri/excelize", "github.com/JohannesKaufmann/html-to-markdown"}
	for pkg, allowed := range map[string][]string{
		"/document/split":        nil,
		"/document/convert":      nil,
		"/document/convert/text": nil,
		"/document/convert/csv":  nil,
		"/document/convert/docx": nil,
		"/document/convert/pptx": nil,
		"/document/convert/pdf":  {"github.com/klippa-app/go-pdfium", "github.com/tetratelabs/wazero"},
		"/document/convert/xlsx": {"github.com/xuri/excelize"},
		"/document/convert/html": {"github.com/JohannesKaufmann/html-to-markdown"},
	} {
		out, err := exec.Command("go", "list", "-deps", module+pkg).CombinedOutput()
		if err != nil {
			t.Fatalf("go list: %v\n%s", err, out)
		}
		for _, dep := range strings.Fields(string(out)) {
			for _, h := range heavy {
				if under(dep, h) && !slices.Contains(allowed, h) {
					t.Errorf("%s depends on %s", pkg, dep)
				}
			}
		}
	}
}

// TestSearchCoreIsLight 确保只用默认分词器的应用不会链接 gse 和它嵌入的词典，它们只由 search/gse 引入。
func TestSearchCoreIsLight(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", module+"/search").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	for _, dep := range strings.Fields(string(out)) {
		if under(dep, "github.com/go-ego/gse") {
			t.Errorf("search depends on %s", dep)
		}
	}
}

// TestAICoreIsLight 确保只用到模型能力（图片、语音、向量、重排、决策）的应用不会链接对话式 agent 的依赖：
// trpc-agent-go 及其带来的 gRPC、Huma 和数据库组件都只由 ai/chat 引入。
func TestAICoreIsLight(t *testing.T) {
	heavy := []string{module + "/ai/chat", "trpc.group/trpc-go", "google.golang.org/grpc", "github.com/danielgtaylor/huma", "github.com/uptrace/bun", module + "/httpx", module + "/broadcast", module + "/lock"}
	for _, pkg := range []string{"/ai", "/ai/openai", "/ai/openrouter", "/ai/systemone", "/ai/aitest"} {
		out, err := exec.Command("go", "list", "-deps", module+pkg).CombinedOutput()
		if err != nil {
			t.Fatalf("go list: %v\n%s", err, out)
		}
		for _, dep := range strings.Fields(string(out)) {
			for _, h := range heavy {
				if under(dep, h) {
					t.Errorf("%s depends on %s", pkg, dep)
				}
			}
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
	for _, pkg := range []string{module + "/httpx", module + "/httpx/rest", module + "/session", module + "/apitoken", module + "/authz"} {
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
// Redis 实现分别在 lock/redislock、ratelimit/redislimit、broadcast/redisbroadcast 和 queue/redisqueue 中。
func TestCoreDoesNotLinkRedis(t *testing.T) {
	for _, pkg := range []string{module + "/lock", module + "/ratelimit", module + "/httpx", module + "/broadcast", module + "/queue"} {
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

// TestCoreDoesNotLinkNATS 确保不用 NATS 的应用不会链接 nats.go，NATS 实现在 broadcast/natsbroadcast 和 queue/natsqueue 中。
func TestCoreDoesNotLinkNATS(t *testing.T) {
	for _, pkg := range []string{module + "/broadcast", module + "/broadcast/redisbroadcast", module + "/queue", module + "/queue/redisqueue"} {
		out, err := exec.Command("go", "list", "-deps", pkg).CombinedOutput()
		if err != nil {
			t.Fatalf("go list: %v\n%s", err, out)
		}
		for _, dep := range strings.Fields(string(out)) {
			if under(dep, "github.com/nats-io") {
				t.Errorf("%s depends on %s", pkg, dep)
			}
		}
	}
}

// TestCommandCoreIsLight 确保只用 command 核心包的应用（例如常驻 worker）
// 不会链接 HTTP API 和数据库组件；它们分别在 command/httpcmd 和 command/migratecmd 中。
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
// 核心包、迁移、迁移命令与测试用的 dbtest 不依赖任何驱动，各驱动子包也互不依赖。
func TestDatabaseCoreDoesNotLinkDrivers(t *testing.T) {
	drivers := map[string]string{
		"postgres": "github.com/jackc/pgx",
		"sqlite":   "modernc.org/sqlite",
		"mysql":    "github.com/go-sql-driver/mysql",
	}
	all := []string{drivers["postgres"], drivers["sqlite"], drivers["mysql"]}
	checks := map[string][]string{
		module + "/database":           all,
		module + "/database/migrate":   all,
		module + "/database/dbtest":    all,
		module + "/command/migratecmd": all,
		module + "/ai/chat":            all,
	}
	// 各驱动子包和 AI 对话存储的同名子包只链接自己的驱动。
	for name, own := range drivers {
		for _, other := range all {
			if other != own {
				for _, pkg := range []string{module + "/database/" + name, module + "/ai/chat/" + name} {
					checks[pkg] = append(checks[pkg], other)
				}
			}
		}
	}
	for pkg, forbidden := range checks {
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
