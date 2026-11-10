package migrate

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ryan053857/mygoravel/database"
)

const testMigration = "-- +goose Up\nCREATE TABLE example (id integer);\n-- +goose Down\nDROP TABLE example;\n"

func TestPublishUpgradePreservesApplicationSQL(t *testing.T) {
	dir := t.TempDir()
	source := Source{Name: "files", FS: fstest.MapFS{
		"postgres/0001_create_files.sql": {Data: []byte(testMigration)},
	}}
	first, err := Publish(dir, database.Postgres, source)
	if err != nil || len(first) != 1 {
		t.Fatalf("first=%v err=%v", first, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, first[0]))
	if err != nil || !strings.HasPrefix(string(data), "-- mygoravel: files/0001_create_files\n") {
		t.Fatalf("data=%s err=%v", data, err)
	}
	// 应用可在首次部署前修改 SQL、重命名文件；来源标记使升级仍然只追加。
	custom := "-- mygoravel: files/0001_create_files\n" + testMigration + "-- application column additions\n"
	if err := os.Remove(filepath.Join(dir, first[0])); err != nil {
		t.Fatal(err)
	}
	renamed := filepath.Join(dir, "29990101000000_custom_files.sql")
	if err := os.WriteFile(renamed, []byte(custom), 0o600); err != nil {
		t.Fatal(err)
	}
	source.FS.(fstest.MapFS)["postgres/0002_add_field.sql"] = &fstest.MapFile{Data: []byte(testMigration)}
	second, err := Publish(dir, database.Postgres, source)
	if err != nil || len(second) != 1 || !strings.HasPrefix(second[0], "29990101000001_files_0002_") {
		t.Fatalf("second=%v err=%v", second, err)
	}
	data, _ = os.ReadFile(renamed)
	if string(data) != custom {
		t.Fatal("application migration was overwritten")
	}
	again, err := Publish(dir, database.Postgres, source)
	if err != nil || len(again) != 0 {
		t.Fatalf("again=%v err=%v", again, err)
	}
}

func TestPublishOrdersSourceVersionsNumerically(t *testing.T) {
	files := fstest.MapFS{
		"postgres/10_later.sql": {Data: []byte(testMigration)},
		"postgres/2_first.sql":  {Data: []byte(testMigration)},
	}
	names, err := Publish(t.TempDir(), database.Postgres, Source{Name: "files", FS: files})
	if err != nil || len(names) != 2 || !strings.HasSuffix(names[0], "_2_first.sql") || !strings.HasSuffix(names[1], "_10_later.sql") {
		t.Fatalf("names=%v err=%v", names, err)
	}
}

func TestPublishTimestampAndNumericVersions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		existing int64
		want     int64
	}{
		{"future timestamp", 29990101000059, 29990101000060},
		{"numeric", 30000000000000, 30000000000001},
		{"largest version", math.MaxInt64 - 1, math.MaxInt64},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d_existing.sql", tc.existing)), []byte(testMigration), 0o600); err != nil {
				t.Fatal(err)
			}
			names, err := Publish(dir, database.Postgres, Source{Name: "files", FS: fstest.MapFS{"postgres/1_new.sql": {Data: []byte(testMigration)}}})
			if err != nil || len(names) != 1 || names[0] != fmt.Sprintf("%d_files_1_new.sql", tc.want) {
				t.Fatalf("names=%v err=%v want=%d", names, err, tc.want)
			}
		})
	}
}

func TestPublishPlansAllVersionsBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, fmt.Sprintf("%d_existing.sql", int64(math.MaxInt64-1)))
	if err := os.WriteFile(name, []byte(testMigration), 0o600); err != nil {
		t.Fatal(err)
	}
	written, err := Publish(dir, database.Postgres, Source{Name: "files", FS: fstest.MapFS{
		"postgres/1_first.sql": {Data: []byte(testMigration)},
		"postgres/2_next.sql":  {Data: []byte(testMigration)},
	}})
	if err == nil || !strings.Contains(err.Error(), "version overflow") || len(written) != 0 {
		t.Fatalf("written=%v err=%v", written, err)
	}
	names, _ := filepath.Glob(filepath.Join(dir, "*.sql"))
	if len(names) != 1 {
		t.Fatalf("overflow published a partial batch: %v", names)
	}
}

