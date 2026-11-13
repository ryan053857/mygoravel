package project_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/go-tenon/tenon/cmd/tenon/internal/project"
)

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 没有 tenon.json 时全部用默认值；单页应用和 templ 按项目中的文件判断。
func TestDefaults(t *testing.T) {
	dir := t.TempDir()
	cfg, err := project.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Main != "./cmd/api" || strings.Join(cfg.Serve, " ") != "serve --config config/app.yaml" || cfg.URL != "http://localhost:8080" ||
		cfg.Frontend != nil || cfg.Templ != nil || cfg.Generate.TS != "" {
		t.Fatalf("empty project %+v", cfg)
	}

	write(t, dir, "config/local.yaml", "")
	write(t, dir, "frontend/index.html", "")
	write(t, dir, "internal/web/layout.templ", "")
	write(t, dir, "node_modules/x/y.templ", "") // 依赖中的 .templ 不算
	cfg, err = project.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.Serve, " ") != "serve --config config/app.yaml,config/local.yaml" ||
		cfg.Frontend == nil || cfg.Frontend.Dir != "frontend" || cfg.Frontend.DevURL != "http://localhost:5173" ||
		cfg.Generate.TS != "frontend/src/api" || cfg.Templ == nil || cfg.Templ.ProxyPort != 7331 {
		t.Fatalf("spa project %+v %+v %+v", cfg, cfg.Frontend, cfg.Templ)
	}
}

func TestConfigFile(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "tenon.json", `{"$schema": "x", "main": "./cmd/web", "url": "http://localhost:3000",
		"frontend": {"dir": "app"}, "generate": {"ts": "-", "go": "../api"},
		"tailwind": {"input": "a.css", "output": "b.css"}, "tools": [{"watch": ["esbuild", "--watch"]}]}`)
	cfg, err := project.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Main != "./cmd/web" || cfg.Frontend.Dir != "app" || cfg.Generate.TS != "" || cfg.Generate.Go != "../api" ||
		cfg.Tailwind.Output != "b.css" || len(cfg.Tools) != 1 || cfg.Templ != nil {
		t.Fatalf("%+v", cfg)
	}
	for name, content := range map[string]string{
		"unknown field": `{"mian": "./cmd/api"}`,
		"bad url":       `{"url": "localhost:8080"}`,
		"tailwind":      `{"tailwind": {"input": "a.css"}}`,
		"empty tool":    `{"tools": [{"build": ["x"]}]}`,
	} {
		write(t, dir, "tenon.json", content)
		if _, err := project.Load(dir); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// schema.json 与 Config 的字段一致：编辑器的补全与 tenon 读取的字段相同。
func TestSchemaMatchesConfig(t *testing.T) {
	data, err := os.ReadFile("../../schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	var compare func(where string, typ reflect.Type, props map[string]any)
	compare = func(where string, typ reflect.Type, props map[string]any) {
		var fields []string
		for i := range typ.NumField() {
			f := typ.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			fields = append(fields, name)
			p, _ := props[name].(map[string]any)
			if p == nil {
				t.Errorf("%s.%s missing from schema.json", where, name)
				continue
			}
			ft := f.Type
			for ft.Kind() == reflect.Pointer || ft.Kind() == reflect.Slice && ft.Elem().Kind() == reflect.Struct {
				if ft.Kind() == reflect.Slice {
					p, _ = p["items"].(map[string]any)
				}
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				sub, _ := p["properties"].(map[string]any)
				compare(where+"."+name, ft, sub)
			}
		}
		for name := range props {
			if !slices.Contains(fields, name) {
				t.Errorf("%s.%s in schema.json is not a Config field", where, name)
			}
		}
	}
	compare("tenon", reflect.TypeFor[project.Config](), schema["properties"].(map[string]any))
}
