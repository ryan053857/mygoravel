package clientgen

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cockroachdb/errors"
)

// Write 把 files 写入目录 out，并删除上次生成、这次不再生成的文件（第一行是 Header 的 .go、.ts 文件）
// 以及因此变空的目录。out 中的其他文件保持不变。
func Write(out string, files map[string][]byte) error {
	var stale []string
	err := filepath.WalkDir(out, func(p string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return fs.SkipAll
		}
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, ".ts") {
			return err
		}
		rel, err := filepath.Rel(out, p)
		if err != nil {
			return err
		}
		if _, ok := files[filepath.ToSlash(rel)]; ok {
			return nil
		}
		if generated(p) {
			stale = append(stale, p)
		}
		return nil
	})
	if err != nil {
		return errors.Wrap(err, "clientgen")
	}
	for name, data := range files {
		p := filepath.Join(out, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return errors.Wrap(err, "clientgen")
		}
		if old, err := os.ReadFile(p); err == nil && bytes.Equal(old, data) {
			continue
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return errors.Wrap(err, "clientgen")
		}
	}
	slices.Sort(stale)
	for _, p := range slices.Backward(stale) {
		if err := os.Remove(p); err != nil {
			return errors.Wrap(err, "clientgen")
		}
		// 删除变空的目录，直到 out。
		for dir := filepath.Dir(p); dir != filepath.Clean(out); dir = filepath.Dir(dir) {
			if os.Remove(dir) != nil {
				break
			}
		}
	}
	return nil
}

func generated(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, len(Header))
	n, _ := f.Read(head)
	return string(head[:n]) == Header
}