func TestPublishPrevalidatesAllSources(t *testing.T) {
	valid := Source{Name: "files", FS: fstest.MapFS{"postgres/1_valid.sql": {Data: []byte(testMigration)}}}
	for _, bad := range []Source{
		{Name: "../escape", FS: valid.FS},
		{Name: "nil"},
		{Name: "files", FS: valid.FS},
		{Name: "duplicate", FS: fstest.MapFS{
			"postgres/01_first.sql": {Data: []byte(testMigration)},
			"postgres/1_second.sql": {Data: []byte(testMigration)},
		}},
		{Name: "invalid", FS: fstest.MapFS{"postgres/wrong.sql": {Data: []byte(testMigration)}}},
		{Name: "negative", FS: fstest.MapFS{"postgres/-1_wrong.sql": {Data: []byte(testMigration)}}},
		{Name: "zero", FS: fstest.MapFS{"postgres/0_wrong.sql": {Data: []byte(testMigration)}}},
		{Name: "missing_up", FS: fstest.MapFS{"postgres/1_wrong.sql": {Data: []byte("select 1;")}}},
	} {
		t.Run(bad.Name, func(t *testing.T) {
			dir := t.TempDir()
			if written, err := Publish(dir, database.Postgres, valid, bad); err == nil || len(written) != 0 {
				t.Fatalf("written=%v err=%v", written, err)
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 0 {
				t.Fatalf("prevalidation wrote %v", entries)
			}
		})
	}
}

func TestPublishRejectsInvalidApplicationDirectory(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
	}{
		{"invalid name", map[string]string{"notes.sql": testMigration}},
		{"overflow", map[string]string{"9223372036854775807_last.sql": testMigration}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, data := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			written, err := Publish(dir, database.Postgres, Source{Name: "new", FS: fstest.MapFS{"postgres/1_new.sql": {Data: []byte(testMigration)}}})
			if err == nil || len(written) != 0 {
				t.Fatalf("written=%v err=%v", written, err)
			}
		})
	}
}

func TestPublishFileNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	if err := publishFile(filepath.Join(dir, "1_file.sql"), []byte("original")); err != nil {
		t.Fatal(err)
	}
	if err := publishFile(filepath.Join(dir, "1_file.sql"), []byte("replacement")); err == nil {
		t.Fatal("expected exclusive creation failure")
	}
	data, _ := os.ReadFile(filepath.Join(dir, "1_file.sql"))
	if string(data) != "original" {
		t.Fatalf("data=%s", data)
	}
}

func TestPublishSelectsDriverDirectory(t *testing.T) {
	source := Source{Name: "files", FS: fstest.MapFS{
		"postgres/0001_create_files.sql": {Data: []byte(testMigration + "-- postgres\n")},
		"sqlite/0001_create_files.sql":   {Data: []byte(testMigration + "-- sqlite\n")},
	}}
	dir := t.TempDir()
	names, err := Publish(dir, database.SQLite, source)
	if err != nil || len(names) != 1 {
		t.Fatalf("names=%v err=%v", names, err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, names[0]))
	if !strings.HasPrefix(string(data), "-- mygoravel: files/0001_create_files\n") || !strings.HasSuffix(string(data), "-- sqlite\n") {
		t.Fatalf("data=%s", data)
	}
	// 同一次变更在各数据库中共用来源标记，换数据库后不会重复发布。
	if again, err := Publish(dir, database.Postgres, source); err != nil || len(again) != 0 {
		t.Fatalf("again=%v err=%v", again, err)
	}
	if _, err := Publish(t.TempDir(), "mysql", source); err == nil || !strings.Contains(err.Error(), "has no mysql migrations") {
		t.Fatalf("err=%v", err)
	}
	if _, err := Publish(t.TempDir(), "", source); err == nil {
		t.Fatal("empty driver accepted")
	}
}
