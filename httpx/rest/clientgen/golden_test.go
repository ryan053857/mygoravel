package clientgen_test

import (
	"context"
	"flag"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ryan053857/mygoravel/httpx/rest"
	"github.com/ryan053857/mygoravel/httpx/rest/clientgen"
	"github.com/ryan053857/mygoravel/httpx/rest/clientgen/internal/example/article"
	"github.com/ryan053857/mygoravel/identity"
)

var update = flag.Bool("update", false, "rewrite the generated example client in internal/example/api")

const (
	exampleMain = "github.com/ryan053857/mygoravel/httpx/rest/clientgen/internal/example/article"
	exampleOut  = "internal/example/api"
)

// tokens 是示例接口接受的 token。
type tokens map[string]bool

func (t tokens) validate(_ context.Context, token string) (identity.Subject, error) {
	if !t[token] {
		return identity.Subject{}, identity.ErrUnauthenticated.New()
	}
	return identity.Subject{Type: "user", ID: "alice"}, nil
}

func exampleAPI(mode rest.Mode, valid tokens) *rest.API {
	// 往返测试故意触发 500，不输出它的错误日志。
	api := rest.New(rest.Options{Title: "Blog", Mode: mode, BasePath: "/api", Accounts: identity.Accounts{"user": nil},
		Logger: slog.New(slog.DiscardHandler)})
	article.Register(api, article.NewStore(), func(op *huma.Operation) { rest.RequireBearer(api, valid.validate)(op) })
	return api
}

func generate(t *testing.T, api *rest.API, opts clientgen.Options) *clientgen.Result {
	t.Helper()
	if opts.Out == "" {
		opts.Out = exampleOut
	}
	if opts.Main == "" {
		opts.Main = exampleMain
	}
	res, err := clientgen.Generate(api, opts)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestGeneratedExampleIsUpToDate(t *testing.T) {
	res := generate(t, exampleAPI(rest.ModeOpenAPI, nil), clientgen.Options{})
	if want := []string{"article-events: SSE endpoints are not generated yet"}; strings.Join(res.Skipped, "\n") != strings.Join(want, "\n") {
		t.Fatalf("skipped %q", res.Skipped)
	}
	if *update {
		if err := clientgen.Write(exampleOut, res.Files); err != nil {
			t.Fatal(err)
		}
		return
	}
	for name, data := range res.Files {
		disk, err := os.ReadFile(filepath.Join(exampleOut, name))
		if err != nil || string(disk) != string(data) {
			t.Errorf("%s is out of date; run go test ./httpx/rest/clientgen -run TestGeneratedExampleIsUpToDate -update", name)
		}
	}
	_ = filepath.WalkDir(exampleOut, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(exampleOut, p)
			if _, ok := res.Files[filepath.ToSlash(rel)]; !ok {
				t.Errorf("%s is no longer generated", rel)
			}
		}
		return err
	})
}
