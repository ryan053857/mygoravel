// Package clocktest 检查框架中的 SQL 不读取数据库的时钟：时间都由 Go 的 time.Now 算出，作为参数传给数据库。
// 这样 testing/synctest 的假时钟对整个框架生效，测试中 time.Sleep 一小时，数据库中的过期判断也跟着走。
package clocktest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// root 是模块根目录。
const root = "../../.."

// exempt 是允许使用数据库时钟的目录：ai/chat 的迁移是 trpc-agent-go 的表结构，由上游代码写入。
var exempt = map[string]bool{
	"ai/chat/migrations": true,
}

// dbClock 匹配 PostgreSQL、MySQL 和 SQLite 中读取当前时间的写法。
var dbClock = regexp.MustCompile(`(?i)\b(now|clock_timestamp|statement_timestamp|transaction_timestamp|sysdate|curdate|curtime|utc_timestamp|utc_date|utc_time|unix_timestamp|unixepoch)\s*\(|\b(current_timestamp|current_date|current_time|localtimestamp|localtime)\b|'now'`)

func TestSQLDoesNotReadDatabaseClock(t *testing.T) {
	var files int
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if exempt[rel] || d.Name() == "testdata" || (strings.HasPrefix(d.Name(), ".") && rel != ".") {
				return filepath.SkipDir
			}
			return nil
		}
		switch {
		case strings.HasSuffix(path, ".sql"):
			files++
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for i, line := range strings.Split(string(data), "\n") {
				line, _, _ = strings.Cut(line, "--")
				if m := dbClock.FindString(line); m != "" {
					t.Errorf("%s:%d: %q reads the database clock; write a time computed in Go instead", rel, i+1, m)
				}
			}
		case strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go"):
			files++
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				s, err := strconv.Unquote(lit.Value)
				if err != nil {
					return true
				}
				if m := dbClock.FindString(s); m != "" {
					t.Errorf("%s:%d: %q reads the database clock; pass time.Now() as a query argument instead",
						rel, fset.Position(lit.Pos()).Line, m)
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files < 100 {
		t.Fatalf("only %d files checked; is root %q the module root?", files, root)
	}
}
