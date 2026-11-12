package imports_test

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/filesystem"
	"github.com/ryan053857/mygoravel/filesystem/local"
	"github.com/ryan053857/mygoravel/i18n"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/internal/dbtesting"
	"github.com/ryan053857/mygoravel/operation"
	"github.com/ryan053857/mygoravel/queue"
	"github.com/ryan053857/mygoravel/sheet"
	"github.com/ryan053857/mygoravel/sheet/imports"
)

var (
	alice = identity.Subject{ID: "u1", Type: "user", Tenant: "acme"}
	bob   = identity.Subject{ID: "u2", Type: "user", Tenant: "acme"}
)

var errTaken = errorx.Define(errorx.Conflict, "test.taken")

type changes struct {
	Title string `json:"title" sheet:"标题" minLength:"1"`
	Slug  string `json:"slug" sheet:"Slug"`
}

type env struct {
	db        *bun.DB
	files     *filesystem.Service
	ops       *operation.Service
	imports   *imports.Service
	languages *goi18n.Bundle
}

// migrations 合并文件、队列、操作和导入的迁移。
func migrations(t *testing.T) fs.FS {
	t.Helper()
	merged := fstest.MapFS{}
	for prefix, src := range map[string]fs.FS{"1": filesystem.Migrations(), "2": queue.Migrations(), "3": operation.Migrations(), "4": imports.Migrations()} {
		err := fs.WalkDir(src, ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := fs.ReadFile(src, p)
			dir, name := path.Split(p)
			merged[dir+prefix+name] = &fstest.MapFile{Data: data}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return merged
}

func newEnv(t *testing.T, driver database.Driver) env {
	t.Helper()
	db := dbtesting.Open(t, driver, migrations(t))
	disk, err := local.Open(filesystem.LocalConfig{Root: filepath.Join(t.TempDir(), "files"), PublicURL: "/storage"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = disk.Close() })
	var cfg filesystem.Config
	cfg.SetDefaults()
	files, err := filesystem.New(db, cfg, map[string]filesystem.Disk{"local": disk})
	if err != nil {
		t.Fatal(err)
	}
	languages, err := i18n.New("zh-CN", fstest.MapFS{"zh-CN.yaml": {Data: []byte("test.taken: 已被占用\n")}})
	if err != nil {
		t.Fatal(err)
	}
	ops := operation.New(db)
	return env{db: db, files: files, ops: ops, imports: imports.New(db, ops, files, languages), languages: languages}
}

// start 以 owner 的身份上传 body 并登记导入，请求的语言是中文。
func (e env) start(t *testing.T, owner identity.Subject, name, body string) *imports.Import {
	t.Helper()
	ctx := i18n.WithLocale(context.Background(), e.languages, "zh-CN")
	f, err := e.files.Upload(ctx, owner, filesystem.FileInput{Filename: name, ContentType: "text/csv", Size: int64(len(body))}, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	imp, err := e.imports.Start(ctx, e.db, owner, "articles.import", f.ID)
	if err != nil {
		t.Fatal(err)
	}
	return imp
}

func (e env) get(t *testing.T, owner identity.Subject, id uuid.UUID) *imports.Import {
	t.Helper()
	imp, err := e.imports.Get(context.Background(), owner, id)
	if err != nil {
		t.Fatal(err)
	}
	return imp
}

// recorder 记录每一行被处理的次数，以及每次保存进度时 saved 收到的导入。
type recorder struct {
	mu    sync.Mutex
	seen  map[int]int
	saves []imports.Import
	fail  func(line int) error
	slugs map[string]bool
}

func newRecorder() *recorder {
	return &recorder{seen: map[int]int{}, slugs: map[string]bool{}}
}

func (r *recorder) row(_ context.Context, imp *imports.Import, rec *sheet.Record[changes]) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		if err := r.fail(rec.Line); err != nil {
			return err
		}
	}
	r.seen[rec.Line]++
	if o := imp.Owner(); o.ID != "u1" || o.Type != "user" || o.Tenant != "acme" {
		return errors.New("wrong owner")
	}
	switch rec.Data.Slug {
	case "taken":
		return errTaken.New()
	case "dup":
		rec.SetError("slug", "重复")
	}
	return nil
}

func (r *recorder) saved(_ context.Context, tx database.Tx, imp *imports.Import) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.saves = append(r.saves, *imp)
	return nil
}

// progress 返回每次保存时的状态和已处理的行数，例如 running:100。
func (r *recorder) progress() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	list := make([]string, len(r.saves))
	for i, imp := range r.saves {
		list[i] = fmt.Sprintf("%s:%d", imp.Status, imp.Processed)
	}
	return list
}

