// Package scaffold 生成新项目：template 目录中的每个 .tmpl 文件按 Options 渲染后写入目标目录。
// 模板的分隔符是 [[ ]]，不与 JSX 的 {{ }} 冲突；gitignore.tmpl 写成 .gitignore。
package scaffold

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"text/template"
)

//go:embed all:template
var files embed.FS

// GoVersion 是生成的 go.mod 中的 go 版本，与框架要求的最低版本相同。
const GoVersion = "1.27.3"

// Options 是模板的参数。
type Options struct {
	// Module 是 Go 模块路径，Name 是应用的名称（页面标题、命令的说明）。
	Module, Name string
	// TenonVersion 是 go.mod 中要求的框架版本。
	TenonVersion string
	// TenonDir 不为空时，go.mod 用 replace 指向这个本地的框架目录（开发框架本身时使用）。
	TenonDir  string
	GoVersion string
}

// Write 把模板渲染到 dir。dir 必须不存在或为空。
func Write(dir string, opts Options) error {
	if opts.GoVersion == "" {
		opts.GoVersion = GoVersion
	}
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return fmt.Errorf("%s is not empty", dir)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	err := fs.WalkDir(files, "template", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		src, err := files.ReadFile(p)
		if err != nil {
			return err
		}
		tmpl, err := template.New(p).Delims("[[", "]]").Option("missingkey=error").Parse(string(src))
		if err != nil {
			return err
		}
		var out bytes.Buffer
		if err := tmpl.Execute(&out, opts); err != nil {
			return err
		}
		rel := strings.TrimSuffix(strings.TrimPrefix(p, "template/"), ".tmpl")
		if path.Base(rel) == "gitignore" {
			rel = path.Join(path.Dir(rel), ".gitignore")
		}
		return writeFile(filepath.Join(dir, filepath.FromSlash(rel)), out.Bytes())
	})
	if err != nil {
		return fmt.Errorf("scaffold: %w", err)
	}
	return nil
}

func writeFile(p string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}
