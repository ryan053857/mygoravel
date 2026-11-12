package exports_test

import (
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
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/database/dbtest"
	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/filesystem"
	"github.com/ryan053857/mygoravel/filesystem/local"
	"github.com/ryan053857/mygoravel/i18n"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/operation"
	"github.com/ryan053857/mygoravel/queue"
	"github.com/ryan053857/mygoravel/sheet"
	"github.com/ryan053857/mygoravel/sheet/exports"
)

var (
	alice = identity.Subject{ID: "u1", Type: "user", Tenant: "acme"}
	bob   = identity.Subject{ID: "u2", Type: "user", Tenant: "acme"}
)

type env struct {
	db      *bun.DB
	files   *filesystem.Service
	ops     *operation.Service
	exports *exports.Service
}

func migrations(t *testing.T) fs.FS {
	t.Helper()
	merged := fstest.MapFS{}
	for prefix, src := range map[string]fs.FS{"1": filesystem.Migrations(), "2": queue.Migrations(), "3": operation.Migrations(), "4": exports.Migrations()} {
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

func newEnv(t *testing.T) env {
	t.Helper()
	t.Cleanup(exports.SetSaveInterval(0)) // 每一行都保存进度，使保存的序列可以预期
	db := dbtest.Open(t, migrations(t))
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
	ops := operation.New(db)
	return env{db: db, files: files, ops: ops, exports: exports.New(db, ops, files)}
}

func (e env) start(t *testing.T, format sheet.Format) *exports.Export {
	t.Helper()
	exp, err := e.exports.Start(context.Background(), e.db, alice, "articles.export", format)
	if err != nil {
		t.Fatal(err)
	}
	return exp
}

func (e env) get(t *testing.T, id uuid.UUID) *exports.Export {
	t.Helper()
	exp, err := e.exports.Get(context.Background(), alice, id)
	if err != nil {
		t.Fatal(err)
	}
	return exp
}

// recorder 记录每次保存进度时 saved 收到的导出。
type recorder struct {
	mu    sync.Mutex
	saves []exports.Export
}

func (r *recorder) saved(_ context.Context, _ database.Tx, exp *exports.Export) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.saves = append(r.saves, *exp)
	return nil
}

// progress 返回每次保存时的状态、已写出和预计的行数，例如 running:3/5。
func (r *recorder) progress() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	list := make([]string, len(r.saves))
	for i, exp := range r.saves {
		list[i] = fmt.Sprintf("%s:%d/%d", exp.Status, exp.Processed, exp.Total)
	}
	return list
}

// rows 写出表头和 n 行。
func rows(n int) func(ctx context.Context, exp *exports.Export, w *exports.Writer) error {
	return func(ctx context.Context, exp *exports.Export, w *exports.Writer) error {
		if err := w.SetTotal(n); err != nil {
			return err
		}
		if err := w.Write("标题", "序号"); err != nil {
			return err
		}
		for i := 1; i <= n; i++ {
			if err := w.Write(fmt.Sprintf("T%d", i), i); err != nil {
				return err
			}
		}
		return nil
	}
}

func TestProcess(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	exp := e.start(t, sheet.XLSX)
	if exp.Status != operation.StatusPending {
		t.Fatalf("started: %+v", exp)
	}
	rec := &recorder{}
	if err := e.exports.Process(ctx, exp.ID, rows(3), rec.saved); err != nil {
		t.Fatal(err)
	}
	want := []string{"running:0/0", "running:0/3", "running:1/3", "running:2/3", "running:3/3", "completed:3/3"}
	if got := rec.progress(); !slices.Equal(got, want) {
		t.Fatalf("saves: %v", got)
	}

	got := e.get(t, exp.ID)
	name := "articles-export-" + time.Now().Format("20060102") + ".xlsx"
	if !got.Finished() || got.Filename != name || got.ExpiresAt == nil || !strings.HasPrefix(got.URL, "/storage/") || got.CompletedAt == nil {
		t.Fatalf("completed: %+v", got)
	}
	body, file, err := e.files.Open(ctx, got.FileID)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	if file.Status != filesystem.StatusTemporary || file.OwnerID != "u1" || file.URL != got.URL {
		t.Fatalf("file: %+v", file)
	}
	r, err := sheet.NewReader(body, sheet.XLSX)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	n := 0
	for range r.Rows() {
		n++
	}
	if !slices.Equal(r.Header(), []string{"标题", "序号"}) || n != 3 {
		t.Fatalf("file content: %v %d", r.Header(), n)
	}

	// 已经结束的导出再处理一次什么也不做。
	if err := e.exports.Process(ctx, exp.ID, rows(3), rec.saved); err != nil || len(rec.progress()) != len(want) {
		t.Fatalf("again: %v %v", err, rec.progress())
	}
}