func csv(lines int) string {
	var b strings.Builder
	b.WriteString("标题,Slug,备注\n")
	for i := 2; i <= lines+1; i++ {
		switch i {
		case 10:
			b.WriteString(",empty,x\n")
		case 20:
			b.WriteString("T20,taken,x\n")
		case 30:
			b.WriteString("T30,dup,x\n")
		default:
			fmt.Fprintf(&b, "T%d,s%d,x\n", i, i)
		}
	}
	return b.String()
}

func TestProcessResumesAndRecordsFailures(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		ctx := context.Background()
		e := newEnv(t, driver)
		imp := e.start(t, alice, "articles.csv", csv(250))
		if imp.Status != operation.StatusPending || imp.Locale != "zh-CN" {
			t.Fatalf("started: %+v", imp)
		}

		rec := newRecorder()
		boom := errors.New("database down")
		rec.fail = func(line int) error {
			if line == 150 {
				return boom
			}
			return nil
		}
		if err := imports.Process(ctx, e.imports, imp.ID, rec.row, rec.saved); !errors.Is(err, boom) {
			t.Fatalf("first run: %v", err)
		}
		got := e.get(t, alice, imp.ID)
		if got.Status != operation.StatusRunning || got.Total != 250 || got.Processed != 148 || got.Failed != 3 {
			t.Fatalf("after first run: %+v", got)
		}

		rec.fail = nil
		if err := imports.Process(ctx, e.imports, imp.ID, rec.row, rec.saved); err != nil {
			t.Fatal(err)
		}
		got = e.get(t, alice, imp.ID)
		if got.Status != operation.StatusCompleted || got.Processed != 250 || got.Failed != 3 || got.CompletedAt == nil {
			t.Fatalf("completed: %+v", got)
		}
		for line := 2; line <= 251; line++ {
			want := 1
			if line == 10 {
				want = 0 // 验证失败的行不交给 row
			}
			if rec.seen[line] != want {
				t.Fatalf("line %d handled %d times", line, rec.seen[line])
			}
		}
		// 变为 running、读出表头、每 100 行、出错前、结束各保存一次，每次都调用 saved。
		want := []string{"running:0", "running:0", "running:100", "running:148", "running:248", "completed:250"}
		if got := rec.progress(); !slices.Equal(got, want) {
			t.Fatalf("saves: %v", got)
		}
		if rec.saves[1].Total != 250 || rec.saves[5].Failed != 3 {
			t.Fatalf("saves: %+v", rec.saves)
		}

		// 已经结束的导入再处理一次什么也不做。
		if err := imports.Process(ctx, e.imports, imp.ID, rec.row, rec.saved); err != nil || len(rec.saves) != 6 {
			t.Fatalf("again: %v %d", err, len(rec.saves))
		}

		// 导入结束时上传的文件被删除。
		if _, err := e.files.Get(ctx, imp.FileID); !errors.Is(err, filesystem.ErrNotFound) {
			t.Fatalf("file after import: %v", err)
		}

		failures, err := e.imports.Failures(ctx, got, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(failures) != 3 {
			t.Fatalf("failures: %+v", failures)
		}
		messages := []string{"标题: 不能为空", "已被占用", "Slug: 重复"}
		for i, f := range failures {
			if f.Message != messages[i] {
				t.Fatalf("failure %d: %+v", i, f)
			}
		}
		if failures[0].Line != 10 || failures[0].Cells[1] != "empty" || failures[0].Errors["title"] != "不能为空" {
			t.Fatalf("failure: %+v", failures[0])
		}

		var buf bytes.Buffer
		w := sheet.NewWriter(&buf, sheet.CSV)
		zh := i18n.WithLocale(ctx, e.languages, "zh-CN")
		if err := e.imports.WriteFailures(zh, got, w); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		wantCSV := "\xef\xbb\xbf标题,Slug,备注,错误\n,empty,x,标题: 不能为空\nT20,taken,x,已被占用\nT30,dup,x,Slug: 重复\n"
		if buf.String() != wantCSV {
			t.Fatalf("failures csv:\n%s", buf.String())
		}
	})
}

func TestProcessRejectsFile(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		ctx := context.Background()
		e := newEnv(t, driver)
		imp := e.start(t, alice, "articles.csv", "备注\nx\n")
		rec := newRecorder()
		if err := imports.Process(ctx, e.imports, imp.ID, rec.row, rec.saved); err != nil {
			t.Fatal(err)
		}
		got := e.get(t, alice, imp.ID)
		zh := i18n.WithLocale(ctx, e.languages, "zh-CN")
		if got.Status != operation.StatusFailed || got.Error != "sheet.missing_columns" || got.ErrorMessage(zh) != "缺少必需的列：标题、Slug" {
			t.Fatalf("failed: %+v %q", got, got.ErrorMessage(zh))
		}
		if got := rec.progress(); !slices.Equal(got, []string{"running:0", "failed:0"}) {
			t.Fatalf("saves: %v", got)
		}
	})
}

