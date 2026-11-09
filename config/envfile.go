package config

import (
	"os"
	"path/filepath"

	"github.com/cockroachdb/errors"
	"github.com/joho/godotenv"
	"go.yaml.in/yaml/v4"
)

// envFileKey 是保留的顶层键，指向一个 dotenv 文件。
const envFileKey = "envfile"

// loadEnvFile 读取 node 指向的 dotenv 文件。路径可引用进程环境变量，
// 相对路径基于 dir 解析；文件不存在时返回错误。
func loadEnvFile(node *yaml.Node, dir string, lookup LookupFunc) (map[string]string, error) {
	if node.Kind != yaml.ScalarNode || node.ShortTag() != "!!str" {
		return nil, errors.Newf("line %d: %s must be a string path", node.Line, envFileKey)
	}
	path, err := expand(node.Value, lookup)
	if err != nil {
		return nil, errors.Wrapf(err, "line %d: %s", node.Line, envFileKey)
	}
	if path == "" {
		return nil, errors.Newf("line %d: %s must not be empty", node.Line, envFileKey)
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, errors.Wrap(err, envFileKey)
	}
	defer f.Close()

	env, err := godotenv.Parse(f)
	if err != nil {
		// godotenv 的诊断会回显文件内容，可能包含密钥，因此不包装原始错误。
		return nil, errors.Newf("%s %s: invalid dotenv syntax", envFileKey, path)
	}
	return env, nil
}

// withFallback 先查 primary，未设置时再查 fallback；primary 中的空值同样优先。
func withFallback(primary LookupFunc, fallback map[string]string) LookupFunc {
	return func(key string) (string, bool) {
		if v, ok := primary(key); ok {
			return v, true
		}
		v, ok := fallback[key]
		return v, ok
	}
}
