package migrate

import (
	"bytes"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/cockroachdb/errors"

	"github.com/ryan053857/mygoravel/database"
)

const sourceMarker = "-- mygoravel: "

var sourceName = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// Source 是一个迁移来源。Name 是稳定标识；FS 根目录下按数据库类型分目录（postgres、sqlite），
// 每个目录直接包含带数字版本前缀的 SQL，例如 postgres/0001_create_files.sql。
// 各数据库的同名文件描述同一次变更，因此共用一个来源标记。已发布的源文件不改名，升级只追加更高版本。
type Source struct {
	Name string
	FS   fs.FS
}

type publication struct {
	source  string
	name    string
	marker  string
	version int64
	data    []byte
}

// Publish 把 sources 中 driver 对应目录下尚未发布的迁移复制到 dir，返回本次写入的文件名。
// 每个文件首行写入来源标记（如 "-- mygoravel: files/0001_create_files"），
// 目录中已有相同标记的迁移会被跳过，应用可以自由修改或重命名已发布的文件。
// 新文件的版本取当前 UTC 时间戳，并保证大于目录中已有的最大版本。
func Publish(dir string, driver database.Driver, sources ...Source) ([]string, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("migrate: publish directory is required")
	}
	items, err := publications(driver, sources)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, errors.Wrap(err, "migrate: create publish directory")
	}
	published, maxVersion, err := publishedSources(dir)
	if err != nil {
		return nil, err
	}
	var pending []publication
	for _, item := range items {
		if !published[item.marker] {
			pending = append(pending, item)
		}
	}
	if len(pending) == 0 {
		return nil, nil
	}
	if maxVersion > math.MaxInt64-int64(len(pending)) {
		return nil, errors.New("migrate: migration version overflow")
	}
	version := nextVersion(maxVersion)

	var written []string
	for _, item := range pending {
		name := fmt.Sprintf("%d_%s_%s", version, item.source, item.name)
		content := append([]byte(sourceMarker+item.marker+"\n"), item.data...)
		if err := publishFile(filepath.Join(dir, name), content); err != nil {
			return written, errors.Wrapf(err, "migrate: publish %s", name)
		}
		written = append(written, name)
		version++
	}
	return written, nil
}

// publications 在写入前读取并校验全部来源中 driver 的迁移，按来源顺序、来源内按版本排序。
func publications(driver database.Driver, sources []Source) ([]publication, error) {
	if driver == nil {
		return nil, errors.New("migrate: publish driver is required")
	}
	if len(sources) == 0 {
		return nil, errors.New("migrate: at least one migration source is required")
	}
	seen := make(map[string]bool)
	var items []publication
	for _, source := range sources {
		if !sourceName.MatchString(source.Name) {
			return nil, errors.Newf("migrate: invalid source name %q", source.Name)
		}
		if seen[source.Name] {
			return nil, errors.Newf("migrate: duplicate source %q", source.Name)
		}
		seen[source.Name] = true
		if source.FS == nil {
			return nil, errors.Newf("migrate: source %s has nil filesystem", source.Name)
		}
		if info, err := fs.Stat(source.FS, driver.Name()); err != nil || !info.IsDir() {
			return nil, errors.Newf("migrate: source %s has no %s migrations", source.Name, driver.Name())
		}
		files, err := fs.Sub(source.FS, driver.Name())
		if err != nil {
			return nil, errors.Wrapf(err, "migrate: read source %s", source.Name)
		}
		names, err := fs.Glob(files, "*.sql")
		if err != nil {
			return nil, errors.Wrapf(err, "migrate: read source %s", source.Name)
		}
		var group []publication
		versions := make(map[int64]string)
		for _, name := range names {
			version, err := migrationVersion(name)
			if err != nil {
				return nil, err
			}
			if previous, ok := versions[version]; ok {
				return nil, errors.Newf("migrate: source %s has duplicate version in %s and %s", source.Name, previous, name)
			}
			versions[version] = name
			data, err := fs.ReadFile(files, name)
			if err != nil {
				return nil, errors.Wrapf(err, "migrate: read %s/%s", source.Name, name)
			}
			if !bytes.Contains(data, []byte("-- +goose Up")) {
				return nil, errors.Newf("migrate: %s/%s is missing goose Up annotation", source.Name, name)
			}
			group = append(group, publication{
				source: source.Name, name: name, version: version, data: data,
				marker: source.Name + "/" + strings.TrimSuffix(name, ".sql"),
			})
		}
		slices.SortFunc(group, func(a, b publication) int { return int(min(max(a.version-b.version, -1), 1)) })
		items = append(items, group...)
	}
	return items, nil
}

func migrationVersion(name string) (int64, error) {
	versionText, description, ok := strings.Cut(strings.TrimSuffix(name, ".sql"), "_")
	version, err := strconv.ParseInt(versionText, 10, 64)
	if !ok || description == "" || err != nil || version <= 0 || strings.ContainsAny(versionText, "+-") {
		return 0, errors.Newf("migrate: invalid SQL migration filename %q", name)
	}
	return version, nil
}

// publishedSources 返回目录中已发布的来源标记和最大版本。
func publishedSources(dir string) (map[string]bool, int64, error) {
	names, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		return nil, 0, err
	}
	published := make(map[string]bool)
	var maxVersion int64
	for _, path := range names {
		version, err := migrationVersion(filepath.Base(path))
		if err != nil {
			return nil, 0, err
		}
		maxVersion = max(maxVersion, version)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, 0, errors.Wrapf(err, "migrate: read %s", path)
		}
		line, _, _ := strings.Cut(string(data), "\n")
		if marker, ok := strings.CutPrefix(strings.TrimSpace(line), sourceMarker); ok {
			published[strings.TrimSpace(marker)] = true
		}
	}
	return published, maxVersion, nil
}

// publishFile 独占创建文件，绝不覆盖已有迁移；写入失败时删除半成品。
func publishFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
	}
	return err
}