func TestStartAndGet(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		ctx := context.Background()
		e := newEnv(t, driver)
		f, err := e.files.Upload(ctx, alice, filesystem.FileInput{Filename: "a.txt", ContentType: "text/plain", Size: 1}, strings.NewReader("x"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.imports.Start(ctx, e.db, alice, "articles.import", f.ID); !errors.Is(err, sheet.ErrFormat) {
			t.Fatalf("format: %v", err)
		}
		imp := e.start(t, alice, "a.csv", "标题,Slug\n")
		file, err := e.files.Get(ctx, imp.FileID)
		if err != nil || file.Status != filesystem.StatusReady {
			t.Fatalf("file: %+v %v", file, err)
		}
		if _, err := e.imports.Get(ctx, bob, imp.ID); !errors.Is(err, operation.ErrNotFound) {
			t.Fatalf("other owner: %v", err)
		}
		other, err := e.ops.Start(ctx, e.db, alice, "articles.bulk_publish")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.imports.Get(ctx, alice, other.ID); !errors.Is(err, operation.ErrNotFound) {
			t.Fatalf("not an import: %v", err)
		}
	})
}

func TestFinalAttemptFailsImport(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		e := newEnv(t, driver)
		imp := e.start(t, alice, "a.csv", csv(3))
		rec := newRecorder()
		rec.fail = func(int) error { return errors.New("always down") }
		topic := queue.Topic[uuid.UUID]("imports.test")

		cfg := queue.DefaultConfig()
		cfg.PollInterval = 20 * time.Millisecond
		cfg.MaxAttempts = 1
		logger := slog.New(slog.DiscardHandler)
		r := queue.NewRouter(logger)
		sub := queue.NewSubscriber(queue.Database(e.db), cfg, logger)
		topic.Handle(r, sub, func(ctx context.Context, id uuid.UUID) error {
			return imports.Process(ctx, e.imports, id, rec.row, rec.saved)
		})
		ctx, cancel := context.WithCancel(context.Background())
		stopped := make(chan error, 1)
		go func() { stopped <- r.Run(ctx) }()
		<-r.Running()
		t.Cleanup(func() {
			cancel()
			<-stopped
			_ = sub.Close()
		})
		if err := topic.Publish(context.Background(), queue.Database(e.db), imp.ID); err != nil {
			t.Fatal(err)
		}

		deadline := time.Now().Add(10 * time.Second)
		for {
			got := e.get(t, alice, imp.ID)
			if got.Status == operation.StatusFailed {
				if got.Error != "operation.failed" || got.Processed != 0 {
					t.Fatalf("failed: %+v", got)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("import %+v did not fail", got)
			}
			time.Sleep(20 * time.Millisecond)
		}
		if got := rec.progress(); !slices.Equal(got, []string{"running:0", "running:0", "failed:0"}) {
			t.Fatalf("saves: %v", got)
		}
	})
}

func TestCleanup(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		ctx := context.Background()
		e := newEnv(t, driver)
		old := e.start(t, alice, "old.csv", csv(12))
		recent := e.start(t, alice, "recent.csv", csv(3))
		if err := imports.Process(ctx, e.imports, old.ID, newRecorder().row, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := e.db.NewUpdate().Model((*operation.Operation)(nil)).Set("created_at = ?", time.Now().Add(-48*time.Hour).UTC()).
			Where("id = ?", old.ID).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		n, err := e.ops.Cleanup(24*time.Hour)(ctx, 10)
		if err != nil || n != 1 {
			t.Fatalf("cleanup: %d %v", n, err)
		}
		if _, err := e.imports.Get(ctx, alice, old.ID); !errors.Is(err, operation.ErrNotFound) {
			t.Fatalf("old import: %v", err)
		}
		e.get(t, alice, recent.ID)
		// 导入和失败行随操作级联删除。
		left, err := e.db.NewSelect().Model((*imports.Failure)(nil)).Count(ctx)
		if err != nil || left != 0 {
			t.Fatalf("failures left: %d %v", left, err)
		}
		if n, err := e.db.NewSelect().Model((*imports.Import)(nil)).Count(ctx); err != nil || n != 1 {
			t.Fatalf("imports left: %d %v", n, err)
		}
		if _, err := e.files.Get(ctx, old.FileID); !errors.Is(err, filesystem.ErrNotFound) {
			t.Fatalf("file: %v", err)
		}
	})
}
