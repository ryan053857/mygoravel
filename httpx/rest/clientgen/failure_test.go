package clientgen_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/go-tenon/tenon/httpx/rest"
	"github.com/go-tenon/tenon/httpx/rest/clientgen"
	"github.com/go-tenon/tenon/httpx/rest/clientgen/internal/example/bad"
)

const badMain = "github.com/go-tenon/tenon/httpx/rest/clientgen/internal/example/bad"

// badAPI 在示例接口之外登记一个输出类型为 O 的公开接口。
func badAPI[O any]() *rest.API {
	api := exampleAPI(rest.ModeOpenAPI, nil)
	op := huma.Operation{OperationID: "bad-get", Method: http.MethodGet, Path: "/bad"}
	rest.Public()(&op)
	rest.Register[struct{}, O](api, op, nil)
	return api
}

func generateErr(api *rest.API, opts clientgen.Options) error {
	opts.Out, opts.Main = exampleOut, badMain
	_, err := clientgen.Generate(api, opts)
	return err
}

func TestTypesWithCustomJSONAreRejected(t *testing.T) {
	err := generateErr(badAPI[bad.MoneyOutput](), clientgen.Options{})
	if err == nil || !strings.Contains(err.Error(), "bad.Money has method MarshalJSON") {
		t.Fatal(err)
	}
}

func TestThirdPartyTypesNeedAnImport(t *testing.T) {
	err := generateErr(badAPI[bad.RowOutput](), clientgen.Options{})
	if err == nil || !strings.Contains(err.Error(), "--import github.com/uptrace/bun/schema") {
		t.Fatal(err)
	}
	res, err := clientgen.Generate(badAPI[bad.RowOutput](), clientgen.Options{Out: exampleOut, Main: badMain, Imports: []string{"github.com/uptrace/bun"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res.Files["bad/bad.go"]), "Deleted schema.NullTime `json:\"deleted\"`") {
		t.Fatalf("%s", res.Files["bad/bad.go"])
	}
}

func TestMirrorPackageNamesMustBeUnique(t *testing.T) {
	err := generateErr(badAPI[bad.ConflictOutput](), clientgen.Options{})
	if err == nil || !strings.Contains(err.Error(), "--rename") {
		t.Fatal(err)
	}
	other := badMain + "/article"
	res, err := clientgen.Generate(badAPI[bad.ConflictOutput](), clientgen.Options{Out: exampleOut, Main: badMain, Rename: map[string]string{other: "note"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res.Files["bad.go"]), "(note.Note, error)") || res.Files["note/note.go"] == nil {
		t.Fatalf("%s", res.Files["bad.go"])
	}
}

func TestOperationAndGroupNamesMustNotCollide(t *testing.T) {
	api := exampleAPI(rest.ModeOpenAPI, nil)
	op := huma.Operation{OperationID: "article", Method: http.MethodGet, Path: "/article"}
	rest.Public()(&op)
	rest.Register(api, op, func(context.Context, *struct{}) (*struct{}, error) { return nil, nil })
	if err := generateErr(api, clientgen.Options{}); err == nil || !strings.Contains(err.Error(), "Client.Article") {
		t.Fatal(err)
	}
}
