package migrate

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/cockroachdb/errors"

	"github.com/ryan053857/mygoravel/database"
)

var (
	migrationName = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)
	createPattern = regexp.MustCompile(`^create_(\w+?)(?:_table)?$`)
	alterPattern  = regexp.MustCompile(`^.+_(?:to|from|in)_(\w+?)(?:_table)?$`)
)

// Create 在 dir 中为 driver 新建一个迁移，返回文件路径。name 必须是 snake_case，例如
// create_articles_table 或 add_published_at_to_articles_table；文件名为
// "<UTC 时间戳>_<name>.sql"，版本保证大于目录中已有的最大版本。
//
// 与 Laravel 一样，Create 从 name 推断表名并生成对应的骨架：create_<table>[_table]
// 生成 CREATE TABLE，主键是由应用用 uuid.NewV7() 生成的 UUID；..._to|from|in_<table>[_table] 生成 ALTER TABLE，其余只有 goose 注解。
func Create(driver database.Driver, dir, name string) (string, error) {
	if driver == nil {
		return "", errors.New("migrate: nil driver")
	}
	create, ok := createStubs[driver.Name()]
	if !ok {
		return "", errors.Newf("migrate: unsupported driver %q", driver.Name())
	}
	if !migrationName.MatchString(name) {
		return "", errors.Newf("migrate: migration name %q must be snake_case, for example create_articles_table", name)
	}
	_, maxVersion, err := publishedSources(dir)
	if err != nil {
		return "", err
	}
	if maxVersion == math.MaxInt64 {
		return "", errors.New("migrate: migration version overflow")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", errors.Wrap(err, "migrate: create migration directory")
	}
	path := filepath.Join(dir, fmt.Sprintf("%d_%s.sql", nextVersion(maxVersion), name))
	if err := publishFile(path, []byte(stub(create, name))); err != nil {
		return "", errors.Wrapf(err, "migrate: create %s", filepath.Base(path))
	}
	return path, nil
}

// createStubs 是各方言的建表骨架。时间都由应用经 Bun 写入，不设数据库默认值；
// SQLite 没有 uuid 与时间类型，都存为 TEXT；MySQL 的 UUID 存为 CHAR(36)，时间精确到微秒。
var createStubs = map[string]string{
	"postgres": `-- +goose Up
CREATE TABLE %[1]s (
    id         uuid PRIMARY KEY,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

-- +goose Down
DROP TABLE %[1]s;
`,
	"mysql": `-- +goose Up
CREATE TABLE %[1]s (
    id         CHAR(36) NOT NULL PRIMARY KEY,
    created_at DATETIME(6) NOT NULL,
    updated_at DATETIME(6) NOT NULL
);

-- +goose Down
DROP TABLE %[1]s;
`,
	"sqlite": `-- +goose Up
CREATE TABLE %[1]s (
    id         TEXT PRIMARY KEY,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

-- +goose Down
DROP TABLE %[1]s;
`,
}

// stub 按 name 推断的表名返回迁移骨架。
func stub(create, name string) string {
	if m := createPattern.FindStringSubmatch(name); m != nil {
		return fmt.Sprintf(create, m[1])
	}
	if m := alterPattern.FindStringSubmatch(name); m != nil {
		return fmt.Sprintf(`-- +goose Up
ALTER TABLE %[1]s;

-- +goose Down
ALTER TABLE %[1]s;
`, m[1])
	}
	return "-- +goose Up\n\n-- +goose Down\n"
}

// nextVersion 返回当前 UTC 时间戳形式的版本，并保证大于 maxVersion。
func nextVersion(maxVersion int64) int64 {
	now, _ := strconv.ParseInt(time.Now().UTC().Format("20060102150405"), 10, 64)
	return max(now, maxVersion+1)
}
