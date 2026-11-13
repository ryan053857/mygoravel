// Package project 读取项目根目录的 tenon.json，补上约定的默认值。
package project

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// File 是项目配置文件的名字。
const File = "tenon.json"

// Config 是 tenon.json。所有字段都可以省略，Load 按约定补上默认值。
type Config struct {
	// Schema 是编辑器补全用的 JSON Schema 地址，tenon 不读取它。
	Schema string `json:"$schema,omitempty"`
	// Main 是应用入口的包，默认 ./cmd/api。
	Main string `json:"main,omitempty"`
	// Serve 是开发时运行应用的参数，默认 serve --config config/app.yaml,config/local.yaml（local.yaml 不存在时去掉它）。
	Serve []string `json:"serve,omitempty"`
	// URL 是应用的地址，dev 打印它，templ 的代理访问它，默认 http://localhost:8080。
	URL string `json:"url,omitempty"`
	// Frontend 是单页应用；项目中有 frontend/index.html 时默认启用。
	Frontend *Frontend `json:"frontend,omitempty"`
	// Generate 是每次重新编译成功后生成的客户端。
	Generate *Generate `json:"generate,omitempty"`
	// Templ 是服务端渲染的页面；项目中有 .templ 文件时默认启用。
	Templ *Templ `json:"templ,omitempty"`
	// Tailwind 是用 Tailwind 生成的样式表，可空。
	Tailwind *Tailwind `json:"tailwind,omitempty"`
	// Tools 是与应用一起运行的其他工具，可空。
	Tools []Tool `json:"tools,omitempty"`
}

// Frontend 是 Vite 项目。
type Frontend struct {
	// Dir 是前端源码的目录，其中有 index.html 和 frontend.go（go:embed dist），默认 frontend。
	Dir string `json:"dir,omitempty"`
	// DevURL 是 Vite 开发服务器的地址，默认 http://localhost:5173。dev 等它能访问后再启动应用。
	DevURL string `json:"devUrl,omitempty"`
}

// Generate 配置生成的客户端，目录为空表示不生成。
type Generate struct {
	// TS 是 TypeScript 客户端的目录；有前端时默认 <frontend.dir>/src/api，"-" 表示不生成。
	TS string `json:"ts,omitempty"`
	// Go 是 Go 客户端的目录，例如 ../api，默认不生成。
	Go string `json:"go,omitempty"`
}

// Templ 配置服务端渲染页面的开发。
type Templ struct {
	// ProxyPort 是浏览器访问的端口，templ 在这里代理应用并注入自动刷新的脚本，默认 7331。
	ProxyPort int `json:"proxyPort,omitempty"`
	// Static 是静态文件目录，其中的文件变化时刷新浏览器，可空。
	Static string `json:"static,omitempty"`
}

// Tailwind 是 Tailwind standalone CLI 的输入和输出。
type Tailwind struct {
	Input  string `json:"input"`
	Output string `json:"output"`
}

// Tool 是一个与应用一起运行的工具：开发时运行 Watch，停止后运行一次 Build。
type Tool struct {
	Watch []string `json:"watch"`
	Build []string `json:"build,omitempty"`
}

// Load 读取 dir 中的 tenon.json（没有时全部用默认值），补上默认值并检查。
func Load(dir string) (*Config, error) {
	cfg := &Config{}
	data, err := os.ReadFile(filepath.Join(dir, File))
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(cfg); err != nil {
			return nil, fmt.Errorf("%s: %w", File, err)
		}
	}
	cfg.defaults(dir)
	return cfg, cfg.check()
}

func (c *Config) defaults(dir string) {
	exists := func(p string) bool {
		_, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p)))
		return err == nil
	}
	if c.Main == "" {
		c.Main = "./cmd/api"
	}
	if c.Serve == nil {
		configs := "config/app.yaml"
		if exists("config/local.yaml") {
			configs += ",config/local.yaml"
		}
		c.Serve = []string{"serve", "--config", configs}
	}
	if c.URL == "" {
		c.URL = "http://localhost:8080"
	}
	if c.Frontend == nil && exists("frontend/index.html") {
		c.Frontend = &Frontend{}
	}
	if f := c.Frontend; f != nil {
		if f.Dir == "" {
			f.Dir = "frontend"
		}
		if f.DevURL == "" {
			f.DevURL = "http://localhost:5173"
		}
	}
	if c.Generate == nil {
		c.Generate = &Generate{}
	}
	switch {
	case c.Generate.TS == "-":
		c.Generate.TS = ""
	case c.Generate.TS == "" && c.Frontend != nil:
		c.Generate.TS = path.Join(c.Frontend.Dir, "src", "api")
	}
	if c.Templ == nil && hasTempl(dir) {
		c.Templ = &Templ{}
	}
	if c.Templ != nil && c.Templ.ProxyPort == 0 {
		c.Templ.ProxyPort = 7331
	}
}

func (c *Config) check() error {
	if t := c.Tailwind; t != nil && (t.Input == "" || t.Output == "") {
		return fmt.Errorf("%s: tailwind needs input and output", File)
	}
	for i, t := range c.Tools {
		if len(t.Watch) == 0 {
			return fmt.Errorf("%s: tools[%d].watch is empty", File, i)
		}
	}
	if !strings.HasPrefix(c.URL, "http://") && !strings.HasPrefix(c.URL, "https://") {
		return fmt.Errorf("%s: url %q must start with http:// or https://", File, c.URL)
	}
	return nil
}

// SkipDir 报告扫描项目时是否跳过这个目录：隐藏目录、依赖、测试数据和前端的构建产物。
func SkipDir(name string) bool {
	return strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == "testdata" || name == "dist"
}

// hasTempl 报告 dir 中是否有 .templ 文件。
func hasTempl(dir string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		switch {
		case found:
			return fs.SkipAll
		case err != nil:
			return nil
		case d.IsDir() && p != dir && SkipDir(d.Name()):
			return filepath.SkipDir
		case !d.IsDir() && strings.HasSuffix(p, ".templ"):
			found = true
		}
		return nil
	})
	return found
}
