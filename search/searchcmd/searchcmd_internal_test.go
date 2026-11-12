package searchcmd

import (
	"bytes"
	"context"
	"io/fs"
	"log/slog"
	"path"
	"strings"
	"testing"
	"testing/fstest"
	"testing/synctest"
	"time"

	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/command"
	"github.com/ryan053857/mygoravel/database/dbtest"
	"github.com/ryan053857/mygoravel/operation"
	"github.com/ryan053857/mygoravel/queue"
	"github.com/ryan053857/mygoravel/queue/queuetest"
	"github.com/ryan053857/mygoravel/search"
)

type doc struct {
	Title string `json:"title" search:"text"`
}

var articles = search.Define[doc]("articles")

func load(_ context.Context, _ bun.IDB, key string) ([]doc, error) {
	return []doc{{Title: "文章" + key}}, nil
}

func scan(_ context.Context, _ bun.IDB, after string, _ int) ([]string, error) {
	if after == "" {
		return []string{"1", "2"}, nil
	}
	return nil, nil
}

func open(t *testing.T) *bun.DB {
	t.Helper()
	merged := fstest.MapFS{}
	for prefix, src := range map[string]fs.FS{"1": queue.Migrations(), "2": operation.Migrations(), "3": search.Migrations()} {
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
	return dbtest.Open(t, merged)
}

func execute(ctx context.Context, db *bun.DB, args ...string) (string, error) {
	cmd := New(db, articles)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(ctx)
	return out.String(), err
}

func TestReindexDetached(t *testing.T) {
	db := open(t)
	out, err := execute(context.Background(), db, "reindex", "articles", "--detach")
	if err != nil || !strings.Contains(out, "已发起 articles 的重建") {
		t.Fatalf("output %q, err %v", out, err)
	}
	ops, err := operation.New(db).List(context.Background(), owner, operation.Query{Kind: "search.reindex.articles"})
	if err != nil || len(ops) != 1 || ops[0].Status != operation.StatusPending {
		t.Fatalf("operations = %+v, %v", ops, err)
	}
}

func TestReindexUnknownIndex(t *testing.T) {
	_, err := execute(context.Background(), open(t), "reindex", "posts")
	if err == nil || command.ExitCode(err) != command.ExitUsage || !strings.Contains(err.Error(), "available: articles") {
		t.Fatalf("err = %v", err)
	}
}

func TestReindexWaits(t *testing.T) {
	pollInterval = 10 * time.Millisecond
	db := open(t)
	cfg := queuetest.Config(t)
	w := queue.NewWorker(db, cfg, slog.New(slog.DiscardHandler))
	articles.Handle(w, db, load, scan)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	defer func() {
		cancel()
		<-done
	}()

	out, err := execute(context.Background(), db, "reindex", "articles")
	if err != nil || !strings.Contains(out, "重建完成：2 个 key，其中 0 个无法建索引") {
		t.Fatalf("output %q, err %v", out, err)
	}
	res, err := articles.Search(context.Background(), db, search.Query{Text: "文章"})
	if err != nil || res.Total != 2 {
		t.Fatalf("search: %+v, %v", res, err)
	}
}

func TestReindexStopsWatchingOnCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		db := open(t)
		// 查看两次进度之后取消
		ctx, cancel := context.WithTimeout(context.Background(), 2*pollInterval+pollInterval/2)
		defer cancel()
		out, err := execute(ctx, db, "reindex", "articles") // 没有 worker，一直等待
		if err != nil || !strings.Contains(out, "等待 worker 领取任务") || !strings.Contains(out, "重建在 worker 中继续") {
			t.Fatalf("output %q, err %v", out, err)
		}
	})
}

func TestDuplicateIndexNames(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("duplicate index names accepted")
		}
	}()
	New(nil, articles, search.Define[doc]("articles"))
}
