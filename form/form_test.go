package form_test

import (
	"context"
	"log/slog"
	"reflect"
	"testing"

	"github.com/go-tenon/tenon/form"
	"github.com/go-tenon/tenon/i18n"
)

type input struct {
	Title  string   `json:"title" minLength:"1"`
	Level  int8     `json:"level,omitempty"`
	Active bool     `json:"active"`
	Tags   []string `json:"tags,omitempty"`
}

func zh(t *testing.T) context.Context {
	t.Helper()
	b, err := i18n.New("zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	return i18n.WithLocale(context.Background(), b, "zh-CN")
}

func TestDecode(t *testing.T) {
	data, errs := form.Decode[input](zh(t), map[string][]string{
		"title": {"Hello"}, "level": {" 3 "}, "active": {"TRUE"}, "tags": {"a", "b"},
	})
	if errs != nil {
		t.Fatalf("errors: %v", errs)
	}
	if want := (input{Title: "Hello", Level: 3, Active: true, Tags: []string{"a", "b"}}); !reflect.DeepEqual(data, want) {
		t.Fatalf("data: %+v", data)
	}
}

func TestDecodeErrors(t *testing.T) {
	data, errs := form.Decode[input](zh(t), map[string][]string{"title": {""}, "level": {"300"}, "active": {"yes"}})
	want := map[string]string{"title": "不能为空", "level": "不能大于 127", "active": "必须是 true、false、1 或 0"}
	if !reflect.DeepEqual(errs, want) {
		t.Fatalf("errors: %v", errs)
	}
	if data.Level != 0 || data.Active {
		t.Fatalf("data: %+v", data)
	}
}

func TestSchemaRequired(t *testing.T) {
	s := form.Schema(reflect.TypeFor[input]())
	if !reflect.DeepEqual(s.Required, []string{"title", "active"}) {
		t.Fatalf("required: %v", s.Required)
	}
}

// Source 与 log/slog.Source 同名，两者都能取得各自的 schema。
type Source struct {
	Path string `json:"path" minLength:"1"`
}

func TestSchemaSameNameDifferentPackages(t *testing.T) {
	if s := form.Schema(reflect.TypeFor[slog.Source]()); s.Properties["function"] == nil {
		t.Fatalf("slog.Source: %v", s.Properties)
	}
	if s := form.Schema(reflect.TypeFor[Source]()); !reflect.DeepEqual(s.Required, []string{"path"}) {
		t.Fatalf("form_test.Source required: %v", s.Required)
	}
}