func TestProcessErrors(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)

	// 业务错误使导出失败，不再重试。
	exp := e.start(t, sheet.CSV)
	rec := &recorder{}
	unknown := func(context.Context, *exports.Export, *exports.Writer) error {
		return sheet.ErrUnknownColumn.New(errorx.Public("column", "x"))
	}
	if err := e.exports.Process(ctx, exp.ID, unknown, rec.saved); err != nil {
		t.Fatal(err)
	}
	got := e.get(t, exp.ID)
	languages, err := i18n.New("zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	zh := i18n.WithLocale(ctx, languages, "zh-CN")
	if got.Status != operation.StatusFailed || got.Error != "sheet.unknown_column" || got.ErrorMessage(zh) != "没有这一列：x" {
		t.Fatalf("failed: %+v", got)
	}
	if p := rec.progress(); !slices.Equal(p, []string{"running:0/0", "failed:0/0"}) {
		t.Fatalf("saves: %v", p)
	}

	// 其他错误返回给队列，重试时从头开始，行数不会重复累计。
	exp = e.start(t, sheet.CSV)
	down := errors.New("database down")
	flaky := func(ctx context.Context, exp *exports.Export, w *exports.Writer) error {
		if err := rows(2)(ctx, exp, w); err != nil {
			return err
		}
		return down
	}
	if err := e.exports.Process(ctx, exp.ID, flaky, nil); !errors.Is(err, down) {
		t.Fatalf("first run: %v", err)
	}
	if got := e.get(t, exp.ID); got.Status != operation.StatusRunning || got.Processed != 2 {
		t.Fatalf("after first run: %+v", got)
	}
	if err := e.exports.Process(ctx, exp.ID, rows(2), nil); err != nil {
		t.Fatal(err)
	}
	if got := e.get(t, exp.ID); got.Status != operation.StatusCompleted || got.Processed != 2 || !strings.HasSuffix(got.Filename, ".csv") {
		t.Fatalf("retried: %+v", got)
	}
}

func TestFinalAttemptFailsExport(t *testing.T) {
	e := newEnv(t)
	exp := e.start(t, sheet.XLSX)
	rec := &recorder{}
	topic := queue.Topic[uuid.UUID]("exports.test")
	cfg := queue.DefaultConfig()
	cfg.PollInterval = 20 * time.Millisecond
	cfg.MaxAttempts = 1
	w := queue.NewWorker(queue.Database(e.db), cfg, slog.New(slog.DiscardHandler))
	topic.Handle(w, func(ctx context.Context, id uuid.UUID) error {
		return e.exports.Process(ctx, id, func(context.Context, *exports.Export, *exports.Writer) error {
			return errors.New("always down")
		}, rec.saved)
	})
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- w.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-stopped
	})
	if err := topic.Publish(context.Background(), queue.Database(e.db), exp.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		got := e.get(t, exp.ID)
		if got.Status == operation.StatusFailed {
			if got.Error != "operation.failed" {
				t.Fatalf("failed: %+v", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("export %+v did not fail", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if p := rec.progress(); !slices.Equal(p, []string{"running:0/0", "failed:0/0"}) {
		t.Fatalf("saves: %v", p)
	}
}

func TestStartGetCleanup(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	if _, err := e.exports.Start(ctx, e.db, alice, "articles.export", "xls"); !errors.Is(err, sheet.ErrFormat) {
		t.Fatalf("format: %v", err)
	}
	old := e.start(t, sheet.CSV)
	recent := e.start(t, sheet.CSV)
	if _, err := e.exports.Get(ctx, bob, old.ID); !errors.Is(err, operation.ErrNotFound) {
		t.Fatalf("other owner: %v", err)
	}
	other, err := e.ops.Start(ctx, e.db, alice, "articles.bulk_publish")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.exports.Get(ctx, alice, other.ID); !errors.Is(err, operation.ErrNotFound) {
		t.Fatalf("not an export: %v", err)
	}
	if _, err := e.db.NewUpdate().Model((*operation.Operation)(nil)).Set("created_at = ?", time.Now().Add(-48*time.Hour).UTC()).
		Where("id = ?", old.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	n, err := e.ops.Cleanup(24*time.Hour)(ctx, 10)
	if err != nil || n != 1 {
		t.Fatalf("cleanup: %d %v", n, err)
	}
	if _, err := e.exports.Get(ctx, alice, old.ID); !errors.Is(err, operation.ErrNotFound) {
		t.Fatalf("old: %v", err)
	}
	e.get(t, recent.ID)
}
